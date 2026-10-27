package app

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/fin-assist/bank-bff/internal/config"
	"github.com/fin-assist/bank-bff/internal/testpeer"
)

func TestUnitOptionsAdjustOnlyTransportAndTimings(t *testing.T) {
	o := defaults()
	clock := func() time.Time { return time.Unix(1, 0) }
	for _, opt := range []Option{WithDialOptions(nil), WithStreamTimings(time.Second, 2*time.Second, 3*time.Second), WithServerTimeouts(4*time.Second, 5*time.Second), WithClock(clock)} {
		opt(&o)
	}
	if len(o.dial) != 1 || o.heartbeat != time.Second || o.answerStream != 2*time.Second || o.phoneStream != 3*time.Second ||
		o.readTimeout != 4*time.Second || o.writeTimeout != 5*time.Second || !o.clock().Equal(time.Unix(1, 0)) {
		t.Fatalf("%+v", o)
	}
	s := server(http.NewServeMux(), slog.New(slog.DiscardHandler), o)
	if s.ReadTimeout != 4*time.Second || s.WriteTimeout != 5*time.Second {
		t.Fatal(s.ReadTimeout)
	}
}

// The assembly builds without contacting any peer: connections are lazy and
// identity reads only its configured files.
func TestUnitPublicAPIAssemblyWithoutNetwork(t *testing.T) {
	cfg := apiConfig(testpeer.NewStageFiles(t))
	api, err := newPublicAPI(cfg, slog.New(slog.DiscardHandler), prometheus.NewRegistry(), defaults())
	if err != nil {
		t.Fatal(err)
	}
	defer api.close(slog.New(slog.DiscardHandler))
	for _, tc := range []struct {
		target, version string
		status          int
		code            string
	}{
		{"/v1/profile", "", 400, "CLIENT_VERSION_UNSUPPORTED"},
		{"/v1/profile", clientVersion, 401, "INVALID_TOKEN"},
		{"/v1/unknown", clientVersion, 404, "NOT_FOUND"},
		{"/v1/consents/marketing", clientVersion, 404, "NOT_FOUND"},
	} {
		req := httptest.NewRequest("GET", tc.target, nil)
		if tc.version != "" {
			req.Header.Set("X-Client-Version", tc.version)
		}
		res := httptest.NewRecorder()
		api.handler.ServeHTTP(res, req)
		if res.Code != tc.status || !strings.Contains(res.Body.String(), tc.code) {
			t.Fatalf("%s: %d %s", tc.target, res.Code, res.Body.String())
		}
	}
	if res := httptest.NewRecorder(); true {
		api.handler.ServeHTTP(res, httptest.NewRequest("GET", "/metrics", nil))
		if res.Code != 404 {
			t.Fatal("the public handler has no operational endpoint")
		}
	}
}

func TestUnitPublicAPIAssemblyFailuresReleaseEverything(t *testing.T) {
	stage := testpeer.NewStageFiles(t)
	log := slog.New(slog.DiscardHandler)
	for name, mutate := range map[string]func(*config.Config, *prometheus.Registry){
		"missing stage key": func(c *config.Config, _ *prometheus.Registry) { c.API.Stage.SigningKeyFile = t.TempDir() + "/none" },
		"dev identity":      func(c *config.Config, _ *prometheus.Registry) { c.Env = "dev" },
		"stage files in prod": func(c *config.Config, _ *prometheus.Registry) {
			c.Env = "prod"
			c.API.Kubernetes.TokenFile = "/var/run/secrets/finassist/token"
		},
		"metrics registered twice": func(_ *config.Config, reg *prometheus.Registry) {
			if _, err := newPublicAPI(apiConfig(stage), log, reg, defaults()); err != nil {
				t.Fatal(err)
			}
		},
		"bad user target":       func(c *config.Config, _ *prometheus.Registry) { c.API.UserAddr = "%zz" },
		"bad analytics target":  func(c *config.Config, _ *prometheus.Registry) { c.API.AnalyticsAddr = "%zz" },
		"bad statements target": func(c *config.Config, _ *prometheus.Registry) { c.API.StatementsAddr = "%zz" },
		"bad assistant target":  func(c *config.Config, _ *prometheus.Registry) { c.API.AssistantAddr = "%zz" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := apiConfig(stage)
			reg := prometheus.NewRegistry()
			mutate(&cfg, reg)
			if api, err := newPublicAPI(cfg, log, reg, defaults()); err == nil || api != nil {
				t.Fatalf("accepted: %v", err)
			}
		})
	}
}
