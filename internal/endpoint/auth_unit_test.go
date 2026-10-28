package endpoint

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	userv1 "github.com/fin-assist/contracts/gen/go/finassist/user/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/fin-assist/bank-bff/internal/rest"
)

func TestUnitStartPhonePreservesConsentAgeAndVersionAssertions(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	for _, accepted := range []bool{true, false} {
		for _, age := range []bool{true, false} {
			var got *userv1.StartPhoneVerificationRequest
			u.f.start = func(in *userv1.StartPhoneVerificationRequest) (*userv1.StartPhoneVerificationResponse, error) {
				got = in
				return &userv1.StartPhoneVerificationResponse{ExpiresAt: timestamppb.New(time.Now())}, nil
			}
			version := strings.Repeat("🙂", 64)
			body, _ := json.Marshal(map[string]any{"phone": "+79001234567", "pd_consent_accepted": accepted, "pd_consent_version": version, "age_confirmed": age,
				"PD_CONSENT_ACCEPTED": !accepted, "AGE_CONFIRMED": !age, "Pd_Consent_Version": "unvalidated-alias"})
			r := u.serve(t, "POST", "/v1/auth/phone", string(body), nil)
			if r.Code != 200 || got.GetPhone() != "+79001234567" || got.GetPdConsentAccepted() != accepted || got.GetPdConsentVersion() != version || got.GetAgeConfirmed() != age {
				t.Fatalf("REST assertions changed before USER: %d %+v", r.Code, got)
			}
		}
	}
}

func TestUnitStartPhoneStructuralReceiptFieldsRejectBeforeRPC(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	calls := 0
	u.f.start = func(*userv1.StartPhoneVerificationRequest) (*userv1.StartPhoneVerificationResponse, error) {
		calls++
		return nil, finerr.InternalError()
	}
	base := map[string]any{"phone": "+79001234567", "pd_consent_accepted": true, "pd_consent_version": "v", "age_confirmed": true}
	for _, field := range []string{"phone", "pd_consent_accepted", "pd_consent_version", "age_confirmed"} {
		for _, mode := range []string{"missing", "null", "type"} {
			t.Run(field+"/"+mode, func(t *testing.T) {
				in := make(map[string]any, len(base))
				for key, value := range base {
					in[key] = value
				}
				switch mode {
				case "missing":
					delete(in, field)
				case "null":
					in[field] = nil
				case "type":
					in[field] = 17
				}
				body, _ := json.Marshal(in)
				r := u.serve(t, "POST", "/v1/auth/phone", string(body), nil)
				if r.Code != 400 || r.code() != "VALIDATION_ERROR" || calls != 0 {
					t.Fatalf("invalid receipt reached USER: %d %v calls%d", r.Code, r.json, calls)
				}
				details := r.json["error"].(map[string]any)["details"].([]any)
				code := "INVALID_FORMAT"
				if mode == "missing" {
					code = "REQUIRED"
				}
				if len(details) != 1 || details[0].(map[string]any)["field"] != field || details[0].(map[string]any)["code"] != code {
					t.Fatal("structural field detail changed", details)
				}
			})
		}
	}
	for _, version := range []string{"", strings.Repeat("🙂", 65)} {
		base["pd_consent_version"] = version
		body, _ := json.Marshal(base)
		r := u.serve(t, "POST", "/v1/auth/phone", string(body), nil)
		if r.Code != 400 || r.code() != "VALIDATION_ERROR" || calls != 0 {
			t.Fatalf("out-of-bounds version reached USER: %d %v", r.Code, r.json)
		}
	}
}

func TestUnitStartPhone(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	expires := time.Date(2026, 10, 4, 19, 17, 0, 0, time.UTC)
	var got *userv1.StartPhoneVerificationRequest
	u.f.start = func(in *userv1.StartPhoneVerificationRequest) (*userv1.StartPhoneVerificationResponse, error) {
		got = in
		return &userv1.StartPhoneVerificationResponse{VerificationToken: "vt", CallbackNumber: "+74951234567", ExpiresAt: timestamppb.New(expires), ExpiresIn: proto.Int32(300)}, nil
	}
	r := u.serve(t, "POST", "/v1/auth/phone", `{"phone":"+79001234567","pd_consent_accepted":true,"pd_consent_version":"v","age_confirmed":true}`, map[string]string{"Authorization": ""})
	if r.Code != 200 || got.GetPhone() != "+79001234567" || r.json["expires_at"] != "2026-10-04T22:17:00+03:00" || r.json["expires_in"] != 300.0 || r.json["verification_token"] != "vt" {
		t.Fatalf("%d %v", r.Code, r.json)
	}
	if d := u.f.deadlines[0]; d <= 0 || d > UnaryDeadline {
		t.Fatalf("unary deadline %v", d)
	}
	u.f.start = func(*userv1.StartPhoneVerificationRequest) (*userv1.StartPhoneVerificationResponse, error) {
		return &userv1.StartPhoneVerificationResponse{ExpiresAt: timestamppb.New(expires)}, nil
	}
	if r := u.serve(t, "POST", "/v1/auth/phone", `{"phone":"+79001234567","pd_consent_accepted":true,"pd_consent_version":"v","age_confirmed":true}`, nil); r.Code != 200 || r.json["expires_in"] != nil {
		t.Fatalf("old server without expires_in: %v", r.json)
	}
	retryAt := time.Now().Add(time.Minute).Truncate(time.Second)
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{finerr.RateLimited(retryAt), 429, "RATE_LIMITED"},
		{finerr.ValidationError(&errdetails.BadRequest_FieldViolation{Field: "phone", Reason: "INVALID_FORMAT"}), 400, "VALIDATION_ERROR"},
		{finerr.ValidationError(&errdetails.BadRequest_FieldViolation{Field: "age_confirmed", Reason: "REQUIRED"}), 400, "VALIDATION_ERROR"},
		{finerr.ConsentRequired(), 403, "CONSENT_REQUIRED"},
		{finerr.ConsentOutdated(), 422, "CONSENT_OUTDATED"},
		{finerr.IdempotencyConflict(), 409, "IDEMPOTENCY_CONFLICT"},
		{finerr.RequestInProgress(2 * time.Second), 409, "REQUEST_IN_PROGRESS"},
		{status.Error(codes.Unavailable, "down"), 500, "INTERNAL_ERROR"},
		{finerr.NotFound(), 500, "INTERNAL_ERROR"}, // not declared by the operation
	} {
		u.f.start = func(*userv1.StartPhoneVerificationRequest) (*userv1.StartPhoneVerificationResponse, error) {
			return nil, tc.err
		}
		r := u.serve(t, "POST", "/v1/auth/phone", `{"phone":"+79001234567","pd_consent_accepted":true,"pd_consent_version":"v","age_confirmed":true}`, nil)
		if r.Code != tc.status || r.code() != tc.code {
			t.Fatalf("%v: %d %v", tc.err, r.Code, r.json)
		}
		if (tc.status == 429 || tc.code == "REQUEST_IN_PROGRESS") != (r.Header().Get("Retry-After") != "") {
			t.Fatalf("Retry-After of %s: %q", tc.code, r.Header().Get("Retry-After"))
		}
	}
}

func phoneStream(msgs []*userv1.WatchPhoneVerificationResponse, err error, block bool) func(context.Context, *userv1.WatchPhoneVerificationRequest) (grpc.ServerStreamingClient[userv1.WatchPhoneVerificationResponse], error) {
	return func(ctx context.Context, _ *userv1.WatchPhoneVerificationRequest) (grpc.ServerStreamingClient[userv1.WatchPhoneVerificationResponse], error) {
		return &fakeStream[userv1.WatchPhoneVerificationResponse]{ctx: ctx, msgs: msgs, err: err, block: block}, nil
	}
}

func status_(s userv1.VerificationStatus) *userv1.WatchPhoneVerificationResponse {
	return &userv1.WatchPhoneVerificationResponse{Status: s}
}

func TestUnitWatchPhone(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	var token string
	pending := status_(userv1.VerificationStatus_VERIFICATION_STATUS_PENDING)
	existing := &userv1.WatchPhoneVerificationResponse{Status: userv1.VerificationStatus_VERIFICATION_STATUS_VERIFIED, IsNewUser: proto.Bool(false),
		Tokens: &userv1.TokenPair{AccessToken: "at", RefreshToken: "rt"}}
	newUser := &userv1.WatchPhoneVerificationResponse{Status: userv1.VerificationStatus_VERIFICATION_STATUS_VERIFIED, IsNewUser: proto.Bool(true), RegistrationToken: proto.String("reg")}
	open := func(msgs []*userv1.WatchPhoneVerificationResponse, err error, block bool) {
		inner := phoneStream(msgs, err, block)
		u.f.watchPhone = func(ctx context.Context, in *userv1.WatchPhoneVerificationRequest) (grpc.ServerStreamingClient[userv1.WatchPhoneVerificationResponse], error) {
			token = in.GetVerificationToken()
			return inner(ctx, in)
		}
	}
	serve := func(header map[string]string) result {
		if header == nil {
			header = map[string]string{"X-Verification-Token": "vt-1"}
		}
		header["Authorization"] = ""
		return u.serve(t, "GET", "/v1/auth/phone/events", "", header)
	}
	open([]*userv1.WatchPhoneVerificationResponse{pending, pending, existing, pending}, nil, false)
	r := serve(nil)
	events, _ := r.events(t)
	if r.Code != 200 || r.Header().Get("Content-Type") != "text/event-stream" || token != "vt-1" || len(events) != 3 ||
		events[0].data["status"] != "pending" || events[2].data["access_token"] != "at" || events[2].data["refresh_token"] != "rt" || events[2].data["is_new_user"] != false || events[2].data["registration_token"] != nil {
		t.Fatalf("existing user: %d %v", r.Code, events)
	}
	open([]*userv1.WatchPhoneVerificationResponse{newUser}, nil, false)
	if events, _ = serve(nil).events(t); len(events) != 1 || events[0].data["registration_token"] != "reg" || events[0].data["access_token"] != nil {
		t.Fatalf("new user: %v", events)
	}
	open([]*userv1.WatchPhoneVerificationResponse{status_(userv1.VerificationStatus_VERIFICATION_STATUS_EXPIRED)}, nil, false)
	if events, _ = serve(nil).events(t); len(events) != 1 || events[0].data["status"] != "expired" {
		t.Fatalf("expired: %v", events)
	}
	// A transport failure mid-stream closes it without a terminal or error event.
	open([]*userv1.WatchPhoneVerificationResponse{pending}, status.Error(codes.Unavailable, "pod gone"), false)
	if events, _ = serve(nil).events(t); len(events) != 1 || events[0].name != "status" {
		t.Fatalf("mid-stream failure: %v", events)
	}
	// Heartbeats while pending; the stream deadline ends it.
	open([]*userv1.WatchPhoneVerificationResponse{pending}, nil, true)
	if events, pings := serve(nil).events(t); len(events) != 1 || pings == 0 {
		t.Fatalf("heartbeats: %v %d", events, pings)
	}
	// An unknown later status ends the stream; an unknown first one is a 500.
	open([]*userv1.WatchPhoneVerificationResponse{pending, status_(99)}, nil, false)
	if events, _ = serve(nil).events(t); len(events) != 1 {
		t.Fatalf("unknown status: %v", events)
	}
	open([]*userv1.WatchPhoneVerificationResponse{status_(0)}, nil, false)
	if r := serve(nil); r.Code != 500 || r.code() != "INTERNAL_ERROR" {
		t.Fatalf("unknown first status: %d", r.Code)
	}
	// Errors before the first message are ordinary HTTP answers.
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{{finerr.InvalidToken(), 401, "INVALID_TOKEN"}, {finerr.RateLimited(time.Now().Add(time.Minute)), 429, "RATE_LIMITED"}, {status.Error(codes.Internal, "x"), 500, "INTERNAL_ERROR"}} {
		open(nil, tc.err, false)
		if r := serve(nil); r.Code != tc.status || r.code() != tc.code {
			t.Fatalf("%v: %d %v", tc.err, r.Code, r.json)
		}
	}
	u.f.watchPhone = func(context.Context, *userv1.WatchPhoneVerificationRequest) (grpc.ServerStreamingClient[userv1.WatchPhoneVerificationResponse], error) {
		return nil, status.Error(codes.Unavailable, "dial")
	}
	if r := serve(nil); r.Code != 500 {
		t.Fatal(r.Code)
	}
	// Only one bounded header is forwarded; others reach user as an empty token.
	open([]*userv1.WatchPhoneVerificationResponse{pending}, nil, false)
	for _, header := range []map[string]string{{"X-Verification-Token": strings.Repeat("v", maxVerificationToken+1)}, {"X-Verification-Token": ""}} {
		serve(header)
		if token != "" {
			t.Fatalf("forwarded %q", token)
		}
	}
}

func TestUnitStreamLimitRefusesWithFreshRetry(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{Max: 1, PerIP: 1, RetryAfter: 3 * time.Second})
	started := make(chan struct{})
	release := make(chan struct{})
	u.f.watchPhone = func(ctx context.Context, _ *userv1.WatchPhoneVerificationRequest) (grpc.ServerStreamingClient[userv1.WatchPhoneVerificationResponse], error) {
		close(started)
		<-release
		return nil, finerr.InvalidToken()
	}
	done := make(chan result)
	go func() { done <- u.serve(t, "GET", "/v1/auth/phone/events", "", map[string]string{"Authorization": ""}) }()
	<-started
	r := u.serve(t, "GET", "/v1/assistant/messages/a1/events", "", nil)
	if r.Code != 429 || r.code() != "RATE_LIMITED" || r.Header().Get("Retry-After") != "3" {
		t.Fatalf("%d %v", r.Code, r.json)
	}
	close(release)
	<-done
	if n, err := testutil.GatherAndCount(u.reg, "finassist_sse_rejected_total"); err != nil || n != 1 {
		t.Fatalf("rejection not counted: %d %v", n, err)
	}
}

func TestUnitRegisterRefreshLogout(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	var reg *userv1.RegisterRequest
	u.f.register = func(in *userv1.RegisterRequest) (*userv1.RegisterResponse, error) {
		reg = in
		return &userv1.RegisterResponse{Tokens: &userv1.TokenPair{AccessToken: "a", RefreshToken: "r"}, UserId: "u1"}, nil
	}
	r := u.serve(t, "POST", "/v1/auth/register", `{"registration_token":"reg","pd_consent_accepted":true,"pd_consent_version":"2026-09-28","timezone":"Asia/Yekaterinburg"}`, map[string]string{"Authorization": ""})
	if r.Code != 201 || r.json["user_id"] != "u1" || reg.GetTimezone() != "Asia/Yekaterinburg" || !reg.GetPdConsentAccepted() || reg.GetPdConsentVersion() != "2026-09-28" || reg.GetRegistrationToken() != "reg" {
		t.Fatalf("%d %v %v", r.Code, r.json, reg)
	}
	u.serve(t, "POST", "/v1/auth/register", `{"registration_token":"reg","pd_consent_accepted":false,"pd_consent_version":"v"}`, nil)
	if reg.Timezone != nil {
		t.Fatal("absent timezone stays absent")
	}
	for _, err := range []error{finerr.TokenExpired(), finerr.ConsentRequired(), finerr.ConsentOutdated(), finerr.InvalidToken()} {
		u.f.register = func(*userv1.RegisterRequest) (*userv1.RegisterResponse, error) { return nil, err }
		r := u.serve(t, "POST", "/v1/auth/register", `{"registration_token":"reg","pd_consent_accepted":true,"pd_consent_version":"v"}`, nil)
		want, _ := finerr.ToREST(err)
		if r.Code != want.Spec.HTTPStatus || r.code() != want.Code {
			t.Fatalf("%v: %d %v", err, r.Code, r.json)
		}
	}
	u.f.refresh = func(in *userv1.RefreshTokensRequest) (*userv1.RefreshTokensResponse, error) {
		if in.GetRefreshToken() != "rt" {
			return nil, finerr.TokenRevoked()
		}
		return &userv1.RefreshTokensResponse{Tokens: &userv1.TokenPair{AccessToken: "a2", RefreshToken: "r2"}}, nil
	}
	if r := u.serve(t, "POST", "/v1/auth/token", `{"refresh_token":"rt"}`, map[string]string{"Authorization": ""}); r.Code != 200 || r.json["refresh_token"] != "r2" {
		t.Fatalf("%d %v", r.Code, r.json)
	}
	if r := u.serve(t, "POST", "/v1/auth/token", `{"refresh_token":"old"}`, nil); r.Code != 401 || r.code() != "TOKEN_REVOKED" {
		t.Fatalf("%d %v", r.Code, r.json)
	}
	var out *userv1.LogoutRequest
	u.f.logout = func(in *userv1.LogoutRequest) (*userv1.LogoutResponse, error) {
		out = in
		return &userv1.LogoutResponse{}, nil
	}
	if r := u.serve(t, "POST", "/v1/auth/logout", `{"refresh_token":"rt"}`, map[string]string{"Idempotency-Key": ""}); r.Code != 204 || out.GetRefreshToken() != "rt" {
		t.Fatalf("logout: %d %s", r.Code, r.Body.String())
	}
	if r := u.serve(t, "POST", "/v1/auth/logout", `{"refresh_token":"rt"}`, map[string]string{"Authorization": "", "Idempotency-Key": ""}); r.Code != 401 {
		t.Fatalf("logout needs the access token: %d", r.Code)
	}
	u.f.logout = func(*userv1.LogoutRequest) (*userv1.LogoutResponse, error) {
		return nil, finerr.RateLimited(time.Now().Add(time.Minute))
	}
	if r := u.serve(t, "POST", "/v1/auth/logout", `{"refresh_token":"rt"}`, nil); r.Code != 429 {
		t.Fatal(r.Code)
	}
}

func TestUnitConsentDocument(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	var asked userv1.ConsentType
	u.f.document = func(in *userv1.GetConsentDocumentRequest) (*userv1.GetConsentDocumentResponse, error) {
		asked = in.GetType()
		return &userv1.GetConsentDocumentResponse{Document: &userv1.ConsentDocument{Type: in.GetType(), Version: "2026-09-28", Title: "T", Url: "https://example.ru/doc"}}, nil
	}
	if r := u.serve(t, "GET", "/v1/consents/personal_data", "", map[string]string{"Authorization": ""}); r.Code != 200 || asked != userv1.ConsentType_CONSENT_TYPE_PERSONAL_DATA || r.json["type"] != "personal_data" || r.json["url"] != "https://example.ru/doc" {
		t.Fatalf("personal data without token: %d %v", r.Code, r.json)
	}
	if r := u.serve(t, "GET", "/v1/consents/personal_data", "", map[string]string{"Authorization": "Bearer forged"}); r.Code != 200 {
		t.Fatalf("an Authorization header of the public document is ignored: %d", r.Code)
	}
	if r := u.serve(t, "GET", "/v1/consents/assistant", "", map[string]string{"Authorization": ""}); r.Code != 401 || r.code() != "INVALID_TOKEN" {
		t.Fatalf("assistant without token: %d", r.Code)
	}
	if r := u.serve(t, "GET", "/v1/consents/assistant", "", nil); r.Code != 200 || asked != userv1.ConsentType_CONSENT_TYPE_ASSISTANT || r.json["type"] != "assistant" {
		t.Fatalf("assistant: %d %v", r.Code, r.json)
	}
	asked = 0
	if r := u.serve(t, "GET", "/v1/consents/marketing", "", map[string]string{"Authorization": ""}); r.Code != 404 || r.code() != "NOT_FOUND" || asked != 0 {
		t.Fatalf("unknown type: %d", r.Code)
	}
	u.f.document = func(*userv1.GetConsentDocumentRequest) (*userv1.GetConsentDocumentResponse, error) {
		return nil, finerr.InvalidToken()
	}
	if r := u.serve(t, "GET", "/v1/consents/assistant", "", nil); r.Code != 401 {
		t.Fatal(r.Code)
	}
}

func TestUnitDecodeFailureIsValidation(t *testing.T) {
	rec := httptest.NewRecorder()
	if decode(&rest.Call{W: rec, Body: []byte(`[`)}, &struct{}{}) || rec.Code != 400 {
		t.Fatal(rec.Code)
	}
}
