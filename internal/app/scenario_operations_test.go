package app

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	statementdatav1 "github.com/fin-assist/contracts/gen/go/finassist/statementdata/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/fin-assist/bank-bff/internal/endpoint"
	"github.com/fin-assist/bank-bff/internal/testpeer"
)

func operationMessage() *statementdatav1.Operation {
	at := timestamppb.New(time.Date(2026, 9, 25, 16, 11, 0, 123456789, time.UTC))
	return &statementdatav1.Operation{Id: idemKey, Kind: 1, OccurredAt: at, AmountMinor: 123456, Currency: "EUR", Title: "", Category: &statementdatav1.CategoryRef{Id: "cat_shop", Name: "Покупки", Icon: "future_icon"}, IsCategoryManual: true, Note: proto.String("возврат · Покупки"), Status: 2, Account: &commonv1.AccountRef{TypeName: "Текущий", Mask: proto.String("··1234"), Spoken: proto.String("текущий счёт")}, Description: proto.String("описание"), PostedAt: at, NoteDetails: proto.String("готовое пояснение"), BankCategory: proto.String("Покупки"), Mcc: proto.String("1234"), SourceName: "Выписка Т-Банка"}
}
func feedMessage() *statementdatav1.ListFeedResponse {
	item := operationMessage()
	r := &commonv1.TimeRange{Start: item.OccurredAt, End: item.OccurredAt}
	return &statementdatav1.ListFeedResponse{Items: []*statementdatav1.Operation{item}, Summary: []*statementdatav1.MonthSummary{{Month: "2026-09", Range: r, ExpenseMinor: -123, IncomeMinor: 456, DataTo: item.OccurredAt}}, State: &statementdatav1.FeedState{Stale: true, DataTo: item.OccurredAt, LastOperationAt: item.OccurredAt, LastUploadedAt: item.OccurredAt}, Range: r, NextBefore: item.OccurredAt, TimeZone: "Asia/Vladivostok"}
}

func scenarioOperations(t *testing.T, start starter) {
	b := start(t, nil)
	access := b.keys.Access("u1")
	auth := base(map[string]string{"Authorization": "Bearer " + access, "X-Read-Primary": "1", "Idempotency-Key": "not-a-key", "X-User-Id": "u2", "Cookie": "secret=value"})
	var mu sync.Mutex
	var before *timestamppb.Timestamp
	b.peers.Operations.Set(func(p *testpeer.Operations) {
		p.ListFeedFunc = func(ctx context.Context, in *statementdatav1.ListFeedRequest) (*statementdatav1.ListFeedResponse, error) {
			profileDeadline(t, ctx)
			mu.Lock()
			before = in.Before
			mu.Unlock()
			return feedMessage(), nil
		}
	})
	for _, query := range []string{"", "?before=2026-09-01T00:00:00%2B03:00"} {
		r := b.do(t, request{method: "GET", path: "/v1/operations" + query, route: "/v1/operations", header: auth})
		if r.StatusCode != 200 {
			t.Fatal(r.StatusCode, string(r.body))
		}
		item := r.json["items"].([]any)[0].(map[string]any)
		if item["occurred_at"] != "2026-09-26T02:11:00.123456789+10:00" || item["currency"] != "EUR" || item["title"] != "" || item["description"] != nil || item["account"].(map[string]any)["spoken"] != nil || r.json["summary"].([]any)[0].(map[string]any)["expense"] != -123.0 || r.json["state"].(map[string]any)["stale"] != true {
			t.Fatal(string(r.body))
		}
		mu.Lock()
		got := before
		mu.Unlock()
		if query != "" && (got == nil || got.AsTime().Format(time.RFC3339) != "2026-08-31T21:00:00Z") {
			t.Fatal(got)
		}
		call := lastCall(t, b, statementdatav1.OperationsService_ListFeed_FullMethodName, access, "")
		if !reflect.DeepEqual(call.MD.Get("x-read-primary"), []string{"1"}) || len(call.MD.Get("x-user-id")) != 0 || len(call.MD.Get("cookie")) != 0 || len(call.MD.Get("idempotency-key")) != 0 {
			t.Fatal(call.MD)
		}
	}
	var input *statementdatav1.SearchOperationsRequest
	b.peers.Operations.Set(func(p *testpeer.Operations) {
		p.SearchOperationsFunc = func(ctx context.Context, in *statementdatav1.SearchOperationsRequest) (*statementdatav1.SearchOperationsResponse, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) <= 5*time.Second || time.Until(deadline) > endpoint.SearchDeadline {
				t.Error("search deadline", deadline)
			}
			mu.Lock()
			input = proto.Clone(in).(*statementdatav1.SearchOperationsRequest)
			mu.Unlock()
			return &statementdatav1.SearchOperationsResponse{Items: []*statementdatav1.Operation{operationMessage()}, TotalCount: 1, HasOlderData: proto.Bool(false), Range: feedMessage().Range, TimeZone: "Asia/Vladivostok"}, nil
		}
	})
	for _, q := range []string{"from=2026-01-01T00:00:00Z&to=2026-02-01T00:00:00Z&q=Самокат&category_id=cat_shop&kind=expense&amount_from=0&amount_to=9223372036854775807&transfer_mode=without&selection=s_merchant_opaque&user_id=u2", "all_time=true&kind=income&transfer_mode=with", "all_time=false", "q=abc", "to=2026-01-01T00:00:00Z"} {
		r := b.do(t, request{method: "GET", path: "/v1/operations?" + q, route: "/v1/operations", header: auth})
		if r.StatusCode != 200 || r.json["has_older_data"] != false || r.json["total_count"] != 1.0 || r.json["summary"] != nil {
			t.Fatal(r.StatusCode, string(r.body))
		}
		mu.Lock()
		in := input
		mu.Unlock()
		if strings.HasPrefix(q, "from=") && (in.Filter.GetQ() != "Самокат" || in.Filter.GetSelection() != "s_merchant_opaque" || in.Filter.GetCategoryId() != "cat_shop" || in.Filter.Kind != 1 || in.Filter.TransferMode != 2 || in.Filter.AmountFromMinor == nil || in.Filter.GetAmountToMinor() != 9223372036854775807 || in.Filter.From == nil || in.Filter.To == nil) {
			t.Fatal(in)
		}
		if strings.HasPrefix(q, "all_time=true") && (!in.AllTime || in.Filter.Kind != 2 || in.Filter.TransferMode != 1) {
			t.Fatal(in)
		}
	}
	searchCall := lastCall(t, b, statementdatav1.OperationsService_SearchOperations_FullMethodName, access, "")
	if len(searchCall.MD.Get("idempotency-key")) != 0 || len(searchCall.MD.Get("x-user-id")) != 0 || len(searchCall.MD.Get("cookie")) != 0 || !reflect.DeepEqual(searchCall.MD.Get("x-read-primary"), []string{"1"}) {
		t.Fatal(searchCall.MD)
	}
	// Empty collections keep JSON arrays; no operations or fabricated times are added.
	b.peers.Operations.Set(func(p *testpeer.Operations) {
		p.ListFeedFunc = func(context.Context, *statementdatav1.ListFeedRequest) (*statementdatav1.ListFeedResponse, error) {
			in := feedMessage()
			in.Items = nil
			in.Summary = nil
			in.NextBefore = nil
			in.State = &statementdatav1.FeedState{Stale: true}
			return in, nil
		}
		p.SearchOperationsFunc = func(context.Context, *statementdatav1.SearchOperationsRequest) (*statementdatav1.SearchOperationsResponse, error) {
			return &statementdatav1.SearchOperationsResponse{}, nil
		}
	})
	for _, q := range []string{"", "?all_time=true"} {
		r := b.do(t, request{method: "GET", path: "/v1/operations" + q, route: "/v1/operations", header: auth})
		if r.StatusCode != 200 || len(r.json["items"].([]any)) != 0 || r.json["has_older_data"] != nil {
			t.Fatal(r.StatusCode, string(r.body))
		}
	}
}

func scenarioOperationMutations(t *testing.T, start starter) {
	b := start(t, nil)
	access := b.keys.Access("u1")
	auth := base(map[string]string{"Authorization": "Bearer " + access, "Idempotency-Key": "not-a-key", "X-User-Id": "u2"})
	b.peers.Operations.Set(func(p *testpeer.Operations) {
		p.GetOperationFunc = func(ctx context.Context, in *statementdatav1.GetOperationRequest) (*statementdatav1.GetOperationResponse, error) {
			profileDeadline(t, ctx)
			if in.Id != idemKey || in.OccurredAt.Nanos != 123456789 {
				t.Error(in)
			}
			return &statementdatav1.GetOperationResponse{Operation: operationMessage(), TimeZone: "Asia/Vladivostok"}, nil
		}
		p.ChangeCategoryFunc = func(ctx context.Context, in *statementdatav1.ChangeCategoryRequest) (*statementdatav1.ChangeCategoryResponse, error) {
			profileDeadline(t, ctx)
			if in.Id != idemKey || in.CategoryId != "cat_refund" || in.OccurredAt.Nanos != 123456789 {
				t.Error(in)
			}
			one := operationMessage()
			two := operationMessage()
			two.Id = "second"
			two.Kind = 2
			return &statementdatav1.ChangeCategoryResponse{Items: []*statementdatav1.Operation{one, two}, ReplacedId: proto.String(idemKey), TimeZone: "Asia/Vladivostok"}, nil
		}
	})
	r := b.do(t, request{method: "GET", path: "/v1/operations/" + idemKey + "?occurred_at=" + url.QueryEscape("2026-09-25T19:11:00.123456789+03:00"), route: "/v1/operations/{id}", header: auth})
	if r.StatusCode != 200 || r.json["occurred_at"] != "2026-09-26T02:11:00.123456789+10:00" || r.json["posted_at"] != r.json["occurred_at"] || r.json["source_name"] != "Выписка Т-Банка" || r.json["account"].(map[string]any)["spoken"] != "текущий счёт" || r.json["description"] != "описание" || r.json["mcc"] != "1234" || r.json["bank_category"] != "Покупки" {
		t.Fatal(r.StatusCode, string(r.body))
	}
	for range 2 {
		r = b.do(t, request{method: "PATCH", path: "/v1/operations/" + idemKey, route: "/v1/operations/{id}", body: `{"category_id":"cat_refund","occurred_at":"2026-09-25T16:11:00.123456789Z","Category_id":"foreign"}`, header: auth})
		if r.StatusCode != 200 || len(r.json["items"].([]any)) != 2 || r.json["replaced_id"] != idemKey {
			t.Fatal(r.StatusCode, string(r.body))
		}
	}
	pair := operationMessage()
	pair.Kind = statementdatav1.OperationKind_OPERATION_KIND_OWN_TRANSFER
	pair.Status = statementdatav1.OperationStatus_OPERATION_STATUS_POSTED
	pair.Account = nil
	pair.FromAccount = &commonv1.AccountRef{TypeName: "Накопительный", Spoken: proto.String("накопительный счёт")}
	pair.ToAccount = &commonv1.AccountRef{TypeName: "Текущий", Mask: proto.String("··4321"), Spoken: proto.String("текущий счёт")}
	pair.Description = nil
	pair.PostedAt = nil
	pair.NoteDetails = nil
	pair.BankCategory = nil
	pair.Mcc = nil
	b.peers.Operations.Set(func(p *testpeer.Operations) {
		p.GetOperationFunc = func(context.Context, *statementdatav1.GetOperationRequest) (*statementdatav1.GetOperationResponse, error) {
			return &statementdatav1.GetOperationResponse{Operation: pair}, nil
		}
		p.ChangeCategoryFunc = func(context.Context, *statementdatav1.ChangeCategoryRequest) (*statementdatav1.ChangeCategoryResponse, error) {
			return &statementdatav1.ChangeCategoryResponse{Items: []*statementdatav1.Operation{pair}}, nil
		}
	})
	r = b.do(t, request{method: "GET", path: "/v1/operations/" + idemKey + "?occurred_at=2026-09-01T00:00:00Z", route: "/v1/operations/{id}", header: auth})
	if r.StatusCode != 200 || r.json["kind"] != "own_transfer" || r.json["account"] != nil || r.json["from_account"].(map[string]any)["spoken"] != "накопительный счёт" || r.json["to_account"].(map[string]any)["mask"] != "··4321" || r.json["posted_at"] != nil || r.json["description"] != nil {
		t.Fatal(r.StatusCode, string(r.body))
	}
	r = b.do(t, request{method: "PATCH", path: "/v1/operations/" + idemKey, route: "/v1/operations/{id}", body: `{"category_id":"cat_own_transfer","occurred_at":"2026-09-01T00:00:00Z"}`, header: auth})
	if r.StatusCode != 200 || len(r.json["items"].([]any)) != 1 || r.json["replaced_id"] != nil {
		t.Fatal(r.StatusCode, string(r.body))
	}
	b.peers.Categories.Set(func(p *testpeer.Categories) {
		p.ListCategoriesFunc = func(ctx context.Context, _ *statementdatav1.ListCategoriesRequest) (*statementdatav1.ListCategoriesResponse, error) {
			profileDeadline(t, ctx)
			return &statementdatav1.ListCategoriesResponse{Categories: []*statementdatav1.Category{{Id: "cat_refund", Name: "Возврат", Icon: "future_icon", IsSystem: true, Kinds: []statementdatav1.CategoryKind{1, 2, 99, 0}, Assignable: true, Note: proto.String("готовое пояснение")}}}, nil
		}
	})
	r = b.do(t, request{method: "GET", path: "/v1/categories", route: "/v1/categories", header: auth})
	if r.StatusCode != 200 || len(r.json["categories"].([]any)[0].(map[string]any)["kinds"].([]any)) != 2 {
		t.Fatal(r.StatusCode, string(r.body))
	}
	b.peers.RegularPayments.Set(func(p *testpeer.RegularPayments) {
		p.DismissRegularPaymentFunc = func(ctx context.Context, in *statementdatav1.DismissRegularPaymentRequest) (*statementdatav1.DismissRegularPaymentResponse, error) {
			profileDeadline(t, ctx)
			if in.RegularPaymentId != idemKey {
				t.Error(in)
			}
			return &statementdatav1.DismissRegularPaymentResponse{}, nil
		}
		p.RestoreRegularPaymentFunc = func(ctx context.Context, in *statementdatav1.RestoreRegularPaymentRequest) (*statementdatav1.RestoreRegularPaymentResponse, error) {
			profileDeadline(t, ctx)
			if in.RegularPaymentId != idemKey {
				t.Error(in)
			}
			return &statementdatav1.RestoreRegularPaymentResponse{}, nil
		}
	})
	for _, method := range []string{"PUT", "DELETE"} {
		for range 2 {
			r = b.do(t, request{method: method, path: "/v1/analytics/regular-payments/" + idemKey + "/dismissal", route: "/v1/analytics/regular-payments/{id}/dismissal", header: auth})
			if r.StatusCode != 204 || len(r.body) != 0 || r.Header.Get("Content-Type") != "" {
				t.Fatal(r.StatusCode, string(r.body))
			}
		}
	}
	for _, method := range []string{statementdatav1.OperationsService_GetOperation_FullMethodName, statementdatav1.OperationsService_ChangeCategory_FullMethodName, statementdatav1.CategoriesService_ListCategories_FullMethodName, statementdatav1.RegularPaymentsService_DismissRegularPayment_FullMethodName, statementdatav1.RegularPaymentsService_RestoreRegularPayment_FullMethodName} {
		call := lastCall(t, b, method, access, "")
		if len(call.MD.Get("x-user-id")) != 0 || len(call.MD.Get("idempotency-key")) != 0 {
			t.Fatal(call.MD)
		}
	}
	if strings.Contains(b.logs.text(), "готовое пояснение") || strings.Contains(b.logs.text(), "описание") {
		t.Fatal("financial data leaked into logs")
	}
}

func operationErrorEndpoints() []failingOperation {
	return []failingOperation{
		{request{method: "GET", path: "/v1/operations", route: "/v1/operations"}, statementdatav1.OperationsService_ListFeed_FullMethodName, declared(400, 401, 422, 429, 500, 503), func(p *testpeer.Peers, err error) {
			p.Operations.Set(func(o *testpeer.Operations) {
				o.ListFeedFunc = func(context.Context, *statementdatav1.ListFeedRequest) (*statementdatav1.ListFeedResponse, error) {
					return nil, err
				}
			})
		}},
		{request{method: "GET", path: "/v1/operations?q=abc", route: "/v1/operations"}, statementdatav1.OperationsService_SearchOperations_FullMethodName, declared(400, 401, 422, 429, 500, 503), func(p *testpeer.Peers, err error) {
			p.Operations.Set(func(o *testpeer.Operations) {
				o.SearchOperationsFunc = func(context.Context, *statementdatav1.SearchOperationsRequest) (*statementdatav1.SearchOperationsResponse, error) {
					return nil, err
				}
			})
		}},
		{request{method: "GET", path: "/v1/operations/" + idemKey + "?occurred_at=2026-09-25T00:00:00Z", route: "/v1/operations/{id}"}, statementdatav1.OperationsService_GetOperation_FullMethodName, declared(400, 401, 404, 429, 500, 503), func(p *testpeer.Peers, err error) {
			p.Operations.Set(func(o *testpeer.Operations) {
				o.GetOperationFunc = func(context.Context, *statementdatav1.GetOperationRequest) (*statementdatav1.GetOperationResponse, error) {
					return nil, err
				}
			})
		}},
		{request{method: "PATCH", path: "/v1/operations/" + idemKey, route: "/v1/operations/{id}", body: `{"category_id":"cat","occurred_at":"2026-09-25T00:00:00Z"}`}, statementdatav1.OperationsService_ChangeCategory_FullMethodName, declared(400, 401, 404, 422, 429, 500, 503), func(p *testpeer.Peers, err error) {
			p.Operations.Set(func(o *testpeer.Operations) {
				o.ChangeCategoryFunc = func(context.Context, *statementdatav1.ChangeCategoryRequest) (*statementdatav1.ChangeCategoryResponse, error) {
					return nil, err
				}
			})
		}},
		{request{method: "GET", path: "/v1/categories", route: "/v1/categories"}, statementdatav1.CategoriesService_ListCategories_FullMethodName, declared(400, 401, 429, 500, 503), func(p *testpeer.Peers, err error) {
			p.Categories.Set(func(o *testpeer.Categories) {
				o.ListCategoriesFunc = func(context.Context, *statementdatav1.ListCategoriesRequest) (*statementdatav1.ListCategoriesResponse, error) {
					return nil, err
				}
			})
		}},
		{request{method: "PUT", path: "/v1/analytics/regular-payments/" + idemKey + "/dismissal", route: "/v1/analytics/regular-payments/{id}/dismissal"}, statementdatav1.RegularPaymentsService_DismissRegularPayment_FullMethodName, declared(400, 401, 404, 429, 500, 503), func(p *testpeer.Peers, err error) {
			p.RegularPayments.Set(func(o *testpeer.RegularPayments) {
				o.DismissRegularPaymentFunc = func(context.Context, *statementdatav1.DismissRegularPaymentRequest) (*statementdatav1.DismissRegularPaymentResponse, error) {
					return nil, err
				}
			})
		}},
		{request{method: "DELETE", path: "/v1/analytics/regular-payments/" + idemKey + "/dismissal", route: "/v1/analytics/regular-payments/{id}/dismissal"}, statementdatav1.RegularPaymentsService_RestoreRegularPayment_FullMethodName, declared(400, 401, 404, 429, 500, 503), func(p *testpeer.Peers, err error) {
			p.RegularPayments.Set(func(o *testpeer.RegularPayments) {
				o.RestoreRegularPaymentFunc = func(context.Context, *statementdatav1.RestoreRegularPaymentRequest) (*statementdatav1.RestoreRegularPaymentResponse, error) {
					return nil, err
				}
			})
		}},
	}
}
func scenarioOperationsErrorsAndGates(t *testing.T, start starter) {
	b := start(t, nil)
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1")})
	failures := registry(finerr.InvalidToken(), finerr.TokenRevoked(), finerr.RateLimited(retryAt()), finerr.ValidationError(&errdetails.BadRequest_FieldViolation{Field: "occurred_at", Reason: "INVALID_FORMAT", Description: "SECRET"}), finerr.NotFound(), finerr.TooManyResults(), finerr.SelectionNotFound(), finerr.CategoryNotAssignable(), finerr.InternalError(), finerr.WithRequestRetryInfo(finerr.InternalError(), true))
	failures = append(failures, peerError{err: errors.New("SECRET_TRANSPORT"), status: 500, code: "INTERNAL_ERROR"})
	for _, op := range operationErrorEndpoints() {
		for _, pe := range failures {
			op.fail(b.peers, pe.err)
			r := op.request
			r.header = auth
			want := pe
			if !op.declared[pe.status] {
				want = peerError{status: 500, code: "INTERNAL_ERROR"}
			}
			got := b.do(t, r)
			if got.StatusCode != want.status || errorCode(got) != want.code || strings.Contains(string(got.body), "SECRET") {
				t.Fatal(r.method, r.path, got.StatusCode, string(got.body), want)
			}
			if want.code == "RATE_LIMITED" && got.Header.Get("Retry-After") == "" {
				t.Fatal("missing Retry-After")
			}
		}
		for _, header := range []map[string]string{base(nil), base(map[string]string{"Authorization": "Bearer forged"}), base(map[string]string{"Authorization": auth["Authorization"], "X-Client-Version": "android/0.0.1"})} {
			calls := len(b.peers.Calls(op.method))
			r := op.request
			r.header = header
			got := b.do(t, r)
			if got.StatusCode != 400 && got.StatusCode != 401 {
				t.Fatal(got.StatusCode, string(got.body))
			}
			if len(b.peers.Calls(op.method)) != calls {
				t.Fatal("invalid request reached peer")
			}
		}
		for _, permission := range []bool{false, true} {
			op.fail(b.peers, finerr.WithRequestRetryInfo(finerr.RateLimited(retryAt()), permission))
			r := op.request
			r.header = auth
			got := b.do(t, r)
			if got.StatusCode != 429 || got.json["error"].(map[string]any)["retryable"] != permission {
				t.Fatal(got.StatusCode, string(got.body))
			}
		}
	}
	// Only identity's verified token may select the tenant. A foreign/deleted object
	// remains the service's NOT_FOUND/TOKEN_REVOKED, never an ID from client headers.
	for _, op := range operationErrorEndpoints() {
		calls := len(b.peers.Calls(op.method))
		r := op.request
		r.header = base(map[string]string{"Authorization": auth["Authorization"], "X-User-Id": "u2"})
		op.fail(b.peers, finerr.TokenRevoked())
		got := b.do(t, r)
		if got.StatusCode != 401 || len(b.peers.Calls(op.method)) != calls+1 {
			t.Fatal(got.StatusCode)
		}
		call := b.peers.Calls(op.method)[calls]
		if len(call.MD.Get("x-user-id")) != 0 || len(call.MD.Get("idempotency-key")) != 0 {
			t.Fatal(call.MD)
		}
	}
	for _, q := range []string{"before=2026-01-01T00:00:00Z&q=x", "all_time=true&from=2026-01-01T00:00:00Z", "kind=future", "transfer_mode=future", "amount_from=-1", "amount_to=9223372036854775808", "from=bad", "to=bad", "before=bad", "all_time=bad"} {
		r := b.do(t, request{method: "GET", path: "/v1/operations?" + q, route: "/v1/operations", header: auth})
		if r.StatusCode != 400 {
			t.Fatal(q, r.StatusCode, string(r.body))
		}
	}
	for _, r := range []request{{method: "GET", path: "/v1/operations/" + idemKey, route: "/v1/operations/{id}"}, {method: "GET", path: "/v1/operations/" + idemKey + "?occurred_at=bad", route: "/v1/operations/{id}"}, {method: "PATCH", path: "/v1/operations/" + idemKey, route: "/v1/operations/{id}", body: `{"category_id":"cat"}`}, {method: "PATCH", path: "/v1/operations/" + idemKey, route: "/v1/operations/{id}", body: `{"category_id":"cat","occurred_at":"bad"}`}, {method: "PUT", path: "/v1/analytics/regular-payments/description/dismissal", route: "/v1/analytics/regular-payments/{id}/dismissal"}, {method: "DELETE", path: "/v1/analytics/regular-payments/" + strings.ToUpper(idemKey) + "/dismissal", route: "/v1/analytics/regular-payments/{id}/dismissal"}} {
		r.header = auth
		got := b.do(t, r)
		if got.StatusCode != 400 {
			t.Fatal(r.path, got.StatusCode, string(got.body))
		}
	}
	// Reject bodies on each read/assignment method that has no body in OpenAPI.
	for _, op := range operationErrorEndpoints() {
		if op.request.method == "PATCH" {
			continue
		}
		r := op.request
		r.header = auth
		r.body = `{"user_id":"u2"}`
		got := b.do(t, r)
		if got.StatusCode != 400 {
			t.Fatal(got.StatusCode, string(got.body))
		}
	}
}

func scenarioOperationLargeSearch(t *testing.T, start starter) {
	b := start(t, nil)
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1")})
	item := operationMessage()
	item.Title = strings.Repeat("x", 5<<20)
	b.peers.Operations.Set(func(p *testpeer.Operations) {
		p.SearchOperationsFunc = func(context.Context, *statementdatav1.SearchOperationsRequest) (*statementdatav1.SearchOperationsResponse, error) {
			return &statementdatav1.SearchOperationsResponse{Items: []*statementdatav1.Operation{item}, TotalCount: 1}, nil
		}
		p.GetOperationFunc = func(context.Context, *statementdatav1.GetOperationRequest) (*statementdatav1.GetOperationResponse, error) {
			return &statementdatav1.GetOperationResponse{Operation: item}, nil
		}
	})
	r := b.do(t, request{method: "GET", path: "/v1/operations?q=x", route: "/v1/operations", header: auth})
	if r.StatusCode != 200 || len(r.json["items"].([]any)[0].(map[string]any)["title"].(string)) != 5<<20 {
		t.Fatal(r.StatusCode)
	}
	r = b.do(t, request{method: "GET", path: "/v1/operations/" + idemKey + "?occurred_at=2026-09-01T00:00:00Z", route: "/v1/operations/{id}", header: auth})
	if r.StatusCode != 500 {
		t.Fatal("16MiB exception leaked to detail", r.StatusCode)
	}
	item = proto.Clone(item).(*statementdatav1.Operation)
	item.Title = strings.Repeat("x", 17<<20)
	b.peers.Operations.Set(func(p *testpeer.Operations) {
		p.SearchOperationsFunc = func(context.Context, *statementdatav1.SearchOperationsRequest) (*statementdatav1.SearchOperationsResponse, error) {
			return &statementdatav1.SearchOperationsResponse{Items: []*statementdatav1.Operation{item}, TotalCount: 1}, nil
		}
	})
	r = b.do(t, request{method: "GET", path: "/v1/operations?q=x", route: "/v1/operations", header: auth})
	if r.StatusCode != 500 {
		t.Fatal("unbounded search receive", r.StatusCode)
	}
}

func scenarioOperationMalformed(t *testing.T, start starter) {
	b := start(t, nil)
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1")})
	for _, mutate := range []func(*statementdatav1.Operation){func(o *statementdatav1.Operation) { o.Kind = 99 }, func(o *statementdatav1.Operation) { o.Status = 0 }, func(o *statementdatav1.Operation) { o.Currency = "" }, func(o *statementdatav1.Operation) { o.AmountMinor = -1 }, func(o *statementdatav1.Operation) { o.OccurredAt = nil }, func(o *statementdatav1.Operation) { o.OccurredAt.Nanos = -1 }, func(o *statementdatav1.Operation) { o.PostedAt = &timestamppb.Timestamp{Nanos: -1} }} {
		item := operationMessage()
		mutate(item)
		b.peers.Operations.Set(func(p *testpeer.Operations) {
			p.GetOperationFunc = func(context.Context, *statementdatav1.GetOperationRequest) (*statementdatav1.GetOperationResponse, error) {
				return &statementdatav1.GetOperationResponse{Operation: item}, nil
			}
		})
		r := b.do(t, request{method: "GET", path: "/v1/operations/" + idemKey + "?occurred_at=2026-09-01T00:00:00Z", route: "/v1/operations/{id}", header: auth})
		if r.StatusCode != 500 {
			t.Fatal(r.StatusCode, string(r.body))
		}
	}
	for _, mutate := range []func(*statementdatav1.ListFeedResponse){func(r *statementdatav1.ListFeedResponse) { r.Items = []*statementdatav1.Operation{nil} }, func(r *statementdatav1.ListFeedResponse) { r.State = nil }, func(r *statementdatav1.ListFeedResponse) { r.Range = nil }, func(r *statementdatav1.ListFeedResponse) { r.NextBefore = &timestamppb.Timestamp{Nanos: -1} }, func(r *statementdatav1.ListFeedResponse) { r.Summary[0].Month = "2026-Q1" }, func(r *statementdatav1.ListFeedResponse) { r.Summary[0].Range = nil }, func(r *statementdatav1.ListFeedResponse) { r.Summary[0].DataTo = &timestamppb.Timestamp{Nanos: -1} }} {
		in := feedMessage()
		mutate(in)
		b.peers.Operations.Set(func(p *testpeer.Operations) {
			p.ListFeedFunc = func(context.Context, *statementdatav1.ListFeedRequest) (*statementdatav1.ListFeedResponse, error) {
				return in, nil
			}
		})
		r := b.do(t, request{method: "GET", path: "/v1/operations", route: "/v1/operations", header: auth})
		if r.StatusCode != 500 {
			t.Fatal(r.StatusCode, string(r.body))
		}
	}
}

// A lost successful mutation result must stay a transport failure. Retrying the
// old ID after a split/join hides the applied result behind a new NOT_FOUND.
func scenarioOperationMutationTransportLoss(t *testing.T, start starter) {
	b := start(t, nil)
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1")})
	for _, joining := range []bool{false, true} {
		var mu sync.Mutex
		applied := false
		changed := []*statementdatav1.Operation{operationMessage(), operationMessage()}
		changed[0].Id = "updated-expense"
		changed[1].Id = "updated-income"
		changed[1].Kind = 2
		if joining {
			changed = changed[:1]
			changed[0].Id = "updated-pair"
			changed[0].Kind = 3
		}
		b.peers.Operations.Set(func(p *testpeer.Operations) {
			p.ChangeCategoryFunc = func(_ context.Context, in *statementdatav1.ChangeCategoryRequest) (*statementdatav1.ChangeCategoryResponse, error) {
				mu.Lock()
				defer mu.Unlock()
				if in.Id != idemKey {
					t.Error(in)
				}
				if applied {
					return nil, finerr.NotFound()
				}
				applied = true
				return nil, status.Error(codes.Unavailable, "result lost after committed split or join")
			}
			p.ListFeedFunc = func(context.Context, *statementdatav1.ListFeedRequest) (*statementdatav1.ListFeedResponse, error) {
				mu.Lock()
				defer mu.Unlock()
				r := feedMessage()
				if applied {
					r.Items = changed
				}
				return r, nil
			}
		})
		calls := len(b.peers.Calls(statementdatav1.OperationsService_ChangeCategory_FullMethodName))
		got := b.do(t, request{method: "PATCH", path: "/v1/operations/" + idemKey, route: "/v1/operations/{id}", body: `{"category_id":"cat_refund","occurred_at":"2026-09-25T00:00:00Z"}`, header: auth})
		if got.StatusCode != 500 || errorCode(got) != "INTERNAL_ERROR" || len(b.peers.Calls(statementdatav1.OperationsService_ChangeCategory_FullMethodName)) != calls+1 {
			t.Fatal("mutation was silently replayed after commit", joining, got.StatusCode, string(got.body))
		}
		mu.Lock()
		didApply := applied
		mu.Unlock()
		if !didApply {
			t.Fatal("scenario did not apply effect")
		}
		recovered := b.do(t, request{method: "GET", path: "/v1/operations", route: "/v1/operations", header: auth})
		if recovered.StatusCode != 200 || len(recovered.json["items"].([]any)) != len(changed) {
			t.Fatal("changed records could not be recovered", recovered.StatusCode, string(recovered.body))
		}
		for i, item := range recovered.json["items"].([]any) {
			if item.(map[string]any)["id"] != changed[i].Id || item.(map[string]any)["id"] == idemKey {
				t.Fatal(item)
			}
		}
	}
	// Dismiss/restore follow the same BFF policy: service advice, never a hidden replay.
	for _, method := range []string{"PUT", "DELETE"} {
		var calls atomic.Int32
		b.peers.RegularPayments.Set(func(p *testpeer.RegularPayments) {
			p.DismissRegularPaymentFunc = func(context.Context, *statementdatav1.DismissRegularPaymentRequest) (*statementdatav1.DismissRegularPaymentResponse, error) {
				calls.Add(1)
				return nil, status.Error(codes.Unavailable, "assignment committed")
			}
			p.RestoreRegularPaymentFunc = func(context.Context, *statementdatav1.RestoreRegularPaymentRequest) (*statementdatav1.RestoreRegularPaymentResponse, error) {
				calls.Add(1)
				return nil, status.Error(codes.Unavailable, "assignment committed")
			}
		})
		got := b.do(t, request{method: method, path: "/v1/analytics/regular-payments/" + idemKey + "/dismissal", route: "/v1/analytics/regular-payments/{id}/dismissal", header: auth})
		if got.StatusCode != 500 || calls.Load() != 1 {
			t.Fatal("assignment was replayed", method, calls.Load(), got.StatusCode)
		}
	}
}

func scenarioOperationOtherMalformed(t *testing.T, start starter) {
	b := start(t, nil)
	auth := base(map[string]string{"Authorization": "Bearer " + b.keys.Access("u1")})
	cases := []struct {
		name, method, path, route, body, field, code string
		setup                                        func(*testpeer.Peers)
	}{
		{name: "search nil item", method: "GET", path: "/v1/operations?q=x", route: "/v1/operations", field: "Operation.kind", code: "unknown", setup: func(p *testpeer.Peers) {
			p.Operations.Set(func(o *testpeer.Operations) {
				o.SearchOperationsFunc = func(context.Context, *statementdatav1.SearchOperationsRequest) (*statementdatav1.SearchOperationsResponse, error) {
					return &statementdatav1.SearchOperationsResponse{Items: []*statementdatav1.Operation{nil}}, nil
				}
			})
		}},
		{name: "search invalid item", method: "GET", path: "/v1/operations?q=x", route: "/v1/operations", field: "Operation.status", code: "unknown", setup: func(p *testpeer.Peers) {
			p.Operations.Set(func(o *testpeer.Operations) {
				o.SearchOperationsFunc = func(context.Context, *statementdatav1.SearchOperationsRequest) (*statementdatav1.SearchOperationsResponse, error) {
					item := operationMessage()
					item.Status = 99
					return &statementdatav1.SearchOperationsResponse{Items: []*statementdatav1.Operation{item}}, nil
				}
			})
		}},
		{name: "search invalid range", method: "GET", path: "/v1/operations?q=x", route: "/v1/operations", field: "SearchOperationsResponse.range.end", code: "invalid", setup: func(p *testpeer.Peers) {
			p.Operations.Set(func(o *testpeer.Operations) {
				o.SearchOperationsFunc = func(context.Context, *statementdatav1.SearchOperationsRequest) (*statementdatav1.SearchOperationsResponse, error) {
					return &statementdatav1.SearchOperationsResponse{Range: &commonv1.TimeRange{Start: operationMessage().OccurredAt, End: &timestamppb.Timestamp{Nanos: -1}}}, nil
				}
			})
		}},
		{name: "change invalid item", method: "PATCH", path: "/v1/operations/" + idemKey, route: "/v1/operations/{id}", body: `{"category_id":"cat","occurred_at":"2026-01-01T00:00:00Z"}`, field: "Operation.kind", code: "unknown", setup: func(p *testpeer.Peers) {
			p.Operations.Set(func(o *testpeer.Operations) {
				o.ChangeCategoryFunc = func(context.Context, *statementdatav1.ChangeCategoryRequest) (*statementdatav1.ChangeCategoryResponse, error) {
					item := operationMessage()
					item.Kind = 99
					return &statementdatav1.ChangeCategoryResponse{Items: []*statementdatav1.Operation{item}}, nil
				}
			})
		}},
		{name: "category nil item on wire", method: "GET", path: "/v1/categories", route: "/v1/categories", field: "Category.id", code: "absent", setup: func(p *testpeer.Peers) {
			p.Categories.Set(func(o *testpeer.Categories) {
				o.ListCategoriesFunc = func(context.Context, *statementdatav1.ListCategoriesRequest) (*statementdatav1.ListCategoriesResponse, error) {
					return &statementdatav1.ListCategoriesResponse{Categories: []*statementdatav1.Category{nil}}, nil
				}
			})
		}},
	}
	for _, tc := range cases {
		tc.setup(b.peers)
		before := b.analyticsCounter(t, "finassist_untranslated_code_total", tc.field, tc.code)
		r := b.do(t, request{method: tc.method, path: tc.path, route: tc.route, body: tc.body, header: auth})
		if r.StatusCode != 500 || errorCode(r) != "INTERNAL_ERROR" || b.analyticsCounter(t, "finassist_untranslated_code_total", tc.field, tc.code) != before+1 {
			t.Fatal(tc.name, r.StatusCode, string(r.body), tc.field, tc.code)
		}
	}
}
