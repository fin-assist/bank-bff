package testpeer

import (
	"context"
	"sync"

	analyticsv1 "github.com/fin-assist/contracts/gen/go/finassist/analytics/v1"
)

// Analytics fakes the analytics read service over a real gRPC transport.
type Analytics struct {
	analyticsv1.UnimplementedAnalyticsServiceServer
	mu        sync.Mutex
	GetFunc   func(context.Context, *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error)
	ApplyFunc func(context.Context, *analyticsv1.ApplyRegularPaymentDismissalRequest) (*analyticsv1.ApplyRegularPaymentDismissalResponse, error)
	PageFunc  func(context.Context, *analyticsv1.GetChartPageRequest) (*analyticsv1.GetChartPageResponse, error)
	ListFunc  func(context.Context, *analyticsv1.ListPeriodsRequest) (*analyticsv1.ListPeriodsResponse, error)
}

// GetAnalytics implements the RPC.
func (a *Analytics) GetAnalytics(ctx context.Context, r *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
	if f := get(&a.mu, &a.GetFunc); f != nil {
		return f(ctx, r)
	}
	return a.UnimplementedAnalyticsServiceServer.GetAnalytics(ctx, r)
}

// ListPeriods implements the RPC.
func (a *Analytics) ListPeriods(ctx context.Context, r *analyticsv1.ListPeriodsRequest) (*analyticsv1.ListPeriodsResponse, error) {
	if f := get(&a.mu, &a.ListFunc); f != nil {
		return f(ctx, r)
	}
	return a.UnimplementedAnalyticsServiceServer.ListPeriods(ctx, r)
}

// Set replaces handlers safely between requests.
func (a *Analytics) Set(apply func(*Analytics)) { a.mu.Lock(); defer a.mu.Unlock(); apply(a) }

// GetChartPage implements the user-only page RPC.
func (a *Analytics) GetChartPage(ctx context.Context, r *analyticsv1.GetChartPageRequest) (*analyticsv1.GetChartPageResponse, error) {
	if f := get(&a.mu, &a.PageFunc); f != nil {
		return f(ctx, r)
	}
	return a.UnimplementedAnalyticsServiceServer.GetChartPage(ctx, r)
}

// ApplyRegularPaymentDismissal implements the user-only composition RPC.
func (a *Analytics) ApplyRegularPaymentDismissal(ctx context.Context, r *analyticsv1.ApplyRegularPaymentDismissalRequest) (*analyticsv1.ApplyRegularPaymentDismissalResponse, error) {
	if f := get(&a.mu, &a.ApplyFunc); f != nil {
		return f(ctx, r)
	}
	return a.UnimplementedAnalyticsServiceServer.ApplyRegularPaymentDismissal(ctx, r)
}
