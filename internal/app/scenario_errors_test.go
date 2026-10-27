package app

import (
	"context"
	"testing"
	"time"

	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	userv1 "github.com/fin-assist/contracts/gen/go/finassist/user/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	"github.com/fin-assist/bank-bff/internal/testpeer"
)

// failingOperation is one protected operation whose peer method answers err.
type failingOperation struct {
	request request
	method  string
	// declared are the statuses the operation declares in openapi.yaml.
	declared map[int]bool
	fail     func(*testpeer.Peers, error)
}

func declared(statuses ...int) map[int]bool {
	out := map[int]bool{}
	for _, s := range statuses {
		out[s] = true
	}
	return out
}

// scenarioOperationErrors is the per-operation matrix of service failures: each
// registry error the operation declares keeps its status and code, one it does not
// declare and a bare transport failure become 500, and a request without a token
// is 401 without a service call. Every answer is validated against openapi.yaml.
func scenarioOperationErrors(t *testing.T, start starter) {
	b := start(t, nil)
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1")})
	ops := []failingOperation{
		{request{method: "PATCH", path: "/v1/profile", route: "/v1/profile", body: `{"theme":"dark"}`}, userv1.ProfileService_UpdateProfile_FullMethodName, declared(400, 401, 429, 500, 503), func(p *testpeer.Peers, err error) {
			p.Profile.Set(func(pr *testpeer.Profile) {
				pr.UpdateFunc = func(context.Context, *userv1.UpdateProfileRequest) (*userv1.UpdateProfileResponse, error) {
					return nil, err
				}
			})
		}},
		{request{method: "DELETE", path: "/v1/profile", route: "/v1/profile"}, userv1.ProfileService_DeleteAccount_FullMethodName, declared(400, 401, 429, 500, 503), func(p *testpeer.Peers, err error) {
			p.Profile.Set(func(pr *testpeer.Profile) {
				pr.DeleteFunc = func(context.Context, *userv1.DeleteAccountRequest) (*userv1.DeleteAccountResponse, error) {
					return nil, err
				}
			})
		}},
		{request{method: "GET", path: "/v1/assistant/limit", route: "/v1/assistant/limit"}, assistantv1.AssistantService_GetLimit_FullMethodName,
			declared(400, 401, 429, 500, 503), func(p *testpeer.Peers, err error) {
				p.Assistant.Set(func(a *testpeer.Assistant) {
					a.LimitFunc = func(context.Context, *assistantv1.GetLimitRequest) (*assistantv1.GetLimitResponse, error) {
						return nil, err
					}
				})
			}},
		{request{method: "GET", path: "/v1/assistant/messages", route: "/v1/assistant/messages"}, assistantv1.AssistantService_ListMessages_FullMethodName,
			declared(400, 401, 429, 500, 503), func(p *testpeer.Peers, err error) {
				p.Assistant.Set(func(a *testpeer.Assistant) {
					a.ListFunc = func(context.Context, *assistantv1.ListMessagesRequest) (*assistantv1.ListMessagesResponse, error) {
						return nil, err
					}
				})
			}},
		{request{method: "POST", path: "/v1/auth/logout", route: "/v1/auth/logout", body: `{"refresh_token":"rt"}`}, userv1.AuthService_Logout_FullMethodName,
			declared(400, 401, 429, 500, 503), func(p *testpeer.Peers, err error) {
				p.Auth.Set(func(a *testpeer.Auth) {
					a.LogoutFunc = func(context.Context, *userv1.LogoutRequest) (*userv1.LogoutResponse, error) { return nil, err }
				})
			}},
		{request{method: "PUT", path: "/v1/profile/assistant-consent", route: "/v1/profile/assistant-consent", body: `{"version":"2026-09-28"}`},
			userv1.ProfileService_GrantAssistantConsent_FullMethodName, declared(400, 401, 422, 429, 500, 503), func(p *testpeer.Peers, err error) {
				p.Profile.Set(func(pr *testpeer.Profile) {
					pr.GrantFunc = func(context.Context, *userv1.GrantAssistantConsentRequest) (*userv1.GrantAssistantConsentResponse, error) {
						return nil, err
					}
				})
			}},
		{request{method: "DELETE", path: "/v1/profile/assistant-consent", route: "/v1/profile/assistant-consent"},
			userv1.ProfileService_RevokeAssistantConsent_FullMethodName, declared(400, 401, 429, 500, 503), func(p *testpeer.Peers, err error) {
				p.Profile.Set(func(pr *testpeer.Profile) {
					pr.RevokeFunc = func(context.Context, *userv1.RevokeAssistantConsentRequest) (*userv1.RevokeAssistantConsentResponse, error) {
						return nil, err
					}
				})
			}},
		{request{method: "GET", path: "/v1/profile", route: "/v1/profile"}, userv1.ProfileService_GetProfile_FullMethodName,
			declared(400, 401, 429, 500, 503), func(p *testpeer.Peers, err error) {
				p.Profile.Set(func(pr *testpeer.Profile) {
					pr.GetFunc = func(context.Context, *userv1.GetProfileRequest) (*userv1.GetProfileResponse, error) { return nil, err }
				})
			}},
	}
	failures := registry(finerr.InvalidToken(), finerr.TokenRevoked(), finerr.RateLimited(retryAt()),
		finerr.ValidationError(&errdetails.BadRequest_FieldViolation{Field: "limit", Reason: "OUT_OF_RANGE"}),
		finerr.ConsentOutdated(), finerr.NotFound(), finerr.LimitExceeded(time.Now().Add(time.Hour)), finerr.InternalError())
	for _, op := range ops {
		for _, pe := range failures {
			op.fail(b.peers, pe.err)
			r := op.request
			r.header = auth
			want := pe
			if !op.declared[want.status] {
				want = peerError{status: 500, code: "INTERNAL_ERROR"}
			}
			got := b.do(t, r)
			if got.StatusCode != want.status || errorCode(got) != want.code {
				t.Fatalf("%s %s, peer %v: %d %s, want %d %s", r.method, r.path, pe.err, got.StatusCode, got.body, want.status, want.code)
			}
			if want.code == "RATE_LIMITED" && (got.Header.Get("Retry-After") == "" || got.json["error"].(map[string]any)["retry_at"] == nil) {
				t.Fatalf("%s: RATE_LIMITED without Retry-After or retry_at: %s", r.path, got.body)
			}
		}
		calls := len(b.peers.Calls(op.method))
		r := op.request
		r.header = base(nil)
		if got := b.do(t, r); got.StatusCode != 401 || errorCode(got) != "INVALID_TOKEN" || len(b.peers.Calls(op.method)) != calls {
			t.Fatalf("%s %s without a token: %d %s", r.method, r.path, got.StatusCode, got.body)
		}
	}
}
