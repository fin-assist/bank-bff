package app

import (
	"context"
	"strings"
	"testing"
	"time"

	userv1 "github.com/fin-assist/contracts/gen/go/finassist/user/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/fin-assist/bank-bff/internal/testpeer"
)

const (
	idemKey   = "0190c3a0-1b2c-7d3e-8f40-123456789abc"
	requestID = "0190c3a0-1b2c-7d3e-8f40-000000000001"
)

// peerError is one internal failure and the REST answer the contract requires.
type peerError struct {
	err    error
	status int
	code   string
}

func registry(errs ...error) []peerError {
	out := make([]peerError, 0, len(errs)+1)
	for _, err := range errs {
		r, ok := finerr.ToREST(err)
		if !ok {
			panic(err)
		}
		out = append(out, peerError{err, r.Spec.HTTPStatus, r.Code})
	}
	// A peer outage and an unknown reason are a generic 500 (grpc-api.md 5).
	return append(out, peerError{status.Error(codes.Unavailable, "dial tcp 10.0.0.7: refused"), 500, "INTERNAL_ERROR"})
}

func retryAt() time.Time { return time.Now().Add(10 * time.Minute).Truncate(time.Second) }

// lastCall returns the last received call of method and checks the trusted
// metadata the BFF must send: its own verified service identity, the request ID,
// the client hints, and the user token only when the route verified it.
func lastCall(t *testing.T, b *bff, method, userToken, key string) testpeer.Call {
	t.Helper()
	calls := b.peers.Calls(method)
	if len(calls) == 0 {
		t.Fatalf("%s was not called", method)
	}
	c := calls[len(calls)-1]
	want := map[string]string{"x-request-id": requestID, "x-client-ip": "127.0.0.1", "x-client-version": clientVersion}
	if userToken != "" {
		want["authorization"] = "Bearer " + userToken
	}
	if key != "" {
		want["idempotency-key"] = key
	}
	for k, v := range want {
		if got := c.MD.Get(k); len(got) != 1 || got[0] != v {
			t.Fatalf("%s %s: %v, want %q", method, k, got, v)
		}
	}
	if c.Caller != "bank-bff" {
		t.Fatalf("%s: service token not verified as bank-bff", method)
	}
	if userToken == "" && len(c.MD.Get("authorization")) != 0 {
		t.Fatalf("%s: an unverified Authorization reached the service", method)
	}
	if key == "" && len(c.MD.Get("idempotency-key")) != 0 {
		t.Fatalf("%s: idempotency-key without the contract", method)
	}
	for _, k := range []string{"x-verification-token", "cookie", "x-forwarded-for"} {
		if len(c.MD.Get(k)) != 0 {
			t.Fatalf("%s: header %s forwarded", method, k)
		}
	}
	return c
}

func base(header map[string]string) map[string]string {
	out := map[string]string{"X-Request-Id": requestID}
	for k, v := range header {
		out[k] = v
	}
	return out
}

func scenarioStartPhone(t *testing.T, start starter) {
	b := start(t, nil)
	var got *userv1.StartPhoneVerificationRequest
	b.peers.Auth.Set(func(a *testpeer.Auth) {
		a.StartFunc = func(_ context.Context, in *userv1.StartPhoneVerificationRequest) (*userv1.StartPhoneVerificationResponse, error) {
			got = in
			return &userv1.StartPhoneVerificationResponse{VerificationToken: "vt", CallbackNumber: "+74951234567",
				ExpiresAt: timestamppb.New(time.Date(2026, 10, 4, 19, 17, 0, 0, time.UTC)), ExpiresIn: proto.Int32(300)}, nil
		}
	})
	ok := request{method: "POST", path: "/v1/auth/phone", route: "/v1/auth/phone", body: `{"phone":"+79001234567"}`,
		header: base(map[string]string{"Idempotency-Key": idemKey, "Authorization": "Bearer " + b.keys.Access("u1"), "X-Forwarded-For": "1.2.3.4"})}
	r := b.do(t, ok)
	if r.StatusCode != 200 || r.json["expires_at"] != "2026-10-04T22:17:00+03:00" || r.json["expires_in"] != 300.0 || got.GetPhone() != "+79001234567" || r.Header.Get("X-Request-Id") != requestID {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	lastCall(t, b, userv1.AuthService_StartPhoneVerification_FullMethodName, "", idemKey)
	for _, pe := range registry(finerr.ValidationError(&errdetails.BadRequest_FieldViolation{Field: "phone", Reason: "INVALID_FORMAT", Description: "+7 and 10 digits"}),
		finerr.IdempotencyConflict(), finerr.RequestInProgress(3*time.Second), finerr.RateLimited(retryAt())) {
		b.peers.Auth.Set(func(a *testpeer.Auth) {
			a.StartFunc = func(context.Context, *userv1.StartPhoneVerificationRequest) (*userv1.StartPhoneVerificationResponse, error) {
				return nil, pe.err
			}
		})
		r := b.do(t, ok)
		if r.StatusCode != pe.status || errorCode(r) != pe.code {
			t.Fatalf("%v: %d %s", pe.err, r.StatusCode, r.body)
		}
		// Neither a status message nor a violation description of the service reaches
		// the client: the detail message is the BFF's own.
		if strings.Contains(string(r.body), "10.0.0.7") || strings.Contains(string(r.body), "digits") {
			t.Fatalf("internal text leaked: %s", r.body)
		}
		if pe.code == "VALIDATION_ERROR" {
			d := r.json["error"].(map[string]any)["details"].([]any)[0].(map[string]any)
			if d["field"] != "phone" || d["code"] != "INVALID_FORMAT" || d["message"] != "has invalid format" {
				t.Fatalf("detail %v", d)
			}
		}
	}
	calls := len(b.peers.Calls(userv1.AuthService_StartPhoneVerification_FullMethodName))
	for _, bad := range []request{
		{body: `{"phone":"89001234567"}`, header: base(map[string]string{"Idempotency-Key": idemKey})},
		{body: `{"phone":"+79001234567"}`, header: base(nil)},
		{body: `{"phone":"+79001234567"}`, header: base(map[string]string{"Idempotency-Key": "not-a-uuid"})},
		{body: `{"phone":`, header: base(map[string]string{"Idempotency-Key": idemKey})},
		{body: `{"phone":"+79001234567"}`, header: base(map[string]string{"Idempotency-Key": idemKey, "Content-Type": "text/plain"})},
	} {
		bad.method, bad.path, bad.route = "POST", "/v1/auth/phone", "/v1/auth/phone"
		if r := b.do(t, bad); r.StatusCode != 400 || errorCode(r) != "VALIDATION_ERROR" {
			t.Fatalf("%v: %d %s", bad, r.StatusCode, r.body)
		}
	}
	if n := len(b.peers.Calls(userv1.AuthService_StartPhoneVerification_FullMethodName)); n != calls {
		t.Fatal("a request refused by the BFF reached the service")
	}
}

// scenarioRequestTooLarge: a body over the 64 KiB budget is refused before the
// service with the contract's 413 REQUEST_TOO_LARGE on all six JSON operations.
func scenarioRequestTooLarge(t *testing.T, start starter) {
	b := start(t, nil)
	for _, route := range [][2]string{{"POST", "/v1/auth/phone"}, {"POST", "/v1/auth/register"}, {"POST", "/v1/auth/token"}, {"POST", "/v1/auth/logout"}, {"PUT", "/v1/profile/assistant-consent"}, {"POST", "/v1/assistant/messages"}} {
		big := request{method: route[0], path: route[1], route: route[1], body: `{"phone":"` + strings.Repeat("1", 70000) + `"}`,
			header: base(map[string]string{"Idempotency-Key": idemKey, "Authorization": "Bearer " + b.keys.Access("u1")})}
		got := b.do(t, big)
		if got.StatusCode != 413 || errorCode(got) != "REQUEST_TOO_LARGE" {
			t.Fatalf("%v: %d %s", route, got.StatusCode, got.body)
		}
	}
	// The token is checked before the body: without Authorization an oversized body and
	// a body outside the schema are both 401, not 413 or 400.
	for _, body := range []string{`{"text":"` + strings.Repeat("1", 70000) + `"}`, `{"text":5}`} {
		r := b.do(t, request{method: "POST", path: "/v1/assistant/messages", route: "/v1/assistant/messages", body: body,
			header: base(map[string]string{"Idempotency-Key": idemKey})})
		if r.StatusCode != 401 || errorCode(r) != "INVALID_TOKEN" {
			t.Fatalf("no token, %d-byte body: %d %s", len(body), r.StatusCode, r.body)
		}
	}
	for _, c := range b.peers.Calls("") {
		if c.Method != userv1.UserInternalService_GetSigningKeys_FullMethodName {
			t.Fatalf("an oversized body reached %s", c.Method)
		}
	}
}

func scenarioWatchPhone(t *testing.T, start starter) {
	b := start(t, nil, WithStreamTimings(20*time.Millisecond, time.Second, 2*time.Second))
	var token string
	b.peers.Auth.Set(func(a *testpeer.Auth) {
		a.WatchFunc = func(in *userv1.WatchPhoneVerificationRequest, s grpc.ServerStreamingServer[userv1.WatchPhoneVerificationResponse]) error {
			token = in.GetVerificationToken()
			if in.GetVerificationToken() == "limited" {
				return finerr.RateLimited(retryAt())
			}
			if in.GetVerificationToken() != "vt" {
				return finerr.InvalidToken()
			}
			_ = s.Send(&userv1.WatchPhoneVerificationResponse{Status: userv1.VerificationStatus_VERIFICATION_STATUS_PENDING})
			// The call comes later than several heartbeat intervals.
			<-time.After(100 * time.Millisecond)
			return s.Send(&userv1.WatchPhoneVerificationResponse{Status: userv1.VerificationStatus_VERIFICATION_STATUS_VERIFIED, IsNewUser: proto.Bool(false),
				Tokens: &userv1.TokenPair{AccessToken: "at", RefreshToken: "rt"}})
		}
	})
	r := request{method: "GET", path: "/v1/auth/phone/events", route: "/v1/auth/phone/events", header: base(map[string]string{"X-Verification-Token": "vt"})}
	got := b.raw(t, r)
	resp, body := got.Response, got.body
	events, pings := b.contract.Events(t, "GET", r.route, strings.NewReader(string(body)))
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("Date") == "" || len(events) != 2 ||
		events[0].Data["status"] != "pending" || events[1].Data["access_token"] != "at" || token != "vt" || pings == 0 {
		t.Fatalf("%d %v %d %s", resp.StatusCode, events, pings, body)
	}
	c := lastCall(t, b, userv1.AuthService_WatchPhoneVerification_FullMethodName, "", "")
	if len(c.MD.Get("x-verification-token")) != 0 {
		t.Fatal("the secret travels in the request message, not metadata")
	}
	for tok, want := range map[string]peerError{"bad": {status: 401, code: "INVALID_TOKEN"}, "limited": {status: 429, code: "RATE_LIMITED"}, "": {status: 401, code: "INVALID_TOKEN"}} {
		r.header = base(map[string]string{"X-Verification-Token": tok})
		got := b.do(t, r)
		if got.StatusCode != want.status || errorCode(got) != want.code || (want.status == 429) != (got.Header.Get("Retry-After") != "") {
			t.Fatalf("token %q: %d %s", tok, got.StatusCode, got.body)
		}
	}
}

func scenarioRegisterRefreshLogout(t *testing.T, start starter) {
	b := start(t, nil)
	keyed := func(path, body string, header map[string]string) request {
		h := base(map[string]string{"Idempotency-Key": idemKey})
		for k, v := range header {
			h[k] = v
		}
		return request{method: "POST", path: path, route: path, body: body, header: h}
	}
	b.peers.Auth.Set(func(a *testpeer.Auth) {
		a.RegisterFunc = func(_ context.Context, in *userv1.RegisterRequest) (*userv1.RegisterResponse, error) {
			if !in.GetPdConsentAccepted() {
				return nil, finerr.ConsentRequired()
			}
			return &userv1.RegisterResponse{Tokens: &userv1.TokenPair{AccessToken: "a", RefreshToken: "r"}, UserId: "u1"}, nil
		}
		a.RefreshFunc = func(context.Context, *userv1.RefreshTokensRequest) (*userv1.RefreshTokensResponse, error) {
			return &userv1.RefreshTokensResponse{Tokens: &userv1.TokenPair{AccessToken: "a2", RefreshToken: "r2"}}, nil
		}
		a.LogoutFunc = func(context.Context, *userv1.LogoutRequest) (*userv1.LogoutResponse, error) {
			return &userv1.LogoutResponse{}, nil
		}
	})
	register := keyed("/v1/auth/register", `{"registration_token":"reg","pd_consent_accepted":true,"pd_consent_version":"2026-09-28","timezone":"Europe/Moscow"}`, nil)
	if r := b.do(t, register); r.StatusCode != 201 || r.json["user_id"] != "u1" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	lastCall(t, b, userv1.AuthService_Register_FullMethodName, "", idemKey)
	if r := b.do(t, keyed("/v1/auth/register", `{"registration_token":"reg","pd_consent_accepted":false,"pd_consent_version":"v"}`, nil)); r.StatusCode != 403 || errorCode(r) != "CONSENT_REQUIRED" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	for _, pe := range registry(finerr.InvalidToken(), finerr.TokenExpired(), finerr.ConsentOutdated(), finerr.IdempotencyConflict(), finerr.RateLimited(retryAt()),
		finerr.ValidationError(&errdetails.BadRequest_FieldViolation{Field: "timezone", Reason: "INVALID_FORMAT"})) {
		b.peers.Auth.Set(func(a *testpeer.Auth) {
			a.RegisterFunc = func(context.Context, *userv1.RegisterRequest) (*userv1.RegisterResponse, error) { return nil, pe.err }
			a.RefreshFunc = func(context.Context, *userv1.RefreshTokensRequest) (*userv1.RefreshTokensResponse, error) {
				return nil, pe.err
			}
		})
		if r := b.do(t, register); r.StatusCode != pe.status || errorCode(r) != pe.code {
			t.Fatalf("register %v: %d %s", pe.err, r.StatusCode, r.body)
		}
		refresh := b.do(t, keyed("/v1/auth/token", `{"refresh_token":"rt"}`, nil))
		want := pe
		if pe.code == "CONSENT_OUTDATED" {
			want = peerError{status: 500, code: "INTERNAL_ERROR"} // 422 is not declared by /v1/auth/token
		}
		if refresh.StatusCode != want.status || errorCode(refresh) != want.code {
			t.Fatalf("refresh %v: %d %s", pe.err, refresh.StatusCode, refresh.body)
		}
	}
	b.peers.Auth.Set(func(a *testpeer.Auth) {
		a.RefreshFunc = func(context.Context, *userv1.RefreshTokensRequest) (*userv1.RefreshTokensResponse, error) {
			return &userv1.RefreshTokensResponse{Tokens: &userv1.TokenPair{AccessToken: "a2", RefreshToken: "r2"}}, nil
		}
	})
	if r := b.do(t, keyed("/v1/auth/token", `{"refresh_token":"rt"}`, nil)); r.StatusCode != 200 || r.json["refresh_token"] != "r2" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	lastCall(t, b, userv1.AuthService_RefreshTokens_FullMethodName, "", idemKey)
	if r := b.do(t, keyed("/v1/auth/token", `{}`, nil)); r.StatusCode != 400 {
		t.Fatalf("missing refresh_token: %d", r.StatusCode)
	}
	access := b.keys.Access("u1")
	logout := request{method: "POST", path: "/v1/auth/logout", route: "/v1/auth/logout", body: `{"refresh_token":"rt"}`, header: base(map[string]string{"Authorization": "Bearer " + access})}
	if r := b.do(t, logout); r.StatusCode != 204 || len(r.body) != 0 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	lastCall(t, b, userv1.AuthService_Logout_FullMethodName, access, "")
	calls := len(b.peers.Calls(userv1.AuthService_Logout_FullMethodName))
	logout.header = base(nil)
	if r := b.do(t, logout); r.StatusCode != 401 || errorCode(r) != "INVALID_TOKEN" || len(b.peers.Calls(userv1.AuthService_Logout_FullMethodName)) != calls {
		t.Fatalf("logout without token: %d", r.StatusCode)
	}
}

func scenarioConsentDocuments(t *testing.T, start starter) {
	b := start(t, nil)
	b.peers.Consents.Set(func(c *testpeer.Consents) {
		c.GetFunc = func(_ context.Context, in *userv1.GetConsentDocumentRequest) (*userv1.GetConsentDocumentResponse, error) {
			return &userv1.GetConsentDocumentResponse{Document: &userv1.ConsentDocument{Type: in.GetType(), Version: "2026-09-28", Title: "Согласие", Url: "https://example.ru/legal/pd"}}, nil
		}
	})
	get := func(kind string, header map[string]string) response {
		return b.do(t, request{method: "GET", path: "/v1/consents/" + kind, route: "/v1/consents/{type}", header: base(header)})
	}
	access := b.keys.Access("u1")
	if r := get("personal_data", map[string]string{"Authorization": "Bearer forged"}); r.StatusCode != 200 || r.json["type"] != "personal_data" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	lastCall(t, b, userv1.ConsentDocumentService_GetConsentDocument_FullMethodName, "", "")
	if r := get("assistant", map[string]string{"Authorization": "Bearer " + access}); r.StatusCode != 200 || r.json["type"] != "assistant" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	lastCall(t, b, userv1.ConsentDocumentService_GetConsentDocument_FullMethodName, access, "")
	if r := get("assistant", nil); r.StatusCode != 401 {
		t.Fatalf("assistant document without token: %d", r.StatusCode)
	}
	if r := get("marketing", nil); r.StatusCode != 404 || errorCode(r) != "NOT_FOUND" {
		t.Fatalf("unknown type: %d", r.StatusCode)
	}
	for _, pe := range registry(finerr.InvalidToken(), finerr.NotFound(), finerr.RateLimited(retryAt())) {
		b.peers.Consents.Set(func(c *testpeer.Consents) {
			c.GetFunc = func(context.Context, *userv1.GetConsentDocumentRequest) (*userv1.GetConsentDocumentResponse, error) {
				return nil, pe.err
			}
		})
		if r := get("assistant", map[string]string{"Authorization": "Bearer " + access}); r.StatusCode != pe.status || errorCode(r) != pe.code {
			t.Fatalf("%v: %d %s", pe.err, r.StatusCode, r.body)
		}
	}
}
