package app

import (
	"context"
	"net/http"
	"reflect"
	"strings"
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

func rollingAPIMessage(t *testing.T, period commonv1.PeriodType) *analyticsv1.GetAnalyticsResponse {
	t.Helper()
	r := analyticsMessage(t)
	key := "rolling_year"
	from := "2025-10-31T00:00:00+03:00"
	if period == commonv1.PeriodType_PERIOD_TYPE_ROLLING_QUARTER {
		key = "rolling_quarter"
		from = "2026-07-31T00:00:00+03:00"
	}
	start, err := time.Parse(time.RFC3339, from)
	if err != nil {
		t.Fatal(err)
	}
	end, err := time.Parse(time.RFC3339, "2026-10-31T00:00:00+03:00")
	if err != nil {
		t.Fatal(err)
	}
	s, e := timestamppb.New(start), timestamppb.New(end)
	r.Params.Period, r.Params.PeriodKey = period, key
	r.TimeZone = "Europe/Moscow"
	r.Period = &analyticsv1.PeriodInfo{Range: &commonv1.TimeRange{Start: s, End: e}, Bounds: &commonv1.DataBounds{Coverage: commonv1.Coverage_COVERAGE_PARTIAL, DataFrom: s, DataTo: timestamppb.New(end.Add(-time.Second))}}
	r.Navigation = &analyticsv1.Navigation{}
	r.State.DataFrom, r.State.DataTo = s, e
	data, err := structpb.NewStruct(map[string]any{"kind": "bar", "value_kind": "amount", "points": []any{
		map[string]any{"range": map[string]any{"from": from, "to": start.AddDate(0, 0, 1).Format(time.RFC3339)}, "coverage": "partial", "value": -1.0, "action": map[string]any{"type": "select_analytics_period", "period": "month", "date": start.Format("2006-01")}},
		map[string]any{"range": map[string]any{"from": "2026-08-01T00:00:00+03:00", "to": "2026-09-01T00:00:00+03:00"}, "coverage": "no_data", "value": nil},
		map[string]any{"range": map[string]any{"from": "2026-10-01T00:00:00+03:00", "to": "2026-10-31T00:00:00+03:00"}, "coverage": "partial", "value": 0.0, "action": map[string]any{"type": "select_analytics_period", "period": "month", "date": "2026-10"}},
	}, "future": true})
	if err != nil {
		t.Fatal(err)
	}
	r.Blocks = []*commonv1.Block{{Id: "monthly_chart", Type: "chart", V: 1, Data: data}}
	return r
}

func TestAPIRollingReadsPreserveRangesModesAndIdentity(t *testing.T) {
	b := startBFF(t, nil)
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1"), "X-User-Id": "foreign"})
	for _, period := range []commonv1.PeriodType{commonv1.PeriodType_PERIOD_TYPE_ROLLING_YEAR, commonv1.PeriodType_PERIOD_TYPE_ROLLING_QUARTER} {
		fixture := rollingAPIMessage(t, period)
		key := fixture.Params.GetPeriodKey()
		b.peers.Analytics.Set(func(a *testpeer.Analytics) {
			a.GetFunc = func(ctx context.Context, in *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
				profileDeadline(t, ctx)
				if in.GetPeriod() != period || in.UserId != nil || in.GetPeriodKey() != "" && in.GetPeriodKey() != key {
					t.Error(in)
				}
				out := proto.Clone(fixture).(*analyticsv1.GetAnalyticsResponse)
				out.Params.TransferMode = in.GetTransferMode()
				return out, nil
			}
			a.ListFunc = func(_ context.Context, in *analyticsv1.ListPeriodsRequest) (*analyticsv1.ListPeriodsResponse, error) {
				if in.GetPeriod() != period || in.UserId != nil {
					t.Error(in)
				}
				return &analyticsv1.ListPeriodsResponse{TimeZone: "Europe/Moscow", Periods: []*analyticsv1.PeriodItem{{Key: key, Range: fixture.Period.Range, Coverage: commonv1.Coverage_COVERAGE_PARTIAL}}}, nil
			}
		})
		for _, mode := range []string{"with", "without"} {
			for _, date := range []string{"", "&date=" + key} {
				r := b.do(t, request{method: "GET", path: "/v1/analytics?period=" + key + "&transfer_mode=" + mode + date + "&user_id=foreign", route: "/v1/analytics", header: auth})
				if r.StatusCode != 200 {
					t.Fatal(r.StatusCode, string(r.body))
				}
				params := r.json["params"].(map[string]any)
				p := r.json["period"].(map[string]any)
				if params["period"] != key || params["date"] != key || params["transfer_mode"] != mode || p["is_current"] != false || p["coverage"] != "partial" {
					t.Fatal(string(r.body))
				}
				if !reflect.DeepEqual(r.json["blocks"].([]any)[0].(map[string]any)["data"], fixture.Blocks[0].Data.AsMap()) {
					t.Fatal("server edge/zero/gap payload changed", string(r.body))
				}
			}
		}
		r := b.do(t, request{method: "GET", path: "/v1/analytics/periods?period=" + key, route: "/v1/analytics/periods", header: auth})
		if r.StatusCode != 200 || r.json["periods"].([]any)[0].(map[string]any)["key"] != key {
			t.Fatal(r.StatusCode, string(r.body))
		}
	}
}

func TestAPIRollingErrorsAndEmptyResponses(t *testing.T) {
	b := startBFF(t, nil)
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1")})
	for _, key := range []string{"2026", "rolling_quarter", "all_time"} {
		b.peers.Analytics.Set(func(a *testpeer.Analytics) {
			a.GetFunc = func(_ context.Context, in *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
				if in.GetPeriodKey() != key {
					t.Error(in)
				}
				return nil, finerr.ValidationError(&errdetails.BadRequest_FieldViolation{Field: "date", Reason: "INVALID_FORMAT", Description: "private diagnostic"})
			}
		})
		r := b.do(t, request{method: "GET", path: "/v1/analytics?period=rolling_year&date=" + key, route: "/v1/analytics", header: auth})
		if r.StatusCode != 400 || errorCode(r) != "VALIDATION_ERROR" || strings.Contains(string(r.body), "private diagnostic") {
			t.Fatal(r.StatusCode, string(r.body))
		}
		detail := r.json["error"].(map[string]any)["details"].([]any)[0].(map[string]any)
		if detail["field"] != "date" || detail["code"] != "INVALID_FORMAT" {
			t.Fatal(detail)
		}
	}
	for _, path := range []string{"/v1/analytics", "/v1/analytics/periods"} {
		if r := b.do(t, request{method: "GET", path: path + "?period=rolling_quarter", route: path}); r.StatusCode != http.StatusUnauthorized {
			t.Fatal(r.StatusCode, string(r.body))
		}
	}
	b.peers.Analytics.Set(func(a *testpeer.Analytics) {
		a.GetFunc = func(context.Context, *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
			return &analyticsv1.GetAnalyticsResponse{}, nil
		}
		a.ListFunc = func(context.Context, *analyticsv1.ListPeriodsRequest) (*analyticsv1.ListPeriodsResponse, error) {
			return &analyticsv1.ListPeriodsResponse{}, nil
		}
	})
	if r := b.do(t, request{method: "GET", path: "/v1/analytics?period=rolling_year", route: "/v1/analytics", header: auth}); r.StatusCode != 200 || len(r.json) != 1 || r.json["has_data"] != false {
		t.Fatal(r.StatusCode, string(r.body))
	}
	if r := b.do(t, request{method: "GET", path: "/v1/analytics/periods?period=rolling_year", route: "/v1/analytics/periods", header: auth}); r.StatusCode != 200 || len(r.json["periods"].([]any)) != 0 {
		t.Fatal(r.StatusCode, string(r.body))
	}
	b.peers.Analytics.Set(func(a *testpeer.Analytics) {
		a.GetFunc = func(context.Context, *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
			r := rollingAPIMessage(t, commonv1.PeriodType_PERIOD_TYPE_ROLLING_QUARTER)
			return r, nil
		}
	})
	if r := b.do(t, request{method: "GET", path: "/v1/analytics?period=rolling_year", route: "/v1/analytics", header: auth}); r.StatusCode != 500 || errorCode(r) != "INTERNAL_ERROR" {
		t.Fatal(r.StatusCode, string(r.body))
	}
}
