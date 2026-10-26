package testpeer

import (
	"context"
	"sync"

	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	userv1 "github.com/fin-assist/contracts/gen/go/finassist/user/v1"
	"google.golang.org/grpc"
)

// Auth fakes user.AuthService; each field answers its RPC when set.
type Auth struct {
	userv1.UnimplementedAuthServiceServer
	mu           sync.Mutex
	StartFunc    func(context.Context, *userv1.StartPhoneVerificationRequest) (*userv1.StartPhoneVerificationResponse, error)
	WatchFunc    func(*userv1.WatchPhoneVerificationRequest, grpc.ServerStreamingServer[userv1.WatchPhoneVerificationResponse]) error
	RegisterFunc func(context.Context, *userv1.RegisterRequest) (*userv1.RegisterResponse, error)
	RefreshFunc  func(context.Context, *userv1.RefreshTokensRequest) (*userv1.RefreshTokensResponse, error)
	LogoutFunc   func(context.Context, *userv1.LogoutRequest) (*userv1.LogoutResponse, error)
}

// StartPhoneVerification implements the RPC.
func (a *Auth) StartPhoneVerification(ctx context.Context, r *userv1.StartPhoneVerificationRequest) (*userv1.StartPhoneVerificationResponse, error) {
	if f := get(&a.mu, &a.StartFunc); f != nil {
		return f(ctx, r)
	}
	return a.UnimplementedAuthServiceServer.StartPhoneVerification(ctx, r)
}

// WatchPhoneVerification implements the RPC.
func (a *Auth) WatchPhoneVerification(r *userv1.WatchPhoneVerificationRequest, s grpc.ServerStreamingServer[userv1.WatchPhoneVerificationResponse]) error {
	if f := get(&a.mu, &a.WatchFunc); f != nil {
		return f(r, s)
	}
	return a.UnimplementedAuthServiceServer.WatchPhoneVerification(r, s)
}

// Register implements the RPC.
func (a *Auth) Register(ctx context.Context, r *userv1.RegisterRequest) (*userv1.RegisterResponse, error) {
	if f := get(&a.mu, &a.RegisterFunc); f != nil {
		return f(ctx, r)
	}
	return a.UnimplementedAuthServiceServer.Register(ctx, r)
}

// RefreshTokens implements the RPC.
func (a *Auth) RefreshTokens(ctx context.Context, r *userv1.RefreshTokensRequest) (*userv1.RefreshTokensResponse, error) {
	if f := get(&a.mu, &a.RefreshFunc); f != nil {
		return f(ctx, r)
	}
	return a.UnimplementedAuthServiceServer.RefreshTokens(ctx, r)
}

// Logout implements the RPC.
func (a *Auth) Logout(ctx context.Context, r *userv1.LogoutRequest) (*userv1.LogoutResponse, error) {
	if f := get(&a.mu, &a.LogoutFunc); f != nil {
		return f(ctx, r)
	}
	return a.UnimplementedAuthServiceServer.Logout(ctx, r)
}

// Set replaces a handler under the fake's lock: tests change behaviour between calls.
func (a *Auth) Set(apply func(*Auth)) { a.mu.Lock(); defer a.mu.Unlock(); apply(a) }

// Consents fakes user.ConsentDocumentService.
type Consents struct {
	userv1.UnimplementedConsentDocumentServiceServer
	mu      sync.Mutex
	GetFunc func(context.Context, *userv1.GetConsentDocumentRequest) (*userv1.GetConsentDocumentResponse, error)
}

// GetConsentDocument implements the RPC.
func (c *Consents) GetConsentDocument(ctx context.Context, r *userv1.GetConsentDocumentRequest) (*userv1.GetConsentDocumentResponse, error) {
	if f := get(&c.mu, &c.GetFunc); f != nil {
		return f(ctx, r)
	}
	return c.UnimplementedConsentDocumentServiceServer.GetConsentDocument(ctx, r)
}

// Set replaces a handler under the fake's lock.
func (c *Consents) Set(apply func(*Consents)) { c.mu.Lock(); defer c.mu.Unlock(); apply(c) }

// Profile fakes user.ProfileService.
type Profile struct {
	userv1.UnimplementedProfileServiceServer
	mu         sync.Mutex
	GetFunc    func(context.Context, *userv1.GetProfileRequest) (*userv1.GetProfileResponse, error)
	GrantFunc  func(context.Context, *userv1.GrantAssistantConsentRequest) (*userv1.GrantAssistantConsentResponse, error)
	RevokeFunc func(context.Context, *userv1.RevokeAssistantConsentRequest) (*userv1.RevokeAssistantConsentResponse, error)
}

// GetProfile implements the RPC.
func (p *Profile) GetProfile(ctx context.Context, r *userv1.GetProfileRequest) (*userv1.GetProfileResponse, error) {
	if f := get(&p.mu, &p.GetFunc); f != nil {
		return f(ctx, r)
	}
	return p.UnimplementedProfileServiceServer.GetProfile(ctx, r)
}

// GrantAssistantConsent implements the RPC.
func (p *Profile) GrantAssistantConsent(ctx context.Context, r *userv1.GrantAssistantConsentRequest) (*userv1.GrantAssistantConsentResponse, error) {
	if f := get(&p.mu, &p.GrantFunc); f != nil {
		return f(ctx, r)
	}
	return p.UnimplementedProfileServiceServer.GrantAssistantConsent(ctx, r)
}

// RevokeAssistantConsent implements the RPC.
func (p *Profile) RevokeAssistantConsent(ctx context.Context, r *userv1.RevokeAssistantConsentRequest) (*userv1.RevokeAssistantConsentResponse, error) {
	if f := get(&p.mu, &p.RevokeFunc); f != nil {
		return f(ctx, r)
	}
	return p.UnimplementedProfileServiceServer.RevokeAssistantConsent(ctx, r)
}

// Set replaces a handler under the fake's lock.
func (p *Profile) Set(apply func(*Profile)) { p.mu.Lock(); defer p.mu.Unlock(); apply(p) }

// Assistant fakes assistant.AssistantService.
type Assistant struct {
	assistantv1.UnimplementedAssistantServiceServer
	mu        sync.Mutex
	AskFunc   func(context.Context, *assistantv1.AskQuestionRequest) (*assistantv1.AskQuestionResponse, error)
	WatchFunc func(*assistantv1.WatchAnswerRequest, grpc.ServerStreamingServer[assistantv1.WatchAnswerResponse]) error
	ListFunc  func(context.Context, *assistantv1.ListMessagesRequest) (*assistantv1.ListMessagesResponse, error)
	RetryFunc func(context.Context, *assistantv1.RetryAnswerRequest) (*assistantv1.RetryAnswerResponse, error)
	LimitFunc func(context.Context, *assistantv1.GetLimitRequest) (*assistantv1.GetLimitResponse, error)
}

// AskQuestion implements the RPC.
func (a *Assistant) AskQuestion(ctx context.Context, r *assistantv1.AskQuestionRequest) (*assistantv1.AskQuestionResponse, error) {
	if f := get(&a.mu, &a.AskFunc); f != nil {
		return f(ctx, r)
	}
	return a.UnimplementedAssistantServiceServer.AskQuestion(ctx, r)
}

// WatchAnswer implements the RPC.
func (a *Assistant) WatchAnswer(r *assistantv1.WatchAnswerRequest, s grpc.ServerStreamingServer[assistantv1.WatchAnswerResponse]) error {
	if f := get(&a.mu, &a.WatchFunc); f != nil {
		return f(r, s)
	}
	return a.UnimplementedAssistantServiceServer.WatchAnswer(r, s)
}

// ListMessages implements the RPC.
func (a *Assistant) ListMessages(ctx context.Context, r *assistantv1.ListMessagesRequest) (*assistantv1.ListMessagesResponse, error) {
	if f := get(&a.mu, &a.ListFunc); f != nil {
		return f(ctx, r)
	}
	return a.UnimplementedAssistantServiceServer.ListMessages(ctx, r)
}

// RetryAnswer implements the RPC.
func (a *Assistant) RetryAnswer(ctx context.Context, r *assistantv1.RetryAnswerRequest) (*assistantv1.RetryAnswerResponse, error) {
	if f := get(&a.mu, &a.RetryFunc); f != nil {
		return f(ctx, r)
	}
	return a.UnimplementedAssistantServiceServer.RetryAnswer(ctx, r)
}

// GetLimit implements the RPC.
func (a *Assistant) GetLimit(ctx context.Context, r *assistantv1.GetLimitRequest) (*assistantv1.GetLimitResponse, error) {
	if f := get(&a.mu, &a.LimitFunc); f != nil {
		return f(ctx, r)
	}
	return a.UnimplementedAssistantServiceServer.GetLimit(ctx, r)
}

// Set replaces a handler under the fake's lock.
func (a *Assistant) Set(apply func(*Assistant)) { a.mu.Lock(); defer a.mu.Unlock(); apply(a) }

func get[F any](mu *sync.Mutex, f *F) F {
	mu.Lock()
	defer mu.Unlock()
	return *f
}
