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
	var writes atomic.Int32
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
			if r.Cursor != "opaque" {
				t.Error(r)
			}
			data, _ := structpb.NewStruct(map[string]any{"page": map[string]any{"revision": "v1"}, "future": true})
			return &analyticsv1.GetChartPageResponse{TimeZone: "Asia/Vladivostok", Block: &commonv1.Block{Id: "monthly_chart", Type: "chart", V: 1, Data: data}}, nil
		}
	})
	peers.Statements.Set(func(s *testpeer.Statements) {
		s.ConfigFunc = func(_ context.Context, r *statementdatav1.GetImportConfigRequest) (*statementdatav1.GetImportConfigResponse, error) {
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
}
