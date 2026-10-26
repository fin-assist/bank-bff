package endpoint

import (
	"bufio"
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

	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	userv1 "github.com/fin-assist/contracts/gen/go/finassist/user/v1"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/fin-assist/bank-bff/internal/apispec"
	"github.com/fin-assist/bank-bff/internal/rest"
)

// fakeStream is a client stream that yields msgs then err (io.EOF when nil), or
// blocks until its context ends when block is set.
type fakeStream[T any] struct {
	ctx   context.Context
	msgs  []*T
	err   error
	block bool
	mu    sync.Mutex
}

func (s *fakeStream[T]) Recv() (*T, error) {
	s.mu.Lock()
	if len(s.msgs) > 0 {
		m := s.msgs[0]
		s.msgs = s.msgs[1:]
		s.mu.Unlock()
		return m, nil
	}
	s.mu.Unlock()
	if s.block {
		<-s.ctx.Done()
		return nil, s.ctx.Err()
	}
	if s.err != nil {
		return nil, s.err
	}
	return nil, io.EOF
}
func (s *fakeStream[T]) Header() (metadata.MD, error) { return nil, nil }
func (s *fakeStream[T]) Trailer() metadata.MD         { return nil }
func (s *fakeStream[T]) CloseSend() error             { return nil }
func (s *fakeStream[T]) Context() context.Context     { return s.ctx }
func (s *fakeStream[T]) SendMsg(any) error            { return nil }
func (s *fakeStream[T]) RecvMsg(any) error            { return nil }

// fakes implements every client interface; a nil function fails the test.
type fakes struct {
	t           *testing.T
	start       func(*userv1.StartPhoneVerificationRequest) (*userv1.StartPhoneVerificationResponse, error)
	watchPhone  func(context.Context, *userv1.WatchPhoneVerificationRequest) (grpc.ServerStreamingClient[userv1.WatchPhoneVerificationResponse], error)
	register    func(*userv1.RegisterRequest) (*userv1.RegisterResponse, error)
	refresh     func(*userv1.RefreshTokensRequest) (*userv1.RefreshTokensResponse, error)
	logout      func(*userv1.LogoutRequest) (*userv1.LogoutResponse, error)
	document    func(*userv1.GetConsentDocumentRequest) (*userv1.GetConsentDocumentResponse, error)
	profile     func() (*userv1.GetProfileResponse, error)
	grant       func(*userv1.GrantAssistantConsentRequest) (*userv1.GrantAssistantConsentResponse, error)
	revoke      func() (*userv1.RevokeAssistantConsentResponse, error)
	ask         func(*assistantv1.AskQuestionRequest) (*assistantv1.AskQuestionResponse, error)
	watchAnswer func(context.Context, *assistantv1.WatchAnswerRequest) (grpc.ServerStreamingClient[assistantv1.WatchAnswerResponse], error)
	list        func(*assistantv1.ListMessagesRequest) (*assistantv1.ListMessagesResponse, error)
	retry       func(*assistantv1.RetryAnswerRequest) (*assistantv1.RetryAnswerResponse, error)
	limit       func() (*assistantv1.GetLimitResponse, error)
	deadlines   []time.Duration
	mu          sync.Mutex
}

func must[F any](t *testing.T, f F, name string) F {
	t.Helper()
	if any(f) == nil {
		t.Fatalf("unexpected call %s", name)
	}
	return f
}

func (f *fakes) deadline(ctx context.Context) {
	if d, ok := ctx.Deadline(); ok {
		f.mu.Lock()
		f.deadlines = append(f.deadlines, time.Until(d))
		f.mu.Unlock()
	}
}

func (f *fakes) StartPhoneVerification(ctx context.Context, in *userv1.StartPhoneVerificationRequest, _ ...grpc.CallOption) (*userv1.StartPhoneVerificationResponse, error) {
	f.deadline(ctx)
	return must(f.t, f.start, "start")(in)
}
func (f *fakes) WatchPhoneVerification(ctx context.Context, in *userv1.WatchPhoneVerificationRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[userv1.WatchPhoneVerificationResponse], error) {
	f.deadline(ctx)
	return must(f.t, f.watchPhone, "watchPhone")(ctx, in)
}
func (f *fakes) Register(ctx context.Context, in *userv1.RegisterRequest, _ ...grpc.CallOption) (*userv1.RegisterResponse, error) {
	f.deadline(ctx)
	return must(f.t, f.register, "register")(in)
}
func (f *fakes) RefreshTokens(ctx context.Context, in *userv1.RefreshTokensRequest, _ ...grpc.CallOption) (*userv1.RefreshTokensResponse, error) {
	f.deadline(ctx)
	return must(f.t, f.refresh, "refresh")(in)
}
func (f *fakes) Logout(ctx context.Context, in *userv1.LogoutRequest, _ ...grpc.CallOption) (*userv1.LogoutResponse, error) {
	f.deadline(ctx)
	return must(f.t, f.logout, "logout")(in)
}
func (f *fakes) GetConsentDocument(ctx context.Context, in *userv1.GetConsentDocumentRequest, _ ...grpc.CallOption) (*userv1.GetConsentDocumentResponse, error) {
	f.deadline(ctx)
	return must(f.t, f.document, "document")(in)
}
func (f *fakes) GetProfile(ctx context.Context, _ *userv1.GetProfileRequest, _ ...grpc.CallOption) (*userv1.GetProfileResponse, error) {
	f.deadline(ctx)
	return must(f.t, f.profile, "profile")()
}
func (f *fakes) GrantAssistantConsent(ctx context.Context, in *userv1.GrantAssistantConsentRequest, _ ...grpc.CallOption) (*userv1.GrantAssistantConsentResponse, error) {
	f.deadline(ctx)
	return must(f.t, f.grant, "grant")(in)
}
func (f *fakes) RevokeAssistantConsent(ctx context.Context, _ *userv1.RevokeAssistantConsentRequest, _ ...grpc.CallOption) (*userv1.RevokeAssistantConsentResponse, error) {
	f.deadline(ctx)
	return must(f.t, f.revoke, "revoke")()
}
func (f *fakes) AskQuestion(ctx context.Context, in *assistantv1.AskQuestionRequest, _ ...grpc.CallOption) (*assistantv1.AskQuestionResponse, error) {
	f.deadline(ctx)
	return must(f.t, f.ask, "ask")(in)
}
func (f *fakes) WatchAnswer(ctx context.Context, in *assistantv1.WatchAnswerRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[assistantv1.WatchAnswerResponse], error) {
	f.deadline(ctx)
	return must(f.t, f.watchAnswer, "watchAnswer")(ctx, in)
}
func (f *fakes) ListMessages(ctx context.Context, in *assistantv1.ListMessagesRequest, _ ...grpc.CallOption) (*assistantv1.ListMessagesResponse, error) {
	f.deadline(ctx)
	return must(f.t, f.list, "list")(in)
}
func (f *fakes) RetryAnswer(ctx context.Context, in *assistantv1.RetryAnswerRequest, _ ...grpc.CallOption) (*assistantv1.RetryAnswerResponse, error) {
	f.deadline(ctx)
	return must(f.t, f.retry, "retry")(in)
}
func (f *fakes) GetLimit(ctx context.Context, _ *assistantv1.GetLimitRequest, _ ...grpc.CallOption) (*assistantv1.GetLimitResponse, error) {
	f.deadline(ctx)
	return must(f.t, f.limit, "limit")()
}

// unitAPI serves the routes with a header-checking stand-in for identity: no
// network, the handlers and the whole rest pipeline in process.
type unitAPI struct {
	h          http.Handler
	metrics    *rest.Metrics
	reg        *prometheus.Registry
	f          *fakes
	statements *statementFakes
}

const (
	goodToken = "Bearer good"
	testKey   = "0190c3a0-1b2c-7d3e-8f40-123456789abc"
)

func newUnitAPI(t *testing.T, streams rest.StreamLimits) *unitAPI {
	t.Helper()
	spec, err := apispec.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewRegistry()
	metrics, err := rest.NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	if streams.Max == 0 {
		streams = rest.StreamLimits{Max: 10, PerIP: 10, RetryAfter: time.Second}
	}
	f := &fakes{t: t}
	sf := &statementFakes{}
	deps := Deps{Auth: f, Consents: f, Profile: f, Assistant: f, Statements: sf, Heartbeat: time.Millisecond, AnswerStream: time.Second, PhoneStream: time.Second}
	h, err := rest.Handler(rest.Config{Spec: spec, BodyLimit: rest.MinBodyLimit, Metrics: metrics, Log: slog.New(slog.DiscardHandler), Clock: time.Now,
		Streams: rest.NewStreamLimiter(streams), Gates: rest.Gates{MinVersions: map[string]rest.Version{"android": {"0", "2", "5"}}, Clock: time.Now},
		Authenticate: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != goodToken {
					rest.InvalidToken().Write(w)
					return
				}
				next.ServeHTTP(w, r)
			})
		}}, deps.Routes())
	if err != nil {
		t.Fatal(err)
	}
	return &unitAPI{h: h, metrics: metrics, reg: reg, f: f, statements: sf}
}

type result struct {
	*httptest.ResponseRecorder
	json map[string]any
}

func (u *unitAPI) serve(t *testing.T, method, target, body string, header map[string]string) result {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("X-Client-Version", "android/0.2.5+41")
	req.Header.Set("Authorization", goodToken)
	if method == http.MethodPost {
		req.Header.Set("Idempotency-Key", testKey)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	res := httptest.NewRecorder()
	u.h.ServeHTTP(res, req)
	out := result{ResponseRecorder: res}
	if strings.HasPrefix(res.Header().Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(res.Body.Bytes(), &out.json); err != nil {
			t.Fatalf("%s: %v", res.Body.String(), err)
		}
	}
	return out
}

func (r result) code() string {
	e, _ := r.json["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

type event struct {
	name string
	data map[string]any
}

func (r result) events(t *testing.T) (events []event, pings int) {
	t.Helper()
	scanner := bufio.NewScanner(strings.NewReader(r.Body.String()))
	var name string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, ":"):
			pings++
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			var data map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data); err != nil {
				t.Fatalf("%q: %v", line, err)
			}
			events = append(events, event{name, data})
		}
	}
	return events, pings
}
