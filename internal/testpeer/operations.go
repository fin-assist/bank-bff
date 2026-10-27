package testpeer

import (
	"context"
	"sync"

	statementdatav1 "github.com/fin-assist/contracts/gen/go/finassist/statementdata/v1"
)

// Operations provides the contract service over real test gRPC transport.
type Operations struct {
	statementdatav1.UnimplementedOperationsServiceServer
	mu                   sync.Mutex
	ListFeedFunc         func(context.Context, *statementdatav1.ListFeedRequest) (*statementdatav1.ListFeedResponse, error)
	SearchOperationsFunc func(context.Context, *statementdatav1.SearchOperationsRequest) (*statementdatav1.SearchOperationsResponse, error)
	GetOperationFunc     func(context.Context, *statementdatav1.GetOperationRequest) (*statementdatav1.GetOperationResponse, error)
	ChangeCategoryFunc   func(context.Context, *statementdatav1.ChangeCategoryRequest) (*statementdatav1.ChangeCategoryResponse, error)
}

// ListFeed implements the RPC.
func (a *Operations) ListFeed(ctx context.Context, r *statementdatav1.ListFeedRequest) (*statementdatav1.ListFeedResponse, error) {
	if f := get(&a.mu, &a.ListFeedFunc); f != nil {
		return f(ctx, r)
	}
	return a.UnimplementedOperationsServiceServer.ListFeed(ctx, r)
}

// SearchOperations implements the RPC.
func (a *Operations) SearchOperations(ctx context.Context, r *statementdatav1.SearchOperationsRequest) (*statementdatav1.SearchOperationsResponse, error) {
	if f := get(&a.mu, &a.SearchOperationsFunc); f != nil {
		return f(ctx, r)
	}
	return a.UnimplementedOperationsServiceServer.SearchOperations(ctx, r)
}

// GetOperation implements the RPC.
func (a *Operations) GetOperation(ctx context.Context, r *statementdatav1.GetOperationRequest) (*statementdatav1.GetOperationResponse, error) {
	if f := get(&a.mu, &a.GetOperationFunc); f != nil {
		return f(ctx, r)
	}
	return a.UnimplementedOperationsServiceServer.GetOperation(ctx, r)
}

// ChangeCategory implements the RPC.
func (a *Operations) ChangeCategory(ctx context.Context, r *statementdatav1.ChangeCategoryRequest) (*statementdatav1.ChangeCategoryResponse, error) {
	if f := get(&a.mu, &a.ChangeCategoryFunc); f != nil {
		return f(ctx, r)
	}
	return a.UnimplementedOperationsServiceServer.ChangeCategory(ctx, r)
}

// Set safely replaces handlers between calls.
func (a *Operations) Set(apply func(*Operations)) { a.mu.Lock(); defer a.mu.Unlock(); apply(a) }

// Categories provides the contract service over real test gRPC transport.
type Categories struct {
	statementdatav1.UnimplementedCategoriesServiceServer
	mu                 sync.Mutex
	ListCategoriesFunc func(context.Context, *statementdatav1.ListCategoriesRequest) (*statementdatav1.ListCategoriesResponse, error)
}

// ListCategories implements the RPC.
func (a *Categories) ListCategories(ctx context.Context, r *statementdatav1.ListCategoriesRequest) (*statementdatav1.ListCategoriesResponse, error) {
	if f := get(&a.mu, &a.ListCategoriesFunc); f != nil {
		return f(ctx, r)
	}
	return a.UnimplementedCategoriesServiceServer.ListCategories(ctx, r)
}

// Set safely replaces handlers between calls.
func (a *Categories) Set(apply func(*Categories)) { a.mu.Lock(); defer a.mu.Unlock(); apply(a) }

// RegularPayments provides the contract service over real test gRPC transport.
type RegularPayments struct {
	statementdatav1.UnimplementedRegularPaymentsServiceServer
	mu                        sync.Mutex
	DismissRegularPaymentFunc func(context.Context, *statementdatav1.DismissRegularPaymentRequest) (*statementdatav1.DismissRegularPaymentResponse, error)
	RestoreRegularPaymentFunc func(context.Context, *statementdatav1.RestoreRegularPaymentRequest) (*statementdatav1.RestoreRegularPaymentResponse, error)
}

// DismissRegularPayment implements the RPC.
func (a *RegularPayments) DismissRegularPayment(ctx context.Context, r *statementdatav1.DismissRegularPaymentRequest) (*statementdatav1.DismissRegularPaymentResponse, error) {
	if f := get(&a.mu, &a.DismissRegularPaymentFunc); f != nil {
		return f(ctx, r)
	}
	return a.UnimplementedRegularPaymentsServiceServer.DismissRegularPayment(ctx, r)
}

// RestoreRegularPayment implements the RPC.
func (a *RegularPayments) RestoreRegularPayment(ctx context.Context, r *statementdatav1.RestoreRegularPaymentRequest) (*statementdatav1.RestoreRegularPaymentResponse, error) {
	if f := get(&a.mu, &a.RestoreRegularPaymentFunc); f != nil {
		return f(ctx, r)
	}
	return a.UnimplementedRegularPaymentsServiceServer.RestoreRegularPayment(ctx, r)
}

// Set safely replaces handlers between calls.
func (a *RegularPayments) Set(apply func(*RegularPayments)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	apply(a)
}
