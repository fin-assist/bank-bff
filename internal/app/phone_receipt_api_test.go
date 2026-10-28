package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	userv1 "github.com/fin-assist/contracts/gen/go/finassist/user/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/fin-assist/bank-bff/internal/config"
	"github.com/fin-assist/bank-bff/internal/testpeer"
)

func phoneReceiptJSON(accepted, age bool, version string) string {
	body, _ := json.Marshal(map[string]any{"phone": "+79001234567", "pd_consent_accepted": accepted, "pd_consent_version": version, "age_confirmed": age})
	return string(body)
}

func phoneReceiptRequest(body string) request {
	return request{method: "POST", path: "/v1/auth/phone", route: "/v1/auth/phone", body: body, header: base(map[string]string{"Idempotency-Key": idemKey, "X-Client-IP": "192.0.2.44", "X-Forwarded-For": "203.0.113.99, 198.51.100.7, 10.1.2.3", "X-Consent-Received-At": "1970-01-01T00:00:00Z", "User-Agent": "untrusted-receipt-agent", "Authorization": "Bearer unverified-client"})}
}

func TestAPIPhoneReceiptMappingTrustedIPAndFreshResponse(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		t.Run(strconv.FormatBool(trusted), func(t *testing.T) {
			b := startBFF(t, func(c *config.Config, _ *testpeer.Peers) {
				if trusted {
					c.API.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("10.0.0.0/8")}
				}
			})
			received := make(chan *userv1.StartPhoneVerificationRequest, 4)
			expires := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Millisecond)
			remaining := []int32{299, 298}
			var calls atomic.Int32
			b.peers.Auth.Set(func(a *testpeer.Auth) {
				a.StartFunc = func(_ context.Context, in *userv1.StartPhoneVerificationRequest) (*userv1.StartPhoneVerificationResponse, error) {
					received <- proto.Clone(in).(*userv1.StartPhoneVerificationRequest)
					left := remaining[calls.Add(1)-1]
					return &userv1.StartPhoneVerificationResponse{VerificationToken: "synthetic-secret", CallbackNumber: "+74951234567", ExpiresAt: timestamppb.New(expires), ExpiresIn: proto.Int32(left)}, nil
				}
			})
			version := strings.Repeat("🙂", 64)
			for index := range 2 {
				resp := b.do(t, phoneReceiptRequest(phoneReceiptJSON(true, true, version)))
				got := <-received
				if resp.StatusCode != 200 || got.Phone != "+79001234567" || !got.PdConsentAccepted || !got.AgeConfirmed || got.PdConsentVersion != version || resp.json["expires_in"] != float64(remaining[index]) || resp.Header.Get("Cache-Control") != "no-store" {
					t.Fatalf("receipt mapping/replay changed: %d %s %+v", resp.StatusCode, resp.body, got)
				}
				parsed, err := time.Parse(time.RFC3339Nano, resp.json["expires_at"].(string))
				if err != nil || !parsed.Equal(expires) {
					t.Fatal("provider public expiry replaced", parsed, err)
				}
				date, err := http.ParseTime(resp.Header.Get("Date"))
				if err != nil || time.Since(date) > 5*time.Second || date.After(time.Now().Add(time.Second)) {
					t.Fatal("missing/stale HTTP Date", err)
				}
				all := b.peers.Calls(userv1.AuthService_StartPhoneVerification_FullMethodName)
				last := all[len(all)-1]
				ip := "127.0.0.1"
				if trusted {
					ip = "198.51.100.7"
				}
				if last.Caller != "bank-bff" || len(last.MD.Get("x-client-ip")) != 1 || last.MD.Get("x-client-ip")[0] != ip || last.MD.Get("idempotency-key")[0] != idemKey || last.MD.Get("x-client-version")[0] != clientVersion {
					t.Fatal("untrusted receipt metadata", last.MD)
				}
				for _, name := range []string{"authorization", "x-consent-received-at", "x-forwarded-for"} {
					if len(last.MD.Get(name)) != 0 {
						t.Fatal("client receipt hint forwarded", name)
					}
				}
				if strings.Contains(strings.Join(last.MD.Get("user-agent"), " "), "untrusted-receipt-agent") {
					t.Fatal("client user agent became receipt metadata")
				}
			}
			if len(b.peers.Calls(userv1.AuthService_StartPhoneVerification_FullMethodName)) != 2 || len(b.peers.Calls(userv1.ConsentDocumentService_GetConsentDocument_FullMethodName)) != 0 {
				t.Fatal("BFF cached replay or fetched current consent")
			}
		})
	}
}

func TestAPIPhoneReceiptStructuralFailuresNeverReachUSER(t *testing.T) {
	b := startBFF(t, nil)
	for _, field := range []string{"phone", "pd_consent_accepted", "pd_consent_version", "age_confirmed"} {
		for _, mode := range []string{"missing", "null", "type"} {
			in := map[string]any{"phone": "+79001234567", "pd_consent_accepted": true, "pd_consent_version": "v", "age_confirmed": true}
			switch mode {
			case "missing":
				delete(in, field)
			case "null":
				in[field] = nil
			case "type":
				in[field] = 17
			}
			body, _ := json.Marshal(in)
			r := b.do(t, phoneReceiptRequest(string(body)))
			if r.StatusCode != 400 || errorCode(r) != "VALIDATION_ERROR" || r.Header.Get("Cache-Control") != "no-store" || r.json["error"].(map[string]any)["retryable"] != false {
				t.Fatalf("%s/%s: %d %s", field, mode, r.StatusCode, r.body)
			}
		}
	}
	for _, body := range []string{`{"phone":"+79001234567"}`, phoneReceiptJSON(true, true, ""), phoneReceiptJSON(true, true, strings.Repeat("🙂", 65))} {
		r := b.do(t, phoneReceiptRequest(body))
		if r.StatusCode != 400 || errorCode(r) != "VALIDATION_ERROR" {
			t.Fatalf("invalid receipt accepted: %d %s", r.StatusCode, r.body)
		}
	}
	old := phoneReceiptRequest(`{"phone":"+79001234567"}`)
	old.header["X-Client-Version"] = "android/0.1.0+1"
	if r := b.do(t, old); r.StatusCode != 400 || errorCode(r) != "CLIENT_VERSION_UNSUPPORTED" {
		t.Fatalf("schema preceded version gate: %d %s", r.StatusCode, r.body)
	}
	if len(b.peers.Calls(userv1.AuthService_StartPhoneVerification_FullMethodName)) != 0 {
		t.Fatal("invalid receipt reached USER")
	}
}

func TestAPIPhoneReceiptServicePrecedenceErrorsAndRetryAdvice(t *testing.T) {
	b := startBFF(t, nil)
	accepted := make(chan *userv1.StartPhoneVerificationRequest, 16)
	at := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	for _, tc := range []struct {
		name         string
		consent, age bool
		version      string
		err          error
		code         string
		status       int
	}{
		{"age", true, false, "v", finerr.ValidationError(&errdetails.BadRequest_FieldViolation{Field: "age_confirmed", Reason: "REQUIRED", Description: "PRIVATE"}), "VALIDATION_ERROR", 400},
		{"consent", false, true, "v", finerr.ConsentRequired(), "CONSENT_REQUIRED", 403},
		{"outdated", true, true, "old", finerr.ConsentOutdated(), "CONSENT_OUTDATED", 422},
		{"key-before-policy", false, false, "old", finerr.IdempotencyConflict(), "IDEMPOTENCY_CONFLICT", 409},
		{"limit-before-policy", false, false, "old", finerr.WithRequestRetryInfo(finerr.RateLimited(at), true), "RATE_LIMITED", 429},
		{"in-flight", true, true, "v", finerr.WithRequestRetryInfo(finerr.RequestInProgress(3*time.Second), true), "REQUEST_IN_PROGRESS", 409},
		{"unknown-permitted", true, true, "v", finerr.WithRequestRetryInfo(finerr.InternalError(), true), "INTERNAL_ERROR", 500},
		{"transport-loss", true, true, "v", status.Error(codes.Unavailable, "PRIVATE provider transport"), "INTERNAL_ERROR", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b.peers.Auth.Set(func(a *testpeer.Auth) {
				a.StartFunc = func(_ context.Context, in *userv1.StartPhoneVerificationRequest) (*userv1.StartPhoneVerificationResponse, error) {
					accepted <- proto.Clone(in).(*userv1.StartPhoneVerificationRequest)
					return nil, tc.err
				}
			})
			before := len(b.peers.Calls(userv1.AuthService_StartPhoneVerification_FullMethodName))
			r := b.do(t, phoneReceiptRequest(phoneReceiptJSON(tc.consent, tc.age, tc.version)))
			got := <-accepted
			if got.PdConsentAccepted != tc.consent || got.AgeConfirmed != tc.age || got.PdConsentVersion != tc.version || r.StatusCode != tc.status || errorCode(r) != tc.code || r.json["error"].(map[string]any)["retryable"] != false || r.Header.Get("Cache-Control") != "no-store" || strings.Contains(string(r.body), "PRIVATE") {
				t.Fatalf("policy precedence/projection changed: %d %s %+v", r.StatusCode, r.body, got)
			}
			if len(b.peers.Calls(userv1.AuthService_StartPhoneVerification_FullMethodName)) != before+1 {
				t.Fatal("keyed phone effect retried by BFF")
			}
			if tc.code == "RATE_LIMITED" {
				retry := r.json["error"].(map[string]any)["retry_at"]
				if retry != at.Format(time.RFC3339Nano) || r.Header.Get("Retry-After") == "" {
					t.Fatal("retry metadata lost", retry, r.Header)
				}
			}
			if tc.code == "REQUEST_IN_PROGRESS" && r.Header.Get("Retry-After") != "3" {
				t.Fatal("in-flight delay changed", r.Header)
			}
			if tc.name == "age" {
				d := r.json["error"].(map[string]any)["details"].([]any)[0].(map[string]any)
				if d["field"] != "age_confirmed" || d["code"] != "REQUIRED" {
					t.Fatal(d)
				}
			}
		})
	}
}

func TestAPIRegisterRetainsOriginalReceiptAssertionsWithoutCurrentDocumentFetch(t *testing.T) {
	b := startBFF(t, nil)
	received := make(chan *userv1.RegisterRequest, 2)
	b.peers.Auth.Set(func(a *testpeer.Auth) {
		a.RegisterFunc = func(_ context.Context, in *userv1.RegisterRequest) (*userv1.RegisterResponse, error) {
			received <- proto.Clone(in).(*userv1.RegisterRequest)
			if !in.PdConsentAccepted {
				return nil, finerr.InvalidToken()
			}
			return &userv1.RegisterResponse{Tokens: &userv1.TokenPair{AccessToken: "a", RefreshToken: "r"}, UserId: "u1"}, nil
		}
	})
	for _, consent := range []bool{true, false} {
		body, _ := json.Marshal(map[string]any{"registration_token": "receipt-bound-token", "pd_consent_accepted": consent, "pd_consent_version": "previous-document-version"})
		r := b.do(t, request{method: "POST", path: "/v1/auth/register", route: "/v1/auth/register", body: string(body), header: base(map[string]string{"Idempotency-Key": idemKey})})
		got := <-received
		want := 201
		if !consent {
			want = 401
		}
		if r.StatusCode != want || got.RegistrationToken != "receipt-bound-token" || got.PdConsentAccepted != consent || got.PdConsentVersion != "previous-document-version" {
			t.Fatalf("registration receipt replaced: %d %s %+v", r.StatusCode, r.body, got)
		}
	}
	before := len(b.peers.Calls(userv1.AuthService_Register_FullMethodName))
	for _, version := range []any{"", strings.Repeat("🙂", 65), nil, 17} {
		body, _ := json.Marshal(map[string]any{"registration_token": "receipt-bound-token", "pd_consent_accepted": true, "pd_consent_version": version})
		r := b.do(t, request{method: "POST", path: "/v1/auth/register", route: "/v1/auth/register", body: string(body), header: base(map[string]string{"Idempotency-Key": idemKey})})
		if r.StatusCode != 400 || errorCode(r) != "VALIDATION_ERROR" || len(b.peers.Calls(userv1.AuthService_Register_FullMethodName)) != before {
			t.Fatalf("invalid original receipt version reached USER: %d %s", r.StatusCode, r.body)
		}
	}
	version := strings.Repeat("🙂", 64)
	body, _ := json.Marshal(map[string]any{"registration_token": "receipt-bound-token", "pd_consent_accepted": true, "pd_consent_version": version})
	r := b.do(t, request{method: "POST", path: "/v1/auth/register", route: "/v1/auth/register", body: string(body), header: base(map[string]string{"Idempotency-Key": idemKey})})
	if got := <-received; r.StatusCode != 201 || got.PdConsentVersion != version {
		t.Fatal("Unicode receipt version changed", r.StatusCode)
	}
	if len(b.peers.Calls(userv1.ConsentDocumentService_GetConsentDocument_FullMethodName)) != 0 {
		t.Fatal("Register compared current document instead of receipt")
	}
}
