package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	analyticsv1 "github.com/fin-assist/contracts/gen/go/finassist/analytics/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/fin-assist/bank-bff/internal/testpeer"
)

func analyticsMessage(t *testing.T) *analyticsv1.GetAnalyticsResponse {
	t.Helper()
	from := timestamppb.New(time.Date(2026, 9, 1, 0, 0, 0, 987654321, time.UTC))
	to := timestamppb.New(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	out := &analyticsv1.GetAnalyticsResponse{HasData: true, TimeZone: "Asia/Vladivostok", Currency: "RUB",
		Params:     &commonv1.AnalyticsParams{Period: commonv1.PeriodType_PERIOD_TYPE_MONTH, PeriodKey: "2026-09", TransferMode: commonv1.TransferMode_TRANSFER_MODE_WITH},
		Period:     &analyticsv1.PeriodInfo{Range: &commonv1.TimeRange{Start: from, End: to}, IsCurrent: true, Bounds: &commonv1.DataBounds{Coverage: commonv1.Coverage_COVERAGE_PARTIAL, DataFrom: from, DataTo: to, Gaps: []*commonv1.TimeRange{{Start: from, End: to}}}},
		Navigation: &analyticsv1.Navigation{Previous: proto.String("2026-08")}, State: &analyticsv1.ScreenState{Stale: true, LastOperationAt: from, DataFrom: from, DataTo: to, CalculatedAt: to, FullMonths: 2, UnreadLinesCount: 3, Recalculating: true}}
	// Every primitive, plus a future one, carries arbitrary nested data unchanged.
	for _, kind := range []string{"heading", "tile_group", "chart", "ranked_list", "insight_card", "rows", "text", "future"} {
		data, err := structpb.NewStruct(map[string]any{"text": "серверный текст", "basis": map[string]any{"range": map[string]any{"from": "2026-09-01T00:00:00+03:00", "to": "2026-10-01T00:00:00+03:00"}, "full_months": 2.0}, "tiles": []any{map[string]any{"id": "negative", "value": -12345.0, "value_kind": "amount", "lock": map[string]any{"text": ""}}}, "unknown_field": []any{nil, true, "literal"}})
		if err != nil {
			t.Fatal(err)
		}
		out.Blocks = append(out.Blocks, &commonv1.Block{Id: kind, Type: kind, V: 1, AltText: proto.String("доступная версия"), Data: data})
	}
	out.Blocks[1].Action = &commonv1.BlockAction{Type: "open_operations", Filter: &commonv1.OperationsFilter{From: from, To: to, CategoryId: proto.String("cat_uncategorized"), Kind: commonv1.KindFilter_KIND_FILTER_EXPENSE, AmountFromMinor: proto.Int64(0), Selection: proto.String("s_bank_fees"), SelectionName: proto.String("Комиссии"), TransferMode: commonv1.TransferMode_TRANSFER_MODE_WITHOUT}}
	out.Blocks[4].Lock = &commonv1.BlockLock{Reason: "future_reason", Required: proto.Int32(3), Available: proto.Int32(0), AvailableFrom: from}
	out.Blocks[4].Status = "locked"
	out.Blocks[5].Action = &commonv1.BlockAction{Type: "dismiss_regular_payment", PaymentId: proto.String(idemKey), Label: proto.String("Не подписка")}
	return out
}

func scenarioAnalytics(t *testing.T, start starter) {
	b := start(t, nil)
	access := b.keys.Access("u1")
	auth := base(map[string]string{"Authorization": "Bearer " + access, "X-Read-Primary": "1", "X-User-Id": "foreign", "Idempotency-Key": "not-a-key", "Cookie": "private=value"})
	var mu sync.Mutex
	var got *analyticsv1.GetAnalyticsRequest
	fixture := analyticsMessage(t)
	b.peers.Analytics.Set(func(a *testpeer.Analytics) {
		a.GetFunc = func(ctx context.Context, in *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
			profileDeadline(t, ctx)
			mu.Lock()
			got = proto.Clone(in).(*analyticsv1.GetAnalyticsRequest)
			mu.Unlock()
			out := proto.Clone(fixture).(*analyticsv1.GetAnalyticsResponse)
			out.Params.Period = in.GetPeriod()
			out.Params.TransferMode = in.GetTransferMode()
			if in.GetPeriod() == commonv1.PeriodType_PERIOD_TYPE_QUARTER {
				out.Params.PeriodKey = "2026-Q3"
			}
			if in.GetPeriod() == commonv1.PeriodType_PERIOD_TYPE_YEAR {
				out.Params.PeriodKey = "2026"
			}
			return out, nil
		}
	})
	for _, tc := range []struct{ q, key, period, mode string }{
		{"", "2026-09", "month", "with"}, {"?period=quarter&date=2026-09&transfer_mode=without&supports=chart@2,future@4&exp=analytics_order:b,future:x&user_id=foreign", "2026-Q3", "quarter", "without"}, {"?period=year&date=2026", "2026", "year", "with"},
	} {
		r := b.do(t, request{method: "GET", path: "/v1/analytics" + tc.q, route: "/v1/analytics", header: auth})
		if r.StatusCode != 200 {
			t.Fatal(r.StatusCode, string(r.body))
		}
		params := r.json["params"].(map[string]any)
		p := r.json["period"].(map[string]any)
		s := r.json["state"].(map[string]any)
		if params["date"] != tc.key || params["period"] != tc.period || params["transfer_mode"] != tc.mode || r.json["currency"] != "RUB" || p["data_from"] != "2026-09-01T10:00:00.987654321+10:00" || s["calculated_at"] != "2026-10-01T10:00:00+10:00" || s["recalculating"] != true || s["unread_lines_count"] != 3.0 || len(p["gaps"].([]any)) != 1 {
			t.Fatal(string(r.body))
		}
		blocks := r.json["blocks"].([]any)
		if len(blocks) != 8 {
			t.Fatal(blocks)
		}
		for i, block := range blocks {
			if !reflect.DeepEqual(block.(map[string]any)["data"], fixture.Blocks[i].Data.AsMap()) {
				t.Fatal("Struct mutated", block)
			}
		}
		lock := blocks[4].(map[string]any)["lock"].(map[string]any)
		if lock["text"] != "Пока недоступно" || lock["reason"] != "future_reason" || lock["available"] != 0.0 || lock["available_from"] != "2026-09-01T10:00:00.987654321+10:00" {
			t.Fatal(lock)
		}
		action := blocks[1].(map[string]any)["action"].(map[string]any)["filter"].(map[string]any)
		if action["selection"] != "s_bank_fees" || action["amount_from"] != 0.0 || action["transfer_mode"] != "without" || action["from"] != p["data_from"] {
			t.Fatal(action)
		}
		mu.Lock()
		in := got
		mu.Unlock()
		if in.UserId != nil || (tc.period == "quarter" && (in.GetPeriodKey() != "2026-09" || len(in.Supports) != 2 || in.Supports[1].GetMaxVersion() != 4 || in.Experiments["future"] != "x")) {
			t.Fatal(in)
		}
		call := lastCall(t, b, analyticsv1.AnalyticsService_GetAnalytics_FullMethodName, access, "")
		if len(call.MD.Get("x-read-primary")) != 1 || call.MD.Get("x-read-primary")[0] != "1" || len(call.MD.Get("x-user-id")) != 0 || len(call.MD.Get("cookie")) != 0 || len(call.MD.Get("idempotency-key")) != 0 {
			t.Fatal(call.MD)
		}
	}
	// Every server state passes through; no metric, threshold or date is recomputed.
	for _, state := range []string{"ready", "tentative", "locked", "none", "unavailable", "future"} {
		response := analyticsMessage(t)
		response.Blocks[0].Status = state
		response.Blocks[0].EmptyText = proto.String("готовый текст")
		response.Currency = ""
		b.peers.Analytics.Set(func(a *testpeer.Analytics) {
			a.GetFunc = func(context.Context, *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
				return response, nil
			}
		})
		r := b.do(t, request{method: "GET", path: "/v1/analytics", route: "/v1/analytics", header: auth})
		if r.StatusCode != 200 || r.json["blocks"].([]any)[0].(map[string]any)["status"] != state || r.json["currency"] != "RUB" {
			t.Fatal(string(r.body))
		}
	}
	b.peers.Analytics.Set(func(a *testpeer.Analytics) {
		a.GetFunc = func(context.Context, *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
			return &analyticsv1.GetAnalyticsResponse{Currency: "ignored", Blocks: fixture.Blocks}, nil
		}
	})
	r := b.do(t, request{method: "GET", path: "/v1/analytics", route: "/v1/analytics", header: auth})
	if r.StatusCode != 200 || !reflect.DeepEqual(r.json, map[string]any{"has_data": false}) {
		t.Fatal(string(r.body))
	}
	before := len(b.peers.Calls(analyticsv1.AnalyticsService_GetAnalytics_FullMethodName))
	for _, q := range []string{"period=week", "transfer_mode=unknown", "date=2026-13", "supports=chart@0", "supports=chart@x", "supports=@1", "supports=chart@2147483648", "exp=missing", "exp=:b", "exp=x:"} {
		r := b.do(t, request{method: "GET", path: "/v1/analytics?" + q, route: "/v1/analytics", header: auth})
		if r.StatusCode != 400 || errorCode(r) != "VALIDATION_ERROR" {
			t.Fatal(q, r.StatusCode, string(r.body))
		}
	}
	for _, body := range []string{`{}`, `secret`} {
		r := b.do(t, request{method: "GET", path: "/v1/analytics", route: "/v1/analytics", header: auth, body: body})
		if r.StatusCode != 400 {
			t.Fatal(r.StatusCode)
		}
	}
	if len(b.peers.Calls(analyticsv1.AnalyticsService_GetAnalytics_FullMethodName)) != before {
		t.Fatal("invalid input reached analytics")
	}
}

func scenarioAnalyticsPeriods(t *testing.T, start starter) {
	b := start(t, nil)
	access := b.keys.Access("u1")
	auth := base(map[string]string{"Authorization": "Bearer " + access, "X-Read-Primary": "1"})
	fixture := analyticsMessage(t)
	for _, period := range []string{"month", "quarter", "year"} {
		b.peers.Analytics.Set(func(a *testpeer.Analytics) {
			a.ListFunc = func(ctx context.Context, in *analyticsv1.ListPeriodsRequest) (*analyticsv1.ListPeriodsResponse, error) {
				profileDeadline(t, ctx)
				if in.GetUserId() != "" || strings.ToLower(strings.TrimPrefix(in.GetPeriod().String(), "PERIOD_TYPE_")) != period {
					t.Error(in)
				}
				return &analyticsv1.ListPeriodsResponse{TimeZone: "Asia/Vladivostok", Periods: []*analyticsv1.PeriodItem{{Key: "2026-09", Range: fixture.Period.Range, Coverage: commonv1.Coverage_COVERAGE_COMPLETE}, {Key: "2026-08", Range: fixture.Period.Range, Coverage: commonv1.Coverage(99)}}}, nil
			}
		})
		r := b.do(t, request{method: "GET", path: "/v1/analytics/periods?period=" + period, route: "/v1/analytics/periods", header: auth})
		if r.StatusCode != 200 || len(r.json["periods"].([]any)) != 2 || r.json["periods"].([]any)[0].(map[string]any)["coverage"] != "complete" || r.json["periods"].([]any)[1].(map[string]any)["coverage"] != "partial" {
			t.Fatal(string(r.body))
		}
		lastCall(t, b, analyticsv1.AnalyticsService_ListPeriods_FullMethodName, access, "")
	}
	b.peers.Analytics.Set(func(a *testpeer.Analytics) {
		a.ListFunc = func(context.Context, *analyticsv1.ListPeriodsRequest) (*analyticsv1.ListPeriodsResponse, error) {
			return &analyticsv1.ListPeriodsResponse{}, nil
		}
	})
	r := b.do(t, request{method: "GET", path: "/v1/analytics/periods", route: "/v1/analytics/periods", header: auth})
	if r.StatusCode != 200 || len(r.json["periods"].([]any)) != 0 {
		t.Fatal(string(r.body))
	}
	before := len(b.peers.Calls(analyticsv1.AnalyticsService_ListPeriods_FullMethodName))
	for _, req := range []request{{method: "GET", path: "/v1/analytics/periods?period=unknown", route: "/v1/analytics/periods", header: auth}, {method: "GET", path: "/v1/analytics/periods", route: "/v1/analytics/periods", header: auth, body: `{}`}} {
		if r := b.do(t, req); r.StatusCode != 400 {
			t.Fatal(r.StatusCode)
		}
	}
	if len(b.peers.Calls(analyticsv1.AnalyticsService_ListPeriods_FullMethodName)) != before {
		t.Fatal("invalid periods reached service")
	}
}

func scenarioAnalyticsErrors(t *testing.T, start starter) {
	b := start(t, nil)
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1")})
	failures := registry(finerr.InvalidToken(), finerr.TokenRevoked(), finerr.RateLimited(retryAt()), finerr.ValidationError(&errdetails.BadRequest_FieldViolation{Field: "period_key", Reason: "INVALID_FORMAT", Description: "SECRET_SQL"}), finerr.InternalError())
	failures = append(failures, peerError{errors.New("SECRET_SQL"), 500, "INTERNAL_ERROR"}, peerError{context.DeadlineExceeded, 500, "INTERNAL_ERROR"})
	for _, failure := range failures {
		for _, permission := range []bool{false, true} {
			err := finerr.WithRequestRetryInfo(failure.err, permission)
			b.peers.Analytics.Set(func(a *testpeer.Analytics) {
				a.GetFunc = func(context.Context, *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
					return nil, err
				}
				a.ListFunc = func(context.Context, *analyticsv1.ListPeriodsRequest) (*analyticsv1.ListPeriodsResponse, error) {
					return nil, err
				}
			})
			for _, path := range []string{"/v1/analytics", "/v1/analytics/periods"} {
				r := b.do(t, request{method: "GET", path: path, route: path, header: auth})
				if r.StatusCode != failure.status || errorCode(r) != failure.code || strings.Contains(string(r.body), "SECRET_SQL") {
					t.Fatal(path, r.StatusCode, string(r.body))
				}
				if failure.code == "RATE_LIMITED" && (r.Header.Get("Retry-After") == "" || r.json["error"].(map[string]any)["retryable"] != permission) {
					t.Fatal(string(r.body))
				}
			}
		}
	}
	// Authorization prevents both RPCs for missing, malformed, expired, wrong issuer and audience tokens.
	for _, token := range []string{"", "bad", b.keys.Token("u1", time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour), testpeer.UserIssuer, testpeer.UserAudience), b.keys.Token("u1", time.Now(), time.Now().Add(time.Hour), "foreign", testpeer.UserAudience), b.keys.Token("u1", time.Now(), time.Now().Add(time.Hour), testpeer.UserIssuer, "foreign")} {
		before := len(b.peers.Calls(""))
		for _, path := range []string{"/v1/analytics", "/v1/analytics/periods"} {
			r := b.do(t, request{method: "GET", path: path, route: path, header: base(map[string]string{"Authorization": "Bearer " + token})})
			if r.StatusCode != 401 {
				t.Fatal(r.StatusCode, string(r.body))
			}
		}
		if len(b.peers.Calls("")) != before {
			t.Fatal("invalid token reached service")
		}
	}
	// A broken required enum must fail closed, with a generic response.
	invalid := analyticsMessage(t)
	invalid.Params.Period = commonv1.PeriodType(99)
	b.peers.Analytics.Set(func(a *testpeer.Analytics) {
		a.GetFunc = func(context.Context, *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
			return invalid, nil
		}
	})
	r := b.do(t, request{method: "GET", path: "/v1/analytics", route: "/v1/analytics", header: auth})
	if r.StatusCode != 500 {
		t.Fatal(string(r.body))
	}
	// Error JSON remains the common contract envelope, not a serialized proto error.
	var body map[string]any
	if err := json.Unmarshal(r.body, &body); err != nil {
		t.Fatal(err)
	}
}
