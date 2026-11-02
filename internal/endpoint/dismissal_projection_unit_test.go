package endpoint

import (
	"net/http/httptest"
	"strings"
	"testing"

	analyticsv1 "github.com/fin-assist/contracts/gen/go/finassist/analytics/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	statementdatav1 "github.com/fin-assist/contracts/gen/go/finassist/statementdata/v1"
	"google.golang.org/protobuf/proto"

	"github.com/fin-assist/bank-bff/internal/rest"
)

func TestUnitDismissalForwardsBoundSourcePayloadUnchanged(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	var source, expected *analyticsv1.MetricProjection
	u.operations.dismissRegularPayment = func(in *statementdatav1.DismissRegularPaymentRequest) (*statementdatav1.DismissRegularPaymentResponse, error) {
		if in.GetParams().GetPeriod() != commonv1.PeriodType_PERIOD_TYPE_MONTH || in.GetParams().GetPeriodKey() != "2026-09" || in.GetParams().GetTransferMode() != commonv1.TransferMode_TRANSFER_MODE_WITHOUT {
			t.Fatal("displayed context did not reach BSD", in)
		}
		source = dismissalProjection(in.Params)
		source.ProtoReflect().SetUnknown([]byte{0xf8, 0x07, 0x01})
		source.Params.ProtoReflect().SetUnknown([]byte{0xf8, 0x07, 0x01})
		expected = proto.Clone(source).(*analyticsv1.MetricProjection)
		return &statementdatav1.DismissRegularPaymentResponse{CalcVersion: 42, Projection: source}, nil
	}
	u.f.analyticsApply = func(in *analyticsv1.ApplyRegularPaymentDismissalRequest) (*analyticsv1.ApplyRegularPaymentDismissalResponse, error) {
		if in.GetCalcVersion() != 42 || in.GetRegularPaymentId() != testKey || !proto.Equal(in.Projection, expected) {
			t.Fatal("source payload changed between BSD and analytics", in)
		}
		return dismissalResponse(in), nil
	}
	r := u.serve(t, "PUT", "/v1/analytics/regular-payments/"+testKey+"/dismissal?date=2026-09&transfer_mode=without&user_id=foreign&calc_version=999", "", map[string]string{"X-User-Id": "foreign"})
	if r.Code != 200 || r.json["required_calc_version"] != 42.0 {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestUnitDismissalRejectsUnboundSourceBeforeAnalytics(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*statementdatav1.DismissRegularPaymentResponse) *statementdatav1.DismissRegularPaymentResponse
	}{
		{"absent response", func(*statementdatav1.DismissRegularPaymentResponse) *statementdatav1.DismissRegularPaymentResponse {
			return nil
		}},
		{"zero target", func(r *statementdatav1.DismissRegularPaymentResponse) *statementdatav1.DismissRegularPaymentResponse {
			r.CalcVersion = 0
			return r
		}},
		{"absent projection", func(r *statementdatav1.DismissRegularPaymentResponse) *statementdatav1.DismissRegularPaymentResponse {
			r.Projection = nil
			return r
		}},
		{"another user", func(r *statementdatav1.DismissRegularPaymentResponse) *statementdatav1.DismissRegularPaymentResponse {
			r.Projection.UserId = testKey
			return r
		}},
		{"absent context", func(r *statementdatav1.DismissRegularPaymentResponse) *statementdatav1.DismissRegularPaymentResponse {
			r.Projection.Params = nil
			return r
		}},
		{"another month", func(r *statementdatav1.DismissRegularPaymentResponse) *statementdatav1.DismissRegularPaymentResponse {
			r.Projection.Params.PeriodKey = "2026-08"
			return r
		}},
		{"another mode", func(r *statementdatav1.DismissRegularPaymentResponse) *statementdatav1.DismissRegularPaymentResponse {
			r.Projection.Params.TransferMode = commonv1.TransferMode_TRANSFER_MODE_WITH
			return r
		}},
		{"over three MiB", func(r *statementdatav1.DismissRegularPaymentResponse) *statementdatav1.DismissRegularPaymentResponse {
			r.Projection.Metrics[0].Metric.Lock = &analyticsv1.MetricLock{Text: strings.Repeat("x", 3<<20)}
			return r
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newUnitAPI(t, rest.StreamLimits{})
			writes, applies := 0, 0
			u.operations.dismissRegularPayment = func(in *statementdatav1.DismissRegularPaymentRequest) (*statementdatav1.DismissRegularPaymentResponse, error) {
				writes++
				return tc.change(&statementdatav1.DismissRegularPaymentResponse{CalcVersion: 42, Projection: dismissalProjection(in.Params)}), nil
			}
			u.f.analyticsApply = func(in *analyticsv1.ApplyRegularPaymentDismissalRequest) (*analyticsv1.ApplyRegularPaymentDismissalResponse, error) {
				applies++
				return dismissalResponse(in), nil
			}
			r := u.serve(t, "PUT", "/v1/analytics/regular-payments/"+testKey+"/dismissal?date=2026-09&transfer_mode=without", "", nil)
			if r.Code != 500 || r.json["error"].(map[string]any)["retryable"] != false || writes != 1 || applies != 0 {
				t.Fatal(r.Code, r.Body.String(), writes, applies)
			}
		})
	}
}

func TestUnitDismissalRequiresVerifiedPrincipalBeforeWriting(t *testing.T) {
	r := httptest.NewRequest("PUT", "/v1/analytics/regular-payments/"+testKey+"/dismissal?date=2026-09&transfer_mode=with", nil)
	w := httptest.NewRecorder()
	Deps{}.dismissRegularPayment(&rest.Call{R: r, W: w})
	if w.Code != 500 {
		t.Fatal(w.Code, w.Body.String())
	}
}
