package testpeer

import (
	"context"
	"testing"

	statementdatav1 "github.com/fin-assist/contracts/gen/go/finassist/statementdata/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestUnitOperationFakesUnimplemented(t *testing.T) {
	o, c, p := &Operations{}, &Categories{}, &RegularPayments{}
	ctx := context.Background()
	checks := []func() error{
		func() error { _, e := o.ListFeed(ctx, nil); return e },
		func() error { _, e := o.SearchOperations(ctx, nil); return e },
		func() error { _, e := o.GetOperation(ctx, nil); return e },
		func() error { _, e := o.ChangeCategory(ctx, nil); return e },
		func() error { _, e := c.ListCategories(ctx, nil); return e },
		func() error { _, e := p.DismissRegularPayment(ctx, nil); return e },
		func() error { _, e := p.RestoreRegularPayment(ctx, nil); return e },
	}
	for _, check := range checks {
		if e := check(); status.Code(e) != codes.Unimplemented {
			t.Fatal(e)
		}
	}
	count := 0
	o.Set(func(o *Operations) {
		o.ListFeedFunc = func(context.Context, *statementdatav1.ListFeedRequest) (*statementdatav1.ListFeedResponse, error) {
			count++
			return nil, nil
		}
		o.SearchOperationsFunc = func(context.Context, *statementdatav1.SearchOperationsRequest) (*statementdatav1.SearchOperationsResponse, error) {
			count++
			return nil, nil
		}
		o.GetOperationFunc = func(context.Context, *statementdatav1.GetOperationRequest) (*statementdatav1.GetOperationResponse, error) {
			count++
			return nil, nil
		}
		o.ChangeCategoryFunc = func(context.Context, *statementdatav1.ChangeCategoryRequest) (*statementdatav1.ChangeCategoryResponse, error) {
			count++
			return nil, nil
		}
	})
	c.Set(func(c *Categories) {
		c.ListCategoriesFunc = func(context.Context, *statementdatav1.ListCategoriesRequest) (*statementdatav1.ListCategoriesResponse, error) {
			count++
			return nil, nil
		}
	})
	p.Set(func(p *RegularPayments) {
		p.DismissRegularPaymentFunc = func(context.Context, *statementdatav1.DismissRegularPaymentRequest) (*statementdatav1.DismissRegularPaymentResponse, error) {
			count++
			return nil, nil
		}
		p.RestoreRegularPaymentFunc = func(context.Context, *statementdatav1.RestoreRegularPaymentRequest) (*statementdatav1.RestoreRegularPaymentResponse, error) {
			count++
			return nil, nil
		}
	})
	for _, check := range checks {
		if e := check(); e != nil {
			t.Fatal(e)
		}
	}
	if count != 7 {
		t.Fatal(count)
	}
}
