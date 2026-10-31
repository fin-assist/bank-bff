package endpoint

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	analyticsv1 "github.com/fin-assist/contracts/gen/go/finassist/analytics/v1"
	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/fin-assist/bank-bff/internal/rest"
)

func allTimeMessage() *analyticsv1.GetAnalyticsResponse {
	from := timestamppb.New(time.Date(2025, 10, 30, 21, 0, 0, 0, time.UTC))
	to := timestamppb.New(time.Date(2026, 10, 31, 21, 0, 0, 0, time.UTC))
	out := analyticsFixture()
	out.TimeZone = "Europe/Moscow"
	out.Params.Period, out.Params.PeriodKey = commonv1.PeriodType_PERIOD_TYPE_ALL_TIME, "all_time"
	out.Period.Range = &commonv1.TimeRange{Start: from, End: to}
	out.Period.IsCurrent = false
	out.Period.Bounds = &commonv1.DataBounds{Coverage: commonv1.Coverage_COVERAGE_COMPLETE, DataFrom: from, DataTo: to}
	out.Navigation = &analyticsv1.Navigation{}
	out.State.DataFrom, out.State.DataTo = from, to
	data, _ := structpb.NewStruct(map[string]any{"groups": []any{map[string]any{"tiles": []any{
		map[string]any{"id": "expense", "title": "Расходы за период", "value": 0.0, "value_kind": "amount"},
		map[string]any{"id": "income", "title": "Доходы за период", "value": 12000.0, "value_kind": "amount"},
	}}}, "future": true})
	out.Blocks = []*commonv1.Block{{Id: "tiles", Type: "tile_group", V: 2, Data: data}}
	return out
}

func TestUnitAllTimeInputsAndProjection(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	for _, mode := range []commonv1.TransferMode{commonv1.TransferMode_TRANSFER_MODE_WITH, commonv1.TransferMode_TRANSFER_MODE_WITHOUT} {
		for _, date := range []string{"", "&date=all_time"} {
			var got *analyticsv1.GetAnalyticsRequest
			fixture := allTimeMessage()
			fixture.Params.TransferMode = mode
			u.f.analyticsGet = func(in *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
				got = in
				return fixture, nil
			}
			code, _ := enumCode(mode)
			r := u.serve(t, http.MethodGet, "/v1/analytics?period=all_time&transfer_mode="+code+date+"&supports=tile_group@2,chart@1&user_id=foreign", "", nil)
			if r.Code != 200 || got.GetPeriod() != commonv1.PeriodType_PERIOD_TYPE_ALL_TIME || got.GetTransferMode() != mode || got.UserId != nil {
				t.Fatal(r.Code, r.Body.String(), got)
			}
			if (date == "") != (got.PeriodKey == nil) || date != "" && got.GetPeriodKey() != "all_time" {
				t.Fatal("singleton request key changed", got)
			}
			params := r.json["params"].(map[string]any)
			period := r.json["period"].(map[string]any)
			navigation := r.json["navigation"].(map[string]any)
			if params["period"] != "all_time" || params["date"] != "all_time" || params["transfer_mode"] != code || period["is_current"] != false || navigation["previous"] != nil || navigation["next"] != nil {
				t.Fatal(r.Body.String())
			}
			block := r.json["blocks"].([]any)[0].(map[string]any)
			if block["v"] != 2.0 || !reflect.DeepEqual(block["data"], fixture.Blocks[0].Data.AsMap()) || period["range"].(map[string]any)["from"] != "2025-10-31T00:00:00+03:00" {
				t.Fatal("server interval or composition changed", r.Body.String())
			}
		}
	}
	u.f.analyticsList = func(in *analyticsv1.ListPeriodsRequest) (*analyticsv1.ListPeriodsResponse, error) {
		if in.GetPeriod() != commonv1.PeriodType_PERIOD_TYPE_ALL_TIME || in.UserId != nil {
			t.Fatal(in)
		}
		return &analyticsv1.ListPeriodsResponse{Periods: []*analyticsv1.PeriodItem{{Key: "all_time", Range: allTimeMessage().Period.Range, Coverage: commonv1.Coverage_COVERAGE_COMPLETE}}}, nil
	}
	if r := u.serve(t, "GET", "/v1/analytics/periods?period=all_time", "", nil); r.Code != 200 || r.json["periods"].([]any)[0].(map[string]any)["key"] != "all_time" {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestUnitAllTimeRejectsCrossedProducerKeys(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	for _, key := range []string{"2026", "2026-10", ""} {
		fixture := allTimeMessage()
		fixture.Params.PeriodKey = key
		u.f.analyticsGet = func(*analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) { return fixture, nil }
		if r := u.serve(t, "GET", "/v1/analytics?period=all_time", "", nil); r.Code != 500 || r.code() != "INTERNAL_ERROR" {
			t.Fatal(key, r.Code, r.Body.String())
		}
	}
	fixture := allTimeMessage()
	fixture.Params.Period = commonv1.PeriodType_PERIOD_TYPE_MONTH
	u.f.analyticsGet = func(*analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) { return fixture, nil }
	if r := u.serve(t, "GET", "/v1/analytics", "", nil); r.Code != 500 {
		t.Fatal(r.Code, r.Body.String())
	}
	u.f.analyticsList = func(*analyticsv1.ListPeriodsRequest) (*analyticsv1.ListPeriodsResponse, error) {
		return &analyticsv1.ListPeriodsResponse{Periods: []*analyticsv1.PeriodItem{{Key: "2026", Range: fixture.Period.Range}}}, nil
	}
	if r := u.serve(t, "GET", "/v1/analytics/periods?period=all_time", "", nil); r.Code != 500 {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestUnitSelectionActionPreservesOptionalTarget(t *testing.T) {
	metrics, err := rest.NewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	c := converter{loc: time.UTC, metrics: metrics}
	for _, period := range []commonv1.PeriodType{commonv1.PeriodType_PERIOD_TYPE_MONTH, commonv1.PeriodType_PERIOD_TYPE_ALL_TIME, commonv1.PeriodType(99)} {
		in := &commonv1.Block{Action: &commonv1.BlockAction{Type: "select_analytics_period", Period: &period, PeriodKey: proto.String("2026-10")}}
		before := proto.Clone(in)
		out := c.analyticsBlock(in)
		if out.Action == nil || out.Action.Date == nil || *out.Action.Date != "2026-10" || !proto.Equal(in, before) {
			t.Fatal(out)
		}
		code, known := enumCode(period)
		if known && (out.Action.Period == nil || *out.Action.Period != code) || !known && out.Action.Period != nil {
			t.Fatal(out.Action)
		}
	}
	if out := c.analyticsBlock(&commonv1.Block{Action: &commonv1.BlockAction{Type: "future"}}); out.Action.Period != nil || out.Action.Date != nil {
		t.Fatal(out.Action)
	}
}

func TestUnitAssistantSkipsUnsupportedAnalyticsChips(t *testing.T) {
	metrics, err := rest.NewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	c := converter{loc: time.UTC, metrics: metrics}
	for _, tc := range []struct {
		period commonv1.PeriodType
		key    string
		valid  bool
	}{
		{1, "2026-10", true}, {2, "2026-Q4", true}, {3, "2026", true},
		{4, "all_time", false}, {4, "2026", false}, {1, "all_time", false},
		{1, "2026-13", false}, {1, "2026", false}, {2, "2026-10", false},
		{3, "2026-Q4", false}, {0, "2026", false}, {99, "2026", false},
	} {
		chip := &assistantv1.Chip{Label: "Синтетический переход", Screen: assistantv1.ChipScreen_CHIP_SCREEN_ANALYTICS, Params: &commonv1.AnalyticsParams{Period: tc.period, PeriodKey: tc.key}}
		out := c.chips([]*assistantv1.Chip{chip, {Label: "Операции", Screen: assistantv1.ChipScreen_CHIP_SCREEN_OPERATIONS, Filters: &commonv1.OperationsFilter{Q: proto.String("synthetic")}}})
		want := 1
		if tc.valid {
			want = 2
		}
		if len(out) != want || out[len(out)-1].Screen != "operations" {
			t.Fatalf("period=%v key=%q output=%v", tc.period, tc.key, out)
		}
	}
}

func TestUnitOptionalActionDropsInvalidKeysAndResponseKeysMatchTheirType(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	for _, key := range []string{"", "2026-13", "garbage"} {
		fixture := allTimeMessage()
		fixture.Blocks[0].Action = &commonv1.BlockAction{Type: "select_analytics_period", Period: commonv1.PeriodType_PERIOD_TYPE_MONTH.Enum(), PeriodKey: &key}
		u.f.analyticsGet = func(*analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) { return fixture, nil }
		r := u.serve(t, "GET", "/v1/analytics?period=all_time", "", nil)
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body.String())
		}
		action := r.json["blocks"].([]any)[0].(map[string]any)["action"].(map[string]any)
		if _, exists := action["date"]; exists || action["period"] != "month" {
			t.Fatal(action)
		}
	}
	if !gathered(t, u, "finassist_untranslated_code_total", "Block.action.period_key", 3) {
		t.Fatal("invalid optional targets were not counted")
	}
	for _, period := range []commonv1.PeriodType{commonv1.PeriodType_PERIOD_TYPE_MONTH, commonv1.PeriodType_PERIOD_TYPE_QUARTER, commonv1.PeriodType_PERIOD_TYPE_YEAR} {
		fixture := allTimeMessage()
		fixture.Params.Period = period
		fixture.Params.PeriodKey = map[commonv1.PeriodType]string{1: "2026", 2: "2026-10", 3: "2026-Q4"}[period]
		u.f.analyticsGet = func(*analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) { return fixture, nil }
		if r := u.serve(t, "GET", "/v1/analytics", "", nil); r.Code != 500 {
			t.Fatal(period, r.Code, r.Body.String())
		}
	}
}
