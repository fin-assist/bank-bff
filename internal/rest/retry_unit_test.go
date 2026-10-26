package rest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

func retryFlag(t *testing.T, w *httptest.ResponseRecorder, want bool) errorObject {
	t.Helper()
	body := decodeError(t, w.Body.String())
	if body.Retryable == nil || *body.Retryable != want {
		t.Fatalf("retryable must be explicit %v, got %s", want, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"retryable":null`) {
		t.Fatal("retryable must not be null")
	}
	return body
}
func TestUnitRetryablePresenceAndLocalRefusals(t *testing.T) {
	// The optional wire model can decode an older server. The new writer always gives a conservative boolean.
	for _, input := range []string{`{"error":{"code":"RATE_LIMITED"}}`, `{"error":{"code":"RATE_LIMITED","retryable":false}}`, `{"error":{"code":"RATE_LIMITED","retryable":true}}`} {
		var value errorBody
		if err := json.Unmarshal([]byte(input), &value); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), `"retryable":null`) {
			t.Fatal(string(encoded))
		}
		if strings.Contains(input, "retryable") != strings.Contains(string(encoded), "retryable") {
			t.Fatal("legacy absence lost", string(encoded))
		}
	}
	for _, e := range []Error{Internal(), Validation(), NotFound(), InvalidToken(), TooLarge(), {Status: 503, Code: "MAINTENANCE"}, {Status: 400, Code: "CLIENT_VERSION_UNSUPPORTED"}} {
		w := httptest.NewRecorder()
		e.Write(w)
		retryFlag(t, w, false)
	}
	for _, flag := range []bool{false, true} {
		w := httptest.NewRecorder()
		Error{Status: 429, Code: "RATE_LIMITED", Retryable: &flag}.Write(w)
		retryFlag(t, w, flag)
	}
}
func TestUnitRequestRetryRequiresBothGuarantees(t *testing.T) {
	cases := []struct {
		method, path, target, body string
		keyed, safe                bool
	}{
		{"GET", "/v1/auth/phone/events", "/v1/auth/phone/events", "", false, true},
		{"GET", "/v1/consents/{type}", "/v1/consents/personal_data", "", false, true},
		{"GET", "/v1/profile", "/v1/profile", "", false, true},
		{"GET", "/v1/assistant/messages", "/v1/assistant/messages", "", false, true},
		{"GET", "/v1/assistant/messages/{answer_id}/events", "/v1/assistant/messages/a1/events", "", false, true},
		{"GET", "/v1/assistant/limit", "/v1/assistant/limit", "", false, true},
		{"PUT", "/v1/profile/assistant-consent", "/v1/profile/assistant-consent", `{"version":"v1"}`, false, true},
		{"DELETE", "/v1/profile/assistant-consent", "/v1/profile/assistant-consent", "", false, true},
		{"GET", "/v1/profile", "/v1/profile", "", false, false},
		{"POST", "/v1/auth/phone", "/v1/auth/phone", `{"phone":"+79001234567"}`, true, false},
		{"POST", "/v1/auth/register", "/v1/auth/register", `{"registration_token":"registration","pd_consent_accepted":true,"pd_consent_version":"v1"}`, true, false},
		{"POST", "/v1/auth/token", "/v1/auth/token", `{"refresh_token":"refresh"}`, true, false},
		{"POST", "/v1/auth/logout", "/v1/auth/logout", `{"refresh_token":"refresh"}`, false, false},
		{"POST", "/v1/assistant/messages", "/v1/assistant/messages", `{"text":"Вопрос"}`, true, false},
		{"POST", "/v1/assistant/messages/{answer_id}/retry", "/v1/assistant/messages/a1/retry", "", true, false},
		// A declaration cannot turn a UUID into evidence of its age or cached entry, even for an otherwise safe-looking route.
		{"POST", "/v1/auth/token", "/v1/auth/token", `{"refresh_token":"refresh"}`, true, true},
	}
	for _, tc := range cases {
		for _, advice := range []string{"absent", "false", "true"} {
			t.Run(fmt.Sprintf("%s%s/safe=%t/%s", tc.method, tc.path, tc.safe, advice), func(t *testing.T) {
				cfg := handlerConfig(t)
				peerErr := finerr.RateLimited(time.Now().Add(time.Minute))
				if advice != "absent" {
					peerErr = finerr.WithRequestRetryInfo(peerErr, advice == "true")
				}
				calls := 0
				handler, err := Handler(cfg, []Route{{Method: tc.method, Path: tc.path, Idempotent: tc.keyed, ReplaySafe: tc.safe, Handle: func(c *Call) { calls++; c.Fail(peerErr) }}})
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
				req.Header.Set("X-Client-Version", "android/0.2.5+41")
				req.Header.Set("X-Verification-Token", "verification")
				// A canonical UUIDv7-looking key is no evidence of first use, storage or either TTL.
				req.Header.Set("Idempotency-Key", "0190c3a0-1b2c-7d3e-8f40-123456789abc")
				if tc.body != "" {
					req.Header.Set("Content-Type", "application/json")
				}
				w := httptest.NewRecorder()
				date := time.Now().UTC().Format(http.TimeFormat)
				w.Header().Set("Date", date)
				handler.ServeHTTP(w, req)
				if w.Code != 429 || calls != 1 {
					t.Fatalf("HTTP%d calls%d: %s", w.Code, calls, w.Body.String())
				}
				retryFlag(t, w, tc.safe && !tc.keyed && advice == "true")
				if w.Header().Get("Date") != date || w.Header().Get("Retry-After") == "" || w.Header().Get("Content-Type") != "application/json" {
					t.Fatal("retry translation changed timing or pre-SSE response", w.Header())
				}
			})
		}
	}
}
func TestUnitUnboundCallAndTranslationCannotGrantRetry(t *testing.T) {
	m, _ := newTestMetrics(t)
	err := finerr.WithRequestRetryInfo(finerr.RateLimited(time.Now().Add(time.Minute)), true)
	w := httptest.NewRecorder()
	m.FromGRPC(err, allStatuses).Write(w)
	retryFlag(t, w, false)
	// Merely supplying a GET and a contract object is not proof that the validated route declared whole-operation safety.
	cfg := handlerConfig(t)
	w = httptest.NewRecorder()
	c := Call{W: w, R: httptest.NewRequest("GET", "/v1/profile", nil), Op: operation(t, "GET", "/v1/profile"), Metrics: cfg.Metrics, Log: cfg.Log}
	c.Fail(err)
	retryFlag(t, w, false)
	// A nonmatching request cannot acquire the route's declaration.
	route := Route{Method: "GET", Path: "/v1/profile", ReplaySafe: true, Handle: func(c *Call) { c.Fail(err) }}
	w = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/profile", nil)
	req.Header.Set("X-Client-Version", "android/0.2.5+41")
	routeHandler(cfg, route, operation(t, "GET", "/v1/profile")).ServeHTTP(w, req)
	retryFlag(t, w, false)
}
func TestUnitMalformedRetryStatusCannotAuthorizeRequest(t *testing.T) {
	m, _ := newTestMetrics(t)
	valid := status.Convert(finerr.WithRequestRetryInfo(finerr.NotFound(), true)).Proto()
	duplicate := proto.Clone(valid).(*statuspb.Status)
	duplicate.Details = append(duplicate.Details, proto.Clone(valid.Details[len(valid.Details)-1]).(*anypb.Any))
	malformed := proto.Clone(valid).(*statuspb.Status)
	malformed.Details[len(malformed.Details)-1].Value = []byte{0xff}
	mismatch := proto.Clone(valid).(*statuspb.Status)
	mismatch.Code = int32(codes.ResourceExhausted)
	unknown := proto.Clone(valid).(*statuspb.Status)
	// A known permission does not make an unknown unary reason usable.
	info := new(errdetails.ErrorInfo)
	if err := unknown.Details[0].UnmarshalTo(info); err != nil {
		t.Fatal(err)
	}
	info.Reason = "FUTURE_REASON"
	encoded, err := anypb.New(info)
	if err != nil {
		t.Fatal(err)
	}
	unknown.Details[0] = encoded
	for _, wire := range []*statuspb.Status{duplicate, malformed, mismatch, unknown} {
		e := m.FromGRPC(status.FromProto(wire).Err(), allStatuses)
		w := httptest.NewRecorder()
		e.Write(w)
		if w.Code != 500 || decodeError(t, w.Body.String()).Code != "INTERNAL_ERROR" {
			t.Fatal(w.Code, w.Body.String())
		}
		retryFlag(t, w, false)
	}
	// Unknown extension payloads are allowed, without becoming permission on their own.
	valid.Details = append(valid.Details, &anypb.Any{TypeUrl: "type.googleapis.com/future.Extension", Value: []byte{0xff}})
	parsed, ok := finerr.ToREST(status.FromProto(valid).Err())
	if !ok || parsed.RequestRetryable == nil || !*parsed.RequestRetryable {
		t.Fatal("unknown extension invalidated known advice")
	}
	// A present proto3 default false detail is authoritative, not absence.
	denied, err := anypb.New(&commonv1.RequestRetryInfo{})
	if err != nil {
		t.Fatal(err)
	}
	valid.Details[1] = denied
	parsed, ok = finerr.ToREST(status.FromProto(valid).Err())
	if !ok || parsed.RequestRetryable == nil || *parsed.RequestRetryable {
		t.Fatal("explicit false lost its presence")
	}
}

func TestUnitHandlerRefusalRequiresPermissionAndWholeRequestProof(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body      string
		safe, keyed, permission, want bool
	}{
		{"temporary admission", "GET", "/v1/auth/phone/events", "", true, false, true, true},
		{"no handler permission", "GET", "/v1/auth/phone/events", "", true, false, false, false},
		{"no operation proof", "GET", "/v1/auth/phone/events", "", false, false, true, false},
		{"unknown key window", "POST", "/v1/auth/token", `{"refresh_token":"token"}`, true, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := handlerConfig(t)
			calls := 0
			handler, err := Handler(cfg, []Route{{Method: tc.method, Path: tc.path, ReplaySafe: tc.safe, Idempotent: tc.keyed, Handle: func(c *Call) { calls++; c.Refuse(c.Streams.Busy(c.Clock()), tc.permission) }}})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("X-Client-Version", "android/0.2.5+41")
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			req.Header.Set("Idempotency-Key", "0190c3a0-1b2c-7d3e-8f40-123456789abc")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			result := retryFlag(t, w, tc.want)
			if calls != 1 || w.Code != 429 || result.Code != "RATE_LIMITED" || result.RetryAt == "" || w.Header().Get("Retry-After") != "1" || w.Header().Get("Content-Type") != "application/json" {
				t.Fatal(calls, w.Code, w.Header(), w.Body.String())
			}
		})
	}
	// Neither a catalog-known refusal nor a pre-populated flag grants a declaration-less Call permission.
	flag := true
	w := httptest.NewRecorder()
	(&Call{W: w}).Refuse(Error{Status: 429, Code: "RATE_LIMITED", Retryable: &flag}, true)
	retryFlag(t, w, false)
	// Even a proven call cannot derive permission from the catalog or an earlier flag.
	w = httptest.NewRecorder()
	(&Call{W: w, replaySafe: true}).Refuse(Error{Status: 429, Code: "RATE_LIMITED", Retryable: &flag}, false)
	retryFlag(t, w, false)
	w = httptest.NewRecorder()
	Validation().Write(w)
	retryFlag(t, w, false)
}
