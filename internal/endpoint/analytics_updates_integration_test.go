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
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
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
			if r.RegularPaymentId != testKey {
				t.Error(r)
			}
			writes.Add(1)
			return &statementdatav1.DismissRegularPaymentResponse{CalcVersion: 42}, nil
		}
	})
	peers.Analytics.Set(func(a *testpeer.Analytics) {
		a.ApplyFunc = func(_ context.Context, r *analyticsv1.ApplyRegularPaymentDismissalRequest) (*analyticsv1.ApplyRegularPaymentDismissalResponse, error) {
			if r.GetCalcVersion() != 42 || r.GetParams().GetPeriodKey() != "2026-09" || writes.Load() == 0 {
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
	mux := http.NewServeMux()
	for _, route := range []struct {
		method, path string
		handle       func(*rest.Call)
	}{{"PUT", "/v1/analytics/regular-payments/{id}/dismissal", deps.dismissRegularPayment}, {"GET", "/v1/analytics/charts/monthly_chart", deps.analyticsChartPage}, {"GET", "/v1/statements/config", deps.importConfig}} {
		op, e := spec.Operation(route.method, route.path)
		if e != nil {
			t.Fatal(e)
		}
		mux.HandleFunc(route.method+" "+route.path, func(w http.ResponseWriter, r *http.Request) {
			if details := rest.Validate(r.Context(), r, op, map[string]string{"id": r.PathValue("id")}, nil); len(details) > 0 {
				rest.Validation(details...).Write(w)
				return
			}
			route.handle(&rest.Call{W: w, R: r, Metrics: metrics, Log: slog.New(slog.DiscardHandler), Op: op})
		})
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := &http.Client{Timeout: 5 * time.Second}
	call := func(method, path string) (int, map[string]any) {
		t.Helper()
		req, e := http.NewRequestWithContext(t.Context(), method, server.URL+path, nil)
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("X-Client-Version", "android/0.2.16+1")
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
	peers.Analytics.Set(func(a *testpeer.Analytics) {
		a.ApplyFunc = func(context.Context, *analyticsv1.ApplyRegularPaymentDismissalRequest) (*analyticsv1.ApplyRegularPaymentDismissalResponse, error) {
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
