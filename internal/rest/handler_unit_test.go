package rest

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/fin-assist/go-grpc/reqctx"

	"github.com/fin-assist/bank-bff/internal/apispec"
)

func spec(t *testing.T) *apispec.Spec {
	t.Helper()
	s, err := apispec.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func operation(t *testing.T, method, path string) apispec.Operation {
	t.Helper()
	op, err := spec(t).Operation(method, path)
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func validate(t *testing.T, method, route, target, body string, header map[string]string) []Detail {
	t.Helper()
	op := operation(t, method, route)
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("X-Client-Version", "android/0.2.5+41")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	path := map[string]string{}
	if strings.Contains(route, "{answer_id}") {
		path["answer_id"] = "a1"
	}
	return Validate(context.Background(), req, op, path, []byte(body))
}

func TestUnitValidateMapsSchemaViolations(t *testing.T) {
	key := map[string]string{"Idempotency-Key": "0190c3a0-1b2c-7d3e-8f40-123456789abc"}
	if d := validate(t, "POST", "/v1/auth/phone", "/v1/auth/phone", `{"phone":"+79001234567"}`, key); d != nil {
		t.Fatalf("valid: %+v", d)
	}
	for _, tc := range []struct {
		name, method, route, target, body string
		header                            map[string]string
		want                              []Detail
	}{
		{"pattern", "POST", "/v1/auth/phone", "/v1/auth/phone", `{"phone":"89001234567"}`, key, []Detail{{"phone", DetailInvalidFormat, "has invalid format"}}},
		{"missing property", "POST", "/v1/auth/phone", "/v1/auth/phone", `{}`, key, []Detail{{"phone", DetailRequired, "is required"}}},
		{"type", "POST", "/v1/auth/register", "/v1/auth/register", `{"registration_token":1,"pd_consent_accepted":"yes","pd_consent_version":"v"}`, key,
			[]Detail{{"pd_consent_accepted", DetailInvalidFormat, "has invalid format"}, {"registration_token", DetailInvalidFormat, "has invalid format"}}},
		{"missing header", "POST", "/v1/auth/phone", "/v1/auth/phone", `{"phone":"+79001234567"}`, nil, []Detail{{"Idempotency-Key", DetailRequired, "is required"}}},
		{"empty body", "POST", "/v1/auth/token", "/v1/auth/token", ``, key, []Detail{{"", DetailRequired, "is required"}}},
		{"not json", "POST", "/v1/auth/token", "/v1/auth/token", `{"refresh_token":`, key, []Detail{{"", DetailInvalidFormat, "has invalid format"}}},
		{"length", "POST", "/v1/assistant/messages", "/v1/assistant/messages", `{"text":""}`, key, []Detail{{"text", DetailOutOfRange, "is out of range"}}},
		{"query range", "GET", "/v1/assistant/messages", "/v1/assistant/messages?limit=51", ``, nil, []Detail{{"limit", DetailOutOfRange, "is out of range"}}},
		{"query type", "GET", "/v1/assistant/messages", "/v1/assistant/messages?limit=many", ``, nil, []Detail{{"limit", DetailInvalidFormat, "has invalid format"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := validate(t, tc.method, tc.route, tc.target, tc.body, tc.header)
			if len(got) != len(tc.want) {
				t.Fatalf("%+v", got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("%+v", got)
				}
			}
		})
	}
	// Content-Type other than application/json.
	op := operation(t, "POST", "/v1/auth/token")
	req := httptest.NewRequest("POST", "/v1/auth/token", strings.NewReader(`refresh_token=x`))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-Client-Version", "android/0.2.5+41")
	req.Header.Set("Idempotency-Key", key["Idempotency-Key"])
	if d := Validate(context.Background(), req, op, nil, []byte(`refresh_token=x`)); len(d) != 1 || d[0].Code != DetailInvalidFormat {
		t.Fatalf("content type: %+v", d)
	}
}

func TestUnitValidateCountsCodePoints(t *testing.T) {
	key := map[string]string{"Idempotency-Key": "0190c3a0-1b2c-7d3e-8f40-123456789abc"}
	astral := strings.Repeat("😀", 2000) // 4000 UTF-16 units, 2000 characters (docs/api.md 7.1)
	if d := validate(t, "POST", "/v1/assistant/messages", "/v1/assistant/messages", `{"text":"`+astral+`"}`, key); d != nil {
		t.Fatalf("2000 characters accepted: %+v", d)
	}
	if d := validate(t, "POST", "/v1/assistant/messages", "/v1/assistant/messages", `{"text":"`+astral+`😀"}`, key); len(d) != 1 || d[0].Code != DetailOutOfRange {
		t.Fatalf("2001 characters refused: %+v", d)
	}
}

func TestUnitReadBodyBoundsTheBody(t *testing.T) {
	withBody := operation(t, "POST", "/v1/auth/token")
	without := operation(t, "POST", "/v1/assistant/messages/{answer_id}/retry")
	req := func(body io.Reader, length int64) *http.Request {
		r := httptest.NewRequest("POST", "/", body)
		r.ContentLength = length
		return r
	}
	if b, e := ReadBody(req(strings.NewReader(`{"a":1}`), 7), withBody, 16); e != nil || string(b) != `{"a":1}` {
		t.Fatal(e)
	}
	if _, e := ReadBody(req(strings.NewReader(""), 17), withBody, 16); e == nil || e.Status != 413 || e.Code != "REQUEST_TOO_LARGE" {
		t.Fatalf("Content-Length over the budget: %+v", e)
	}
	if _, e := ReadBody(req(strings.NewReader(strings.Repeat("x", 17)), -1), withBody, 16); e == nil || e.Status != 413 {
		t.Fatalf("chunked body over the budget: %+v", e)
	}
	if _, e := ReadBody(req(iotest.ErrReader(io.ErrUnexpectedEOF), -1), withBody, 16); e == nil || e.Status != 400 {
		t.Fatalf("unfinished body: %+v", e)
	}
	if b, e := ReadBody(req(http.NoBody, 0), without, 16); e != nil || b != nil {
		t.Fatal(e)
	}
	for _, r := range []*http.Request{req(strings.NewReader("{}"), 2), req(strings.NewReader("x"), -1)} {
		if _, e := ReadBody(r, without, 16); e == nil || e.Status != 400 || e.Code != "VALIDATION_ERROR" {
			t.Fatalf("unexpected body: %+v", e)
		}
	}
}

// handlerConfig is a Handler config without identity: Authenticate marks the request.
func handlerConfig(t *testing.T) Config {
	t.Helper()
	m, _ := newTestMetrics(t)
	return Config{Spec: spec(t), BodyLimit: MinBodyLimit, Metrics: m, Log: slog.New(slog.DiscardHandler), Clock: time.Now,
		Streams: NewStreamLimiter(StreamLimits{Max: 1, PerIP: 1, RetryAfter: time.Second}),
		Gates:   Gates{MinVersions: map[string]Version{"android": {"0", "2", "5"}}, Clock: time.Now},
		Authenticate: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer good" {
					InvalidToken().Write(w)
					return
				}
				next.ServeHTTP(w, r)
			})
		}}
}

func TestUnitHandlerOrdersChecksAndRoutes(t *testing.T) {
	var calls []*Call
	record := func(c *Call) { calls = append(calls, c); WriteNoContent(c.W) }
	h, err := Handler(handlerConfig(t), []Route{
		{Method: "POST", Path: "/v1/auth/phone", Idempotent: true, Handle: record},
		{Method: "GET", Path: "/v1/profile", Protected: Always, Handle: record},
		{Method: "POST", Path: "/v1/assistant/messages/{answer_id}/retry", Protected: Always, Idempotent: true, Handle: record},
	})
	if err != nil {
		t.Fatal(err)
	}
	const key = "0190c3a0-1b2c-7d3e-8f40-123456789abc"
	serve := func(method, target, body string, header map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("X-Client-Version", "android/0.2.5+41")
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
		h.ServeHTTP(res, req)
		return res
	}
	for _, tc := range []struct {
		name, method, target, body string
		header                     map[string]string
		status                     int
		code                       string
	}{
		{"unknown v1 path", "GET", "/v1/unknown", "", nil, 404, "NOT_FOUND"},
		{"unserved method", "PATCH", "/v1/profile", "{}", nil, 404, "NOT_FOUND"},
		{"version before routing", "GET", "/v1/unknown", "", map[string]string{"X-Client-Version": ""}, 400, "CLIENT_VERSION_UNSUPPORTED"},
		{"token before validation", "POST", "/v1/assistant/messages/a1/retry", "{}", nil, 401, "INVALID_TOKEN"},
		{"version before token", "GET", "/v1/profile", "", map[string]string{"X-Client-Version": "android/0.1.0+1"}, 400, "CLIENT_VERSION_UNSUPPORTED"},
		{"missing key", "POST", "/v1/auth/phone", `{"phone":"+79001234567"}`, nil, 400, "VALIDATION_ERROR"},
		{"malformed key", "POST", "/v1/auth/phone", `{"phone":"+79001234567"}`, map[string]string{"Idempotency-Key": "k-1"}, 400, "VALIDATION_ERROR"},
		{"body too large", "POST", "/v1/auth/phone", `{"phone":"` + strings.Repeat("1", MinBodyLimit) + `"}`, map[string]string{"Idempotency-Key": key}, 413, "REQUEST_TOO_LARGE"},
		{"body on a bodiless operation", "POST", "/v1/assistant/messages/a1/retry", "{}", map[string]string{"Authorization": "Bearer good", "Idempotency-Key": key}, 400, "VALIDATION_ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(calls)
			res := serve(tc.method, tc.target, tc.body, tc.header)
			if res.Code != tc.status || decodeError(t, res.Body.String()).Code != tc.code || len(calls) != before {
				t.Fatalf("%d %s", res.Code, res.Body.String())
			}
			if !IsUUID(res.Header().Get("X-Request-Id")) {
				t.Fatal("no request id")
			}
		})
	}
	if res := serve("GET", "/healthz", "", nil); res.Code != 404 || strings.Contains(res.Body.String(), "error") {
		t.Fatalf("paths outside /v1 are plain 404 and no operational endpoint: %d %s", res.Code, res.Body.String())
	}
	res := serve("POST", "/v1/assistant/messages/a1/retry", "", map[string]string{"Authorization": "Bearer good", "Idempotency-Key": key, "X-Read-Primary": "1"})
	if res.Code != 204 || len(calls) != 1 {
		t.Fatalf("%d %s", res.Code, res.Body.String())
	}
	c := calls[0]
	h0 := HintsFrom(c.R.Context())
	if h0.IdempotencyKey != key || h0.ClientVersion != "android/0.2.5+41" || !h0.ReadPrimary || c.R.PathValue("answer_id") != "a1" || reqctx.RequestID(c.R.Context()) == "" || c.Op.Route.Path != "/v1/assistant/messages/{answer_id}/retry" {
		t.Fatalf("%+v", h0)
	}
	if res := serve("POST", "/v1/auth/phone", `{"phone":"+79001234567"}`, map[string]string{"Idempotency-Key": key}); res.Code != 204 || string(calls[1].Body) != `{"phone":"+79001234567"}` {
		t.Fatalf("anonymous operation: %d %s", res.Code, res.Body.String())
	}
	if _, err := Handler(handlerConfig(t), []Route{{Method: "GET", Path: "/v1/nothing"}}); err == nil {
		t.Fatal("an operation outside the contract is refused")
	}
}

func TestUnitCallFailTranslates(t *testing.T) {
	m, _ := newTestMetrics(t)
	res := httptest.NewRecorder()
	c := &Call{W: res, R: httptest.NewRequest("GET", "/", nil), Op: operation(t, "GET", "/v1/profile"), Metrics: m, Log: slog.New(slog.DiscardHandler)}
	c.Fail(context.DeadlineExceeded)
	if res.Code != 500 || decodeError(t, res.Body.String()).Code != "INTERNAL_ERROR" {
		t.Fatalf("%d %s", res.Code, res.Body.String())
	}
}
