package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/fin-assist/bank-bff/internal/config"
	"github.com/fin-assist/bank-bff/internal/rest"
	"github.com/fin-assist/bank-bff/internal/testpeer"
)

// serving captures the addresses of the "serving" log line and keeps all logs.
type serving struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	started chan [2]string
}

func (s *serving) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var line struct{ Msg, Public, Operational string }
	if json.Unmarshal(p, &line) == nil && line.Msg == "serving" {
		select {
		case s.started <- [2]string{line.Public, line.Operational}:
		default:
		}
	}
	return s.buf.Write(p)
}

func (s *serving) text() string { s.mu.Lock(); defer s.mu.Unlock(); return s.buf.String() }

// bff is a running full assembly (app.Run) with peers on bufconn.
type bff struct {
	public, operational string
	peers               *testpeer.Peers
	contract            *testpeer.Contract
	keys                *testpeer.Keys
	logs                *serving
	stop                func()
}

const clientVersion = "android/0.2.5+41"

func apiConfig(stage testpeer.StageFiles) config.Config {
	return config.Config{
		Service: "bank-bff", Env: "stage", PublicAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0", LogFormat: "json",
		ShutdownTimeout: 2 * time.Second,
		API: config.API{
			Enabled: true, UserAddr: testpeer.Target, AssistantAddr: testpeer.Target,
			UserIssuer: testpeer.UserIssuer, UserAudience: testpeer.UserAudience,
			Stage:       config.Stage{Issuer: testpeer.StageIssuer, SigningKeyFile: stage.SigningKeyFile, PublicKeysFile: stage.PublicKeysFile},
			MinVersions: map[string]rest.Version{"android": {"0", "2", "5"}},
			Maintenance: rest.Maintenance{RetryAfter: 5 * time.Minute},
			BodyLimit:   rest.MinBodyLimit,
			Streams:     rest.StreamLimits{Max: 100, PerIP: 10, RetryAfter: 10 * time.Second},
		},
	}
}

func startBFF(t *testing.T, mutate func(*config.Config, *testpeer.Peers), opts ...Option) *bff {
	t.Helper()
	keys := testpeer.NewKeys(t)
	stage := testpeer.NewStageFiles(t)
	peers := testpeer.Start(t, keys, stage)
	cfg := apiConfig(stage)
	if mutate != nil {
		mutate(&cfg, peers)
	}
	logs := &serving{started: make(chan [2]string, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	opts = append([]Option{WithDialOptions(peers.DialOptions()...)}, opts...)
	go func() { done <- Run(ctx, cfg, slog.New(slog.NewJSONHandler(logs, nil)), opts...) }()
	b := &bff{peers: peers, contract: testpeer.LoadContract(t), keys: keys, logs: logs}
	var once sync.Once
	b.stop = func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("Run: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("Run did not stop")
			}
		})
	}
	t.Cleanup(b.stop)
	select {
	case addr := <-logs.started:
		b.public, b.operational = addr[0], addr[1]
	case err := <-done:
		t.Fatalf("Run: %v\n%s", err, logs.text())
	case <-time.After(10 * time.Second):
		t.Fatal("startup timeout")
	}
	return b
}

// request is one HTTP call to the public listener.
type request struct {
	method, path, route string
	header              map[string]string
	body                string
}

type response struct {
	*http.Response
	body []byte
	json map[string]any
}

// do sends a real HTTP request and validates a non-SSE answer against openapi.yaml.
func (b *bff) do(t *testing.T, r request) response {
	t.Helper()
	out := b.raw(t, r)
	resp, body := out.Response, out.body
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		req, _ := http.NewRequest(r.method, "http://"+b.public+r.path, nil)
		out.json = b.contract.Response(t, r.method, r.route, req, resp, body)
	}
	if resp.Header.Get("X-Request-Id") == "" {
		t.Fatalf("%s %s: no X-Request-Id", r.method, r.path)
	}
	return out
}

// raw sends a real HTTP request and returns the answer with its body read and closed.
func (b *bff) raw(t *testing.T, r request) response {
	t.Helper()
	got, err := b.fetch(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// fetch is raw without t.Fatal, for requests sent from another goroutine: t.Fatal
// there would stop only that goroutine and leave the test waiting.
func (b *bff) fetch(ctx context.Context, r request) (response, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var body io.Reader
	if r.body != "" {
		body = strings.NewReader(r.body)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, "http://"+b.public+r.path, body)
	if err != nil {
		return response{}, err
	}
	req.Header.Set("X-Client-Version", clientVersion)
	if r.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range r.header {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if err != nil {
		return response{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return response{}, err
	}
	return response{Response: resp, body: data}, nil
}

// await fails the test when ch is not ready within 5 seconds.
func await[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: timed out", what)
		var zero T
		return zero
	}
}

func errorCode(r response) string {
	e, _ := r.json["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

// starter starts the BFF under test at one level: startBFF runs the complete Run
// (API tests), startAssembly serves the public API assembly on an httptest TCP
// server (integration tests). Both reach the peers over gRPC on bufconn.
type starter func(t *testing.T, mutate func(*config.Config, *testpeer.Peers), opts ...Option) *bff

func startAssembly(t *testing.T, mutate func(*config.Config, *testpeer.Peers), opts ...Option) *bff {
	t.Helper()
	keys := testpeer.NewKeys(t)
	stage := testpeer.NewStageFiles(t)
	peers := testpeer.Start(t, keys, stage)
	cfg := apiConfig(stage)
	if mutate != nil {
		mutate(&cfg, peers)
	}
	o := defaults()
	for _, opt := range append([]Option{WithDialOptions(peers.DialOptions()...)}, opts...) {
		opt(&o)
	}
	logs := &serving{started: make(chan [2]string, 1)}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	api, err := newPublicAPI(cfg, log, prometheus.NewRegistry(), o)
	if err != nil {
		t.Fatal(err)
	}
	// The same listener limits as Run, so stream deadline scenarios mean the same here.
	srv := httptest.NewUnstartedServer(api.handler)
	srv.Config = server(api.handler, log, o)
	srv.Config.RegisterOnShutdown(api.streams.Shutdown)
	srv.Start()
	b := &bff{peers: peers, contract: testpeer.LoadContract(t), keys: keys, logs: logs, public: srv.Listener.Addr().String()}
	var once sync.Once
	b.stop = func() {
		once.Do(func() {
			// Like Run: a graceful shutdown with a bound, then close what is left.
			ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
			defer cancel()
			_ = srv.Config.Shutdown(ctx)
			srv.Close()
			api.close(log)
		})
	}
	t.Cleanup(b.stop)
	return b
}
