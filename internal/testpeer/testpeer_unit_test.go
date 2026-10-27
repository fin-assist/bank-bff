package testpeer

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	userv1 "github.com/fin-assist/contracts/gen/go/finassist/user/v1"
	identity "github.com/fin-assist/go-identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestUnitKeysSignVerifiableTokens(t *testing.T) {
	k := NewKeys(t)
	var set struct {
		Keys []map[string]string `json:"keys"`
	}
	if err := json.Unmarshal([]byte(k.JWKS()), &set); err != nil || len(set.Keys) != 1 || set.Keys[0]["kid"] != userKID {
		t.Fatalf("%v %v", set, err)
	}
	parts := strings.Split(k.Access("u1"), ".")
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if len(parts) != 3 || !ed25519.Verify(k.public, []byte(parts[0]+"."+parts[1]), sig) {
		t.Fatal("token signature")
	}
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if !strings.Contains(string(claims), `"sub":"u1"`) || !strings.Contains(string(claims), UserIssuer) {
		t.Fatal(string(claims))
	}
}

func TestUnitStageFilesVerifyTheBFFIdentity(t *testing.T) {
	files := NewStageFiles(t)
	p := &Peers{}
	var err error
	p.verifier, err = identity.NewStageVerifier("stage", identity.VerifyOptions{Issuer: StageIssuer, Audience: "finassist-internal", Clock: time.Now},
		map[string]ed25519.PublicKey{"bank-bff": files.public})
	if err != nil {
		t.Fatal(err)
	}
	source, err := identity.NewStageSource("stage", identity.StageOptions{KeyFile: files.SigningKeyFile, Caller: "bank-bff", Issuer: StageIssuer, Clock: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	token, err := source.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-service-authorization", "Bearer "+token, "x-client-ip", "203.0.113.7"))
	if _, err := p.unary(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/u"}, func(context.Context, any) (any, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	_ = p.stream(nil, fakeServerStream{ctx: metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-service-authorization", "Bearer forged"))},
		&grpc.StreamServerInfo{FullMethod: "/s"}, func(any, grpc.ServerStream) error { return nil })
	calls := p.Calls("")
	if len(calls) != 2 || calls[0].Caller != "bank-bff" || calls[0].MD.Get("x-client-ip")[0] != "203.0.113.7" || calls[1].Caller != "" || len(p.Calls("/s")) != 1 {
		t.Fatalf("%+v", calls)
	}
}

type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f fakeServerStream) Context() context.Context { return f.ctx }

func TestUnitFakesDelegateOrAnswerUnimplemented(t *testing.T) {
	ctx := context.Background()
	a, c, p, s := &Auth{}, &Consents{}, &Profile{}, &Assistant{}
	unimplemented := func(err error) {
		t.Helper()
		if status.Code(err) != codes.Unimplemented {
			t.Fatalf("%v", err)
		}
	}
	_, err := a.StartPhoneVerification(ctx, nil)
	unimplemented(err)
	unimplemented(a.WatchPhoneVerification(nil, nil))
	_, err = a.Register(ctx, nil)
	unimplemented(err)
	_, err = a.RefreshTokens(ctx, nil)
	unimplemented(err)
	_, err = a.Logout(ctx, nil)
	unimplemented(err)
	_, err = c.GetConsentDocument(ctx, nil)
	unimplemented(err)
	_, err = p.GetProfile(ctx, nil)
	unimplemented(err)
	_, err = p.UpdateProfile(ctx, nil)
	unimplemented(err)
	_, err = p.DeleteAccount(ctx, nil)
	unimplemented(err)
	_, err = p.GrantAssistantConsent(ctx, nil)
	unimplemented(err)
	_, err = p.RevokeAssistantConsent(ctx, nil)
	unimplemented(err)
	_, err = s.AskQuestion(ctx, nil)
	unimplemented(err)
	unimplemented(s.WatchAnswer(nil, nil))
	_, err = s.ListMessages(ctx, nil)
	unimplemented(err)
	_, err = s.RetryAnswer(ctx, nil)
	unimplemented(err)
	_, err = s.GetLimit(ctx, nil)
	unimplemented(err)

	calls := 0
	a.Set(func(a *Auth) {
		a.StartFunc = func(context.Context, *userv1.StartPhoneVerificationRequest) (*userv1.StartPhoneVerificationResponse, error) {
			calls++
			return nil, nil
		}
		a.WatchFunc = func(*userv1.WatchPhoneVerificationRequest, grpc.ServerStreamingServer[userv1.WatchPhoneVerificationResponse]) error {
			calls++
			return nil
		}
		a.RegisterFunc = func(context.Context, *userv1.RegisterRequest) (*userv1.RegisterResponse, error) {
			calls++
			return nil, nil
		}
		a.RefreshFunc = func(context.Context, *userv1.RefreshTokensRequest) (*userv1.RefreshTokensResponse, error) {
			calls++
			return nil, nil
		}
		a.LogoutFunc = func(context.Context, *userv1.LogoutRequest) (*userv1.LogoutResponse, error) { calls++; return nil, nil }
	})
	c.Set(func(c *Consents) {
		c.GetFunc = func(context.Context, *userv1.GetConsentDocumentRequest) (*userv1.GetConsentDocumentResponse, error) {
			calls++
			return nil, nil
		}
	})
	p.Set(func(p *Profile) {
		p.UpdateFunc = func(context.Context, *userv1.UpdateProfileRequest) (*userv1.UpdateProfileResponse, error) {
			calls++
			return nil, nil
		}
		p.DeleteFunc = func(context.Context, *userv1.DeleteAccountRequest) (*userv1.DeleteAccountResponse, error) {
			calls++
			return nil, nil
		}
		p.GetFunc = func(context.Context, *userv1.GetProfileRequest) (*userv1.GetProfileResponse, error) {
			calls++
			return nil, nil
		}
		p.GrantFunc = func(context.Context, *userv1.GrantAssistantConsentRequest) (*userv1.GrantAssistantConsentResponse, error) {
			calls++
			return nil, nil
		}
		p.RevokeFunc = func(context.Context, *userv1.RevokeAssistantConsentRequest) (*userv1.RevokeAssistantConsentResponse, error) {
			calls++
			return nil, nil
		}
	})
	s.Set(func(s *Assistant) {
		s.AskFunc = func(context.Context, *assistantv1.AskQuestionRequest) (*assistantv1.AskQuestionResponse, error) {
			calls++
			return nil, nil
		}
		s.WatchFunc = func(*assistantv1.WatchAnswerRequest, grpc.ServerStreamingServer[assistantv1.WatchAnswerResponse]) error {
			calls++
			return nil
		}
		s.ListFunc = func(context.Context, *assistantv1.ListMessagesRequest) (*assistantv1.ListMessagesResponse, error) {
			calls++
			return nil, nil
		}
		s.RetryFunc = func(context.Context, *assistantv1.RetryAnswerRequest) (*assistantv1.RetryAnswerResponse, error) {
			calls++
			return nil, nil
		}
		s.LimitFunc = func(context.Context, *assistantv1.GetLimitRequest) (*assistantv1.GetLimitResponse, error) {
			calls++
			return nil, nil
		}
	})
	_, _ = a.StartPhoneVerification(ctx, nil)
	_ = a.WatchPhoneVerification(nil, nil)
	_, _ = a.Register(ctx, nil)
	_, _ = a.RefreshTokens(ctx, nil)
	_, _ = a.Logout(ctx, nil)
	_, _ = c.GetConsentDocument(ctx, nil)
	_, _ = p.GetProfile(ctx, nil)
	_, _ = p.UpdateProfile(ctx, nil)
	_, _ = p.DeleteAccount(ctx, nil)
	_, _ = p.GrantAssistantConsent(ctx, nil)
	_, _ = p.RevokeAssistantConsent(ctx, nil)
	_, _ = s.AskQuestion(ctx, nil)
	_ = s.WatchAnswer(nil, nil)
	_, _ = s.ListMessages(ctx, nil)
	_, _ = s.RetryAnswer(ctx, nil)
	_, _ = s.GetLimit(ctx, nil)
	if calls != 16 {
		t.Fatal(calls)
	}
	if out, err := (signingKeys{keys: NewKeys(t)}).GetSigningKeys(ctx, nil); err != nil || !strings.Contains(out.GetJwksJson(), "Ed25519") {
		t.Fatal(err)
	}
}

func TestUnitContractValidatesResponsesAndEvents(t *testing.T) {
	c := LoadContract(t)
	req := httptest.NewRequest("GET", "/v1/assistant/limit", nil)
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}, "Date": {"x"}}}
	body := c.Response(t, "GET", "/v1/assistant/limit", req, resp, []byte(`{"remaining":4,"daily_max":5,"resets_at":"2026-10-05T00:00:00+03:00"}`))
	if body["daily_max"] != 5.0 {
		t.Fatal(body)
	}
	resp.StatusCode = 204
	resp.Header = http.Header{"Date": {"x"}}
	if body := c.Response(t, "DELETE", "/v1/profile/assistant-consent", httptest.NewRequest("DELETE", "/v1/profile/assistant-consent", nil), resp, nil); body != nil {
		t.Fatal(body)
	}
	stream := "event: status\ndata: {\"status\":\"pending\"}\n\n: ping\n\nevent: status\ndata: {\"status\":\"verified\",\"is_new_user\":true,\"registration_token\":\"reg\"}\n\n"
	events, pings := c.Events(t, "GET", "/v1/auth/phone/events", strings.NewReader(stream))
	if len(events) != 2 || pings != 1 || events[1].Data["registration_token"] != "reg" {
		t.Fatalf("%v %d", events, pings)
	}
}
