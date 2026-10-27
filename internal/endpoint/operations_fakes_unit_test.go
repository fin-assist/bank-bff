package endpoint

import (
	"context"

	statementdatav1 "github.com/fin-assist/contracts/gen/go/finassist/statementdata/v1"
	"google.golang.org/grpc"
)

type operationFakes struct {
	f                     *fakes
	listFeed              func(*statementdatav1.ListFeedRequest) (*statementdatav1.ListFeedResponse, error)
	searchOperations      func(*statementdatav1.SearchOperationsRequest) (*statementdatav1.SearchOperationsResponse, error)
	getOperation          func(*statementdatav1.GetOperationRequest) (*statementdatav1.GetOperationResponse, error)
	changeCategory        func(*statementdatav1.ChangeCategoryRequest) (*statementdatav1.ChangeCategoryResponse, error)
	listCategories        func(*statementdatav1.ListCategoriesRequest) (*statementdatav1.ListCategoriesResponse, error)
	dismissRegularPayment func(*statementdatav1.DismissRegularPaymentRequest) (*statementdatav1.DismissRegularPaymentResponse, error)
	restoreRegularPayment func(*statementdatav1.RestoreRegularPaymentRequest) (*statementdatav1.RestoreRegularPaymentResponse, error)
}

func (a *operationFakes) ListFeed(ctx context.Context, r *statementdatav1.ListFeedRequest, opts ...grpc.CallOption) (*statementdatav1.ListFeedResponse, error) {
	a.f.deadline(ctx)
	return must(a.f.t, a.listFeed, "ListFeed")(r)
}
func (a *operationFakes) SearchOperations(ctx context.Context, r *statementdatav1.SearchOperationsRequest, opts ...grpc.CallOption) (*statementdatav1.SearchOperationsResponse, error) {
	a.f.deadline(ctx)
	return must(a.f.t, a.searchOperations, "SearchOperations")(r)
}
func (a *operationFakes) GetOperation(ctx context.Context, r *statementdatav1.GetOperationRequest, opts ...grpc.CallOption) (*statementdatav1.GetOperationResponse, error) {
	a.f.deadline(ctx)
	return must(a.f.t, a.getOperation, "GetOperation")(r)
}
func (a *operationFakes) ChangeCategory(ctx context.Context, r *statementdatav1.ChangeCategoryRequest, opts ...grpc.CallOption) (*statementdatav1.ChangeCategoryResponse, error) {
	a.f.deadline(ctx)
	return must(a.f.t, a.changeCategory, "ChangeCategory")(r)
}
func (a *operationFakes) ListCategories(ctx context.Context, r *statementdatav1.ListCategoriesRequest, opts ...grpc.CallOption) (*statementdatav1.ListCategoriesResponse, error) {
	a.f.deadline(ctx)
	return must(a.f.t, a.listCategories, "ListCategories")(r)
}
func (a *operationFakes) DismissRegularPayment(ctx context.Context, r *statementdatav1.DismissRegularPaymentRequest, opts ...grpc.CallOption) (*statementdatav1.DismissRegularPaymentResponse, error) {
	a.f.deadline(ctx)
	return must(a.f.t, a.dismissRegularPayment, "DismissRegularPayment")(r)
}
func (a *operationFakes) RestoreRegularPayment(ctx context.Context, r *statementdatav1.RestoreRegularPaymentRequest, opts ...grpc.CallOption) (*statementdatav1.RestoreRegularPaymentResponse, error) {
	a.f.deadline(ctx)
	return must(a.f.t, a.restoreRegularPayment, "RestoreRegularPayment")(r)
}
