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
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/fin-assist/bank-bff/internal/config"
	"github.com/fin-assist/bank-bff/internal/testpeer"
)

var created = timestamppb.New(time.Date(2026, 10, 4, 19, 10, 0, 0, time.UTC))

func profileMessage(granted bool) *userv1.Profile {
	return &userv1.Profile{UserId: "u1", Phone: "+79161234567", Theme: userv1.Theme_THEME_SYSTEM, Timezone: "Europe/Moscow", CreatedAt: created,
		AssistantConsent: &userv1.AssistantConsent{Granted: granted, Version: proto.String("2026-09-28"), CurrentVersion: "2026-09-28"}}
}

func scenarioProfile(t *testing.T, start starter) {
	b := start(t, nil)
	access := b.keys.Access("u1")
	auth := base(map[string]string{"Authorization": "Bearer " + access, "X-Read-Primary": "1"})
	b.peers.Profile.Set(func(p *testpeer.Profile) {
		p.GetFunc = func(context.Context, *userv1.GetProfileRequest) (*userv1.GetProfileResponse, error) {
			return &userv1.GetProfileResponse{Profile: profileMessage(false)}, nil
		}
		p.GrantFunc = func(_ context.Context, in *userv1.GrantAssistantConsentRequest) (*userv1.GrantAssistantConsentResponse, error) {
			if in.GetVersion() != "2026-09-28" {
				return nil, finerr.ConsentOutdated()
			}
			return &userv1.GrantAssistantConsentResponse{Profile: profileMessage(true)}, nil
		}
		p.RevokeFunc = func(context.Context, *userv1.RevokeAssistantConsentRequest) (*userv1.RevokeAssistantConsentResponse, error) {
			return &userv1.RevokeAssistantConsentResponse{}, nil
		}
	})
	r := b.do(t, request{method: "GET", path: "/v1/profile", route: "/v1/profile", header: auth})
	if r.StatusCode != 200 || r.json["created_at"] != "2026-10-04T22:10:00+03:00" || r.json["consents"].([]any)[0].(map[string]any)["granted"] != false {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	c := lastCall(t, b, userv1.ProfileService_GetProfile_FullMethodName, access, "")
	if c.MD.Get("x-read-primary")[0] != "1" {
		t.Fatal("X-Read-Primary is forwarded")
	}
	grant := request{method: "PUT", path: "/v1/profile/assistant-consent", route: "/v1/profile/assistant-consent", body: `{"version":"2026-09-28"}`, header: auth}
	if r := b.do(t, grant); r.StatusCode != 200 || r.json["consents"].([]any)[0].(map[string]any)["granted"] != true {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	grant.body = `{"version":"2026-01-01"}`
	if r := b.do(t, grant); r.StatusCode != 422 || errorCode(r) != "CONSENT_OUTDATED" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	grant.body = `{"version":5}`
	if r := b.do(t, grant); r.StatusCode != 400 {
		t.Fatalf("version of a wrong type: %d", r.StatusCode)
	}
	if r := b.do(t, request{method: "DELETE", path: "/v1/profile/assistant-consent", route: "/v1/profile/assistant-consent", header: auth}); r.StatusCode != 204 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	lastCall(t, b, userv1.ProfileService_RevokeAssistantConsent_FullMethodName, access, "")
	// A deleted user (tombstone), a revoked session and an outage reach the client as the contract says.
	for _, pe := range registry(finerr.TokenRevoked(), finerr.InvalidToken(), finerr.RateLimited(retryAt())) {
		b.peers.Profile.Set(func(p *testpeer.Profile) {
			p.GetFunc = func(context.Context, *userv1.GetProfileRequest) (*userv1.GetProfileResponse, error) {
				return nil, pe.err
			}
		})
		if r := b.do(t, request{method: "GET", path: "/v1/profile", route: "/v1/profile", header: auth}); r.StatusCode != pe.status || errorCode(r) != pe.code {
			t.Fatalf("%v: %d %s", pe.err, r.StatusCode, r.body)
		}
	}
	// Tokens that fail verification never reach the service.
	calls := len(b.peers.Calls(""))
	now := time.Now()
	other := testpeer.NewKeys(t)
	for name, token := range map[string]string{
		"expired":        b.keys.Token("u1", now.Add(-time.Hour), now.Add(-time.Minute), testpeer.UserIssuer, testpeer.UserAudience),
		"foreign issuer": b.keys.Token("u1", now, now.Add(time.Hour), "https://evil", testpeer.UserAudience),
		"foreign aud":    b.keys.Token("u1", now, now.Add(time.Hour), testpeer.UserIssuer, "other"),
		"empty sub":      b.keys.Token("", now, now.Add(time.Hour), testpeer.UserIssuer, testpeer.UserAudience),
		"foreign key":    other.Access("u1"),
		"garbage":        "abc.def.ghi",
	} {
		r := b.do(t, request{method: "GET", path: "/v1/profile", route: "/v1/profile", header: base(map[string]string{"Authorization": "Bearer " + token})})
		if r.StatusCode != 401 || errorCode(r) != "INVALID_TOKEN" {
			t.Fatalf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
	for _, c := range b.peers.Calls("")[calls:] {
		if c.Method != userv1.UserInternalService_GetSigningKeys_FullMethodName {
			t.Fatalf("a rejected token reached %s", c.Method)
		}
	}
}

// scenarioKeyOutage: without a fresh key snapshot the token is not judged; the BFF
// answers 500, never 401, so a user service outage does not sign users out (FIN-293).
func scenarioKeyOutage(t *testing.T, start starter) {
	b := start(t, func(_ *config.Config, p *testpeer.Peers) { p.KeysUnavailable.Store(true) })
	r := b.do(t, request{method: "GET", path: "/v1/profile", route: "/v1/profile", header: base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1")})})
	if r.StatusCode != 500 || errorCode(r) != "INTERNAL_ERROR" || len(b.peers.Calls(userv1.ProfileService_GetProfile_FullMethodName)) != 0 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func scenarioAssistantUnary(t *testing.T, start starter) {
	b := start(t, nil)
	access := b.keys.Access("u1")
	auth := base(map[string]string{"Authorization": "Bearer " + access})
	keyed := base(map[string]string{"Authorization": "Bearer " + access, "Idempotency-Key": idemKey})
	var asked *assistantv1.AskQuestionRequest
	b.peers.Assistant.Set(func(a *testpeer.Assistant) {
		a.AskFunc = func(_ context.Context, in *assistantv1.AskQuestionRequest) (*assistantv1.AskQuestionResponse, error) {
			asked = in
			return &assistantv1.AskQuestionResponse{AnswerId: "a1", RemainingLimit: 4,
				Question: &assistantv1.Message{Id: "q1", Role: assistantv1.Role_ROLE_USER, Text: proto.String(in.GetText()), CreatedAt: created, Currency: "RUB"}}, nil
		}
		a.ListFunc = func(context.Context, *assistantv1.ListMessagesRequest) (*assistantv1.ListMessagesResponse, error) {
			return &assistantv1.ListMessagesResponse{TimeZone: "Europe/Moscow", HasOlder: false, Messages: []*assistantv1.Message{
				{Id: "q1", Role: assistantv1.Role_ROLE_USER, Text: proto.String("Сколько?"), CreatedAt: created, Currency: "RUB"},
				{Id: "a1", Role: assistantv1.Role_ROLE_ASSISTANT, Status: assistantv1.AnswerStatus_ANSWER_STATUS_COMPLETE, Charged: proto.Bool(true), CreatedAt: created, Currency: "RUB",
					Blocks: []*assistantv1.Block{{Content: &assistantv1.Block_Text{Text: &assistantv1.TextBlock{Text: "**7 200 ₽**"}}},
						{AltText: proto.String("Операций: 9"), Content: &assistantv1.Block_Rows{Rows: &assistantv1.RowsBlock{Rows: []*assistantv1.BlockRow{{Label: "Операций", Value: &assistantv1.BlockRow_Count{Count: 9}}}}}},
						{AltText: proto.String("график"), Content: &assistantv1.Block_Chart{Chart: &assistantv1.ChartBlock{Kind: assistantv1.ChartKind_CHART_KIND_BAR, Title: "По месяцам", ValueKind: assistantv1.ValueKind_VALUE_KIND_AMOUNT,
							Points: []*assistantv1.ChartPoint{{Range: &commonv1.TimeRange{Start: created, End: created}, Value: proto.Float64(1), Bounds: &commonv1.DataBounds{Coverage: commonv1.Coverage_COVERAGE_PARTIAL, DataTo: created}}}}}}},
					Chips: []*assistantv1.Chip{{Label: "Самокат", Screen: assistantv1.ChipScreen_CHIP_SCREEN_OPERATIONS, Filters: &commonv1.OperationsFilter{Selection: proto.String("s1"), TransferMode: commonv1.TransferMode_TRANSFER_MODE_WITH}},
						{Label: "Сентябрь", Screen: assistantv1.ChipScreen_CHIP_SCREEN_ANALYTICS, Params: &commonv1.AnalyticsParams{Period: commonv1.PeriodType_PERIOD_TYPE_MONTH, PeriodKey: "2026-09"}}},
					Source: &assistantv1.AnswerSource{CalculatedAt: created, TransferMode: commonv1.TransferMode_TRANSFER_MODE_WITH}},
				{Id: "a2", Role: assistantv1.Role_ROLE_ASSISTANT, Status: assistantv1.AnswerStatus_ANSWER_STATUS_FAILED, ErrorCode: assistantv1.AnswerErrorCode_ANSWER_ERROR_CODE_OFF_TOPIC,
					ErrorText: proto.String("<b>service</b>"), Retryable: proto.Bool(false), CreatedAt: created, Currency: "RUB"},
			}}, nil
		}
		a.RetryFunc = func(context.Context, *assistantv1.RetryAnswerRequest) (*assistantv1.RetryAnswerResponse, error) {
			return &assistantv1.RetryAnswerResponse{AnswerId: "a3", RemainingLimit: 3}, nil
		}
		a.LimitFunc = func(context.Context, *assistantv1.GetLimitRequest) (*assistantv1.GetLimitResponse, error) {
			return &assistantv1.GetLimitResponse{Remaining: 4, DailyMax: 5, TimeZone: "Europe/Moscow", ResetsAt: timestamppb.New(time.Date(2026, 10, 4, 21, 0, 0, 0, time.UTC))}, nil
		}
	})
	ask := request{method: "POST", path: "/v1/assistant/messages", route: "/v1/assistant/messages", body: `{"text":"Сколько я трачу на Самокат?","transfer_mode":"with"}`, header: keyed}
	if r := b.do(t, ask); r.StatusCode != 201 || r.json["answer_id"] != "a1" || asked.GetTransferMode() != commonv1.TransferMode_TRANSFER_MODE_WITH {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	lastCall(t, b, assistantv1.AssistantService_AskQuestion_FullMethodName, access, idemKey)
	// Members are decoded by their exact names, as the schema validated them: a
	// differently cased duplicate cannot replace the validated text.
	ask.body = `{"text":"valid","Text":"","TEXT":"x"}`
	if r := b.do(t, ask); r.StatusCode != 201 || asked.GetText() != "valid" {
		t.Fatalf("case-folded member: %d %q", r.StatusCode, asked.GetText())
	}
	for _, body := range []string{`{"text":""}`, `{"text":"` + strings.Repeat("я", 2001) + `"}`, `{"text":"x","transfer_mode":"sideways"}`, `{}`} {
		ask.body = body
		if r := b.do(t, ask); r.StatusCode != 400 || errorCode(r) != "VALIDATION_ERROR" {
			t.Fatalf("%s: %d %s", body, r.StatusCode, r.body)
		}
	}
	ask.body = `{"text":"x"}`
	resets := time.Date(2026, 10, 5, 0, 0, 0, 0, time.FixedZone("MSK", 3*3600))
	for _, pe := range registry(finerr.ConsentRequired(), finerr.LimitExceeded(resets), finerr.RateLimited(retryAt()), finerr.RequestInProgress(2*time.Second),
		finerr.IdempotencyConflict(), finerr.InvalidToken(), finerr.ValidationError(&errdetails.BadRequest_FieldViolation{Field: "text", Reason: "OUT_OF_RANGE"})) {
		b.peers.Assistant.Set(func(a *testpeer.Assistant) {
			a.AskFunc = func(context.Context, *assistantv1.AskQuestionRequest) (*assistantv1.AskQuestionResponse, error) {
				return nil, pe.err
			}
		})
		r := b.do(t, ask)
		if r.StatusCode != pe.status || errorCode(r) != pe.code {
			t.Fatalf("ask %v: %d %s", pe.err, r.StatusCode, r.body)
		}
		if pe.code == "LIMIT_EXCEEDED" && r.json["error"].(map[string]any)["resets_at"] != "2026-10-05T00:00:00+03:00" {
			t.Fatalf("resets_at: %s", r.body)
		}
	}
	list := b.do(t, request{method: "GET", path: "/v1/assistant/messages?limit=20", route: "/v1/assistant/messages", header: auth})
	msgs := list.json["messages"].([]any)
	failed := msgs[2].(map[string]any)
	if list.StatusCode != 200 || len(msgs) != 3 || failed["error_code"] != "OFF_TOPIC" || strings.Contains(string(list.body), "service</b>") || failed["retryable"] != false {
		t.Fatalf("%d %s", list.StatusCode, list.body)
	}
	if r := b.do(t, request{method: "GET", path: "/v1/assistant/messages?limit=0", route: "/v1/assistant/messages", header: auth}); r.StatusCode != 400 {
		t.Fatalf("limit=0: %d", r.StatusCode)
	}
	retry := request{method: "POST", path: "/v1/assistant/messages/a2/retry", route: "/v1/assistant/messages/{answer_id}/retry", header: keyed}
	if r := b.do(t, retry); r.StatusCode != 201 || r.json["answer_id"] != "a3" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	lastCall(t, b, assistantv1.AssistantService_RetryAnswer_FullMethodName, access, idemKey)
	for _, pe := range registry(finerr.NotFound(), finerr.RetryNotAllowed(), finerr.ConsentRequired(), finerr.LimitExceeded(resets), finerr.RateLimited(retryAt()), finerr.IdempotencyConflict()) {
		b.peers.Assistant.Set(func(a *testpeer.Assistant) {
			a.RetryFunc = func(context.Context, *assistantv1.RetryAnswerRequest) (*assistantv1.RetryAnswerResponse, error) {
				return nil, pe.err
			}
		})
		if r := b.do(t, retry); r.StatusCode != pe.status || errorCode(r) != pe.code {
			t.Fatalf("retry %v: %d %s", pe.err, r.StatusCode, r.body)
		}
	}
	retry.header = base(map[string]string{"Authorization": "Bearer " + access})
	if r := b.do(t, retry); r.StatusCode != 400 {
		t.Fatalf("retry without Idempotency-Key: %d", r.StatusCode)
	}
	if r := b.do(t, request{method: "GET", path: "/v1/assistant/limit", route: "/v1/assistant/limit", header: auth}); r.StatusCode != 200 || r.json["resets_at"] != "2026-10-05T00:00:00+03:00" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	lastCall(t, b, assistantv1.AssistantService_GetLimit_FullMethodName, access, "")
}

func scenarioWatchAnswer(t *testing.T, start starter) {
	b := start(t, nil, WithStreamTimings(20*time.Millisecond, 2*time.Second, time.Second))
	access := b.keys.Access("u1")
	auth := base(map[string]string{"Authorization": "Bearer " + access})
	b.peers.Assistant.Set(func(a *testpeer.Assistant) {
		a.WatchFunc = func(in *assistantv1.WatchAnswerRequest, s grpc.ServerStreamingServer[assistantv1.WatchAnswerResponse]) error {
			switch in.GetAnswerId() {
			case "foreign":
				return finerr.NotFound()
			case "failed":
				_ = s.Send(&assistantv1.WatchAnswerResponse{TimeZone: "Europe/Moscow", Event: &assistantv1.WatchAnswerResponse_Snapshot{Snapshot: &assistantv1.AnswerSnapshot{Status: assistantv1.AnswerStatus_ANSWER_STATUS_FAILED, Currency: "RUB"}}})
				return s.Send(&assistantv1.WatchAnswerResponse{Event: &assistantv1.WatchAnswerResponse_Error{Error: &assistantv1.AnswerError{Code: assistantv1.AnswerErrorCode_ANSWER_ERROR_CODE_MODEL_ERROR, Retryable: true, RemainingLimit: 5, Message: "timeout calling https://llm"}}})
			}
			msgs := []*assistantv1.WatchAnswerResponse{
				{TimeZone: "Europe/Moscow", Event: &assistantv1.WatchAnswerResponse_Snapshot{Snapshot: &assistantv1.AnswerSnapshot{Status: assistantv1.AnswerStatus_ANSWER_STATUS_GENERATING, Currency: "RUB",
					Blocks: []*assistantv1.Block{{Content: &assistantv1.Block_Text{Text: &assistantv1.TextBlock{Text: "За апрель"}}}}}}},
				{Event: &assistantv1.WatchAnswerResponse_Chunk{Chunk: &assistantv1.AnswerChunk{Index: 0, Text: " — сентябрь"}}},
				{Event: &assistantv1.WatchAnswerResponse_Block{Block: &assistantv1.AnswerBlock{Index: 1, Block: &assistantv1.Block{AltText: proto.String("Операций: 9"),
					Content: &assistantv1.Block_Rows{Rows: &assistantv1.RowsBlock{Rows: []*assistantv1.BlockRow{{Label: "Операций", Value: &assistantv1.BlockRow_Count{Count: 9}}}}}}}}},
				{TimeZone: "Europe/Moscow", Event: &assistantv1.WatchAnswerResponse_Source{Source: &assistantv1.AnswerSource{CalculatedAt: created, TransferMode: commonv1.TransferMode_TRANSFER_MODE_WITH,
					ChangedSince: []assistantv1.ChangedSince{assistantv1.ChangedSince_CHANGED_SINCE_UPLOADS}}}},
				{Event: &assistantv1.WatchAnswerResponse_Chip{Chip: &assistantv1.Chip{Label: "Сентябрь", Screen: assistantv1.ChipScreen_CHIP_SCREEN_ANALYTICS,
					Params: &commonv1.AnalyticsParams{Period: commonv1.PeriodType_PERIOD_TYPE_MONTH, PeriodKey: "2026-09", TransferMode: commonv1.TransferMode_TRANSFER_MODE_WITH}}}},
			}
			for _, m := range msgs {
				if err := s.Send(m); err != nil {
					return err
				}
			}
			<-time.After(100 * time.Millisecond) // the generation goes on: heartbeats are sent meanwhile
			return s.Send(&assistantv1.WatchAnswerResponse{Event: &assistantv1.WatchAnswerResponse_Done{Done: &assistantv1.AnswerDone{Charged: true, RemainingLimit: 4}}})
		}
	})
	route := "/v1/assistant/messages/{answer_id}/events"
	got := b.raw(t, request{method: "GET", path: "/v1/assistant/messages/a1/events", route: route, header: auth})
	resp, body := got.Response, got.body
	events, pings := b.contract.Events(t, "GET", route, strings.NewReader(string(body)))
	names := []string{}
	for _, e := range events {
		names = append(names, e.Name)
	}
	if resp.StatusCode != 200 || strings.Join(names, ",") != "snapshot,chunk,block,source,chip,done" || pings == 0 ||
		events[3].Data["changed_since_text"] != "с тех пор загружались выписки" {
		t.Fatalf("%d %v %d", resp.StatusCode, names, pings)
	}
	lastCall(t, b, assistantv1.AssistantService_WatchAnswer_FullMethodName, access, "")
	got = b.raw(t, request{method: "GET", path: "/v1/assistant/messages/failed/events", route: route, header: auth})
	resp, body = got.Response, got.body
	events, _ = b.contract.Events(t, "GET", route, strings.NewReader(string(body)))
	if resp.StatusCode != 200 || len(events) != 2 || events[1].Name != "error" || events[1].Data["code"] != "MODEL_ERROR" || events[1].Data["retryable"] != true ||
		events[1].Data["user_message"] != events[1].Data["text"] || strings.Contains(string(body), "llm") {
		t.Fatalf("%d %v", resp.StatusCode, events)
	}
	if r := b.do(t, request{method: "GET", path: "/v1/assistant/messages/foreign/events", route: route, header: auth}); r.StatusCode != 404 || errorCode(r) != "NOT_FOUND" {
		t.Fatalf("another user's answer: %d %s", r.StatusCode, r.body)
	}
	if r := b.do(t, request{method: "GET", path: "/v1/assistant/messages/a1/events", route: route, header: base(nil)}); r.StatusCode != 401 {
		t.Fatalf("no token: %d", r.StatusCode)
	}
}
