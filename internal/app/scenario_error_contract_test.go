package app

import (
	"context"
	"strings"
	"testing"
	"time"

	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	userv1 "github.com/fin-assist/contracts/gen/go/finassist/user/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/fin-assist/bank-bff/internal/testpeer"
)

// Both transport levels exercise real serialized advice and the whole external
// method, including a keyed POST whose replay age cannot be proven by the BFF.
func scenarioRequestRetryAdvice(t *testing.T, start starter) {
	b := start(t, nil)
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1")})
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"absent", finerr.RateLimited(retryAt()), false},
		{"forbidden", finerr.WithRequestRetryInfo(finerr.RateLimited(retryAt()), false), false},
		{"permitted", finerr.WithRequestRetryInfo(finerr.RateLimited(retryAt()), true), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b.peers.Profile.Set(func(p *testpeer.Profile) {
				p.UpdateFunc = func(context.Context, *userv1.UpdateProfileRequest) (*userv1.UpdateProfileResponse, error) {
					return nil, tc.err
				}
				p.DeleteFunc = func(context.Context, *userv1.DeleteAccountRequest) (*userv1.DeleteAccountResponse, error) {
					return nil, tc.err
				}
				p.GetFunc = func(context.Context, *userv1.GetProfileRequest) (*userv1.GetProfileResponse, error) {
					return nil, tc.err
				}
				p.GrantFunc = func(context.Context, *userv1.GrantAssistantConsentRequest) (*userv1.GrantAssistantConsentResponse, error) {
					return nil, tc.err
				}
				p.RevokeFunc = func(context.Context, *userv1.RevokeAssistantConsentRequest) (*userv1.RevokeAssistantConsentResponse, error) {
					return nil, tc.err
				}
			})
			b.peers.Assistant.Set(func(a *testpeer.Assistant) {
				a.AskFunc = func(context.Context, *assistantv1.AskQuestionRequest) (*assistantv1.AskQuestionResponse, error) {
					return nil, tc.err
				}
				a.WatchFunc = func(*assistantv1.WatchAnswerRequest, grpc.ServerStreamingServer[assistantv1.WatchAnswerResponse]) error {
					return tc.err
				}
			})
			b.peers.Auth.Set(func(a *testpeer.Auth) {
				a.RefreshFunc = func(context.Context, *userv1.RefreshTokensRequest) (*userv1.RefreshTokensResponse, error) {
					return nil, tc.err
				}
			})
			for _, req := range []request{
				{method: "GET", path: "/v1/profile", route: "/v1/profile"},
				{method: "PATCH", path: "/v1/profile", route: "/v1/profile", body: `{"theme":"dark"}`},
				{method: "DELETE", path: "/v1/profile", route: "/v1/profile"},
				{method: "PUT", path: "/v1/profile/assistant-consent", route: "/v1/profile/assistant-consent", body: `{"version":"2026-09-28"}`},
				{method: "DELETE", path: "/v1/profile/assistant-consent", route: "/v1/profile/assistant-consent"},
				{method: "GET", path: "/v1/assistant/messages/a1/events", route: "/v1/assistant/messages/{answer_id}/events"},
				{method: "POST", path: "/v1/assistant/messages", route: "/v1/assistant/messages", body: `{"text":"Сколько?"}`},
				{method: "POST", path: "/v1/auth/token", route: "/v1/auth/token", body: `{"refresh_token":"refresh-before-network-failure"}`},
			} {
				req.header = auth
				if req.method == "POST" {
					req.header = base(map[string]string{"Authorization": auth["Authorization"], "Idempotency-Key": idemKey})
				}
				got := b.do(t, req)
				want := tc.want && req.method != "POST"
				if got.StatusCode != 429 || errorCode(got) != "RATE_LIMITED" || got.json["error"].(map[string]any)["retryable"] != want || got.Header.Get("Retry-After") == "" {
					t.Fatalf("%s %s: %d %s, want retryable=%v", req.method, req.path, got.StatusCode, got.body, want)
				}
			}
		})
	}
	// A local validation refusal is not permission to resend unchanged input.
	got := b.do(t, request{method: "GET", path: "/v1/assistant/messages?limit=0", route: "/v1/assistant/messages", header: auth})
	if got.StatusCode != 400 || got.json["error"].(map[string]any)["retryable"] != false {
		t.Fatalf("local validation: %d %s", got.StatusCode, got.body)
	}
}

func resultWire(t *testing.T, reason string) *statuspb.Status {
	t.Helper()
	wire, err := finerr.NewResultStatus(codes.FailedPrecondition, reason, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Diagnostics and parameters must not become the catalog's user-facing text.
	wire.Message = "SECRET_PROVIDER_SQL"
	return wire
}

func resultDetail(t *testing.T, msg proto.Message) *anypb.Any {
	t.Helper()
	detail, err := anypb.New(msg)
	if err != nil {
		t.Fatal(err)
	}
	return detail
}

// An embedded failure remains a successful read/stream; canonical presence, not
// text or the old enum, decides whether the legacy adapter is applicable.
func scenarioResultStatus(t *testing.T, start starter) {
	b := start(t, nil)
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1")})
	invalidAdvice := resultWire(t, "OFF_TOPIC")
	invalidAdvice.Details = append(invalidAdvice.Details, resultDetail(t, &commonv1.RequestRetryInfo{Retryable: true}))
	invalidWait := resultWire(t, "OFF_TOPIC")
	invalidWait.Details = append(invalidWait.Details, resultDetail(t, &errdetails.RetryInfo{RetryDelay: durationpb.New(time.Second)}))
	duplicate := resultWire(t, "OFF_TOPIC")
	duplicate.Details = append(duplicate.Details, proto.Clone(duplicate.Details[0]).(*anypb.Any))
	cases := []struct {
		name, want string
		wire       *statuspb.Status
		legacy     assistantv1.AnswerErrorCode
	}{
		{"legacy", "MODEL_ERROR", nil, assistantv1.AnswerErrorCode_ANSWER_ERROR_CODE_MODEL_ERROR},
		{"canonical wins", "OFF_TOPIC", resultWire(t, "OFF_TOPIC"), assistantv1.AnswerErrorCode_ANSWER_ERROR_CODE_MODEL_ERROR},
		{"open reason", "FUTURE_RESULT_FAILURE", resultWire(t, "FUTURE_RESULT_FAILURE"), assistantv1.AnswerErrorCode_ANSWER_ERROR_CODE_UNSPECIFIED},
		{"REST collision", "MAINTENANCE", resultWire(t, "MAINTENANCE"), assistantv1.AnswerErrorCode_ANSWER_ERROR_CODE_MODEL_ERROR},
		{"invalid empty", "INTERNAL_ERROR", &statuspb.Status{}, assistantv1.AnswerErrorCode_ANSWER_ERROR_CODE_MODEL_ERROR},
		{"invalid request advice", "INTERNAL_ERROR", invalidAdvice, assistantv1.AnswerErrorCode_ANSWER_ERROR_CODE_MODEL_ERROR},
		{"invalid relative wait", "INTERNAL_ERROR", invalidWait, assistantv1.AnswerErrorCode_ANSWER_ERROR_CODE_MODEL_ERROR},
		{"invalid duplicate", "INTERNAL_ERROR", duplicate, assistantv1.AnswerErrorCode_ANSWER_ERROR_CODE_MODEL_ERROR},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, permission := range []bool{false, true} {
				b.peers.Assistant.Set(func(a *testpeer.Assistant) {
					a.ListFunc = func(context.Context, *assistantv1.ListMessagesRequest) (*assistantv1.ListMessagesResponse, error) {
						return &assistantv1.ListMessagesResponse{TimeZone: "Europe/Moscow", Messages: []*assistantv1.Message{{
							Id: "a1", Role: assistantv1.Role_ROLE_ASSISTANT, Currency: "RUB", CreatedAt: created,
							Status: assistantv1.AnswerStatus_ANSWER_STATUS_FAILED, ErrorCode: tc.legacy, Error: tc.wire,
							ErrorText: proto.String("SECRET_PROVIDER_SQL"), Retryable: proto.Bool(permission),
						}}}, nil
					}
					a.WatchFunc = func(_ *assistantv1.WatchAnswerRequest, s grpc.ServerStreamingServer[assistantv1.WatchAnswerResponse]) error {
						if err := s.Send(&assistantv1.WatchAnswerResponse{TimeZone: "Europe/Moscow", Event: &assistantv1.WatchAnswerResponse_Snapshot{Snapshot: &assistantv1.AnswerSnapshot{Status: assistantv1.AnswerStatus_ANSWER_STATUS_FAILED, Currency: "RUB"}}}); err != nil {
							return err
						}
						return s.Send(&assistantv1.WatchAnswerResponse{Event: &assistantv1.WatchAnswerResponse_Error{Error: &assistantv1.AnswerError{
							Code: tc.legacy, Status: tc.wire, Text: "SECRET_PROVIDER_SQL", Message: "SECRET_PROVIDER_SQL", Retryable: permission, RemainingLimit: 5,
						}}})
					}
				})
				list := b.do(t, request{method: "GET", path: "/v1/assistant/messages", route: "/v1/assistant/messages", header: auth})
				msg := list.json["messages"].([]any)[0].(map[string]any)
				if list.StatusCode != 200 || msg["error_code"] != tc.want || msg["retryable"] != permission || msg["error_text"] != msg["error_user_message"] || strings.Contains(string(list.body), "SECRET_PROVIDER_SQL") {
					t.Fatalf("history: %d %s", list.StatusCode, list.body)
				}
				stream := b.raw(t, request{method: "GET", path: "/v1/assistant/messages/a1/events", route: "/v1/assistant/messages/{answer_id}/events", header: auth})
				events, _ := b.contract.Events(t, "GET", "/v1/assistant/messages/{answer_id}/events", strings.NewReader(string(stream.body)))
				if stream.StatusCode != 200 || len(events) != 2 || events[1].Name != "error" || events[1].Data["code"] != tc.want || events[1].Data["retryable"] != permission || events[1].Data["text"] != events[1].Data["user_message"] || strings.Contains(string(stream.body), "SECRET_PROVIDER_SQL") {
					t.Fatalf("stream: %d %s", stream.StatusCode, stream.body)
				}
			}
		})
	}
	// Presence of history permission is preserved even on legacy stored rows;
	// neither the enum nor an empty service text manufactures an explicit true.
	b.peers.Assistant.Set(func(a *testpeer.Assistant) {
		a.ListFunc = func(context.Context, *assistantv1.ListMessagesRequest) (*assistantv1.ListMessagesResponse, error) {
			return &assistantv1.ListMessagesResponse{Messages: []*assistantv1.Message{{
				Id: "legacy", Role: assistantv1.Role_ROLE_ASSISTANT, Currency: "RUB", CreatedAt: created,
				Status: assistantv1.AnswerStatus_ANSWER_STATUS_FAILED, ErrorCode: assistantv1.AnswerErrorCode_ANSWER_ERROR_CODE_MODEL_ERROR,
			}}}, nil
		}
	})
	legacy := b.do(t, request{method: "GET", path: "/v1/assistant/messages", route: "/v1/assistant/messages", header: auth})
	msg := legacy.json["messages"].([]any)[0].(map[string]any)
	if value, present := msg["retryable"]; present || msg["error_code"] != "MODEL_ERROR" {
		t.Fatalf("legacy permission presence=%v value=%v: %s", present, value, legacy.body)
	}
}
