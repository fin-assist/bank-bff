package app

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	analyticsv1 "github.com/fin-assist/contracts/gen/go/finassist/analytics/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	statementdatav1 "github.com/fin-assist/contracts/gen/go/finassist/statementdata/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/fin-assist/bank-bff/internal/config"
	"github.com/fin-assist/bank-bff/internal/testpeer"
)

func dismissalAPIProjection(params *commonv1.AnalyticsParams) *analyticsv1.MetricProjection {
	return testpeer.DismissalProjection(params, testpeer.UserID)
}

func dismissalAPIResponse(in *analyticsv1.ApplyRegularPaymentDismissalRequest) *analyticsv1.ApplyRegularPaymentDismissalResponse {
	return &analyticsv1.ApplyRegularPaymentDismissalResponse{Params: in.Params, TimeZone: "Asia/Vladivostok", Currency: "RUB", RequiredCalcVersion: in.CalcVersion,
		Blocks: []*commonv1.Block{{Id: "regular_payments", Type: "insight_card", V: 1, Status: "none", EmptyText: proto.String("Регулярных платежей не нашли"), AltText: proto.String("Регулярных платежей не нашли"), HelpText: proto.String("Методика"), Data: &structpb.Struct{Fields: map[string]*structpb.Value{"icon": structpb.NewStringValue("repeat"), "title": structpb.NewStringValue("Регулярные платежи")}}}}}
}

func TestAPIUpdatedDismissalComposesAfterCommitAndRetries(t *testing.T) {
	b := startBFF(t, nil)
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access(testpeer.UserID), "X-User-Id": "foreign"})
	path := "/v1/analytics/regular-payments/" + idemKey + "/dismissal"
	var writes, applies atomic.Int32
	b.peers.RegularPayments.Set(func(p *testpeer.RegularPayments) {
		p.DismissRegularPaymentFunc = func(ctx context.Context, r *statementdatav1.DismissRegularPaymentRequest) (*statementdatav1.DismissRegularPaymentResponse, error) {
			profileDeadline(t, ctx)
			writes.Add(1)
			if r.RegularPaymentId != idemKey || r.GetParams().GetPeriodKey() != "2026-09" || r.GetParams().GetTransferMode() != commonv1.TransferMode_TRANSFER_MODE_WITHOUT {
				t.Error(r)
			}
			return &statementdatav1.DismissRegularPaymentResponse{CalcVersion: 42, Projection: dismissalAPIProjection(r.Params)}, nil
		}
	})
	b.peers.Analytics.Set(func(a *testpeer.Analytics) {
		a.ApplyFunc = func(ctx context.Context, r *analyticsv1.ApplyRegularPaymentDismissalRequest) (*analyticsv1.ApplyRegularPaymentDismissalResponse, error) {
			profileDeadline(t, ctx)
			applies.Add(1)
			if writes.Load() == 0 || r.CalcVersion != 42 || r.Params.GetPeriodKey() != "2026-09" || r.Params.GetTransferMode() != commonv1.TransferMode_TRANSFER_MODE_WITHOUT || !proto.Equal(r.Projection, dismissalAPIProjection(r.Params)) {
				t.Error(r)
			}
			if applies.Load() == 1 {
				return nil, finerr.WithRequestRetryInfo(finerr.InternalError(), true)
			}
			return dismissalAPIResponse(r), nil
		}
	})
	for _, q := range []string{"", "?date=0000-01&transfer_mode=with", "?date=2026-09&date=2026-10&transfer_mode=with", "?date=2026-09&date=bad;hidden&transfer_mode=with"} {
		r := b.do(t, request{method: "PUT", path: path + q, route: "/v1/analytics/regular-payments/{id}/dismissal", header: auth})
		if r.StatusCode != 400 || writes.Load() != 0 || applies.Load() != 0 {
			t.Fatal(r.StatusCode, string(r.body))
		}
	}
	req := request{method: "PUT", path: path + "?date=2026-09&transfer_mode=without", route: "/v1/analytics/regular-payments/{id}/dismissal", header: auth}
	r := b.do(t, req)
	if r.StatusCode != 500 || r.json["error"].(map[string]any)["retryable"] != true || writes.Load() != 1 || applies.Load() != 1 {
		t.Fatal(r.StatusCode, string(r.body))
	}
	r = b.do(t, req)
	if r.StatusCode != 200 || r.json["required_calc_version"] != 42.0 || r.json["time_zone"] != "Asia/Vladivostok" || r.json["blocks"].([]any)[0].(map[string]any)["status"] != "none" || writes.Load() != 2 || applies.Load() != 2 {
		t.Fatal(r.StatusCode, string(r.body))
	}
	b.peers.RegularPayments.Set(func(p *testpeer.RegularPayments) {
		p.DismissRegularPaymentFunc = func(context.Context, *statementdatav1.DismissRegularPaymentRequest) (*statementdatav1.DismissRegularPaymentResponse, error) {
			return nil, finerr.WithRequestRetryInfo(finerr.AnalyticsContextUnavailable(), false)
		}
	})
	r = b.do(t, req)
	if r.StatusCode != 422 || r.json["error"].(map[string]any)["retryable"] != false || !strings.Contains(r.json["error"].(map[string]any)["user_message"].(string), "Изменение сохранено") {
		t.Fatal(r.StatusCode, string(r.body))
	}
	for _, method := range []string{statementdatav1.RegularPaymentsService_DismissRegularPayment_FullMethodName, analyticsv1.AnalyticsService_ApplyRegularPaymentDismissal_FullMethodName} {
		lastCall(t, b, method, strings.TrimPrefix(auth["Authorization"], "Bearer "), "")
	}
}

func TestAPIChartPageAndImportDatesUseVerifiedUserAndSourceZone(t *testing.T) {
	b := startBFF(t, nil)
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access(testpeer.UserID)})
	data, err := structpb.NewStruct(map[string]any{"kind": "bar", "value_kind": "amount", "points": []any{map[string]any{"range": map[string]any{"from": "2026-09-01T00:00:00+03:00", "to": "2026-10-01T00:00:00+03:00"}, "value": 0, "coverage": "complete", "action": map[string]any{"type": "select_analytics_period", "period": "month", "date": "2026-09"}}}, "page": map[string]any{"revision": "r1", "previous": "opaque-next"}})
	if err != nil {
		t.Fatal(err)
	}
	b.peers.Analytics.Set(func(a *testpeer.Analytics) {
		a.PageFunc = func(ctx context.Context, r *analyticsv1.GetChartPageRequest) (*analyticsv1.GetChartPageResponse, error) {
			profileDeadline(t, ctx)
			if r.Cursor != "opaque" {
				t.Error(r)
			}
			return &analyticsv1.GetChartPageResponse{TimeZone: "Europe/Moscow", Block: &commonv1.Block{Id: "monthly_chart", Type: "chart", V: 1, Status: "ready", Data: data}}, nil
		}
	})
	req := request{method: "GET", path: "/v1/analytics/charts/monthly_chart?cursor=opaque", route: "/v1/analytics/charts/monthly_chart", header: auth}
	r := b.do(t, req)
	if r.StatusCode != 200 || r.json["time_zone"] != "Europe/Moscow" {
		t.Fatal(r.StatusCode, string(r.body))
	}
	lastCall(t, b, analyticsv1.AnalyticsService_GetChartPage_FullMethodName, strings.TrimPrefix(auth["Authorization"], "Bearer "), "")
	req.header = base(nil)
	if r = b.do(t, req); r.StatusCode != 401 {
		t.Fatal(r.StatusCode, string(r.body))
	}
	b.peers.Statements.Set(func(s *testpeer.Statements) {
		s.ConfigFunc = func(ctx context.Context, in *statementdatav1.GetImportConfigRequest) (*statementdatav1.GetImportConfigResponse, error) {
			profileDeadline(t, ctx)
			if in.GetPeriodFrom() != "1900-01-01" || in.GetPeriodTo() != "1900-01-02" {
				t.Error(in)
			}
			return importConfigFixture(), nil
		}
	})
	req = request{method: "GET", path: "/v1/statements/config?period_from=1900-01-01&period_to=1900-01-02", route: "/v1/statements/config", header: auth}
	r = b.do(t, req)
	if r.StatusCode != 200 || r.json["time_zone"] != "Asia/Novosibirsk" {
		t.Fatal(r.StatusCode, string(r.body))
	}
	before := len(b.peers.Calls(statementdatav1.StatementsService_GetImportConfig_FullMethodName))
	for _, field := range []string{"period_from", "period_to"} {
		req.path = "/v1/statements/config?" + field + "=2026-01-010"
		r = b.do(t, req)
		if r.StatusCode != 400 || len(b.peers.Calls(statementdatav1.StatementsService_GetImportConfig_FullMethodName)) != before {
			t.Fatal(r.StatusCode, string(r.body))
		}
		details := r.json["error"].(map[string]any)["details"].([]any)
		if len(details) != 1 || details[0].(map[string]any)["field"] != field || details[0].(map[string]any)["code"] != "INVALID_FORMAT" {
			t.Fatal(details)
		}
	}
}

func TestAPIChartPageErrorsAndValidation(t *testing.T) {
	b := startBFF(t, nil)
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access(testpeer.UserID)})
	path := "/v1/analytics/charts/monthly_chart"
	requestPage := request{method: "GET", path: path + "?cursor=opaque", route: path, header: auth}
	failures := registry(finerr.InvalidToken(), finerr.TokenRevoked(), finerr.RateLimited(retryAt()), finerr.ValidationError(&errdetails.BadRequest_FieldViolation{Field: "cursor", Reason: "OUT_OF_RANGE", Description: "SECRET_CURSOR"}), finerr.InternalError())
	for _, failure := range failures {
		b.peers.Analytics.Set(func(a *testpeer.Analytics) {
			a.PageFunc = func(context.Context, *analyticsv1.GetChartPageRequest) (*analyticsv1.GetChartPageResponse, error) {
				return nil, failure.err
			}
		})
		r := b.do(t, requestPage)
		if r.StatusCode != failure.status || errorCode(r) != failure.code || strings.Contains(string(r.body), "SECRET") {
			t.Fatal(r.StatusCode, string(r.body), failure)
		}
		if failure.code == "RATE_LIMITED" && r.Header.Get("Retry-After") == "" {
			t.Fatal("missing Retry-After")
		}
	}
	before := len(b.peers.Calls(analyticsv1.AnalyticsService_GetChartPage_FullMethodName))
	for _, tc := range []struct{ query, field, code string }{
		{"", "cursor", "REQUIRED"},
		{"?cursor=", "cursor", "REQUIRED"},
		{"?cursor=a&cursor=b", "cursor", "INVALID_FORMAT"},
		{"?cursor=" + strings.Repeat("x", 2049), "cursor", "INVALID_FORMAT"},
		{"?cursor=%FF", "cursor", "INVALID_FORMAT"},
		{"?cursor=opaque&transfer_mode=without", "transfer_mode", "INVALID_FORMAT"},
		{"?cursor=opaque&page_size=60", "page_size", "INVALID_FORMAT"},
		{"?cursor=opaque&X-Read-Primary=1", "X-Read-Primary", "INVALID_FORMAT"},
		{"?cursor=opaque&cursor=bad;hidden", "", "INVALID_FORMAT"},
	} {
		r := requestPage
		r.path = path + tc.query
		got := b.do(t, r)
		if got.StatusCode != 400 || len(b.peers.Calls(analyticsv1.AnalyticsService_GetChartPage_FullMethodName)) != before {
			t.Fatal(got.StatusCode, string(got.body))
		}
		details := got.json["error"].(map[string]any)["details"].([]any)
		if len(details) != 1 {
			t.Fatal(details)
		}
		detail := details[0].(map[string]any)
		field, _ := detail["field"].(string)
		if field != tc.field || detail["code"] != tc.code {
			t.Fatal(details)
		}
	}
	for _, headers := range []map[string]string{base(nil), base(map[string]string{"Authorization": "Bearer forged"}), base(map[string]string{"Authorization": auth["Authorization"], "X-Client-Version": "android/0.0.1"})} {
		r := requestPage
		r.header = headers
		got := b.do(t, r)
		if (got.StatusCode != 400 && got.StatusCode != 401) || len(b.peers.Calls(analyticsv1.AnalyticsService_GetChartPage_FullMethodName)) != before {
			t.Fatal(got.StatusCode, string(got.body))
		}
	}
	maintenance := startBFF(t, func(c *config.Config, _ *testpeer.Peers) { c.API.Maintenance.Enabled = true })
	requestPage.header = base(map[string]string{"Authorization": "Bearer " + maintenance.keys.Access(testpeer.UserID)})
	if r := maintenance.do(t, requestPage); r.StatusCode != 503 || len(maintenance.peers.Calls(analyticsv1.AnalyticsService_GetChartPage_FullMethodName)) != 0 {
		t.Fatal(r.StatusCode, string(r.body))
	}
}

func TestAPIInvalidAnalyticsEnvelopesAreNotSuccessful(t *testing.T) {
	b := startBFF(t, nil)
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access(testpeer.UserID)})
	for _, block := range []*commonv1.Block{
		{Id: "monthly_chart", Type: "chart", V: 1, Status: "ready"},
		{Id: "monthly_chart", Type: "chart", V: 1, Data: &structpb.Struct{}},
		{Id: "monthly_chart", Type: "chart", V: 1, Status: "none", Data: &structpb.Struct{}},
	} {
		b.peers.Analytics.Set(func(a *testpeer.Analytics) {
			a.PageFunc = func(context.Context, *analyticsv1.GetChartPageRequest) (*analyticsv1.GetChartPageResponse, error) {
				return &analyticsv1.GetChartPageResponse{TimeZone: "Europe/Moscow", Block: block}, nil
			}
		})
		r := b.do(t, request{method: "GET", path: "/v1/analytics/charts/monthly_chart?cursor=opaque", route: "/v1/analytics/charts/monthly_chart", header: auth})
		if r.StatusCode != 500 || errorCode(r) != "INTERNAL_ERROR" {
			t.Fatal(r.StatusCode, string(r.body))
		}
	}
	for _, currency := range []string{"rub", "RUBX", "РУБ", "USD"} {
		b.peers.RegularPayments.Set(func(p *testpeer.RegularPayments) {
			p.DismissRegularPaymentFunc = func(_ context.Context, r *statementdatav1.DismissRegularPaymentRequest) (*statementdatav1.DismissRegularPaymentResponse, error) {
				return &statementdatav1.DismissRegularPaymentResponse{CalcVersion: 42, Projection: dismissalAPIProjection(r.Params)}, nil
			}
		})
		b.peers.Analytics.Set(func(a *testpeer.Analytics) {
			a.ApplyFunc = func(_ context.Context, r *analyticsv1.ApplyRegularPaymentDismissalRequest) (*analyticsv1.ApplyRegularPaymentDismissalResponse, error) {
				out := dismissalAPIResponse(r)
				out.Currency = currency
				return out, nil
			}
		})
		beforeWrites := len(b.peers.Calls(statementdatav1.RegularPaymentsService_DismissRegularPayment_FullMethodName))
		beforeApplies := len(b.peers.Calls(analyticsv1.AnalyticsService_ApplyRegularPaymentDismissal_FullMethodName))
		r := b.do(t, request{method: "PUT", path: "/v1/analytics/regular-payments/" + idemKey + "/dismissal?date=2026-09&transfer_mode=without", route: "/v1/analytics/regular-payments/{id}/dismissal", header: auth})
		if r.StatusCode != 500 || errorCode(r) != "INTERNAL_ERROR" || r.json["error"].(map[string]any)["retryable"] != false ||
			len(b.peers.Calls(statementdatav1.RegularPaymentsService_DismissRegularPayment_FullMethodName)) != beforeWrites+1 ||
			len(b.peers.Calls(analyticsv1.AnalyticsService_ApplyRegularPaymentDismissal_FullMethodName)) != beforeApplies+1 {
			t.Fatal(r.StatusCode, string(r.body))
		}
	}
}
