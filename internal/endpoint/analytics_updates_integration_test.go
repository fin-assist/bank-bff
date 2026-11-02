package endpoint

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	analyticsv1 "github.com/fin-assist/contracts/gen/go/finassist/analytics/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	statementdatav1 "github.com/fin-assist/contracts/gen/go/finassist/statementdata/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
	identity "github.com/fin-assist/go-identity"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/fin-assist/bank-bff/internal/apispec"
	"github.com/fin-assist/bank-bff/internal/rest"
	"github.com/fin-assist/bank-bff/internal/testpeer"
)

// Exercise the HTTP-to-protobuf chain without constructing app's public assembly.
func TestIntegrationUpdatedAnalyticsEndpoints(t *testing.T) {
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
	spec, err := apispec.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{Analytics: analyticsv1.NewAnalyticsServiceClient(conn), RegularPayments: statementdatav1.NewRegularPaymentsServiceClient(conn), Statements: statementdatav1.NewStatementsServiceClient(conn)}
	var writes, pages, configs atomic.Int32
	peers.RegularPayments.Set(func(p *testpeer.RegularPayments) {
		p.DismissRegularPaymentFunc = func(_ context.Context, r *statementdatav1.DismissRegularPaymentRequest) (*statementdatav1.DismissRegularPaymentResponse, error) {
			if r.RegularPaymentId != testKey || r.GetParams().GetPeriodKey() != "2026-09" {
				t.Error(r)
			}
			writes.Add(1)
			projection := dismissalProjection(r.Params)
			projection.ProtoReflect().SetUnknown([]byte{0xf8, 0x07, 0x01})
			projection.Params.ProtoReflect().SetUnknown([]byte{0xf8, 0x07, 0x01})
			return &statementdatav1.DismissRegularPaymentResponse{CalcVersion: 42, Projection: projection}, nil
		}
	})
	peers.Analytics.Set(func(a *testpeer.Analytics) {
		a.ApplyFunc = func(_ context.Context, r *analyticsv1.ApplyRegularPaymentDismissalRequest) (*analyticsv1.ApplyRegularPaymentDismissalResponse, error) {
			expected := dismissalProjection(r.Params)
			expected.ProtoReflect().SetUnknown([]byte{0xf8, 0x07, 0x01})
			expected.Params.ProtoReflect().SetUnknown([]byte{0xf8, 0x07, 0x01})
			if r.GetCalcVersion() != 42 || r.GetParams().GetPeriodKey() != "2026-09" || writes.Load() == 0 || !proto.Equal(r.Projection, expected) {
				t.Error(r)
			}
			return dismissalResponse(r), nil
		}
		a.PageFunc = func(_ context.Context, r *analyticsv1.GetChartPageRequest) (*analyticsv1.GetChartPageResponse, error) {
			pages.Add(1)
			if r.Cursor != "opaque" {
				t.Error(r)
			}
			data, _ := structpb.NewStruct(map[string]any{"page": map[string]any{"revision": "v1"}, "future": true})
			return &analyticsv1.GetChartPageResponse{TimeZone: "Asia/Vladivostok", Block: &commonv1.Block{Id: "monthly_chart", Type: "chart", V: 1, Status: "ready", Data: data}}, nil
		}
	})
	peers.Statements.Set(func(s *testpeer.Statements) {
		s.ConfigFunc = func(_ context.Context, r *statementdatav1.GetImportConfigRequest) (*statementdatav1.GetImportConfigResponse, error) {
			configs.Add(1)
			if r.GetPeriodFrom() != "1900-01-01" || r.GetPeriodTo() != "1900-01-02" {
				t.Error(r)
			}
			return &statementdatav1.GetImportConfigResponse{TimeZone: "Europe/Moscow"}, nil
		}
	})
	auth, err := identity.NewHTTP(unitUserChecker{}, func(w http.ResponseWriter, _ *http.Request, _ error) { rest.InvalidToken().Write(w) })
	if err != nil {
		t.Fatal(err)
	}
	handler, err := rest.Handler(rest.Config{Spec: spec, BodyLimit: rest.MinBodyLimit, Metrics: metrics,
		Log: slog.New(slog.DiscardHandler), Clock: time.Now, Authenticate: auth.Middleware,
		Gates: rest.Gates{MinVersions: map[string]rest.Version{"android": {"0", "2", "5"}}, Clock: time.Now}}, []rest.Route{
		{Method: "PUT", Path: "/v1/analytics/regular-payments/{id}/dismissal", Protected: rest.Always, ReplaySafe: true, Handle: deps.dismissRegularPayment},
		{Method: "GET", Path: "/v1/analytics/charts/monthly_chart", Protected: rest.Always, ReplaySafe: true, Handle: deps.analyticsChartPage},
		{Method: "GET", Path: "/v1/statements/config", Protected: rest.Always, ReplaySafe: true, Handle: deps.importConfig},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := &http.Client{Timeout: 5 * time.Second}
	call := func(method, path string) (int, map[string]any) {
		t.Helper()
		req, e := http.NewRequestWithContext(t.Context(), method, server.URL+path, nil)
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("X-Client-Version", "android/0.2.16+1")
		req.Header.Set("Authorization", goodToken)
		r, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		raw, e := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if e != nil {
			t.Fatal(e)
		}
		var body map[string]any
		if e = json.Unmarshal(raw, &body); e != nil {
			t.Fatal(e, string(raw))
		}
		if r.StatusCode == http.StatusInternalServerError && r.Header.Get("Retry-After") != "" {
			t.Fatal("INTERNAL_ERROR retained another code's wait", r.Header)
		}
		return r.StatusCode, body
	}
	path := "/v1/analytics/regular-payments/" + testKey + "/dismissal"
	code, out := call("PUT", path+"?date=2026-09&transfer_mode=without")
	if code != 200 || out["required_calc_version"] != 42.0 || out["blocks"].([]any)[0].(map[string]any)["status"] != "none" {
		t.Fatal(code, out)
	}
	code, _ = call("PUT", path+"?date=0000-01&transfer_mode=with")
	if code != 400 || writes.Load() != 1 {
		t.Fatal(code, writes.Load())
	}
	for _, failure := range []error{finerr.AnalyticsContextUnavailable(), finerr.RateLimited(time.Now().Add(time.Minute))} {
		for _, permission := range []bool{false, true} {
			peers.Analytics.Set(func(a *testpeer.Analytics) {
				a.ApplyFunc = func(context.Context, *analyticsv1.ApplyRegularPaymentDismissalRequest) (*analyticsv1.ApplyRegularPaymentDismissalResponse, error) {
					return nil, finerr.WithRequestRetryInfo(failure, permission)
				}
			})
			beforeWrites := writes.Load()
			beforeApply := len(peers.Calls(analyticsv1.AnalyticsService_ApplyRegularPaymentDismissal_FullMethodName))
			code, out = call("PUT", path+"?date=2026-09&transfer_mode=with")
			problem := out["error"].(map[string]any)
			if code != 500 || problem["code"] != "INTERNAL_ERROR" || problem["retryable"] != permission || problem["retry_at"] != nil ||
				writes.Load() != beforeWrites+1 || len(peers.Calls(analyticsv1.AnalyticsService_ApplyRegularPaymentDismissal_FullMethodName)) != beforeApply+1 {
				t.Fatal(code, out, writes.Load())
			}
		}
	}
	peers.RegularPayments.Set(func(p *testpeer.RegularPayments) {
		p.DismissRegularPaymentFunc = func(context.Context, *statementdatav1.DismissRegularPaymentRequest) (*statementdatav1.DismissRegularPaymentResponse, error) {
			return nil, finerr.WithRequestRetryInfo(finerr.AnalyticsContextUnavailable(), false)
		}
	})
	code, out = call("PUT", path+"?date=2026-09&transfer_mode=with")
	if code != 422 || !strings.Contains(out["error"].(map[string]any)["user_message"].(string), "Изменение сохранено") {
		t.Fatal(code, out)
	}
	code, out = call("GET", "/v1/analytics/charts/monthly_chart?cursor=opaque")
	if code != 200 || out["time_zone"] != "Asia/Vladivostok" || out["block"].(map[string]any)["data"].(map[string]any)["future"] != true {
		t.Fatal(code, out)
	}
	code, out = call("GET", "/v1/statements/config?period_from=1900-01-01&period_to=1900-01-02")
	if code != 200 || out["time_zone"] != "Europe/Moscow" {
		t.Fatal(code, out)
	}
	for _, tc := range []struct{ path, field string }{
		{"/v1/analytics/charts/monthly_chart?cursor=opaque&period=year", "period"},
		{"/v1/analytics/charts/monthly_chart?cursor=" + strings.Repeat("x", 2049), "cursor"},
		{"/v1/analytics/charts/monthly_chart?cursor=%FF", "cursor"},
		{"/v1/statements/config?period_from=2026-01-010&period_to=2026-02-01", "period_from"},
		{"/v1/statements/config?period_from=2026-01-01&period_to=2026-02-010", "period_to"},
	} {
		code, out = call("GET", tc.path)
		if code != 400 || pages.Load() != 1 || configs.Load() != 1 {
			t.Fatal(code, out, pages.Load(), configs.Load())
		}
		details := out["error"].(map[string]any)["details"].([]any)
		if len(details) != 1 || details[0].(map[string]any)["field"] != tc.field || details[0].(map[string]any)["code"] != "INVALID_FORMAT" {
			t.Fatal(details)
		}
	}
	for _, block := range []*commonv1.Block{
		{Id: "monthly_chart", Type: "chart", V: 1, Status: "ready"},
		{Id: "monthly_chart", Type: "chart", V: 1, Status: "none", Data: &structpb.Struct{}},
	} {
		peers.Analytics.Set(func(a *testpeer.Analytics) {
			a.PageFunc = func(context.Context, *analyticsv1.GetChartPageRequest) (*analyticsv1.GetChartPageResponse, error) {
				return &analyticsv1.GetChartPageResponse{TimeZone: "Europe/Moscow", Block: block}, nil
			}
		})
		code, out = call("GET", "/v1/analytics/charts/monthly_chart?cursor=opaque")
		if code != 500 || out["error"].(map[string]any)["code"] != "INTERNAL_ERROR" {
			t.Fatal(code, out)
		}
	}
	peers.RegularPayments.Set(func(p *testpeer.RegularPayments) {
		p.DismissRegularPaymentFunc = func(_ context.Context, r *statementdatav1.DismissRegularPaymentRequest) (*statementdatav1.DismissRegularPaymentResponse, error) {
			return &statementdatav1.DismissRegularPaymentResponse{CalcVersion: 42, Projection: dismissalProjection(r.Params)}, nil
		}
	})
	peers.Analytics.Set(func(a *testpeer.Analytics) {
		a.ApplyFunc = func(_ context.Context, r *analyticsv1.ApplyRegularPaymentDismissalRequest) (*analyticsv1.ApplyRegularPaymentDismissalResponse, error) {
			out := dismissalResponse(r)
			out.Currency = "rub"
			return out, nil
		}
	})
	code, out = call("PUT", path+"?date=2026-09&transfer_mode=with")
	if code != 500 || out["error"].(map[string]any)["code"] != "INTERNAL_ERROR" {
		t.Fatal(code, out)
	}
}
