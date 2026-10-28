package endpoint

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	userv1 "github.com/fin-assist/contracts/gen/go/finassist/user/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
	grpcclient "github.com/fin-assist/go-grpc/client"
	identity "github.com/fin-assist/go-identity"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/fin-assist/bank-bff/internal/apispec"
	"github.com/fin-assist/bank-bff/internal/rest"
	"github.com/fin-assist/bank-bff/internal/testpeer"
)

// This composes only the HTTP validation/mapper and genuine service-authenticated
// gRPC transport. It does not construct the public app assembly or user storage.
func TestIntegrationPhoneReceiptAssertionsAndTrustedMetadata(t *testing.T) {
	keys, stage := testpeer.NewKeys(t), testpeer.NewStageFiles(t)
	peers := testpeer.Start(t, keys, stage)
	assembly, err := identity.NewAssembly(identity.AssemblyOptions{Env: "stage", Service: "bank-bff", UserIssuer: testpeer.UserIssuer, UserAudience: testpeer.UserAudience,
		DialUser: func(tokens identity.TokenSource) (*grpc.ClientConn, error) {
			return grpcclient.Dial(testpeer.Target, grpcclient.Options{Peer: "user", Tokens: tokens, DialOptions: peers.DialOptions()})
		},
		Stage: identity.StageKeyFiles{Issuer: testpeer.StageIssuer, SigningKeyFile: stage.SigningKeyFile, PublicKeysFile: stage.PublicKeysFile}, Clock: time.Now, Registerer: prometheus.NewRegistry()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = assembly.Close() })
	authHTTP, err := identity.NewHTTP(assembly, func(w http.ResponseWriter, _ *http.Request, _ error) { rest.InvalidToken().Write(w) })
	if err != nil {
		t.Fatal(err)
	}
	options := append([]grpc.DialOption{grpc.WithChainUnaryInterceptor(identity.UnaryClient(assembly), rest.UnaryHints())}, peers.DialOptions()...)
	conn, err := grpcclient.Dial(testpeer.Target, grpcclient.Options{Peer: "user", Tokens: assembly, DialOptions: options})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	spec, err := apispec.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := rest.NewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{Auth: userv1.NewAuthServiceClient(conn)}
	received := make(chan *userv1.StartPhoneVerificationRequest, 8)
	peers.Auth.Set(func(auth *testpeer.Auth) {
		auth.StartFunc = func(_ context.Context, in *userv1.StartPhoneVerificationRequest) (*userv1.StartPhoneVerificationResponse, error) {
			received <- proto.Clone(in).(*userv1.StartPhoneVerificationRequest)
			if !in.PdConsentAccepted {
				return nil, finerr.ConsentRequired()
			}
			return &userv1.StartPhoneVerificationResponse{VerificationToken: "synthetic", CallbackNumber: "+74951234567", ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)), ExpiresIn: proto.Int32(59)}, nil
		}
	})
	for _, trusted := range []bool{false, true} {
		cfg := rest.Config{Spec: spec, BodyLimit: rest.MinBodyLimit, Metrics: metrics, Log: slog.New(slog.DiscardHandler), Clock: time.Now, Authenticate: authHTTP.Middleware, Gates: rest.Gates{MinVersions: map[string]rest.Version{"android": {"0", "2", "5"}}, Clock: time.Now}}
		if trusted {
			cfg.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("10.0.0.0/8")}
		}
		h, err := rest.Handler(cfg, []rest.Route{{Method: "POST", Path: "/v1/auth/phone", Idempotent: true, Handle: deps.startPhone}})
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(h)
		client := &http.Client{Timeout: 5 * time.Second}
		for _, accepted := range []bool{true, false} {
			body, _ := json.Marshal(map[string]any{"phone": "+79001234567", "pd_consent_accepted": accepted, "pd_consent_version": "receipt-v", "age_confirmed": true})
			req, err := http.NewRequestWithContext(t.Context(), "POST", server.URL+"/v1/auth/phone", strings.NewReader(string(body)))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Client-Version", "android/0.2.5+41")
			req.Header.Set("Idempotency-Key", testKey)
			req.Header.Set("X-Forwarded-For", "203.0.113.99, 198.51.100.7, 10.1.2.3")
			req.Header.Set("X-Client-IP", "192.0.2.44")
			req.Header.Set("X-Consent-Received-At", "1970-01-01T00:00:00Z")
			req.Header.Set("Authorization", "Bearer unverified-client")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			want := 200
			if !accepted {
				want = 403
			}
			if resp.StatusCode != want || resp.Header.Get("Cache-Control") != "no-store" {
				t.Fatalf("%d %s", resp.StatusCode, data)
			}
			got := <-received
			if got.Phone != "+79001234567" || got.PdConsentAccepted != accepted || got.PdConsentVersion != "receipt-v" || !got.AgeConfirmed {
				t.Fatal("assertions changed", got)
			}
			calls := peers.Calls(userv1.AuthService_StartPhoneVerification_FullMethodName)
			last := calls[len(calls)-1]
			ip := "127.0.0.1"
			if trusted {
				ip = "198.51.100.7"
			}
			if last.Caller != "bank-bff" || len(last.MD.Get("x-client-ip")) != 1 || last.MD.Get("x-client-ip")[0] != ip || last.MD.Get("idempotency-key")[0] != testKey || len(last.MD.Get("authorization")) != 0 || len(last.MD.Get("x-consent-received-at")) != 0 {
				t.Fatal("untrusted receipt metadata", last.MD)
			}
		}
		before := len(peers.Calls(userv1.AuthService_StartPhoneVerification_FullMethodName))
		req, _ := http.NewRequestWithContext(t.Context(), "POST", server.URL+"/v1/auth/phone", strings.NewReader(`{"phone":"+79001234567"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Client-Version", "android/0.2.5+41")
		req.Header.Set("Idempotency-Key", testKey)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 400 || len(peers.Calls(userv1.AuthService_StartPhoneVerification_FullMethodName)) != before {
			t.Fatal("legacy body reached USER")
		}
		server.Close()
	}
}
