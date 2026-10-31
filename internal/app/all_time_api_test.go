package app

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	analyticsv1 "github.com/fin-assist/contracts/gen/go/finassist/analytics/v1"
	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/fin-assist/bank-bff/internal/testpeer"
)

func allTimeAPIMessage(t *testing.T) *analyticsv1.GetAnalyticsResponse {
	t.Helper()
	out := analyticsMessage(t)
	from := timestamppb.New(time.Date(2025, 12, 29, 21, 0, 0, 0, time.UTC))
	to := timestamppb.New(time.Date(2026, 10, 30, 21, 0, 0, 0, time.UTC))
	out.TimeZone = "Europe/Moscow"
	out.Params.Period, out.Params.PeriodKey = commonv1.PeriodType_PERIOD_TYPE_ALL_TIME, "all_time"
	out.Period.Range = &commonv1.TimeRange{Start: from, End: to}
	out.Period.IsCurrent = false
	out.Period.Bounds = &commonv1.DataBounds{Coverage: commonv1.Coverage_COVERAGE_PARTIAL, DataFrom: from, DataTo: to}
	out.State.DataFrom, out.State.DataTo = from, to
	out.Navigation = &analyticsv1.Navigation{}
	groups, err := structpb.NewStruct(map[string]any{"groups": []any{map[string]any{"tiles": []any{
		map[string]any{"id": "expense", "title": "Расходы за период", "value": 0.0, "value_kind": "amount"},
		map[string]any{"id": "income", "title": "Доходы за период", "value": 12000.0, "value_kind": "amount"},
		map[string]any{"id": "balance", "title": "Доходы минус расходы", "value": 12000.0, "value_kind": "amount"},
	}}, map[string]any{"title": "Дополнительные показатели", "tiles": []any{
		map[string]any{"id": "daily_expense", "title": "Средние расходы в день", "value": 0.0, "value_kind": "amount", "caption": "По покрытым дням не позже сегодня"},
	}}}, "future": true})
	if err != nil {
		t.Fatal(err)
	}
	chart, err := structpb.NewStruct(map[string]any{"kind": "bar", "value_kind": "amount", "points": []any{
		map[string]any{"range": map[string]any{"from": "2025-12-01T00:00:00+03:00", "to": "2026-01-01T00:00:00+03:00"}, "value": 0.0, "coverage": "partial", "data_from": "2025-12-30T00:00:00+03:00", "data_to": "2025-12-31T23:59:59+03:00", "action": map[string]any{"type": "select_analytics_period", "period": "month", "date": "2025-12"}},
		map[string]any{"range": map[string]any{"from": "2026-01-01T00:00:00+03:00", "to": "2026-02-01T00:00:00+03:00"}, "value": nil, "coverage": "no_data"},
	}, "future": "preserve"})
	if err != nil {
		t.Fatal(err)
	}
	out.Blocks = []*commonv1.Block{{Id: "tiles", Type: "tile_group", V: 2, AltText: proto.String("Итоги"), Data: groups}, {Id: "monthly_chart", Type: "chart", V: 1, AltText: proto.String("Месяцы"), Data: chart}}
	return out
}

func TestAPIAllTimeReadsPreserveServerDataAndIdentity(t *testing.T) {
	b := startBFF(t, nil)
	access := b.keys.Access("u1")
	auth := base(map[string]string{"Authorization": "Bearer " + access, "X-User-Id": "foreign", "X-Read-Primary": "1"})
	fixture := allTimeAPIMessage(t)
	requests := make(chan *analyticsv1.GetAnalyticsRequest, 8)
	b.peers.Analytics.Set(func(a *testpeer.Analytics) {
		a.GetFunc = func(ctx context.Context, in *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
			profileDeadline(t, ctx)
			requests <- proto.Clone(in).(*analyticsv1.GetAnalyticsRequest)
			out := proto.Clone(fixture).(*analyticsv1.GetAnalyticsResponse)
			out.Params.TransferMode = in.GetTransferMode()
			return out, nil
		}
		a.ListFunc = func(_ context.Context, in *analyticsv1.ListPeriodsRequest) (*analyticsv1.ListPeriodsResponse, error) {
			if in.GetPeriod() != commonv1.PeriodType_PERIOD_TYPE_ALL_TIME || in.UserId != nil {
				t.Error(in)
			}
			return &analyticsv1.ListPeriodsResponse{TimeZone: "Europe/Moscow", Periods: []*analyticsv1.PeriodItem{{Key: "all_time", Range: fixture.Period.Range, Coverage: commonv1.Coverage_COVERAGE_PARTIAL}}}, nil
		}
	})
	for _, mode := range []string{"with", "without"} {
		for _, date := range []string{"", "&date=all_time"} {
			r := b.do(t, request{method: "GET", path: "/v1/analytics?period=all_time&transfer_mode=" + mode + date + "&supports=tile_group@2,chart@1&user_id=foreign", route: "/v1/analytics", header: auth})
			if r.StatusCode != http.StatusOK {
				t.Fatal(r.StatusCode, string(r.body))
			}
			params := r.json["params"].(map[string]any)
			period := r.json["period"].(map[string]any)
			if params["period"] != "all_time" || params["date"] != "all_time" || params["transfer_mode"] != mode || period["is_current"] != false || period["range"].(map[string]any)["from"] != "2025-12-30T00:00:00+03:00" || period["range"].(map[string]any)["to"] != "2026-10-31T00:00:00+03:00" {
				t.Fatal(string(r.body))
			}
			nav := r.json["navigation"].(map[string]any)
			if nav["previous"] != nil || nav["next"] != nil {
				t.Fatal(nav)
			}
			blocks := r.json["blocks"].([]any)
			if len(blocks) != 2 || blocks[0].(map[string]any)["v"] != 2.0 {
				t.Fatal(blocks)
			}
			for i, block := range blocks {
				if !reflect.DeepEqual(block.(map[string]any)["data"], fixture.Blocks[i].Data.AsMap()) {
					t.Fatal("Struct changed", block)
				}
			}
			in := <-requests
			if in.GetPeriod() != commonv1.PeriodType_PERIOD_TYPE_ALL_TIME || in.UserId != nil || (date == "") != (in.PeriodKey == nil) || len(in.Supports) != 2 || in.Supports[0].GetMaxVersion() != 2 {
				t.Fatal(in)
			}
			call := lastCall(t, b, analyticsv1.AnalyticsService_GetAnalytics_FullMethodName, access, "")
			if len(call.MD.Get("x-user-id")) != 0 || len(call.MD.Get("x-read-primary")) != 1 {
				t.Fatal(call.MD)
			}
		}
	}
	r := b.do(t, request{method: "GET", path: "/v1/analytics/periods?period=all_time", route: "/v1/analytics/periods", header: auth})
	if r.StatusCode != 200 || len(r.json["periods"].([]any)) != 1 || r.json["periods"].([]any)[0].(map[string]any)["key"] != "all_time" {
		t.Fatal(r.StatusCode, string(r.body))
	}
	before := len(b.peers.Calls(analyticsv1.AnalyticsService_GetAnalytics_FullMethodName))
	r = b.do(t, request{method: "GET", path: "/v1/analytics?period=all_time", route: "/v1/analytics", header: base(nil)})
	if r.StatusCode != 401 || len(b.peers.Calls(analyticsv1.AnalyticsService_GetAnalytics_FullMethodName)) != before {
		t.Fatal("anonymous request reached analytics", r.StatusCode)
	}
}

func TestAPIAllTimeTypedActionTargets(t *testing.T) {
	b := startBFF(t, nil)
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1")})
	for _, tc := range []struct {
		name   string
		action *commonv1.BlockAction
		want   map[string]any
	}{
		{"both fields", &commonv1.BlockAction{Type: "select_analytics_period", Period: commonv1.PeriodType_PERIOD_TYPE_MONTH.Enum(), PeriodKey: proto.String("2025-12")},
			map[string]any{"type": "select_analytics_period", "period": "month", "date": "2025-12"}},
		{"period only", &commonv1.BlockAction{Type: "select_analytics_period", Period: commonv1.PeriodType_PERIOD_TYPE_MONTH.Enum()},
			map[string]any{"type": "select_analytics_period", "period": "month"}},
		{"date only", &commonv1.BlockAction{Type: "select_analytics_period", PeriodKey: proto.String("2025-12")},
			map[string]any{"type": "select_analytics_period", "date": "2025-12"}},
		{"absent optional fields", &commonv1.BlockAction{Type: "future_action"},
			map[string]any{"type": "future_action"}},
		{"unknown period", &commonv1.BlockAction{Type: "select_analytics_period", Period: commonv1.PeriodType(99).Enum(), PeriodKey: proto.String("2025-12")},
			map[string]any{"type": "select_analytics_period", "date": "2025-12"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := allTimeAPIMessage(t)
			fixture.Blocks[0].Action = tc.action
			b.peers.Analytics.Set(func(a *testpeer.Analytics) {
				a.GetFunc = func(context.Context, *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
					return proto.Clone(fixture).(*analyticsv1.GetAnalyticsResponse), nil
				}
			})
			r := b.do(t, request{method: "GET", path: "/v1/analytics?period=all_time", route: "/v1/analytics", header: auth})
			if r.StatusCode != http.StatusOK {
				t.Fatal(r.StatusCode, string(r.body))
			}
			blocks := r.json["blocks"].([]any)
			action := blocks[0].(map[string]any)["action"]
			if !reflect.DeepEqual(action, tc.want) {
				t.Fatalf("typed action JSON = %#v, want %#v", action, tc.want)
			}
		})
	}
}

func TestAPIAllTimeEmptyAndCanonicalValidation(t *testing.T) {
	b := startBFF(t, nil)
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1")})
	b.peers.Analytics.Set(func(a *testpeer.Analytics) {
		a.GetFunc = func(_ context.Context, in *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
			if in.PeriodKey != nil {
				return nil, finerr.ValidationError(&errdetails.BadRequest_FieldViolation{Field: "date", Reason: "INVALID_FORMAT", Description: "private synthetic diagnostic"})
			}
			return &analyticsv1.GetAnalyticsResponse{HasData: false, Blocks: allTimeAPIMessage(t).Blocks}, nil
		}
		a.ListFunc = func(context.Context, *analyticsv1.ListPeriodsRequest) (*analyticsv1.ListPeriodsResponse, error) {
			return &analyticsv1.ListPeriodsResponse{}, nil
		}
	})
	r := b.do(t, request{method: "GET", path: "/v1/analytics?period=all_time", route: "/v1/analytics", header: auth})
	if r.StatusCode != 200 || !reflect.DeepEqual(r.json, map[string]any{"has_data": false}) {
		t.Fatal(r.StatusCode, string(r.body))
	}
	r = b.do(t, request{method: "GET", path: "/v1/analytics/periods?period=all_time", route: "/v1/analytics/periods", header: auth})
	if r.StatusCode != 200 || len(r.json["periods"].([]any)) != 0 {
		t.Fatal(r.StatusCode, string(r.body))
	}
	for _, query := range []string{"period=all_time&date=2026", "period=all_time&date=2026-10", "period=month&date=all_time", "period=quarter&date=all_time", "period=year&date=all_time"} {
		r := b.do(t, request{method: "GET", path: "/v1/analytics?" + query, route: "/v1/analytics", header: auth})
		if r.StatusCode != 400 || errorCode(r) != "VALIDATION_ERROR" || strings.Contains(string(r.body), "private synthetic diagnostic") {
			t.Fatal(query, r.StatusCode, string(r.body))
		}
		details := r.json["error"].(map[string]any)["details"].([]any)
		if len(details) != 1 || details[0].(map[string]any)["field"] != "date" || details[0].(map[string]any)["code"] != "INVALID_FORMAT" {
			t.Fatal(details)
		}
	}
	bad := allTimeAPIMessage(t)
	bad.Params.PeriodKey = "2026"
	b.peers.Analytics.Set(func(a *testpeer.Analytics) {
		a.GetFunc = func(context.Context, *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
			return bad, nil
		}
	})
	r = b.do(t, request{method: "GET", path: "/v1/analytics?period=all_time", route: "/v1/analytics", header: auth})
	if r.StatusCode != 500 || errorCode(r) != "INTERNAL_ERROR" {
		t.Fatal(r.StatusCode, string(r.body))
	}
}

func TestAPIAllTimeDoesNotLeakThroughAssistantChips(t *testing.T) {
	b := startBFF(t, nil)
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1")})
	b.peers.Assistant.Set(func(a *testpeer.Assistant) {
		a.ListFunc = func(context.Context, *assistantv1.ListMessagesRequest) (*assistantv1.ListMessagesResponse, error) {
			message := &assistantv1.Message{Id: "synthetic-answer", Role: assistantv1.Role_ROLE_ASSISTANT, CreatedAt: timestamppb.New(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)), Status: assistantv1.AnswerStatus_ANSWER_STATUS_COMPLETE, Currency: "RUB", Blocks: []*assistantv1.Block{{Content: &assistantv1.Block_Text{Text: &assistantv1.TextBlock{Text: "Синтетический ответ"}}}}, Chips: []*assistantv1.Chip{
				{Label: "Скрываемая история", Screen: assistantv1.ChipScreen_CHIP_SCREEN_ANALYTICS, Params: &commonv1.AnalyticsParams{Period: commonv1.PeriodType_PERIOD_TYPE_ALL_TIME, PeriodKey: "all_time"}},
				{Label: "Некорректный календарь", Screen: assistantv1.ChipScreen_CHIP_SCREEN_ANALYTICS, Params: &commonv1.AnalyticsParams{Period: commonv1.PeriodType_PERIOD_TYPE_MONTH, PeriodKey: "all_time"}},
				{Label: "Неизвестный период", Screen: assistantv1.ChipScreen_CHIP_SCREEN_ANALYTICS, Params: &commonv1.AnalyticsParams{Period: commonv1.PeriodType(99), PeriodKey: "2026"}},
				{Label: "Календарный месяц", Screen: assistantv1.ChipScreen_CHIP_SCREEN_ANALYTICS, Params: &commonv1.AnalyticsParams{Period: commonv1.PeriodType_PERIOD_TYPE_MONTH, PeriodKey: "2026-10"}},
				{Label: "Календарный квартал", Screen: assistantv1.ChipScreen_CHIP_SCREEN_ANALYTICS, Params: &commonv1.AnalyticsParams{Period: commonv1.PeriodType_PERIOD_TYPE_QUARTER, PeriodKey: "2026-Q4"}},
				{Label: "Календарный год", Screen: assistantv1.ChipScreen_CHIP_SCREEN_ANALYTICS, Params: &commonv1.AnalyticsParams{Period: commonv1.PeriodType_PERIOD_TYPE_YEAR, PeriodKey: "2026"}},
			}}
			return &assistantv1.ListMessagesResponse{TimeZone: "Europe/Moscow", Messages: []*assistantv1.Message{message}}, nil
		}
	})
	r := b.do(t, request{method: "GET", path: "/v1/assistant/messages", route: "/v1/assistant/messages", header: auth})
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(r.body))
	}
	message := r.json["messages"].([]any)[0].(map[string]any)
	chips := message["chips"].([]any)
	if len(chips) != 3 || chips[2].(map[string]any)["label"] != "Календарный год" || !strings.Contains(string(r.body), "Синтетический ответ") || strings.Contains(string(r.body), "Скрываемая история") {
		t.Fatal(string(r.body))
	}
}

func TestAPIAllTimeChipsAreFilteredFromSnapshotsAndEvents(t *testing.T) {
	b := startBFF(t, nil)
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1")})
	blocked := &assistantv1.Chip{Label: "Неподдерживаемая история", Screen: assistantv1.ChipScreen_CHIP_SCREEN_ANALYTICS, Params: &commonv1.AnalyticsParams{Period: commonv1.PeriodType_PERIOD_TYPE_ALL_TIME, PeriodKey: "all_time"}}
	valid := &assistantv1.Chip{Label: "Календарный квартал", Screen: assistantv1.ChipScreen_CHIP_SCREEN_ANALYTICS, Params: &commonv1.AnalyticsParams{Period: commonv1.PeriodType_PERIOD_TYPE_QUARTER, PeriodKey: "2026-Q4"}}
	b.peers.Assistant.Set(func(a *testpeer.Assistant) {
		a.WatchFunc = func(_ *assistantv1.WatchAnswerRequest, stream grpc.ServerStreamingServer[assistantv1.WatchAnswerResponse]) error {
			for _, event := range []*assistantv1.WatchAnswerResponse{
				{TimeZone: "Europe/Moscow", Event: &assistantv1.WatchAnswerResponse_Snapshot{Snapshot: &assistantv1.AnswerSnapshot{Status: assistantv1.AnswerStatus_ANSWER_STATUS_GENERATING, Currency: "RUB", Chips: []*assistantv1.Chip{blocked, valid}}}},
				{Event: &assistantv1.WatchAnswerResponse_Chip{Chip: blocked}},
				{Event: &assistantv1.WatchAnswerResponse_Chip{Chip: valid}},
				{Event: &assistantv1.WatchAnswerResponse_Done{Done: &assistantv1.AnswerDone{Charged: false, RemainingLimit: 4}}},
			} {
				if err := stream.Send(event); err != nil {
					return err
				}
			}
			return nil
		}
	})
	route := "/v1/assistant/messages/{answer_id}/events"
	r := b.raw(t, request{method: "GET", path: "/v1/assistant/messages/synthetic/events", route: route, header: auth})
	events, _ := b.contract.Events(t, "GET", route, strings.NewReader(string(r.body)))
	if r.StatusCode != 200 || len(events) != 3 || events[0].Name != "snapshot" || events[1].Name != "chip" || events[2].Name != "done" || strings.Contains(string(r.body), "Неподдерживаемая история") {
		t.Fatal(r.StatusCode, string(r.body))
	}
	chips := events[0].Data["chips"].([]any)
	if len(chips) != 1 || chips[0].(map[string]any)["label"] != valid.Label || events[1].Data["label"] != valid.Label {
		t.Fatal(events)
	}
}
