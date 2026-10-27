package endpoint

import (
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	analyticsv1 "github.com/fin-assist/contracts/gen/go/finassist/analytics/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/fin-assist/bank-bff/internal/rest"
)

func analyticsFixture() *analyticsv1.GetAnalyticsResponse {
	from := timestamppb.New(time.Date(2026, 9, 1, 0, 0, 0, 123456789, time.UTC))
	to := timestamppb.New(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	return &analyticsv1.GetAnalyticsResponse{HasData: true, Currency: "RUB", TimeZone: "Asia/Vladivostok",
		Params:     &commonv1.AnalyticsParams{Period: commonv1.PeriodType_PERIOD_TYPE_QUARTER, PeriodKey: "2026-Q3", TransferMode: commonv1.TransferMode_TRANSFER_MODE_WITHOUT},
		Period:     &analyticsv1.PeriodInfo{Range: &commonv1.TimeRange{Start: from, End: to}, IsCurrent: true, Bounds: &commonv1.DataBounds{Coverage: commonv1.Coverage_COVERAGE_PARTIAL, DataFrom: from, DataTo: to, Gaps: []*commonv1.TimeRange{{Start: from, End: to}}}},
		Navigation: &analyticsv1.Navigation{Previous: proto.String("2026-Q2")},
		State:      &analyticsv1.ScreenState{Stale: true, DataFrom: from, DataTo: to, LastOperationAt: from, CalculatedAt: to, FullMonths: 3, UnreadLinesCount: 4, Recalculating: true},
		Blocks:     []*commonv1.Block{},
	}
}

func TestUnitAnalyticsInputsAndEmptyResponse(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	var got *analyticsv1.GetAnalyticsRequest
	u.f.analyticsGet = func(in *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
		got = in
		return &analyticsv1.GetAnalyticsResponse{Currency: "unexpected", Blocks: []*commonv1.Block{{Type: "ignored"}}}, nil
	}
	for _, tc := range []struct {
		q      string
		period commonv1.PeriodType
		mode   commonv1.TransferMode
		key    *string
	}{
		{"", commonv1.PeriodType_PERIOD_TYPE_MONTH, commonv1.TransferMode_TRANSFER_MODE_WITH, nil},
		{"?period=quarter&date=2026-09&transfer_mode=without&supports=tile_group@2,future@3&exp=analytics_order:b,future:x&user_id=foreign", commonv1.PeriodType_PERIOD_TYPE_QUARTER, commonv1.TransferMode_TRANSFER_MODE_WITHOUT, proto.String("2026-09")},
		{"?period=year&date=2026", commonv1.PeriodType_PERIOD_TYPE_YEAR, commonv1.TransferMode_TRANSFER_MODE_WITH, proto.String("2026")},
	} {
		r := u.serve(t, http.MethodGet, "/v1/analytics"+tc.q, "", nil)
		if r.Code != 200 || !reflect.DeepEqual(r.json, map[string]any{"has_data": false}) || got.GetPeriod() != tc.period || got.GetTransferMode() != tc.mode || !reflect.DeepEqual(got.PeriodKey, tc.key) || got.UserId != nil {
			t.Fatal(r.Code, r.Body.String(), got)
		}
		if tc.period == commonv1.PeriodType_PERIOD_TYPE_QUARTER && (len(got.Supports) != 2 || got.Supports[1].GetType() != "future" || got.Supports[1].GetMaxVersion() != 3 || got.Experiments["future"] != "x") {
			t.Fatal(got)
		}
	}
	if !gathered(t, u, "finassist_empty_required_field_total", "GetAnalyticsResponse.currency", 0) {
		t.Fatal("empty response counted currency")
	}
	for _, q := range []string{"period=week", "transfer_mode=future", "date=bad", "supports=x", "supports=@1", "supports=x@0", "supports=x@2147483648", "supports=x@one", "exp=x", "exp=:b", "exp=x:"} {
		got = nil
		r := u.serve(t, "GET", "/v1/analytics?"+q, "", nil)
		if r.Code != 400 || r.code() != "VALIDATION_ERROR" || got != nil {
			t.Fatal(q, r.Code, r.Body.String(), got)
		}
	}
	u.f.analyticsGet = func(*analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
		return nil, finerr.TokenRevoked()
	}
	if r := u.serve(t, "GET", "/v1/analytics", "", nil); r.Code != 401 {
		t.Fatal(r.Code)
	}
	u.f.analyticsList = func(in *analyticsv1.ListPeriodsRequest) (*analyticsv1.ListPeriodsResponse, error) {
		if in.GetPeriod() != commonv1.PeriodType_PERIOD_TYPE_MONTH || in.UserId != nil {
			t.Fatal(in)
		}
		return &analyticsv1.ListPeriodsResponse{}, nil
	}
	if r := u.serve(t, "GET", "/v1/analytics/periods", "", nil); r.Code != 200 || len(r.json["periods"].([]any)) != 0 {
		t.Fatal(r.Body.String())
	}
	if r := u.serve(t, "GET", "/v1/analytics/periods?period=week", "", nil); r.Code != 400 {
		t.Fatal(r.Code)
	}
	u.f.analyticsList = func(*analyticsv1.ListPeriodsRequest) (*analyticsv1.ListPeriodsResponse, error) {
		return nil, errors.New("secret")
	}
	if r := u.serve(t, "GET", "/v1/analytics/periods", "", nil); r.Code != 500 {
		t.Fatal(r.Code)
	}
	for _, d := range u.f.deadlines {
		if d <= 0 || d > UnaryDeadline {
			t.Fatal(d)
		}
	}
}

func TestUnitAnalyticsProjectionIsOpenAndPreservesPresence(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	in := analyticsFixture()
	in.Currency = ""
	data, _ := structpb.NewStruct(map[string]any{"basis": map[string]any{"from": "literal-date"}, "tiles": []any{map[string]any{"value": -123.0, "lock": map[string]any{"text": ""}, "new_field": true}}, "future": nil})
	in.Blocks = []*commonv1.Block{{Id: "metric", Type: "future", V: 8, Status: "future_status", Title: proto.String("title"), AltText: proto.String("alt"), EmptyText: proto.String("none"), Data: data, Lock: &commonv1.BlockLock{Reason: "new_lock", Required: proto.Int32(0), Available: proto.Int32(0), AvailableFrom: in.State.DataFrom}, Action: &commonv1.BlockAction{Type: "new_action", PaymentId: proto.String("id"), Label: proto.String("label"), Filter: &commonv1.OperationsFilter{From: in.State.DataFrom, AmountFromMinor: proto.Int64(0), TransferMode: commonv1.TransferMode_TRANSFER_MODE_WITHOUT}}}}
	before := proto.Clone(in)
	u.f.analyticsGet = func(*analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) { return in, nil }
	r := u.serve(t, "GET", "/v1/analytics", "", nil)
	if r.Code != 200 || r.json["currency"] != "RUB" || !proto.Equal(in, before) {
		t.Fatal(r.Code, r.Body.String())
	}
	p := r.json["period"].(map[string]any)
	state := r.json["state"].(map[string]any)
	nav := r.json["navigation"].(map[string]any)
	if p["data_from"] != "2026-09-01T10:00:00.123456789+10:00" || p["coverage"] != "partial" || p["is_current"] != true || len(p["gaps"].([]any)) != 1 || state["stale"] != true || state["recalculating"] != true || state["full_months"] != 3.0 || state["unread_lines_count"] != 4.0 || nav["previous"] != "2026-Q2" || nav["next"] != nil {
		t.Fatal(r.Body.String())
	}
	b := r.json["blocks"].([]any)[0].(map[string]any)
	if b["type"] != "future" || b["status"] != "future_status" || b["v"] != 8.0 || !reflect.DeepEqual(b["data"], data.AsMap()) || b["lock"].(map[string]any)["text"] != "Пока недоступно" || b["lock"].(map[string]any)["available"] != 0.0 || b["action"].(map[string]any)["filter"].(map[string]any)["amount_from"] != 0.0 {
		t.Fatal(r.Body.String())
	}
	for _, f := range []string{"GetAnalyticsResponse.currency", "Block.lock.text"} {
		if !gathered(t, u, "finassist_empty_required_field_total", f, 1) {
			t.Fatal(f)
		}
	}
	in.Currency = "EUR"
	in.Blocks[0].Lock.Text = "server text"
	in.Period.Bounds.Coverage = commonv1.Coverage(123)
	in.Navigation = nil
	in.Blocks[0].Data = nil
	in.Blocks[0].Action = nil
	in.State.LastOperationAt = nil
	r = u.serve(t, "GET", "/v1/analytics", "", nil)
	if r.Code != 200 || r.json["currency"] != "EUR" || r.json["period"].(map[string]any)["coverage"] != "partial" || r.json["state"].(map[string]any)["last_operation_at"] != nil {
		t.Fatal(r.Body.String())
	}
	for _, mutate := range []func(){func() { in.Params.Period = commonv1.PeriodType(123) }, func() { in.Params.PeriodKey = "" }, func() { in.Period = nil }} {
		mutate()
		if r := u.serve(t, "GET", "/v1/analytics", "", nil); r.Code != 500 {
			t.Fatal(r.Body.String())
		}
	}
	conv := converter{}
	if got := conv.analyticsBlock(nil); got.Type != "" {
		t.Fatal(got)
	}
}

func TestUnitAnalyticsPeriodsProjection(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	fixture := analyticsFixture()
	u.f.analyticsList = func(in *analyticsv1.ListPeriodsRequest) (*analyticsv1.ListPeriodsResponse, error) {
		if in.GetPeriod() != commonv1.PeriodType_PERIOD_TYPE_YEAR {
			t.Fatal(in)
		}
		return &analyticsv1.ListPeriodsResponse{TimeZone: "Asia/Vladivostok", Periods: []*analyticsv1.PeriodItem{{Key: "2026", Range: fixture.Period.Range, Coverage: commonv1.Coverage_COVERAGE_COMPLETE}, {Key: "2025", Range: fixture.Period.Range, Coverage: commonv1.Coverage(99)}}}, nil
	}
	r := u.serve(t, "GET", "/v1/analytics/periods?period=year", "", nil)
	items := r.json["periods"].([]any)
	if r.Code != 200 || len(items) != 2 || items[0].(map[string]any)["coverage"] != "complete" || items[1].(map[string]any)["coverage"] != "partial" || items[0].(map[string]any)["range"].(map[string]any)["from"] != "2026-09-01T10:00:00.123456789+10:00" {
		t.Fatal(r.Body.String())
	}
}
