package app

import (
	"context"
	"net/http"
	"strings"
	"testing"

	analyticsv1 "github.com/fin-assist/contracts/gen/go/finassist/analytics/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	"github.com/fin-assist/bank-bff/internal/testpeer"
)

func TestAPIAnalyticsCoarseDateValidationAndMalformedProducer(t *testing.T) {
	b := startBFF(t, nil)
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1")})
	for _, tc := range []struct {
		name, query, key string
		period           commonv1.PeriodType
	}{
		{"year to month", "?period=month&date=2026", "2026", 1},
		{"year to quarter", "?period=quarter&date=2026", "2026", 2},
		{"quarter to month", "?period=month&date=2026-Q3", "2026-Q3", 1},
	} {
		for _, malformed := range []bool{false, true} {
			name := tc.name + "/canonical"
			if malformed {
				name = tc.name + "/missing reason"
			}
			t.Run(name, func(t *testing.T) {
				violation := &errdetails.BadRequest_FieldViolation{Field: "date", Reason: "INVALID_FORMAT", Description: "private analytics validation text"}
				if malformed {
					violation.Reason = ""
				}
				producerError := finerr.ValidationError(violation)
				b.peers.Analytics.Set(func(a *testpeer.Analytics) {
					a.GetFunc = func(_ context.Context, req *analyticsv1.GetAnalyticsRequest) (*analyticsv1.GetAnalyticsResponse, error) {
						if req.GetPeriod() != tc.period || req.GetPeriodKey() != tc.key {
							t.Errorf("analytics request changed: period=%v key=%q", req.GetPeriod(), req.GetPeriodKey())
						}
						return nil, producerError
					}
				})
				response := b.do(t, request{method: "GET", path: "/v1/analytics" + tc.query, route: "/v1/analytics", header: auth})
				if strings.Contains(string(response.body), "private analytics validation text") {
					t.Fatal("producer diagnostic reached the client")
				}
				if malformed {
					if response.StatusCode != http.StatusInternalServerError || errorCode(response) != "INTERNAL_ERROR" {
						t.Fatalf("malformed producer error was accepted: status=%d body=%s", response.StatusCode, response.body)
					}
					return
				}
				if response.StatusCode != http.StatusBadRequest || errorCode(response) != "VALIDATION_ERROR" {
					t.Fatalf("canonical validation did not reach REST400: status=%d body=%s", response.StatusCode, response.body)
				}
				details := response.json["error"].(map[string]any)["details"].([]any)
				if len(details) != 1 || details[0].(map[string]any)["field"] != "date" || details[0].(map[string]any)["code"] != "INVALID_FORMAT" {
					t.Fatalf("wrong canonical detail: %v", details)
				}
			})
		}
	}
}
