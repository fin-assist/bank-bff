package app

import (
	"context"
	"strings"
	"testing"

	analyticsv1 "github.com/fin-assist/contracts/gen/go/finassist/analytics/v1"
	statementdatav1 "github.com/fin-assist/contracts/gen/go/finassist/statementdata/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	"github.com/fin-assist/bank-bff/internal/testpeer"
)

func TestAPIDismissalRejectsUnboundSourceAndPreservesContextRefusal(t *testing.T) {
	b := startBFF(t, nil)
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access(testpeer.UserID), "X-User-Id": idemKey})
	req := request{method: "PUT", path: "/v1/analytics/regular-payments/" + idemKey + "/dismissal?date=2026-09&transfer_mode=without&user_id=" + idemKey,
		route: "/v1/analytics/regular-payments/{id}/dismissal", header: auth}
	for _, tc := range []struct {
		name   string
		change func(*statementdatav1.DismissRegularPaymentResponse)
	}{
		{"foreign user matching spoofed header", func(r *statementdatav1.DismissRegularPaymentResponse) { r.Projection.UserId = idemKey }},
		{"another month", func(r *statementdatav1.DismissRegularPaymentResponse) { r.Projection.Params.PeriodKey = "2026-08" }},
		{"missing projection", func(r *statementdatav1.DismissRegularPaymentResponse) { r.Projection = nil }},
		{"zero target", func(r *statementdatav1.DismissRegularPaymentResponse) { r.CalcVersion = 0 }},
		{"oversize", func(r *statementdatav1.DismissRegularPaymentResponse) {
			r.Projection.Metrics[0].Metric.Lock = &analyticsv1.MetricLock{Text: strings.Repeat("x", 3<<20)}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b.peers.RegularPayments.Set(func(p *testpeer.RegularPayments) {
				p.DismissRegularPaymentFunc = func(_ context.Context, in *statementdatav1.DismissRegularPaymentRequest) (*statementdatav1.DismissRegularPaymentResponse, error) {
					r := &statementdatav1.DismissRegularPaymentResponse{CalcVersion: 42, Projection: dismissalAPIProjection(in.Params)}
					tc.change(r)
					return r, nil
				}
			})
			before := len(b.peers.Calls(analyticsv1.AnalyticsService_ApplyRegularPaymentDismissal_FullMethodName))
			r := b.do(t, req)
			if r.StatusCode != 500 || r.json["error"].(map[string]any)["retryable"] != false ||
				len(b.peers.Calls(analyticsv1.AnalyticsService_ApplyRegularPaymentDismissal_FullMethodName)) != before {
				t.Fatal(r.StatusCode, string(r.body))
			}
		})
	}
	for _, saved := range []bool{false, true} {
		b.peers.RegularPayments.Set(func(p *testpeer.RegularPayments) {
			p.DismissRegularPaymentFunc = func(context.Context, *statementdatav1.DismissRegularPaymentRequest) (*statementdatav1.DismissRegularPaymentResponse, error) {
				if saved {
					return nil, finerr.WithRequestRetryInfo(finerr.AnalyticsContextUnavailable(), false)
				}
				return nil, finerr.WithRequestRetryInfo(finerr.ValidationError(&errdetails.BadRequest_FieldViolation{Field: "date", Reason: "OUT_OF_RANGE", Description: "private source diagnostic"}), false)
			}
		})
		before := len(b.peers.Calls(analyticsv1.AnalyticsService_ApplyRegularPaymentDismissal_FullMethodName))
		r := b.do(t, req)
		want := 400
		if saved {
			want = 422
		}
		if r.StatusCode != want || r.json["error"].(map[string]any)["retryable"] != false || strings.Contains(string(r.body), "private") ||
			len(b.peers.Calls(analyticsv1.AnalyticsService_ApplyRegularPaymentDismissal_FullMethodName)) != before {
			t.Fatal(r.StatusCode, string(r.body))
		}
		if !saved {
			details := r.json["error"].(map[string]any)["details"].([]any)
			if len(details) != 1 || details[0].(map[string]any)["field"] != "date" || details[0].(map[string]any)["code"] != "OUT_OF_RANGE" {
				t.Fatal(details)
			}
		}
	}
}
