package testpeer

import (
	"time"

	analyticsv1 "github.com/fin-assist/contracts/gen/go/finassist/analytics/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// UserID is the canonical subject used by dismissal transport fixtures.
const UserID = "0190c3a0-1b2c-7d3e-8f40-123456789abd"

// DismissalProjection builds a complete empty regular metric for transport fixtures.
// Financial oracles must supply their own values rather than derive expectations from this helper.
func DismissalProjection(params *commonv1.AnalyticsParams, userID string) *analyticsv1.MetricProjection {
	zone := time.FixedZone("Asia/Vladivostok", 10*60*60)
	start := time.Date(2026, time.September, 1, 0, 0, 0, 0, zone)
	end := start.AddDate(0, 1, 0)
	return &analyticsv1.MetricProjection{
		Params: proto.Clone(params).(*commonv1.AnalyticsParams), UserId: userID,
		Currency: "RUB", TimeZone: "Asia/Vladivostok",
		Range:     &commonv1.TimeRange{Start: timestamppb.New(start), End: timestamppb.New(end)},
		Bounds:    &commonv1.DataBounds{Coverage: commonv1.Coverage_COVERAGE_COMPLETE, DataFrom: timestamppb.New(start), DataTo: timestamppb.New(end.Add(-time.Second))},
		DataRange: &commonv1.TimeRange{Start: timestamppb.New(start), End: timestamppb.New(end)},
		Metrics: []*analyticsv1.MetricSnapshot{{MetricId: "insights.regular_payments", DefinitionVersion: 1,
			Metric: &analyticsv1.Metric{Status: analyticsv1.MetricStatus_METRIC_STATUS_NONE}}},
	}
}
