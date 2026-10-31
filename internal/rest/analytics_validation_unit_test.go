package rest

import (
	"net/http"
	"strings"
	"testing"

	"github.com/fin-assist/contracts/gen/go/finerr"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
)

func TestUnitAnalyticsValidationRequiresCanonicalViolationReason(t *testing.T) {
	metrics, _ := newTestMetrics(t)
	for _, reason := range []string{"", "INVALID_FORMAT"} {
		got := metrics.FromGRPC(finerr.ValidationError(&errdetails.BadRequest_FieldViolation{Field: "date", Reason: reason, Description: "private analytics diagnostic"}), allStatuses)
		if reason == "" {
			if got.Status != http.StatusInternalServerError || got.Code != "INTERNAL_ERROR" || got.Details != nil {
				t.Fatalf("missing producer reason was accepted: %+v", got)
			}
		} else if got.Status != http.StatusBadRequest || got.Code != "VALIDATION_ERROR" || len(got.Details) != 1 || got.Details[0].Field != "date" || got.Details[0].Code != "INVALID_FORMAT" || strings.Contains(got.Details[0].Message, "private analytics diagnostic") {
			t.Fatalf("canonical analytics detail was lost or leaked: %+v", got)
		}
	}
}
