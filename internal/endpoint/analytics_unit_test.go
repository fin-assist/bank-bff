package endpoint

import (
	"errors"
	"math"
	"net/http"
	"reflect"
	"strings"
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
	if !gathered(t, u, "finassist_untranslated_code_total", "GetAnalyticsResponse.period.bounds.coverage", 1) {
		t.Fatal("unknown coverage not counted")
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
	if !gathered(t, u, "finassist_untranslated_code_total", "ListPeriodsResponse.periods.coverage", 1) {
		t.Fatal("unknown item coverage not counted")
	}
	items := r.json["periods"].([]any)
	if r.Code != 200 || len(items) != 2 || items[0].(map[string]any)["coverage"] != "complete" || items[1].(map[string]any)["coverage"] != "partial" || items[0].(map[string]any)["range"].(map[string]any)["from"] != "2026-09-01T10:00:00.123456789+10:00" {
		t.Fatal(r.Body.String())
	}
}

func TestUnitAnalyticsIncompleteServiceResponsesFailClosed(t *testing.T) {
	cases := []struct {
		name, field, code string
		mutate            func(*analyticsv1.GetAnalyticsResponse)
	}{
		{"params", "GetAnalyticsResponse.params", "absent", func(r *analyticsv1.GetAnalyticsResponse) { r.Params = nil }},
		{"period enum", "GetAnalyticsResponse.params.period", "unknown", func(r *analyticsv1.GetAnalyticsResponse) { r.Params.Period = commonv1.PeriodType(99) }},
		{"period key", "GetAnalyticsResponse.params.period_key", "absent", func(r *analyticsv1.GetAnalyticsResponse) { r.Params.PeriodKey = "" }},
		{"period", "GetAnalyticsResponse.period", "absent", func(r *analyticsv1.GetAnalyticsResponse) { r.Period = nil }},
		{"state", "GetAnalyticsResponse.state", "absent", func(r *analyticsv1.GetAnalyticsResponse) { r.State = nil }},
		{"range", "GetAnalyticsResponse.period.range", "absent", func(r *analyticsv1.GetAnalyticsResponse) { r.Period.Range = nil }},
		{"start", "GetAnalyticsResponse.period.range.start", "absent", func(r *analyticsv1.GetAnalyticsResponse) { r.Period.Range.Start = nil }},
		{"end", "GetAnalyticsResponse.period.range.end", "absent", func(r *analyticsv1.GetAnalyticsResponse) { r.Period.Range.End = nil }},
		{"bad start", "GetAnalyticsResponse.period.range.start", "invalid", func(r *analyticsv1.GetAnalyticsResponse) {
			r.Period.Range.Start = &timestamppb.Timestamp{Seconds: 253402300800}
		}},
		{"bounds", "GetAnalyticsResponse.period.bounds", "absent", func(r *analyticsv1.GetAnalyticsResponse) { r.Period.Bounds = nil }},
		{"data from", "GetAnalyticsResponse.state.data_from", "absent", func(r *analyticsv1.GetAnalyticsResponse) { r.State.DataFrom = nil }},
		{"data to", "GetAnalyticsResponse.state.data_to", "absent", func(r *analyticsv1.GetAnalyticsResponse) { r.State.DataTo = nil }},
		{"calculated at", "GetAnalyticsResponse.state.calculated_at", "absent", func(r *analyticsv1.GetAnalyticsResponse) { r.State.CalculatedAt = nil }},
		{"gap", "GetAnalyticsResponse.period.bounds.gaps", "absent", func(r *analyticsv1.GetAnalyticsResponse) { r.Period.Bounds.Gaps = []*commonv1.TimeRange{nil} }},
		{"gap end", "GetAnalyticsResponse.period.bounds.gaps.end", "absent", func(r *analyticsv1.GetAnalyticsResponse) { r.Period.Bounds.Gaps[0].End = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := newUnitAPI(t, rest.StreamLimits{})
			in := analyticsFixture()
			in.Currency = ""
			in.Blocks = []*commonv1.Block{{Type: "text", V: 1, Lock: &commonv1.BlockLock{Reason: "unknown"}}}
			tc.mutate(in)
			u.f.analyticsGet = func(*analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) { return in, nil }
			r := u.serve(t, "GET", "/v1/analytics", "", nil)
			if r.Code != 500 || r.code() != "INTERNAL_ERROR" || strings.Contains(r.Body.String(), "1970-") {
				t.Fatal(r.Code, r.Body.String())
			}
			if !gathered(t, u, "finassist_untranslated_code_total", tc.field, 1) {
				t.Fatal(tc.field)
			}
			for _, field := range []string{"GetAnalyticsResponse.currency", "Block.lock.text"} {
				if !gathered(t, u, "finassist_empty_required_field_total", field, 1) {
					t.Fatal("each empty field must be counted before refusal", field)
				}
			}
		})
	}
	for _, tc := range incompletePeriodCases() {
		u := newUnitAPI(t, rest.StreamLimits{})
		in := &analyticsv1.PeriodItem{Key: "2026-09", Range: analyticsFixture().Period.Range}
		tc.mutate(in)
		u.f.analyticsList = func(*analyticsv1.ListPeriodsRequest) (*analyticsv1.ListPeriodsResponse, error) {
			return &analyticsv1.ListPeriodsResponse{Periods: []*analyticsv1.PeriodItem{in}}, nil
		}
		r := u.serve(t, "GET", "/v1/analytics/periods", "", nil)
		if r.Code != 500 || r.code() != "INTERNAL_ERROR" || unitAnalyticsCounter(t, u, tc.field, tc.code) != 1 {
			t.Fatal(tc.field, tc.code, r.Code, r.Body.String())
		}
	}

}

func TestUnitAnalyticsStructNonfiniteBinaryRoundtrip(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	in := analyticsFixture()
	data := &structpb.Struct{Fields: map[string]*structpb.Value{"nan": structpb.NewNumberValue(math.NaN()), "inf": structpb.NewNumberValue(math.Inf(1)), "negative_inf": structpb.NewNumberValue(math.Inf(-1))}}
	in.Blocks = []*commonv1.Block{{Type: "future", V: 1, Data: data}}
	wire, err := proto.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	out := &analyticsv1.GetAnalyticsResponse{}
	if err := proto.Unmarshal(wire, out); err != nil {
		t.Fatal(err)
	}
	u.f.analyticsGet = func(*analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) { return out, nil }
	r := u.serve(t, "GET", "/v1/analytics", "", nil)
	want := map[string]any{"nan": "NaN", "inf": "Infinity", "negative_inf": "-Infinity"}
	if r.Code != 200 || !reflect.DeepEqual(r.json["blocks"].([]any)[0].(map[string]any)["data"], want) {
		t.Fatal(r.Code, r.Body.String())
	}
}

type incompletePeriod struct {
	field, code string
	mutate      func(*analyticsv1.PeriodItem)
}

func incompletePeriodCases() []incompletePeriod {
	return []incompletePeriod{
		{"ListPeriodsResponse.periods.range", "absent", func(p *analyticsv1.PeriodItem) { p.Range = nil }},
		{"ListPeriodsResponse.periods.range.start", "absent", func(p *analyticsv1.PeriodItem) { p.Range.Start = nil }},
		{"ListPeriodsResponse.periods.range.end", "absent", func(p *analyticsv1.PeriodItem) { p.Range.End = nil }},
		{"ListPeriodsResponse.periods.range.end", "invalid", func(p *analyticsv1.PeriodItem) { p.Range.End = &timestamppb.Timestamp{Nanos: -1} }},
		{"ListPeriodsResponse.periods.key", "absent", func(p *analyticsv1.PeriodItem) { p.Key = "" }},
		{"ListPeriodsResponse.periods.key", "invalid", func(p *analyticsv1.PeriodItem) { p.Key = "2026-13" }},
	}
}

func unitAnalyticsCounter(t *testing.T, u *unitAPI, field, code string) float64 {
	t.Helper()
	families, err := u.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "finassist_untranslated_code_total" {
			continue
		}
		for _, metric := range family.Metric {
			labels := map[string]string{}
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["field"] == field && labels["code"] == code {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func TestUnitAnalyticsCalendarKeys(t *testing.T) {
	for _, key := range []string{"0001", "9999", "2026-01", "2026-12", "2026-Q1", "2026-Q4"} {
		u := newUnitAPI(t, rest.StreamLimits{})
		in := analyticsFixture()
		in.Params.PeriodKey = key
		period := "year"
		if strings.Contains(key, "-Q") {
			period = "quarter"
		} else if strings.Contains(key, "-") {
			period = "month"
		}
		in.Params.Period, _ = periodType(period)
		in.Navigation = &analyticsv1.Navigation{Previous: proto.String(key), Next: proto.String(key)}
		u.f.analyticsGet = func(*analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) { return in, nil }
		u.f.analyticsList = func(*analyticsv1.ListPeriodsRequest) (*analyticsv1.ListPeriodsResponse, error) {
			return &analyticsv1.ListPeriodsResponse{Periods: []*analyticsv1.PeriodItem{{Key: key, Range: in.Period.Range}}}, nil
		}
		for _, path := range []string{"/v1/analytics", "/v1/analytics/periods"} {
			if r := u.serve(t, "GET", path+"?period="+period, "", nil); r.Code != 200 {
				t.Fatal(key, r.Code, r.Body.String())
			}
		}
	}
	for _, key := range []string{"", "2026-00", "2026-13", "2026-Q0", "2026-Q5", "2026-1", "26", "2026-09-extra", "2026-W01"} {
		for _, target := range []string{"params", "previous", "next", "periods"} {
			u := newUnitAPI(t, rest.StreamLimits{})
			in := analyticsFixture()
			field := "GetAnalyticsResponse.params.period_key"
			switch target {
			case "params":
				in.Params.PeriodKey = key
			case "previous":
				in.Navigation.Previous = &key
				field = "GetAnalyticsResponse.navigation.previous"
			case "next":
				in.Navigation.Next = &key
				field = "GetAnalyticsResponse.navigation.next"
			case "periods":
				field = "ListPeriodsResponse.periods.key"
			}
			u.f.analyticsGet = func(*analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) { return in, nil }
			u.f.analyticsList = func(*analyticsv1.ListPeriodsRequest) (*analyticsv1.ListPeriodsResponse, error) {
				return &analyticsv1.ListPeriodsResponse{Periods: []*analyticsv1.PeriodItem{{Key: key, Range: in.Period.Range}}}, nil
			}
			path := "/v1/analytics"
			if target == "periods" {
				path += "/periods"
			}
			r := u.serve(t, "GET", path, "", nil)
			code := "invalid"
			if key == "" && (target == "params" || target == "periods") {
				code = "absent"
			}
			if r.Code != 500 || r.code() != "INTERNAL_ERROR" || unitAnalyticsCounter(t, u, field, code) != 1 {
				t.Fatal(target, key, r.Code, r.Body.String())
			}
		}
	}
}

func TestUnitAnalyticsOptionalTimestamps(t *testing.T) {
	for _, field := range []string{"period.bounds.data_from", "period.bounds.data_to", "state.last_operation_at", "blocks.lock.available_from", "blocks.action.filter.from", "blocks.action.filter.to"} {
		for _, bad := range []*timestamppb.Timestamp{{Seconds: 253402300800}, {Nanos: -1}, {Seconds: 253402300799}} {
			u := newUnitAPI(t, rest.StreamLimits{})
			in := analyticsFixture()
			in.Blocks = []*commonv1.Block{{Type: "future", V: 1, Lock: &commonv1.BlockLock{Text: "provided"}, Action: &commonv1.BlockAction{Type: "future", Filter: &commonv1.OperationsFilter{}}}}
			switch field {
			case "period.bounds.data_from":
				in.Period.Bounds.DataFrom = bad
			case "period.bounds.data_to":
				in.Period.Bounds.DataTo = bad
			case "state.last_operation_at":
				in.State.LastOperationAt = bad
			case "blocks.lock.available_from":
				in.Blocks[0].Lock.AvailableFrom = bad
			case "blocks.action.filter.from":
				in.Blocks[0].Action.Filter.From = bad
			case "blocks.action.filter.to":
				in.Blocks[0].Action.Filter.To = bad
			}
			u.f.analyticsGet = func(*analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) { return in, nil }
			r := u.serve(t, "GET", "/v1/analytics", "", nil)
			if r.Code != 500 || r.code() != "INTERNAL_ERROR" || unitAnalyticsCounter(t, u, "GetAnalyticsResponse."+field, "invalid") != 1 {
				t.Fatal(field, r.Code, r.Body.String())
			}
		}
	}
	u := newUnitAPI(t, rest.StreamLimits{})
	in := analyticsFixture()
	in.Period.Bounds.DataFrom = nil
	in.Period.Bounds.DataTo = nil
	in.State.LastOperationAt = nil
	in.Blocks = []*commonv1.Block{{Type: "future", V: 1, Lock: &commonv1.BlockLock{Text: "provided"}, Action: &commonv1.BlockAction{Type: "future", Filter: &commonv1.OperationsFilter{}}}}
	u.f.analyticsGet = func(*analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) { return in, nil }
	if r := u.serve(t, "GET", "/v1/analytics", "", nil); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestUnitAnalyticsLocalYearZeroIsValidRFC3339(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	in := analyticsFixture()
	// The earliest valid UTC Timestamp becomes local year0000 at a fixed -05:00
	// offset. RFC3339 permits four-digit 0000; avoid historical LMT offsets.
	first := timestamppb.New(time.Date(1, time.January, 1, 0, 0, 0, 0, time.UTC))
	if err := first.CheckValid(); err != nil {
		t.Fatal(err)
	}
	in.TimeZone = "Etc/GMT+5"
	in.State.CalculatedAt = first
	in.State.LastOperationAt = first
	in.Blocks = []*commonv1.Block{{Type: "future", V: 1, Lock: &commonv1.BlockLock{Text: "provided", AvailableFrom: first}, Action: &commonv1.BlockAction{Type: "future", Filter: &commonv1.OperationsFilter{From: first, To: first}}}}
	u.f.analyticsGet = func(*analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) { return in, nil }
	r := u.serve(t, "GET", "/v1/analytics", "", nil)
	want := "0000-12-31T19:00:00-05:00"
	if r.Code != 200 || r.json["state"].(map[string]any)["calculated_at"] != want || r.json["state"].(map[string]any)["last_operation_at"] != want {
		t.Fatal(r.Code, r.Body.String())
	}
	block := r.json["blocks"].([]any)[0].(map[string]any)
	if block["lock"].(map[string]any)["available_from"] != want || block["action"].(map[string]any)["filter"].(map[string]any)["from"] != want {
		t.Fatal(block)
	}
	for _, field := range []string{"state.calculated_at", "state.last_operation_at", "blocks.lock.available_from", "blocks.action.filter.from"} {
		if unitAnalyticsCounter(t, u, "GetAnalyticsResponse."+field, "invalid") != 0 {
			t.Fatal("valid lower bound counted invalid", field)
		}
	}
}
