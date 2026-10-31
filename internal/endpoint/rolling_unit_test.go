package endpoint

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	analyticsv1 "github.com/fin-assist/contracts/gen/go/finassist/analytics/v1"
	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/fin-assist/bank-bff/internal/rest"
)

func rollingFixture(period commonv1.PeriodType) *analyticsv1.GetAnalyticsResponse {
	out := analyticsFixture()
	key, _ := enumCode(period)
	start := time.Date(2025, 10, 31, 0, 0, 0, 0, time.FixedZone("MSK", 3*3600))
	if period == commonv1.PeriodType_PERIOD_TYPE_ROLLING_QUARTER {
		start = time.Date(2026, 7, 31, 0, 0, 0, 0, start.Location())
	}
	end := time.Date(2026, 10, 31, 0, 0, 0, 0, start.Location())
	from, to := timestamppb.New(start), timestamppb.New(end)
	out.Params.Period, out.Params.PeriodKey = period, key
	out.Period = &analyticsv1.PeriodInfo{Range: &commonv1.TimeRange{Start: from, End: to}, Bounds: &commonv1.DataBounds{Coverage: commonv1.Coverage_COVERAGE_PARTIAL, DataFrom: from, DataTo: timestamppb.New(end.Add(-time.Second))}}
	out.TimeZone = "Europe/Moscow"
	out.Navigation = &analyticsv1.Navigation{}
	out.State.DataFrom, out.State.DataTo = from, to
	data, _ := structpb.NewStruct(map[string]any{"kind": "bar", "value_kind": "amount", "points": []any{
		map[string]any{"range": map[string]any{"from": start.Format(time.RFC3339), "to": start.AddDate(0, 0, 1).Format(time.RFC3339)}, "value": -1.0, "coverage": "partial", "action": map[string]any{"type": "select_analytics_period", "period": "month", "date": start.Format("2006-01")}},
		map[string]any{"range": map[string]any{"from": "2026-08-01T00:00:00+03:00", "to": "2026-09-01T00:00:00+03:00"}, "value": nil, "coverage": "no_data"},
		map[string]any{"range": map[string]any{"from": "2026-10-01T00:00:00+03:00", "to": "2026-10-31T00:00:00+03:00"}, "value": 0.0, "coverage": "partial", "action": map[string]any{"type": "select_analytics_period", "period": "month", "date": "2026-10"}},
	}, "future": true})
	out.Blocks = []*commonv1.Block{{Id: "monthly_chart", Type: "chart", V: 1, Data: data, Action: &commonv1.BlockAction{Type: "select_analytics_period", Period: commonv1.PeriodType_PERIOD_TYPE_MONTH.Enum(), PeriodKey: proto.String(start.Format("2006-01"))}}}
	return out
}

func TestUnitRollingInputsAndExactProjection(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	for _, period := range []commonv1.PeriodType{commonv1.PeriodType_PERIOD_TYPE_ROLLING_YEAR, commonv1.PeriodType_PERIOD_TYPE_ROLLING_QUARTER} {
		key, _ := enumCode(period)
		for _, mode := range []commonv1.TransferMode{commonv1.TransferMode_TRANSFER_MODE_WITH, commonv1.TransferMode_TRANSFER_MODE_WITHOUT} {
			code, _ := enumCode(mode)
			for _, date := range []string{"", "&date=" + key} {
				fixture := rollingFixture(period)
				fixture.Params.TransferMode = mode
				var got *analyticsv1.GetAnalyticsRequest
				u.f.analyticsGet = func(in *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
					got = in
					return fixture, nil
				}
				r := u.serve(t, "GET", "/v1/analytics?period="+key+"&transfer_mode="+code+date+"&user_id=foreign", "", nil)
				if r.Code != 200 || got.GetPeriod() != period || got.GetTransferMode() != mode || got.UserId != nil || (got.PeriodKey == nil) != (date == "") {
					t.Fatal(r.Code, r.Body.String(), got)
				}
				params := r.json["params"].(map[string]any)
				if params["period"] != key || params["date"] != key || params["transfer_mode"] != code {
					t.Fatal(params)
				}
				block := r.json["blocks"].([]any)[0].(map[string]any)
				if !reflect.DeepEqual(block["data"], fixture.Blocks[0].Data.AsMap()) {
					t.Fatal("financial payload changed", r.Body.String())
				}
				if r.json["navigation"].(map[string]any)["previous"] != nil || r.json["period"].(map[string]any)["is_current"] != false {
					t.Fatal(r.Body.String())
				}
			}
		}
		u.f.analyticsList = func(in *analyticsv1.ListPeriodsRequest) (*analyticsv1.ListPeriodsResponse, error) {
			if in.GetPeriod() != period || in.UserId != nil {
				t.Error(in)
			}
			return &analyticsv1.ListPeriodsResponse{TimeZone: "Europe/Moscow", Periods: []*analyticsv1.PeriodItem{{Key: key, Range: rollingFixture(period).Period.Range, Coverage: commonv1.Coverage_COVERAGE_PARTIAL}}}, nil
		}
		if r := u.serve(t, "GET", "/v1/analytics/periods?period="+key, "", nil); r.Code != 200 || r.json["periods"].([]any)[0].(map[string]any)["key"] != key {
			t.Fatal(r.Code, r.Body.String())
		}
	}
}

func TestUnitRollingMalformedProducerAndEmpty(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	for _, mutate := range []func(*analyticsv1.GetAnalyticsResponse){
		func(r *analyticsv1.GetAnalyticsResponse) { r.Params.PeriodKey = "2026" },
		func(r *analyticsv1.GetAnalyticsResponse) { r.Params.PeriodKey = "rolling_quarter" },
		func(r *analyticsv1.GetAnalyticsResponse) {
			r.Params.Period = commonv1.PeriodType_PERIOD_TYPE_MONTH
			r.Params.PeriodKey = "2026-10"
		},
		func(r *analyticsv1.GetAnalyticsResponse) { r.Navigation.Next = proto.String("2027") },
		func(r *analyticsv1.GetAnalyticsResponse) { r.Navigation.Previous = proto.String("2025") },
	} {
		fixture := rollingFixture(commonv1.PeriodType_PERIOD_TYPE_ROLLING_YEAR)
		mutate(fixture)
		u.f.analyticsGet = func(*analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) { return fixture, nil }
		if r := u.serve(t, "GET", "/v1/analytics?period=rolling_year", "", nil); r.Code != 500 || r.code() != "INTERNAL_ERROR" {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	u.f.analyticsGet = func(*analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
		return &analyticsv1.GetAnalyticsResponse{}, nil
	}
	if r := u.serve(t, "GET", "/v1/analytics?period=rolling_year", "", nil); r.Code != http.StatusOK || len(r.json) != 1 || r.json["has_data"] != false {
		t.Fatal(r.Code, r.Body.String())
	}
	u.f.analyticsList = func(*analyticsv1.ListPeriodsRequest) (*analyticsv1.ListPeriodsResponse, error) {
		return &analyticsv1.ListPeriodsResponse{Periods: []*analyticsv1.PeriodItem{{Key: "2026", Range: rollingFixture(5).Period.Range}}}, nil
	}
	if r := u.serve(t, "GET", "/v1/analytics/periods?period=rolling_year", "", nil); r.Code != 500 {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestUnitRollingResponseMismatchCountsRequiredFields(t *testing.T) {
	for _, absent := range []bool{false, true} {
		u := newUnitAPI(t, rest.StreamLimits{})
		fixture := analyticsFixture()
		fixture.Currency = ""
		fixture.Blocks = []*commonv1.Block{{Type: "text", V: 1, Lock: &commonv1.BlockLock{Reason: "unknown"}}}
		field, code := "GetAnalyticsResponse.params.period", "invalid"
		if absent {
			fixture.Params = nil
			field, code = "GetAnalyticsResponse.params", "absent"
		}
		u.f.analyticsGet = func(*analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) { return fixture, nil }
		if r := u.serve(t, "GET", "/v1/analytics?period=rolling_year", "", nil); r.Code != 500 {
			t.Fatal(r.Code, r.Body.String())
		}
		for _, required := range []string{"GetAnalyticsResponse.currency", "Block.lock.text"} {
			if !gathered(t, u, "finassist_empty_required_field_total", required, 1) {
				t.Fatal("required field not counted before rejecting response", required)
			}
		}
		if allTimeTranslationCount(t, u.reg, field, code) != 1 {
			t.Fatal("wrong response diagnostic", field, code)
		}
		if absent && allTimeTranslationCount(t, u.reg, "GetAnalyticsResponse.params.period", "invalid") != 0 {
			t.Fatal("missing params diagnosed as mismatched period")
		}
	}
}

func TestUnitRollingResponseRejectedForOtherRequestedPeriods(t *testing.T) {
	for _, period := range []commonv1.PeriodType{commonv1.PeriodType_PERIOD_TYPE_ROLLING_YEAR, commonv1.PeriodType_PERIOD_TYPE_ROLLING_QUARTER} {
		for _, query := range []string{"", "?period=month", "?period=quarter", "?period=year", "?period=all_time"} {
			u := newUnitAPI(t, rest.StreamLimits{})
			u.f.analyticsGet = func(*analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
				return rollingFixture(period), nil
			}
			if r := u.serve(t, "GET", "/v1/analytics"+query, "", nil); r.Code != 500 || r.code() != "INTERNAL_ERROR" {
				t.Fatal(query, period, r.Code, r.Body.String())
			}
		}
	}
}

func TestUnitRollingChipsRemainCalendarOnly(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	c := converter{metrics: u.metrics}
	if c.analyticsPeriodKey(commonv1.PeriodType_PERIOD_TYPE_ALL_TIME, "2026", "GetAnalyticsResponse.params.period_key") {
		t.Fatal("a singleton accepted a calendar response key")
	}
	for _, key := range []string{"2026-10", "2026-Q4", "2026", "rolling_year", "rolling_quarter", "all_time", "", "2026-13"} {
		out := c.analyticsBlock(&commonv1.Block{Action: &commonv1.BlockAction{Type: "select_analytics_period", PeriodKey: &key}})
		valid := key != "" && key != "2026-13"
		if (out.Action.Date != nil) != valid {
			t.Fatal("malformed optional action key changed the response schema", key, out)
		}
	}
	for _, period := range []commonv1.PeriodType{commonv1.PeriodType_PERIOD_TYPE_ROLLING_YEAR, commonv1.PeriodType_PERIOD_TYPE_ROLLING_QUARTER, commonv1.PeriodType_PERIOD_TYPE_ALL_TIME} {
		key, _ := enumCode(period)
		if _, ok := c.chip(&assistantv1.Chip{Label: "Unexpected", Screen: assistantv1.ChipScreen_CHIP_SCREEN_ANALYTICS, Params: &commonv1.AnalyticsParams{Period: period, PeriodKey: key}}); ok {
			t.Fatal("singleton chip reached old clients", period)
		}
	}
	for _, tc := range []struct {
		period commonv1.PeriodType
		key    string
		ok     bool
	}{{1, "2026-10", true}, {2, "2026-Q4", true}, {3, "2026", true}, {1, "2026-Q4", false}, {2, "2026-10", false}, {3, "2026-10", false}, {0, "2026", false}, {1, "bad", false}} {
		if got := calendarAnalyticsKey(tc.period, tc.key); got != tc.ok {
			t.Fatal(tc, got)
		}
	}
}
