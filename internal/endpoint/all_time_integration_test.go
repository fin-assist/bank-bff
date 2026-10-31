package endpoint

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	analyticsv1 "github.com/fin-assist/contracts/gen/go/finassist/analytics/v1"
	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/fin-assist/bank-bff/internal/rest"
	"github.com/fin-assist/bank-bff/internal/testpeer"
)

// Exercise real protobuf transport and the mapper separately from public API assembly.
func TestIntegrationAllTimeTransportAndComposition(t *testing.T) {
	peers := testpeer.Start(t, testpeer.NewKeys(t), testpeer.NewStageFiles(t))
	conn, err := grpc.NewClient(testpeer.Target, peers.DialOptions()...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	reg := prometheus.NewRegistry()
	metrics, err := rest.NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	c := converter{loc: time.FixedZone("MSK", 3*60*60), metrics: metrics}
	client := analyticsv1.NewAnalyticsServiceClient(conn)
	for _, mode := range []commonv1.TransferMode{commonv1.TransferMode_TRANSFER_MODE_WITH, commonv1.TransferMode_TRANSFER_MODE_WITHOUT} {
		fixture := allTimeMessage()
		fixture.Params.TransferMode = mode
		fixture.Blocks[0].Action = &commonv1.BlockAction{Type: "select_analytics_period", Period: commonv1.PeriodType_PERIOD_TYPE_MONTH.Enum(), PeriodKey: proto.String("2025-12")}
		peers.Analytics.Set(func(a *testpeer.Analytics) {
			a.GetFunc = func(_ context.Context, in *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
				if in.GetPeriod() != commonv1.PeriodType_PERIOD_TYPE_ALL_TIME || in.GetTransferMode() != mode || in.GetPeriodKey() != "all_time" {
					t.Error(in)
				}
				return proto.Clone(fixture).(*analyticsv1.GetAnalyticsResponse), nil
			}
			a.ListFunc = func(_ context.Context, in *analyticsv1.ListPeriodsRequest) (*analyticsv1.ListPeriodsResponse, error) {
				if in.GetPeriod() != commonv1.PeriodType_PERIOD_TYPE_ALL_TIME {
					t.Error(in)
				}
				return &analyticsv1.ListPeriodsResponse{Periods: []*analyticsv1.PeriodItem{{Key: "all_time", Range: fixture.Period.Range, Coverage: commonv1.Coverage_COVERAGE_COMPLETE}}}, nil
			}
		})
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		wire, err := client.GetAnalytics(ctx, &analyticsv1.GetAnalyticsRequest{Period: commonv1.PeriodType_PERIOD_TYPE_ALL_TIME, PeriodKey: proto.String("all_time"), TransferMode: mode})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		out, ok := c.analytics(wire, commonv1.PeriodType_PERIOD_TYPE_ALL_TIME)
		if !ok || out.Params.Period != "all_time" || out.Params.Date != "all_time" || out.Period.IsCurrent || out.Navigation.Previous != nil || out.Navigation.Next != nil || out.Period.Range.From != "2025-10-31T00:00:00+03:00" {
			cancel()
			t.Fatal(out, ok)
		}
		block := (*out.Blocks)[0]
		if block.V != 2 || !reflect.DeepEqual(block.Data, fixture.Blocks[0].Data.AsMap()) || block.Action.Period == nil || *block.Action.Period != "month" || block.Action.Date == nil || *block.Action.Date != "2025-12" {
			cancel()
			t.Fatal(block)
		}
		page, err := client.ListPeriods(ctx, &analyticsv1.ListPeriodsRequest{Period: commonv1.PeriodType_PERIOD_TYPE_ALL_TIME})
		cancel()
		if err != nil || len(page.Periods) != 1 || !c.analyticsPeriodKey(commonv1.PeriodType_PERIOD_TYPE_ALL_TIME, page.Periods[0].Key, "ListPeriodsResponse.periods.key") {
			t.Fatal(page, err)
		}
	}
	for _, key := range []string{"", "2026-13"} {
		fixture := allTimeMessage()
		fixture.Blocks[0].Action = &commonv1.BlockAction{Type: "select_analytics_period", PeriodKey: &key}
		peers.Analytics.Set(func(a *testpeer.Analytics) {
			a.GetFunc = func(context.Context, *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
				return fixture, nil
			}
		})
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		wire, err := client.GetAnalytics(ctx, &analyticsv1.GetAnalyticsRequest{Period: commonv1.PeriodType_PERIOD_TYPE_ALL_TIME})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		out, ok := c.analytics(wire, commonv1.PeriodType_PERIOD_TYPE_ALL_TIME)
		if !ok || (*out.Blocks)[0].Action.Date != nil {
			t.Fatal(out, ok)
		}
	}
	for _, period := range []*commonv1.PeriodType{nil, commonv1.PeriodType_PERIOD_TYPE_UNSPECIFIED.Enum(), commonv1.PeriodType(99).Enum()} {
		fixture := allTimeMessage()
		fixture.Blocks[0].Action = &commonv1.BlockAction{Type: "select_analytics_period", Period: period, PeriodKey: proto.String("2026-10")}
		peers.Analytics.Set(func(a *testpeer.Analytics) {
			a.GetFunc = func(context.Context, *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
				return fixture, nil
			}
		})
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		wire, err := client.GetAnalytics(ctx, &analyticsv1.GetAnalyticsRequest{Period: commonv1.PeriodType_PERIOD_TYPE_ALL_TIME})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		before := allTimeTranslationCount(t, reg, "Block.action.period", "unknown")
		out, ok := c.analytics(wire, commonv1.PeriodType_PERIOD_TYPE_ALL_TIME)
		if !ok || (*out.Blocks)[0].Action.Period != nil || (*out.Blocks)[0].Action.Date == nil || *(*out.Blocks)[0].Action.Date != "2026-10" {
			t.Fatal(out, ok)
		}
		want := before
		if period != nil {
			want++
		}
		if allTimeTranslationCount(t, reg, "Block.action.period", "unknown") != want {
			t.Fatal("optional period diagnostic changed", period)
		}
	}
	for _, tc := range mismatchedPeriodKeys() {
		fixture := allTimeMessage()
		fixture.Params.Period, fixture.Params.PeriodKey = tc.period, tc.key
		peers.Analytics.Set(func(a *testpeer.Analytics) {
			a.GetFunc = func(context.Context, *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
				return fixture, nil
			}
		})
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		wire, err := client.GetAnalytics(ctx, &analyticsv1.GetAnalyticsRequest{Period: tc.period})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := c.analytics(wire, tc.period); ok {
			t.Fatal("crossed calendar response accepted", wire)
		}
	}
	peers.Assistant.Set(func(a *testpeer.Assistant) {
		a.ListFunc = func(context.Context, *assistantv1.ListMessagesRequest) (*assistantv1.ListMessagesResponse, error) {
			return &assistantv1.ListMessagesResponse{Messages: []*assistantv1.Message{{Text: proto.String("Синтетический ответ"), Chips: []*assistantv1.Chip{
				{Label: "Скрываемая история", Screen: assistantv1.ChipScreen_CHIP_SCREEN_ANALYTICS, Params: &commonv1.AnalyticsParams{Period: commonv1.PeriodType_PERIOD_TYPE_ALL_TIME, PeriodKey: "all_time"}},
				{Label: "Календарный квартал", Screen: assistantv1.ChipScreen_CHIP_SCREEN_ANALYTICS, Params: &commonv1.AnalyticsParams{Period: commonv1.PeriodType_PERIOD_TYPE_QUARTER, PeriodKey: "2026-Q4"}},
			}}}}, nil
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	page, err := assistantv1.NewAssistantServiceClient(conn).ListMessages(ctx, &assistantv1.ListMessagesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	message := page.GetMessages()[0]
	chips := c.chips(message.GetChips())
	if len(chips) != 1 || chips[0].Params.Period != "quarter" || message.GetText() != "Синтетический ответ" {
		t.Fatal(chips, message)
	}
}

// This component integration binds HTTP and composes the endpoint with the real
// gRPC transport; it does not construct the full public BFF assembly.
func TestIntegrationListPeriodsRejectsKeysOfOtherPeriodTypes(t *testing.T) {
	peers := testpeer.Start(t, testpeer.NewKeys(t), testpeer.NewStageFiles(t))
	conn, err := grpc.NewClient(testpeer.Target, peers.DialOptions()...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	reg := prometheus.NewRegistry()
	metrics, err := rest.NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{Analytics: analyticsv1.NewAnalyticsServiceClient(conn)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deps.analyticsPeriods(&rest.Call{W: w, R: r, Metrics: metrics, Log: slog.New(slog.DiscardHandler)})
	}))
	t.Cleanup(server.Close)
	client := &http.Client{Timeout: 5 * time.Second}
	for _, tc := range mismatchedPeriodKeys() {
		peers.Analytics.Set(func(a *testpeer.Analytics) {
			a.ListFunc = func(_ context.Context, in *analyticsv1.ListPeriodsRequest) (*analyticsv1.ListPeriodsResponse, error) {
				if in.GetPeriod() != tc.period {
					t.Error(in)
				}
				return &analyticsv1.ListPeriodsResponse{Periods: []*analyticsv1.PeriodItem{{Key: tc.key, Range: allTimeMessage().Period.Range}}}, nil
			}
		})
		period, _ := enumCode(tc.period)
		req, err := http.NewRequestWithContext(t.Context(), "GET", server.URL+"/v1/analytics/periods?period="+period, nil)
		if err != nil {
			t.Fatal(err)
		}
		before := allTimeTranslationCount(t, reg, "ListPeriodsResponse.periods.key", "invalid")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		var body struct{ Error struct{ Code string } }
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 500 || body.Error.Code != "INTERNAL_ERROR" || allTimeTranslationCount(t, reg, "ListPeriodsResponse.periods.key", "invalid") != before+1 {
			t.Fatal(tc, response.StatusCode, string(data))
		}
	}
}
