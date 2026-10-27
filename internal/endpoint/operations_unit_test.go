package endpoint

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	statementdatav1 "github.com/fin-assist/contracts/gen/go/finassist/statementdata/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/fin-assist/bank-bff/internal/rest"
)

func operationFixture() *statementdatav1.Operation {
	at := timestamppb.New(time.Date(2026, 9, 25, 16, 11, 0, 123456000, time.UTC))
	return &statementdatav1.Operation{Id: testKey, Kind: statementdatav1.OperationKind_OPERATION_KIND_EXPENSE, OccurredAt: at, AmountMinor: 234000, Currency: "RUB", Title: "", Category: &statementdatav1.CategoryRef{Id: "cat_shop", Name: "Покупки", Icon: "future_icon"}, IsCategoryManual: true, Note: proto.String("возврат · Покупки"), Status: statementdatav1.OperationStatus_OPERATION_STATUS_PENDING, Account: &commonv1.AccountRef{TypeName: "Текущий", Mask: proto.String("··1234"), Spoken: proto.String("текущий счёт")}, Description: proto.String("описание"), PostedAt: at, NoteDetails: proto.String("готовое пояснение"), BankCategory: proto.String("Покупки"), Mcc: proto.String("1234"), SourceName: "Выписка Т-Банка"}
}
func feedFixture() *statementdatav1.ListFeedResponse {
	item := operationFixture()
	r := &commonv1.TimeRange{Start: item.OccurredAt, End: item.OccurredAt}
	return &statementdatav1.ListFeedResponse{Items: []*statementdatav1.Operation{item}, Summary: []*statementdatav1.MonthSummary{{Month: "2026-09", Range: r, ExpenseMinor: -123, IncomeMinor: 456, DataTo: item.OccurredAt}}, State: &statementdatav1.FeedState{Stale: true, DataTo: item.OccurredAt, LastOperationAt: item.OccurredAt, LastUploadedAt: item.OccurredAt}, Range: r, NextBefore: item.OccurredAt, TimeZone: "Europe/Moscow"}
}
func TestUnitOperationRoutesAndProjection(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	f := u.operations
	f.listFeed = func(in *statementdatav1.ListFeedRequest) (*statementdatav1.ListFeedResponse, error) {
		return feedFixture(), nil
	}
	r := u.serve(t, "GET", "/v1/operations", "", nil)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	item := r.json["items"].([]any)[0].(map[string]any)
	if item["occurred_at"] != "2026-09-25T19:11:00.123456+03:00" || item["title"] != "" || item["category_icon"] != "future_icon" || item["status"] != "pending" || item["account"].(map[string]any)["spoken"] != nil || item["description"] != nil || item["posted_at"] != nil || item["source_name"] != nil {
		t.Fatal(item)
	}
	if r.json["summary"].([]any)[0].(map[string]any)["expense"] != -123.0 || r.json["state"].(map[string]any)["stale"] != true {
		t.Fatal(r.Body.String())
	}
	f.listFeed = func(in *statementdatav1.ListFeedRequest) (*statementdatav1.ListFeedResponse, error) {
		if in.Before == nil || in.Before.AsTime().Format(time.RFC3339Nano) != "2026-09-25T16:11:00.123456Z" {
			t.Fatal(in)
		}
		return feedFixture(), nil
	}
	if r := u.serve(t, "GET", "/v1/operations?before=2026-09-25T19:11:00.123456%2B03:00", "", nil); r.Code != 200 {
		t.Fatal(r.Code)
	}
	f.getOperation = func(in *statementdatav1.GetOperationRequest) (*statementdatav1.GetOperationResponse, error) {
		if in.Id != testKey || in.OccurredAt.Nanos != 123456000 {
			t.Fatal(in)
		}
		return &statementdatav1.GetOperationResponse{Operation: operationFixture(), TimeZone: "Europe/Moscow"}, nil
	}
	r = u.serve(t, "GET", "/v1/operations/"+testKey+"?occurred_at=2026-09-25T16:11:00.123456Z", "", nil)
	if r.Code != 200 || r.json["account"].(map[string]any)["spoken"] != "текущий счёт" || r.json["source_name"] != "Выписка Т-Банка" || r.json["posted_at"] != r.json["occurred_at"] || r.json["note_details"] != "готовое пояснение" || r.json["mcc"] != "1234" {
		t.Fatal(r.Code, r.Body.String())
	}
	f.changeCategory = func(in *statementdatav1.ChangeCategoryRequest) (*statementdatav1.ChangeCategoryResponse, error) {
		if in.Id != testKey || in.CategoryId != "cat_refund" || in.OccurredAt.Nanos != 123456000 {
			t.Fatal(in)
		}
		one := operationFixture()
		two := operationFixture()
		two.Kind = statementdatav1.OperationKind_OPERATION_KIND_INCOME
		return &statementdatav1.ChangeCategoryResponse{Items: []*statementdatav1.Operation{one, two}, ReplacedId: proto.String(testKey), TimeZone: "Europe/Moscow"}, nil
	}
	r = u.serve(t, "PATCH", "/v1/operations/"+testKey, `{"category_id":"cat_refund","Category_id":"foreign","occurred_at":"2026-09-25T16:11:00.123456Z"}`, nil)
	if r.Code != 200 || len(r.json["items"].([]any)) != 2 || r.json["replaced_id"] != testKey {
		t.Fatal(r.Code, r.Body.String())
	}
	f.listCategories = func(*statementdatav1.ListCategoriesRequest) (*statementdatav1.ListCategoriesResponse, error) {
		return &statementdatav1.ListCategoriesResponse{Categories: []*statementdatav1.Category{{Id: "cat", Name: "Категория", Icon: "new", IsSystem: true, Kinds: []statementdatav1.CategoryKind{1, 2, 0, 99}, Assignable: true, Note: proto.String("не в тратах")}}}, nil
	}
	r = u.serve(t, "GET", "/v1/categories", "", nil)
	if r.Code != 200 || len(r.json["categories"].([]any)[0].(map[string]any)["kinds"].([]any)) != 2 {
		t.Fatal(r.Code, r.Body.String())
	}
	f.dismissRegularPayment = func(in *statementdatav1.DismissRegularPaymentRequest) (*statementdatav1.DismissRegularPaymentResponse, error) {
		if in.RegularPaymentId != testKey {
			t.Fatal(in)
		}
		return &statementdatav1.DismissRegularPaymentResponse{}, nil
	}
	f.restoreRegularPayment = func(in *statementdatav1.RestoreRegularPaymentRequest) (*statementdatav1.RestoreRegularPaymentResponse, error) {
		if in.RegularPaymentId != testKey {
			t.Fatal(in)
		}
		return &statementdatav1.RestoreRegularPaymentResponse{}, nil
	}
	for _, method := range []string{"PUT", "DELETE"} {
		r := u.serve(t, method, "/v1/analytics/regular-payments/"+testKey+"/dismissal", "", nil)
		if r.Code != 204 || r.Body.Len() != 0 || r.Header().Get("Content-Type") != "" {
			t.Fatal(r.Code, r.Body.String())
		}
	}
}

func TestUnitOperationSearchMappingAndValidation(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	var got *statementdatav1.SearchOperationsRequest
	u.operations.searchOperations = func(in *statementdatav1.SearchOperationsRequest) (*statementdatav1.SearchOperationsResponse, error) {
		got = in
		return &statementdatav1.SearchOperationsResponse{Items: []*statementdatav1.Operation{operationFixture()}, TotalCount: 1, HasOlderData: proto.Bool(false), TimeZone: "Europe/Moscow"}, nil
	}
	for _, q := range []string{"from=2026-01-01T00:00:00Z&to=2026-02-01T00:00:00Z&q=Самокат&category_id=cat&kind=expense&amount_from=0&amount_to=9223372036854775807&transfer_mode=without&selection=s_merchant_opaque&selection_name=ignore", "all_time=true&kind=income&transfer_mode=with", "all_time=false", "q=abc", "to=2026-02-01T00:00:00Z"} {
		r := u.serve(t, "GET", "/v1/operations?"+q, "", nil)
		if r.Code != 200 || r.json["total_count"] != 1.0 || r.json["has_older_data"] != false || r.json["summary"] != nil {
			t.Fatal(q, r.Code, r.Body.String())
		}
		if strings.HasPrefix(q, "from=") && (got.Filter.GetQ() != "Самокат" || got.Filter.GetCategoryId() != "cat" || got.Filter.Kind != 1 || got.Filter.TransferMode != 2 || got.Filter.GetSelection() != "s_merchant_opaque" || got.Filter.SelectionName != nil || got.Filter.AmountFromMinor == nil || got.Filter.GetAmountToMinor() != 9223372036854775807 || got.Filter.From == nil || got.Filter.To == nil) {
			t.Fatal(got)
		}
		if strings.HasPrefix(q, "all_time=true") && (!got.AllTime || got.Filter.Kind != 2 || got.Filter.TransferMode != 1) {
			t.Fatal(got)
		}
	}
	for _, q := range []string{"before=2026-01-01T00:00:00Z&q=abc", "all_time=true&from=2026-01-01T00:00:00Z", "all_time=false&to=2026-01-01T00:00:00Z", "kind=own_transfer", "kind=future", "transfer_mode=future", "from=no", "to=no", "before=no", "all_time=wrong", "amount_from=-1", "amount_to=9223372036854775808"} {
		got = nil
		r := u.serve(t, "GET", "/v1/operations?"+q, "", nil)
		if r.Code != 400 || got != nil || r.code() != "VALIDATION_ERROR" {
			t.Fatal(q, r.Code, r.Body.String(), got)
		}
	}
	for _, q := range []string{"", "?occurred_at=bad"} {
		if r := u.serve(t, "GET", "/v1/operations/"+testKey+q, "", nil); r.Code != 400 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	for _, body := range []string{`{}`, `{"category_id":"x"}`, `{"category_id":"x","occurred_at":"no"}`, `{"category_id":3,"occurred_at":"2026-01-01T00:00:00Z"}`} {
		if r := u.serve(t, "PATCH", "/v1/operations/"+testKey, body, nil); r.Code != 400 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	for _, method := range []string{"PUT", "DELETE"} {
		for _, id := range []string{"description", strings.ToUpper(testKey)} {
			if r := u.serve(t, method, "/v1/analytics/regular-payments/"+id+"/dismissal", "", nil); r.Code != 400 {
				t.Fatal(r.Code, r.Body.String())
			}
		}
	}
	// The search RPC alone receives the longer deadline.
	if len(u.f.deadlines) != 5 {
		t.Fatal("missing search deadline observations", u.f.deadlines)
	}
	for _, d := range u.f.deadlines {
		if d <= 5*time.Second || d > SearchDeadline {
			t.Fatal(d)
		}
	}
}

func TestUnitOperationProjectionFailuresAndEmptyCollections(t *testing.T) {
	mutations := []func(*statementdatav1.Operation){func(o *statementdatav1.Operation) { o.Kind = 99 }, func(o *statementdatav1.Operation) { o.Status = 0 }, func(o *statementdatav1.Operation) { o.OccurredAt = nil }, func(o *statementdatav1.Operation) { o.OccurredAt.Nanos = -1 }, func(o *statementdatav1.Operation) { o.Currency = "" }, func(o *statementdatav1.Operation) { o.AmountMinor = -1 }, func(o *statementdatav1.Operation) { o.PostedAt = &timestamppb.Timestamp{Nanos: -1} }}
	for _, mutate := range mutations {
		u := newUnitAPI(t, rest.StreamLimits{})
		o := operationFixture()
		mutate(o)
		u.operations.getOperation = func(*statementdatav1.GetOperationRequest) (*statementdatav1.GetOperationResponse, error) {
			return &statementdatav1.GetOperationResponse{Operation: o}, nil
		}
		r := u.serve(t, "GET", "/v1/operations/"+testKey+"?occurred_at=2026-01-01T00:00:00Z", "", nil)
		if r.Code != 500 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	for _, mutate := range []func(*statementdatav1.ListFeedResponse){func(r *statementdatav1.ListFeedResponse) { r.Items = []*statementdatav1.Operation{nil} }, func(r *statementdatav1.ListFeedResponse) { r.State = nil }, func(r *statementdatav1.ListFeedResponse) { r.Range = nil }, func(r *statementdatav1.ListFeedResponse) { r.NextBefore = &timestamppb.Timestamp{Nanos: -1} }, func(r *statementdatav1.ListFeedResponse) { r.Summary[0].Month = "2026-Q1" }, func(r *statementdatav1.ListFeedResponse) { r.Summary[0].Range = nil }, func(r *statementdatav1.ListFeedResponse) { r.Summary[0].DataTo = &timestamppb.Timestamp{Nanos: -1} }} {
		u := newUnitAPI(t, rest.StreamLimits{})
		in := feedFixture()
		mutate(in)
		u.operations.listFeed = func(*statementdatav1.ListFeedRequest) (*statementdatav1.ListFeedResponse, error) { return in, nil }
		if r := u.serve(t, "GET", "/v1/operations", "", nil); r.Code != 500 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	u := newUnitAPI(t, rest.StreamLimits{})
	u.operations.listFeed = func(*statementdatav1.ListFeedRequest) (*statementdatav1.ListFeedResponse, error) {
		in := feedFixture()
		in.Items = nil
		in.Summary = nil
		in.NextBefore = nil
		in.State = &statementdatav1.FeedState{Stale: true}
		return in, nil
	}
	if r := u.serve(t, "GET", "/v1/operations", "", nil); r.Code != 200 || len(r.json["items"].([]any)) != 0 || len(r.json["summary"].([]any)) != 0 {
		t.Fatal(r.Code, r.Body.String())
	}
	u.operations.searchOperations = func(*statementdatav1.SearchOperationsRequest) (*statementdatav1.SearchOperationsResponse, error) {
		return &statementdatav1.SearchOperationsResponse{}, nil
	}
	if r := u.serve(t, "GET", "/v1/operations?all_time=true", "", nil); r.Code != 200 || len(r.json["items"].([]any)) != 0 || r.json["has_older_data"] != nil || r.json["range"] != nil {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestUnitOperationErrors(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	f := u.operations
	f.listFeed = func(*statementdatav1.ListFeedRequest) (*statementdatav1.ListFeedResponse, error) {
		return nil, finerr.TokenRevoked()
	}
	f.searchOperations = func(*statementdatav1.SearchOperationsRequest) (*statementdatav1.SearchOperationsResponse, error) {
		return nil, finerr.TooManyResults()
	}
	f.getOperation = func(*statementdatav1.GetOperationRequest) (*statementdatav1.GetOperationResponse, error) {
		return nil, finerr.NotFound()
	}
	f.changeCategory = func(*statementdatav1.ChangeCategoryRequest) (*statementdatav1.ChangeCategoryResponse, error) {
		return nil, finerr.CategoryNotAssignable()
	}
	f.listCategories = func(*statementdatav1.ListCategoriesRequest) (*statementdatav1.ListCategoriesResponse, error) {
		return nil, errors.New("SECRET")
	}
	f.dismissRegularPayment = func(*statementdatav1.DismissRegularPaymentRequest) (*statementdatav1.DismissRegularPaymentResponse, error) {
		return nil, finerr.NotFound()
	}
	f.restoreRegularPayment = func(*statementdatav1.RestoreRegularPaymentRequest) (*statementdatav1.RestoreRegularPaymentResponse, error) {
		return nil, finerr.TokenRevoked()
	}
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{{"GET", "/v1/operations", "", 401}, {"GET", "/v1/operations?q=x", "", 422}, {"GET", "/v1/operations/" + testKey + "?occurred_at=" + url.QueryEscape("2026-01-01T00:00:00Z"), "", 404}, {"PATCH", "/v1/operations/" + testKey, `{"category_id":"cat","occurred_at":"2026-01-01T00:00:00Z"}`, 422}, {"GET", "/v1/categories", "", 500}, {"PUT", "/v1/analytics/regular-payments/" + testKey + "/dismissal", "", 404}, {"DELETE", "/v1/analytics/regular-payments/" + testKey + "/dismissal", "", 401}} {
		r := u.serve(t, tc.method, tc.path, tc.body, nil)
		if r.Code != tc.status || strings.Contains(r.Body.String(), "SECRET") {
			t.Fatal(r.Code, r.Body.String())
		}
	}
}

func TestUnitOperationPairAndOptionalFields(t *testing.T) {
	u := newUnitAPI(t, rest.StreamLimits{})
	item := operationFixture()
	item.Kind = statementdatav1.OperationKind_OPERATION_KIND_OWN_TRANSFER
	item.Status = statementdatav1.OperationStatus_OPERATION_STATUS_POSTED
	item.Account = nil
	item.FromAccount = &commonv1.AccountRef{TypeName: "Накопительный", Spoken: proto.String("накопительный счёт")}
	item.ToAccount = &commonv1.AccountRef{TypeName: "Текущий", Mask: proto.String("··4321"), Spoken: proto.String("текущий счёт")}
	item.Description = nil
	item.PostedAt = nil
	item.Note = nil
	item.NoteDetails = nil
	item.BankCategory = nil
	item.Mcc = nil
	u.operations.getOperation = func(*statementdatav1.GetOperationRequest) (*statementdatav1.GetOperationResponse, error) {
		return &statementdatav1.GetOperationResponse{Operation: item, TimeZone: "Europe/Moscow"}, nil
	}
	r := u.serve(t, "GET", "/v1/operations/"+testKey+"?occurred_at=2026-01-01T00:00:00Z", "", nil)
	if r.Code != 200 || r.json["kind"] != "own_transfer" || r.json["account"] != nil || r.json["from_account"].(map[string]any)["spoken"] != "накопительный счёт" || r.json["to_account"].(map[string]any)["mask"] != "··4321" || r.json["description"] != nil || r.json["posted_at"] != nil || r.json["bank_category"] != nil {
		t.Fatal(r.Code, r.Body.String())
	}
	u.operations.changeCategory = func(*statementdatav1.ChangeCategoryRequest) (*statementdatav1.ChangeCategoryResponse, error) {
		return &statementdatav1.ChangeCategoryResponse{Items: []*statementdatav1.Operation{item}, TimeZone: "Europe/Moscow"}, nil
	}
	r = u.serve(t, "PATCH", "/v1/operations/"+testKey, `{"category_id":"cat_own_transfer","occurred_at":"2026-01-01T00:00:00Z"}`, nil)
	if r.Code != 200 || len(r.json["items"].([]any)) != 1 || r.json["replaced_id"] != nil {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestUnitOperationKindDescriptorRoundTrip(t *testing.T) {
	values := commonv1.KindFilter_KIND_FILTER_UNSPECIFIED.Descriptor().Values()
	accepted := 0
	for i := 0; i < values.Len(); i++ {
		kind := commonv1.KindFilter(values.Get(i).Number())
		code, ok := enumCode(kind)
		if !ok {
			continue
		}
		accepted++
		out := operationsFilter(&commonv1.OperationsFilter{Kind: kind}, time.UTC)
		if out.Kind == nil || *out.Kind != code {
			t.Fatal(kind, out)
		}
		got, ok := kindFilterCode(*out.Kind)
		if !ok || got != kind {
			t.Fatal(code, got, ok)
		}
	}
	if accepted == 0 {
		t.Fatal("no generated kind was checked")
	}
	for _, code := range []string{"", "unspecified", "not_a_generated_kind", "EXPENSE", "Expense"} {
		if got, ok := kindFilterCode(code); ok {
			t.Fatal(code, got)
		}
	}
}

func TestUnitOperationOtherMalformedReplies(t *testing.T) {
	cases := []struct {
		name, method, path, body, field, code string
		setup                                 func(*operationFakes)
	}{
		{name: "search nil item", method: "GET", path: "/v1/operations?q=x", field: "Operation", code: "absent", setup: func(f *operationFakes) {
			f.searchOperations = func(*statementdatav1.SearchOperationsRequest) (*statementdatav1.SearchOperationsResponse, error) {
				return &statementdatav1.SearchOperationsResponse{Items: []*statementdatav1.Operation{nil}}, nil
			}
		}},
		{name: "search invalid item", method: "GET", path: "/v1/operations?q=x", field: "Operation.status", code: "unknown", setup: func(f *operationFakes) {
			f.searchOperations = func(*statementdatav1.SearchOperationsRequest) (*statementdatav1.SearchOperationsResponse, error) {
				item := operationFixture()
				item.Status = 99
				return &statementdatav1.SearchOperationsResponse{Items: []*statementdatav1.Operation{item}}, nil
			}
		}},
		{name: "search invalid range", method: "GET", path: "/v1/operations?q=x", field: "SearchOperationsResponse.range.end", code: "invalid", setup: func(f *operationFakes) {
			f.searchOperations = func(*statementdatav1.SearchOperationsRequest) (*statementdatav1.SearchOperationsResponse, error) {
				return &statementdatav1.SearchOperationsResponse{Range: &commonv1.TimeRange{Start: operationFixture().OccurredAt, End: &timestamppb.Timestamp{Nanos: -1}}}, nil
			}
		}},
		{name: "search nil response", method: "GET", path: "/v1/operations?q=x", field: "SearchOperationsResponse", code: "absent", setup: func(f *operationFakes) {
			f.searchOperations = func(*statementdatav1.SearchOperationsRequest) (*statementdatav1.SearchOperationsResponse, error) {
				return nil, nil
			}
		}},
		{name: "change invalid item", method: "PATCH", path: "/v1/operations/" + testKey, body: `{"category_id":"cat","occurred_at":"2026-01-01T00:00:00Z"}`, field: "Operation.kind", code: "unknown", setup: func(f *operationFakes) {
			f.changeCategory = func(*statementdatav1.ChangeCategoryRequest) (*statementdatav1.ChangeCategoryResponse, error) {
				item := operationFixture()
				item.Kind = 99
				return &statementdatav1.ChangeCategoryResponse{Items: []*statementdatav1.Operation{item}}, nil
			}
		}},
		{name: "change nil response", method: "PATCH", path: "/v1/operations/" + testKey, body: `{"category_id":"cat","occurred_at":"2026-01-01T00:00:00Z"}`, field: "ChangeCategoryResponse", code: "absent", setup: func(f *operationFakes) {
			f.changeCategory = func(*statementdatav1.ChangeCategoryRequest) (*statementdatav1.ChangeCategoryResponse, error) {
				return nil, nil
			}
		}},
		{name: "category nil item", method: "GET", path: "/v1/categories", field: "Category", code: "absent", setup: func(f *operationFakes) {
			f.listCategories = func(*statementdatav1.ListCategoriesRequest) (*statementdatav1.ListCategoriesResponse, error) {
				return &statementdatav1.ListCategoriesResponse{Categories: []*statementdatav1.Category{nil}}, nil
			}
		}},
		{name: "category missing id", method: "GET", path: "/v1/categories", field: "Category.id", code: "absent", setup: func(f *operationFakes) {
			f.listCategories = func(*statementdatav1.ListCategoriesRequest) (*statementdatav1.ListCategoriesResponse, error) {
				return &statementdatav1.ListCategoriesResponse{Categories: []*statementdatav1.Category{{}}}, nil
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := newUnitAPI(t, rest.StreamLimits{})
			tc.setup(u.operations)
			r := u.serve(t, tc.method, tc.path, tc.body, nil)
			if r.Code != 500 || r.code() != "INTERNAL_ERROR" || unitAnalyticsCounter(t, u, tc.field, tc.code) != 1 {
				t.Fatal(r.Code, r.Body.String(), tc.field, tc.code)
			}
		})
	}
}
