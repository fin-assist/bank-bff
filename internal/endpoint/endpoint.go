// Package endpoint implements the REST/SSE operations of sign-in, profile,
// assistant and statement imports (FIN-268/FIN-342) over the internal gRPC services. Handlers translate contract
// JSON to protobuf and back; business checks, quotas and idempotent effects stay in
// the services (grpc-api.md 3, 6).
package endpoint

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"time"

	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	statementdatav1 "github.com/fin-assist/contracts/gen/go/finassist/statementdata/v1"
	userv1 "github.com/fin-assist/contracts/gen/go/finassist/user/v1"
	"google.golang.org/grpc"

	"github.com/fin-assist/bank-bff/internal/rest"
)

// AuthClient is the part of user.AuthService the BFF calls.
type AuthClient interface {
	StartPhoneVerification(ctx context.Context, in *userv1.StartPhoneVerificationRequest, opts ...grpc.CallOption) (*userv1.StartPhoneVerificationResponse, error)
	WatchPhoneVerification(ctx context.Context, in *userv1.WatchPhoneVerificationRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[userv1.WatchPhoneVerificationResponse], error)
	Register(ctx context.Context, in *userv1.RegisterRequest, opts ...grpc.CallOption) (*userv1.RegisterResponse, error)
	RefreshTokens(ctx context.Context, in *userv1.RefreshTokensRequest, opts ...grpc.CallOption) (*userv1.RefreshTokensResponse, error)
	Logout(ctx context.Context, in *userv1.LogoutRequest, opts ...grpc.CallOption) (*userv1.LogoutResponse, error)
}

// ConsentClient is user.ConsentDocumentService.
type ConsentClient interface {
	GetConsentDocument(ctx context.Context, in *userv1.GetConsentDocumentRequest, opts ...grpc.CallOption) (*userv1.GetConsentDocumentResponse, error)
}

// ProfileClient is the part of user.ProfileService in the FIN-268 scope.
type ProfileClient interface {
	GetProfile(ctx context.Context, in *userv1.GetProfileRequest, opts ...grpc.CallOption) (*userv1.GetProfileResponse, error)
	GrantAssistantConsent(ctx context.Context, in *userv1.GrantAssistantConsentRequest, opts ...grpc.CallOption) (*userv1.GrantAssistantConsentResponse, error)
	RevokeAssistantConsent(ctx context.Context, in *userv1.RevokeAssistantConsentRequest, opts ...grpc.CallOption) (*userv1.RevokeAssistantConsentResponse, error)
}

// AssistantClient is assistant.AssistantService.
type AssistantClient interface {
	AskQuestion(ctx context.Context, in *assistantv1.AskQuestionRequest, opts ...grpc.CallOption) (*assistantv1.AskQuestionResponse, error)
	WatchAnswer(ctx context.Context, in *assistantv1.WatchAnswerRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[assistantv1.WatchAnswerResponse], error)
	ListMessages(ctx context.Context, in *assistantv1.ListMessagesRequest, opts ...grpc.CallOption) (*assistantv1.ListMessagesResponse, error)
	RetryAnswer(ctx context.Context, in *assistantv1.RetryAnswerRequest, opts ...grpc.CallOption) (*assistantv1.RetryAnswerResponse, error)
	GetLimit(ctx context.Context, in *assistantv1.GetLimitRequest, opts ...grpc.CallOption) (*assistantv1.GetLimitResponse, error)
}

// StatementsClient is the six import RPCs of statementdata.StatementsService.
type StatementsClient interface {
	GetImportConfig(context.Context, *statementdatav1.GetImportConfigRequest, ...grpc.CallOption) (*statementdatav1.GetImportConfigResponse, error)
	ListUploads(context.Context, *statementdatav1.ListUploadsRequest, ...grpc.CallOption) (*statementdatav1.ListUploadsResponse, error)
	UploadStatement(context.Context, ...grpc.CallOption) (grpc.ClientStreamingClient[statementdatav1.UploadStatementRequest, statementdatav1.UploadStatementResponse], error)
	WatchUpload(context.Context, *statementdatav1.WatchUploadRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[statementdatav1.WatchUploadResponse], error)
	DeleteUpload(context.Context, *statementdatav1.DeleteUploadRequest, ...grpc.CallOption) (*statementdatav1.DeleteUploadResponse, error)
	ListUnreadLines(context.Context, *statementdatav1.ListUnreadLinesRequest, ...grpc.CallOption) (*statementdatav1.ListUnreadLinesResponse, error)
}

// Deadlines of internal calls (grpc-api.md 9).
const (
	// UnaryDeadline bounds ordinary unary calls; it is well below the listener write
	// timeout, so the error answer of an expired call is still written.
	UnaryDeadline = 5 * time.Second
	// UploadDeadline bounds file transfer and synchronous service validation.
	UploadDeadline = time.Minute
	// ImportStreamDeadline bounds WatchUpload; parsing runs independently.
	ImportStreamDeadline = 4 * time.Minute
	// AnswerStreamDeadline bounds WatchAnswer (generation ends within 3 minutes).
	AnswerStreamDeadline = 4 * time.Minute
	// PhoneStreamDeadline bounds WatchPhoneVerification: a request lives 5 minutes
	// and the service ends a pending stream at expires_at + 10 s.
	PhoneStreamDeadline = 6 * time.Minute
	// streamWriteMargin lets the last write of a stream finish after its deadline.
	streamWriteMargin = 10 * time.Second
)

// Deps are the peers and settings of the handlers.
type Deps struct {
	Auth       AuthClient
	Consents   ConsentClient
	Profile    ProfileClient
	Assistant  AssistantClient
	Statements StatementsClient
	// Heartbeat is the SSE comment interval; zero means rest.Heartbeat.
	Heartbeat time.Duration
	// AnswerStream and PhoneStream override the stream deadlines; zero keeps the defaults.
	AnswerStream, PhoneStream time.Duration
}

func (d Deps) heartbeat() time.Duration {
	if d.Heartbeat > 0 {
		return d.Heartbeat
	}
	return rest.Heartbeat
}

func orDefault(v, def time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return def
}

// Routes are the auth, profile, assistant and import contract operations.
func (d Deps) Routes() []rest.Route {
	return []rest.Route{
		{Method: http.MethodGet, Path: "/v1/statements/config", Protected: rest.Always, ReplaySafe: true, Handle: d.importConfig},
		{Method: http.MethodPost, Path: "/v1/statements", Protected: rest.Always, Idempotent: true, Input: rest.StreamingInput, Handle: d.uploadStatement},
		{Method: http.MethodGet, Path: "/v1/statements/{upload_id}/progress", Protected: rest.Always, ReplaySafe: true, Handle: d.watchUpload},
		{Method: http.MethodGet, Path: "/v1/statements", Protected: rest.Always, ReplaySafe: true, Handle: d.listUploads},
		{Method: http.MethodDelete, Path: "/v1/statements/{upload_id}", Protected: rest.Always, ReplaySafe: true, Handle: d.deleteUpload},
		{Method: http.MethodGet, Path: "/v1/statements/unread-lines", Protected: rest.Always, ReplaySafe: true, Handle: d.unreadLines},
		{Method: http.MethodPost, Path: "/v1/auth/phone", Idempotent: true, Handle: d.startPhone},
		{Method: http.MethodGet, Path: "/v1/auth/phone/events", ReplaySafe: true, Handle: d.watchPhone},
		{Method: http.MethodPost, Path: "/v1/auth/register", Idempotent: true, Handle: d.register},
		{Method: http.MethodPost, Path: "/v1/auth/token", Idempotent: true, Handle: d.refresh},
		{Method: http.MethodPost, Path: "/v1/auth/logout", Protected: rest.Always, Handle: d.logout},
		{Method: http.MethodGet, Path: "/v1/consents/{type}", Protected: assistantDocument, ReplaySafe: true, Handle: d.consentDocument},
		{Method: http.MethodGet, Path: "/v1/profile", Protected: rest.Always, ReplaySafe: true, Handle: d.profile},
		{Method: http.MethodPut, Path: "/v1/profile/assistant-consent", Protected: rest.Always, ReplaySafe: true, Handle: d.grantConsent},
		{Method: http.MethodDelete, Path: "/v1/profile/assistant-consent", Protected: rest.Always, ReplaySafe: true, Handle: d.revokeConsent},
		{Method: http.MethodGet, Path: "/v1/assistant/messages", Protected: rest.Always, ReplaySafe: true, Handle: d.listMessages},
		{Method: http.MethodPost, Path: "/v1/assistant/messages", Protected: rest.Always, Idempotent: true, Handle: d.ask},
		{Method: http.MethodGet, Path: "/v1/assistant/messages/{answer_id}/events", Protected: rest.Always, ReplaySafe: true, Handle: d.watchAnswer},
		{Method: http.MethodPost, Path: "/v1/assistant/messages/{answer_id}/retry", Protected: rest.Always, Idempotent: true, Handle: d.retry},
		{Method: http.MethodGet, Path: "/v1/assistant/limit", Protected: rest.Always, ReplaySafe: true, Handle: d.limit},
	}
}

// openStream takes a stream slot, extends the connection deadlines to the stream's
// own deadline before the first peer message is awaited (a late refusal is still a
// JSON answer), and returns the stream context: it ends at timeout, when the client
// leaves or on shutdown. done cancels it and releases the slot; it may be called more
// than once. On refusal the answer is written and ok is false.
func openStream(c *rest.Call, timeout time.Duration) (ctx context.Context, done func(), ok bool) {
	release, ok := c.Streams.Acquire(rest.HintsFrom(c.R.Context()).ClientIP)
	if !ok {
		c.Metrics.StreamRejected(c.Op.Route.Path)
		c.Refuse(c.Streams.Busy(c.Clock()), true)
		return nil, nil, false
	}
	if err := rest.ExtendDeadlines(c.W, time.Now().Add(timeout+streamWriteMargin)); err != nil {
		release()
		c.Log.WarnContext(c.R.Context(), "stream deadline not extended")
		rest.Internal().Write(c.W)
		return nil, nil, false
	}
	ctx, cancel := context.WithTimeout(c.R.Context(), timeout)
	detach := c.Streams.StopOnShutdown(cancel)
	return ctx, func() {
		detach()
		cancel()
		release()
	}, true
}

// unary returns the request context bounded by UnaryDeadline.
func unary(c *rest.Call) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.R.Context(), UnaryDeadline)
}

// decode reads the validated body into the flat struct v; a body that passed the
// schema always decodes, so a failure is answered as a validation error rather than
// trusted. Only members whose names equal a json tag of v exactly are decoded: the
// schema validated exact names, while encoding/json matches field names case
// insensitively, so {"text":"ok","Text":""} must not replace the validated text.
func decode(c *rest.Call, v any) bool {
	if err := decodeExact(c.Body, v); err != nil {
		rest.Validation(rest.Detail{Code: rest.DetailInvalidFormat, Message: "has invalid format"}).Write(c.W)
		return false
	}
	return true
}

func decodeExact(body []byte, v any) error {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(body, &members); err != nil {
		return err
	}
	t := reflect.TypeOf(v).Elem()
	exact := make(map[string]json.RawMessage, t.NumField())
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if value, ok := members[name]; ok && name != "" {
			exact[name] = value
		}
	}
	b, err := json.Marshal(exact)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
