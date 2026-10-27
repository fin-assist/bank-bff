package testpeer

import (
	"context"
	"sync"

	analyticsv1 "github.com/fin-assist/contracts/gen/go/finassist/analytics/v1"
)

// Analytics fakes the analytics read service over a real gRPC transport.
type Analytics struct {
	analyticsv1.UnimplementedAnalyticsServiceServer
	mu       sync.Mutex
	GetFunc  func(context.Context, *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error)
	ListFunc func(context.Context, *analyticsv1.ListPeriodsRequest) (*analyticsv1.ListPeriodsResponse, error)
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
