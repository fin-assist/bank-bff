package endpoint

import (
	"context"
	"reflect"
	"testing"
	"time"

	analyticsv1 "github.com/fin-assist/contracts/gen/go/finassist/analytics/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/fin-assist/bank-bff/internal/rest"
	"github.com/fin-assist/bank-bff/internal/testpeer"
)

func TestIntegrationRollingTransportAndOpaqueBlocks(t *testing.T) {
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
	c := converter{loc: time.FixedZone("MSK", 3*3600), metrics: metrics}
	client := analyticsv1.NewAnalyticsServiceClient(conn)
	for _, period := range []commonv1.PeriodType{commonv1.PeriodType_PERIOD_TYPE_ROLLING_YEAR, commonv1.PeriodType_PERIOD_TYPE_ROLLING_QUARTER} {
		key, _ := enumCode(period)
		for _, mode := range []commonv1.TransferMode{commonv1.TransferMode_TRANSFER_MODE_WITH, commonv1.TransferMode_TRANSFER_MODE_WITHOUT} {
			fixture := rollingFixture(period)
			fixture.Params.TransferMode = mode
			peers.Analytics.Set(func(a *testpeer.Analytics) {
				a.GetFunc = func(_ context.Context, in *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
					if in.GetPeriod() != period || in.GetPeriodKey() != key || in.GetTransferMode() != mode || in.UserId != nil {
						t.Error(in)
					}
					return proto.Clone(fixture).(*analyticsv1.GetAnalyticsResponse), nil
				}
				a.ListFunc = func(_ context.Context, in *analyticsv1.ListPeriodsRequest) (*analyticsv1.ListPeriodsResponse, error) {
					if in.GetPeriod() != period {
						t.Error(in)
					}
					return &analyticsv1.ListPeriodsResponse{Periods: []*analyticsv1.PeriodItem{{Key: key, Range: fixture.Period.Range, Coverage: commonv1.Coverage_COVERAGE_PARTIAL}}}, nil
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			wire, err := client.GetAnalytics(ctx, &analyticsv1.GetAnalyticsRequest{Period: period, PeriodKey: proto.String(key), TransferMode: mode})
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			out, ok := c.analytics(wire)
			if !ok || out.Params.Period != key || out.Params.Date != key || out.Period.IsCurrent || out.Navigation.Next != nil || out.Navigation.Previous != nil {
				cancel()
				t.Fatal(out, ok)
			}
			block := (*out.Blocks)[0]
			if !reflect.DeepEqual(block.Data, fixture.Blocks[0].Data.AsMap()) || block.Action.Period == nil || *block.Action.Period != "month" || *block.Action.Date != fixture.Blocks[0].Action.GetPeriodKey() {
				cancel()
				t.Fatal(block)
			}
			page, err := client.ListPeriods(ctx, &analyticsv1.ListPeriodsRequest{Period: period})
			cancel()
			if err != nil || len(page.GetPeriods()) != 1 || !c.analyticsResponseKey(period, page.GetPeriods()[0].GetKey(), "ListPeriodsResponse.periods.key") {
				t.Fatal(page, err)
			}
		}
	}
	fixture := rollingFixture(commonv1.PeriodType_PERIOD_TYPE_ROLLING_YEAR)
	for _, action := range []*commonv1.BlockAction{
		{Type: "future_action", Period: commonv1.PeriodType(99).Enum(), PeriodKey: proto.String("opaque")},
		{Type: "open_operations"},
	} {
		fixture.Blocks[0].Action = action
		out := c.analyticsBlock(fixture.Blocks[0])
		if out.Action.Period != nil {
			t.Fatal("invented optional period", out)
		}
	}
}
