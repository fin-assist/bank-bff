package app

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	userv1 "github.com/fin-assist/contracts/gen/go/finassist/user/v1"
	"google.golang.org/protobuf/proto"

	"github.com/fin-assist/bank-bff/internal/rest"
	"github.com/fin-assist/bank-bff/internal/testpeer"
)

type encodingWireBody struct {
	io.Reader
	reads atomic.Int32
}

func (b *encodingWireBody) Read(p []byte) (int, error) {
	b.reads.Add(1)
	return b.Reader.Read(p)
}

// The client waits for 100 Continue, so a refusal before reading the request
// body must arrive without asking the client for a single body byte.
func encodingRequest(t *testing.T, b *bff, method, path, body string, values []string, length int64) (response, int32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	spy := &encodingWireBody{Reader: strings.NewReader(body)}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+b.public+path, spy)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = length
	if length == 0 {
		req.Body = http.NoBody
	}
	req.Header.Set("X-Client-Version", clientVersion)
	req.Header.Set("Authorization", "Bearer "+b.keys.Access("u1"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idemKey)
	req.Header.Set("Expect", "100-continue")
	for _, v := range values {
		req.Header.Add("Content-Encoding", v)
	}
	transport := &http.Transport{ExpectContinueTimeout: 4 * time.Second}
	defer transport.CloseIdleConnections()
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return response{Response: resp, body: data, json: b.contract.Response(t, method, path, req, resp, data)}, spy.reads.Load()
}

func scenarioJSONEncoding(t *testing.T, start starter) {
	b := start(t, nil)
	b.peers.Auth.Set(func(a *testpeer.Auth) {
		a.StartFunc = func(context.Context, *userv1.StartPhoneVerificationRequest) (*userv1.StartPhoneVerificationResponse, error) {
			return &userv1.StartPhoneVerificationResponse{VerificationToken: "vt", CallbackNumber: "+74951234567", ExpiresAt: created, ExpiresIn: proto.Int32(300)}, nil
		}
		a.RegisterFunc = func(context.Context, *userv1.RegisterRequest) (*userv1.RegisterResponse, error) {
			return &userv1.RegisterResponse{Tokens: &userv1.TokenPair{AccessToken: "a", RefreshToken: "r"}, UserId: "u1"}, nil
		}
		a.RefreshFunc = func(context.Context, *userv1.RefreshTokensRequest) (*userv1.RefreshTokensResponse, error) {
			return &userv1.RefreshTokensResponse{Tokens: &userv1.TokenPair{AccessToken: "a", RefreshToken: "r"}}, nil
		}
		a.LogoutFunc = func(context.Context, *userv1.LogoutRequest) (*userv1.LogoutResponse, error) {
			return &userv1.LogoutResponse{}, nil
		}
	})
	b.peers.Profile.Set(func(p *testpeer.Profile) {
		p.GrantFunc = func(context.Context, *userv1.GrantAssistantConsentRequest) (*userv1.GrantAssistantConsentResponse, error) {
			return &userv1.GrantAssistantConsentResponse{Profile: profileMessage(true)}, nil
		}
	})
	b.peers.Assistant.Set(func(a *testpeer.Assistant) {
		a.AskFunc = func(_ context.Context, in *assistantv1.AskQuestionRequest) (*assistantv1.AskQuestionResponse, error) {
			return &assistantv1.AskQuestionResponse{AnswerId: "a1", RemainingLimit: 4, Question: &assistantv1.Message{Id: "q1", Role: assistantv1.Role_ROLE_USER, Text: proto.String(in.GetText()), CreatedAt: created, Currency: "RUB"}}, nil
		}
		a.RetryFunc = func(context.Context, *assistantv1.RetryAnswerRequest) (*assistantv1.RetryAnswerResponse, error) {
			return &assistantv1.RetryAnswerResponse{AnswerId: "a2", RemainingLimit: 3}, nil
		}
	})
	routes := []struct {
		method, path, body, rpc string
		status                  int
	}{
		{"POST", "/v1/auth/phone", `{"phone":"+79001234567"}`, userv1.AuthService_StartPhoneVerification_FullMethodName, 200},
		{"POST", "/v1/auth/register", `{"registration_token":"rt","pd_consent_accepted":true,"pd_consent_version":"v"}`, userv1.AuthService_Register_FullMethodName, 201},
		{"POST", "/v1/auth/token", `{"refresh_token":"rt"}`, userv1.AuthService_RefreshTokens_FullMethodName, 200},
		{"POST", "/v1/auth/logout", `{"refresh_token":"rt"}`, userv1.AuthService_Logout_FullMethodName, 204},
		{"PUT", "/v1/profile/assistant-consent", `{"version":"v"}`, userv1.ProfileService_GrantAssistantConsent_FullMethodName, 200},
		{"POST", "/v1/assistant/messages", `{"text":"x"}`, assistantv1.AssistantService_AskQuestion_FullMethodName, 201},
	}
	for _, route := range routes {
		t.Run(route.path, func(t *testing.T) {
			for _, values := range [][]string{{"gzip"}, {""}, {"identity,gzip"}, {"identity", "gzip"}, {"identity", "identity"}} {
				for _, length := range []int64{0, int64(len(route.body)), rest.MinBodyLimit + 1, -1} {
					body := route.body
					if length > rest.MinBodyLimit {
						body = strings.Repeat("x", int(length))
					}
					before := len(b.peers.Calls(route.rpc))
					r, reads := encodingRequest(t, b, route.method, route.path, body, values, length)
					if r.StatusCode != 400 || errorCode(r) != "VALIDATION_ERROR" || reads != 0 || !r.Close || len(b.peers.Calls(route.rpc)) != before {
						t.Fatalf("%v length%d: %d %s reads%d close%v", values, length, r.StatusCode, r.body, reads, r.Close)
					}
					details := r.json["error"].(map[string]any)["details"].([]any)
					if len(details) != 1 || details[0].(map[string]any)["field"] != "Content-Encoding" || details[0].(map[string]any)["code"] != "INVALID_FORMAT" {
						t.Fatal(details)
					}
				}
			}
			for _, values := range [][]string{nil, {"identity"}, {"IDENTITY"}, {" \tIdEnTiTy\t "}} {
				before := len(b.peers.Calls(route.rpc))
				r, reads := encodingRequest(t, b, route.method, route.path, route.body, values, int64(len(route.body)))
				if r.StatusCode != route.status || reads == 0 || len(b.peers.Calls(route.rpc)) != before+1 {
					t.Fatalf("accepted %v: %d %s reads%d", values, r.StatusCode, r.body, reads)
				}
			}
		})
	}
	// The protected route still authenticates before applying the encoding policy.
	r := b.do(t, request{method: "POST", path: "/v1/assistant/messages", route: "/v1/assistant/messages", body: `{"text":"x"}`, header: map[string]string{"Authorization": "Bearer invalid", "Content-Encoding": "gzip"}})
	if r.StatusCode != 401 || errorCode(r) != "INVALID_TOKEN" {
		t.Fatalf("auth order: %d %s", r.StatusCode, r.body)
	}
	// FIN-339 does not add an encoding policy to bodyless keyed operations:
	// empty retry still works, while even one actual body byte is refused.
	retry := request{method: "POST", path: "/v1/assistant/messages/a1/retry", route: "/v1/assistant/messages/{answer_id}/retry", header: map[string]string{"Authorization": "Bearer " + b.keys.Access("u1"), "Idempotency-Key": idemKey, "Content-Encoding": "gzip"}}
	before := len(b.peers.Calls(assistantv1.AssistantService_RetryAnswer_FullMethodName))
	if r := b.do(t, retry); r.StatusCode != 201 {
		t.Fatalf("bodyless empty: %d %s", r.StatusCode, r.body)
	}
	retry.body = "x"
	if r := b.do(t, retry); r.StatusCode != 400 || errorCode(r) != "VALIDATION_ERROR" {
		t.Fatalf("bodyless byte: %d %s", r.StatusCode, r.body)
	}
	if len(b.peers.Calls(assistantv1.AssistantService_RetryAnswer_FullMethodName)) != before+1 {
		t.Fatal("bodyless refused request reached peer")
	}
}

func TestAPIJSONEncoding(t *testing.T) { scenarioJSONEncoding(t, startBFF) }
