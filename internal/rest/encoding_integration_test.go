package rest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	grpcclient "github.com/fin-assist/go-grpc/client"
	identity "github.com/fin-assist/go-identity"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"

	"github.com/fin-assist/bank-bff/internal/testpeer"
)

type encodingComponentBody struct {
	io.Reader
	reads atomic.Int32
}

func (b *encodingComponentBody) Read(p []byte) (int, error) { b.reads.Add(1); return b.Reader.Read(p) }

// Compose the transport pipeline with genuine JWT verification and an outbound
// gRPC call. No app.Run, public API assembly or business endpoint is involved;
// the route callback is a probe for whether validation admitted an effect.
func TestIntegrationJSONEncodingHTTPIdentityAndRPC(t *testing.T) {
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
	life, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- assembly.Run(life) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("identity: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("identity did not stop")
		}
	})
	auth, err := identity.NewHTTP(assembly, func(w http.ResponseWriter, _ *http.Request, _ error) { InvalidToken().Write(w) })
	if err != nil {
		t.Fatal(err)
	}
	options := append([]grpc.DialOption{grpc.WithChainUnaryInterceptor(identity.UnaryClient(assembly), UnaryHints())}, peers.DialOptions()...)
	conn, err := grpcclient.Dial(testpeer.Target, grpcclient.Options{Peer: "assistant", Tokens: assembly, DialOptions: options})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	peer := assistantv1.NewAssistantServiceClient(conn)
	peers.Assistant.Set(func(a *testpeer.Assistant) {
		a.AskFunc = func(context.Context, *assistantv1.AskQuestionRequest) (*assistantv1.AskQuestionResponse, error) {
			return &assistantv1.AskQuestionResponse{}, nil
		}
	})
	cfg := handlerConfig(t)
	cfg.Authenticate = auth.Middleware
	var routes []Route
	for _, route := range encodingJSONRoutes {
		routes = append(routes, Route{Method: route.method, Path: route.path, Protected: Always, Idempotent: true, Handle: func(call *Call) {
			if _, err := peer.AskQuestion(call.R.Context(), &assistantv1.AskQuestionRequest{Text: string(call.Body)}); err != nil {
				call.Fail(err)
				return
			}
			WriteNoContent(call.W)
		}})
	}
	h, err := Handler(cfg, routes)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	transport := &http.Transport{ExpectContinueTimeout: 4 * time.Second}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	send := func(route struct{ method, path, body string }, values []string, length int64) (int, int32, string) {
		t.Helper()
		body := route.body
		if length > MinBodyLimit {
			body = strings.Repeat("x", int(length))
		}
		spy := &encodingComponentBody{Reader: strings.NewReader(body)}
		req, err := http.NewRequestWithContext(t.Context(), route.method, server.URL+route.path, spy)
		if err != nil {
			t.Fatal(err)
		}
		req.ContentLength = length
		if length == 0 {
			req.Body = http.NoBody
		}
		req.Header.Set("X-Client-Version", "android/0.2.5+41")
		req.Header.Set("Authorization", "Bearer "+keys.Access("u1"))
		req.Header.Set("Idempotency-Key", "0190c3a0-1b2c-7d3e-8f40-123456789abc")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Expect", "100-continue")
		for _, value := range values {
			req.Header.Add("Content-Encoding", value)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, spy.reads.Load(), string(raw)
	}
	for _, route := range encodingJSONRoutes {
		for _, values := range [][]string{{"gzip"}, {""}, {"identity", "identity"}} {
			for _, length := range []int64{0, int64(len(route.body)), MinBodyLimit + 1, -1} {
				before := len(peers.Calls(assistantv1.AssistantService_AskQuestion_FullMethodName))
				status, reads, body := send(route, values, length)
				if status != 400 || reads != 0 || len(peers.Calls(assistantv1.AssistantService_AskQuestion_FullMethodName)) != before {
					t.Fatalf("%s %v len%d: %d reads%d %s", route.path, values, length, status, reads, body)
				}
				failure := decodeError(t, body)
				if failure.Code != "VALIDATION_ERROR" || len(failure.Details) != 1 || failure.Details[0].Field != "Content-Encoding" {
					t.Fatal(failure)
				}
			}
		}
		for _, values := range [][]string{nil, {"IdEnTiTy"}} {
			before := len(peers.Calls(assistantv1.AssistantService_AskQuestion_FullMethodName))
			status, reads, body := send(route, values, int64(len(route.body)))
			if status != 204 || reads == 0 || len(peers.Calls(assistantv1.AssistantService_AskQuestion_FullMethodName)) != before+1 {
				t.Fatalf("accepted %s %v: %d reads%d %s", route.path, values, status, reads, body)
			}
		}
	}
}
