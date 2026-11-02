package endpoint

import (
	"strings"
	"testing"
	"time"

	analyticsv1 "github.com/fin-assist/contracts/gen/go/finassist/analytics/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	statementdatav1 "github.com/fin-assist/contracts/gen/go/finassist/statementdata/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/fin-assist/bank-bff/internal/rest"
	"github.com/fin-assist/bank-bff/internal/testpeer"
)

func dismissalProjection(params *commonv1.AnalyticsParams) *analyticsv1.MetricProjection {
	return testpeer.DismissalProjection(params, testpeer.UserID)
}

func dismissalResponse(in *analyticsv1.ApplyRegularPaymentDismissalRequest) *analyticsv1.ApplyRegularPaymentDismissalResponse {
	return &analyticsv1.ApplyRegularPaymentDismissalResponse{Params: in.Params, TimeZone: "Asia/Vladivostok", Currency: "RUB", RequiredCalcVersion: in.CalcVersion,
		Blocks: []*commonv1.Block{{Id: "regular_payments", Type: "insight_card", V: 1, Status: "none", EmptyText: proto.String("Регулярных платежей не нашли"), AltText: proto.String("Регулярных платежей не нашли"), HelpText: proto.String("Методика"), Data: &structpb.Struct{Fields: map[string]*structpb.Value{"icon": structpb.NewStringValue("repeat"), "title": structpb.NewStringValue("Регулярные платежи")}}}}}
}

func TestUnitDismissalUpdatedBlockAndValidation(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	f := u.operations
	calls := 0
	f.dismissRegularPayment = func(in *statementdatav1.DismissRegularPaymentRequest) (*statementdatav1.DismissRegularPaymentResponse, error) {
		calls++
		if in.RegularPaymentId != testKey {
			t.Fatal(in)
		}
		return &statementdatav1.DismissRegularPaymentResponse{CalcVersion: 42, Projection: dismissalProjection(in.Params)}, nil
	}
	u.f.analyticsApply = func(in *analyticsv1.ApplyRegularPaymentDismissalRequest) (*analyticsv1.ApplyRegularPaymentDismissalResponse, error) {
		if in.CalcVersion != 42 || in.RegularPaymentId != testKey || in.Params.GetPeriodKey() != "2026-09" || in.Params.GetTransferMode() != commonv1.TransferMode_TRANSFER_MODE_WITHOUT {
			t.Fatal(in)
		}
		return dismissalResponse(in), nil
	}
	path := "/v1/analytics/regular-payments/" + testKey + "/dismissal"
	for _, q := range []string{"", "?date=2026-09", "?transfer_mode=with", "?date=0000-01&transfer_mode=with", "?date=2026-09&date=2026-10&transfer_mode=with", "?date=2026-09&transfer_mode=with&transfer_mode=without", "?date=2026&transfer_mode=with", "?date=2026-09&transfer_mode=bad"} {
		r := u.serve(t, "PUT", path+q, "", nil)
		if r.Code != 400 || calls != 0 {
			t.Fatal(q, r.Code, r.Body.String(), calls)
		}
	}
	r := u.serve(t, "PUT", path+"?date=2026-09&transfer_mode=without&user_id=foreign", "", nil)
	if r.Code != 200 || calls != 1 || r.json["required_calc_version"] != 42.0 || r.json["time_zone"] != "Asia/Vladivostok" || r.json["blocks"].([]any)[0].(map[string]any)["help_text"] != "Методика" {
		t.Fatal(r.Code, r.Body.String(), calls)
	}
	f.dismissRegularPayment = func(*statementdatav1.DismissRegularPaymentRequest) (*statementdatav1.DismissRegularPaymentResponse, error) {
		return nil, finerr.WithRequestRetryInfo(finerr.AnalyticsContextUnavailable(), false)
	}
	r = u.serve(t, "PUT", path+"?date=2026-09&transfer_mode=without", "", nil)
	e := r.json["error"].(map[string]any)
	if r.Code != 422 || e["retryable"] != false || !strings.Contains(e["user_message"].(string), "Изменение сохранено") {
		t.Fatal(r.Code, r.Body.String())
	}
	f.dismissRegularPayment = func(in *statementdatav1.DismissRegularPaymentRequest) (*statementdatav1.DismissRegularPaymentResponse, error) {
		return &statementdatav1.DismissRegularPaymentResponse{CalcVersion: 42, Projection: dismissalProjection(in.Params)}, nil
	}
	for _, mutate := range []func(*analyticsv1.ApplyRegularPaymentDismissalResponse){
		func(r *analyticsv1.ApplyRegularPaymentDismissalResponse) { r.RequiredCalcVersion = 0 },
		func(r *analyticsv1.ApplyRegularPaymentDismissalResponse) { r.Params = nil },
		func(r *analyticsv1.ApplyRegularPaymentDismissalResponse) {
			r.Params = proto.Clone(r.Params).(*commonv1.AnalyticsParams)
			r.Params.PeriodKey = "2026-08"
		},
		func(r *analyticsv1.ApplyRegularPaymentDismissalResponse) { r.TimeZone = "bad/zone" },
		func(r *analyticsv1.ApplyRegularPaymentDismissalResponse) { r.TimeZone = "Europe/Moscow" },
		func(r *analyticsv1.ApplyRegularPaymentDismissalResponse) { r.Currency = "" },
		func(r *analyticsv1.ApplyRegularPaymentDismissalResponse) { r.Currency = "USD" },
		func(r *analyticsv1.ApplyRegularPaymentDismissalResponse) { r.Currency = "rub" },
		func(r *analyticsv1.ApplyRegularPaymentDismissalResponse) { r.Currency = "RUBX" },
		func(r *analyticsv1.ApplyRegularPaymentDismissalResponse) { r.Currency = "РУБ" },
		func(r *analyticsv1.ApplyRegularPaymentDismissalResponse) { r.Blocks = nil },
		func(r *analyticsv1.ApplyRegularPaymentDismissalResponse) { r.Blocks = append(r.Blocks, r.Blocks[0]) },
		func(r *analyticsv1.ApplyRegularPaymentDismissalResponse) { r.Blocks[0].Id = "other" },
		func(r *analyticsv1.ApplyRegularPaymentDismissalResponse) { r.Blocks[0].Data = nil },
	} {
		u.f.analyticsApply = func(in *analyticsv1.ApplyRegularPaymentDismissalRequest) (*analyticsv1.ApplyRegularPaymentDismissalResponse, error) {
			r := dismissalResponse(in)
			mutate(r)
			return r, nil
		}
		r = u.serve(t, "PUT", path+"?date=2026-09&transfer_mode=without", "", nil)
		if r.Code != 500 || r.json["error"].(map[string]any)["retryable"] != false {
			t.Fatal(r.Code, r.Body.String())
		}
	}
}

func TestUnitChartPagePassThroughAndErrors(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	calls := 0
	u.f.analyticsPage = func(in *analyticsv1.GetChartPageRequest) (*analyticsv1.GetChartPageResponse, error) {
		calls++
		if in.Cursor != "opaque" {
			t.Fatal(in)
		}
		return &analyticsv1.GetChartPageResponse{TimeZone: "Europe/Moscow", Block: &commonv1.Block{Id: "monthly_chart", Type: "chart", V: 1, Status: "ready", Data: &structpb.Struct{}, HelpText: proto.String("Подсказка")}}, nil
	}
	path := "/v1/analytics/charts/monthly_chart"
	for _, q := range []string{"", "?cursor=", "?cursor=a&cursor=b", "?cursor=" + strings.Repeat("x", 2049)} {
		r := u.serve(t, "GET", path+q, "", nil)
		if r.Code != 400 || calls != 0 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	r := u.serve(t, "GET", path+"?cursor=opaque", "", nil)
	if r.Code != 200 || r.json["time_zone"] != "Europe/Moscow" {
		t.Fatal(r.Code, r.Body.String())
	}
	u.f.analyticsPage = func(*analyticsv1.GetChartPageRequest) (*analyticsv1.GetChartPageResponse, error) {
		return nil, finerr.WithRequestRetryInfo(finerr.InternalError(), true)
	}
	if r = u.serve(t, "GET", path+"?cursor=opaque", "", nil); r.Code != 500 || r.json["error"].(map[string]any)["retryable"] != true {
		t.Fatal(r.Code, r.Body.String())
	}
	u.f.analyticsPage = func(*analyticsv1.GetChartPageRequest) (*analyticsv1.GetChartPageResponse, error) {
		return &analyticsv1.GetChartPageResponse{}, nil
	}
	if r = u.serve(t, "GET", path+"?cursor=opaque", "", nil); r.Code != 500 {
		t.Fatal(r.Code, r.Body.String())
	}
	for _, block := range []*commonv1.Block{
		{Id: "monthly_chart", Type: "chart", V: 1, Status: "ready"},
		{Id: "monthly_chart", Type: "chart", V: 1, Data: &structpb.Struct{}},
		{Id: "monthly_chart", Type: "chart", V: 1, Status: "none", Data: &structpb.Struct{}},
	} {
		u.f.analyticsPage = func(*analyticsv1.GetChartPageRequest) (*analyticsv1.GetChartPageResponse, error) {
			return &analyticsv1.GetChartPageResponse{TimeZone: "Europe/Moscow", Block: block}, nil
		}
		if r = u.serve(t, "GET", path+"?cursor=opaque", "", nil); r.Code != 500 || r.json["error"].(map[string]any)["code"] != "INTERNAL_ERROR" {
			t.Fatal(r.Code, r.Body.String())
		}
	}
}

func TestUnitImportDateQueryAndHistoricalMoment(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	u.statements.config = &statementdatav1.GetImportConfigResponse{TimeZone: "Europe/Moscow"}
	path := "/v1/statements/config"
	for _, q := range []string{"?period_from=2026-02-30&period_to=2026-03-01", "?period_from=&period_to=2026-03-01", "?period_from=0000-01-01&period_to=2026-03-01", "?period_to=2026-03-01&period_to=2026-03-02"} {
		r := u.serve(t, "GET", path+q, "", nil)
		if r.Code != 400 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	for _, q := range []string{"", "?period_from=1900-01-01&period_to=1900-01-01"} {
		if r := u.serve(t, "GET", path+q, "", nil); r.Code != 200 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	ts := timestamppb.New(time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC))
	got := moment(ts, loc)
	parsed, err := time.Parse(time.RFC3339Nano, got)
	if err != nil || !parsed.Equal(ts.AsTime()) || !strings.HasSuffix(got, "Z") {
		t.Fatal(got, err)
	}
	if !validSourceZone("", false) || validSourceZone("", true) || validSourceZone("bad/zone", true) || validSourceZone("Local", true) {
		t.Fatal("source zone policy")
	}
}
