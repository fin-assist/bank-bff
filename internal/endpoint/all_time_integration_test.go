package endpoint

import (
	"context"
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
	metrics, err := rest.NewMetrics(prometheus.NewRegistry())
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
				return &analyticsv1.ListPeriodsResponse{Periods: []*analyticsv1.PeriodItem{{Key: "all_time", Range: fixture.Period.Range, Coverage: commonv1.Coverage_COVERAGE_PARTIAL}}}, nil
			}
		})
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		wire, err := client.GetAnalytics(ctx, &analyticsv1.GetAnalyticsRequest{Period: commonv1.PeriodType_PERIOD_TYPE_ALL_TIME, PeriodKey: proto.String("all_time"), TransferMode: mode})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		out, ok := c.analytics(wire)
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
