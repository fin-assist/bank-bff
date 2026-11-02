package endpoint

import (
	"net/http/httptest"
	"strings"
	"testing"

	analyticsv1 "github.com/fin-assist/contracts/gen/go/finassist/analytics/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	statementdatav1 "github.com/fin-assist/contracts/gen/go/finassist/statementdata/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/encoding/protowire"
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

func TestUnitDismissalComposerRefusalsRemainUnknownWrites(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		retry  bool
	}{
		{"validation without permission", finerr.ValidationError(&errdetails.BadRequest_FieldViolation{Field: "date", Reason: "OUT_OF_RANGE", Description: "SECRET"}), 500, false},
		{"validation with permission", finerr.WithRequestRetryInfo(finerr.ValidationError(&errdetails.BadRequest_FieldViolation{Field: "date", Reason: "OUT_OF_RANGE"}), true), 500, true},
		{"malformed validation", finerr.WithRequestRetryInfo(finerr.ValidationError(), true), 500, false},
		{"not found", finerr.WithRequestRetryInfo(finerr.NotFound(), false), 500, false},
		{"saved context", finerr.WithRequestRetryInfo(finerr.AnalyticsContextUnavailable(), false), 500, false},
		{"authentication", finerr.TokenRevoked(), 401, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newUnitAPI(t, rest.StreamLimits{})
			writes, applies := 0, 0
			u.operations.dismissRegularPayment = func(in *statementdatav1.DismissRegularPaymentRequest) (*statementdatav1.DismissRegularPaymentResponse, error) {
				writes++
				return &statementdatav1.DismissRegularPaymentResponse{CalcVersion: 42, Projection: dismissalProjection(in.Params)}, nil
			}
			u.f.analyticsApply = func(*analyticsv1.ApplyRegularPaymentDismissalRequest) (*analyticsv1.ApplyRegularPaymentDismissalResponse, error) {
				applies++
				return nil, tc.err
			}
			r := u.serve(t, "PUT", "/v1/analytics/regular-payments/"+testKey+"/dismissal?date=2026-09&transfer_mode=with", "", nil)
			if r.Code != tc.status || r.json["error"].(map[string]any)["retryable"] != tc.retry || writes != 1 || applies != 1 || strings.Contains(r.Body.String(), "SECRET") {
				t.Fatal(r.Code, r.Body.String(), writes, applies)
			}
		})
	}
}

func TestUnitDismissalProjectionExactWireLimit(t *testing.T) {
	for _, extra := range []int{0, 1} {
		u := newUnitAPI(t, rest.StreamLimits{})
		applies := 0
		u.operations.dismissRegularPayment = func(in *statementdatav1.DismissRegularPaymentRequest) (*statementdatav1.DismissRegularPaymentResponse, error) {
			p := dismissalProjection(in.Params)
			want := (3 << 20) + extra
			// A two-byte field tag and four-byte length precede this >2 MiB opaque value.
			raw := protowire.AppendTag(nil, 127, protowire.BytesType)
			raw = protowire.AppendBytes(raw, make([]byte, want-proto.Size(p)-6))
			p.ProtoReflect().SetUnknown(raw)
			if proto.Size(p) != want {
				t.Fatal("fixture is not at the exact wire boundary", proto.Size(p), want)
			}
			return &statementdatav1.DismissRegularPaymentResponse{CalcVersion: 42, Projection: p}, nil
		}
		u.f.analyticsApply = func(in *analyticsv1.ApplyRegularPaymentDismissalRequest) (*analyticsv1.ApplyRegularPaymentDismissalResponse, error) {
			applies++
			return dismissalResponse(in), nil
		}
		r := u.serve(t, "PUT", "/v1/analytics/regular-payments/"+testKey+"/dismissal?date=2026-09&transfer_mode=with", "", nil)
		if extra == 0 && (r.Code != 200 || applies != 1) || extra == 1 && (r.Code != 500 || applies != 0) {
			t.Fatal(extra, r.Code, applies, r.Body.String())
		}
	}
}
