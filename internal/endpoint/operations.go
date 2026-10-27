package endpoint

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	statementdatav1 "github.com/fin-assist/contracts/gen/go/finassist/statementdata/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/fin-assist/bank-bff/internal/rest"
)

var operationFilterFields = [...]string{"from", "to", "all_time", "q", "category_id", "kind", "amount_from", "amount_to", "transfer_mode", "selection"}

func requestMoment(c *rest.Call, value, field string) (*timestamppb.Timestamp, bool) {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		queryInvalid(c, field)
		return nil, false
	}
	ts := timestamppb.New(t)
	if ts.CheckValid() != nil {
		queryInvalid(c, field)
		return nil, false
	}
	return ts, true
}

func (d Deps) listOperations(c *rest.Call) {
	q := c.R.URL.Query()
	search := false
	for _, field := range operationFilterFields {
		search = search || q.Has(field)
	}
	if search {
		if q.Has("before") {
			queryInvalid(c, "before")
			return
		}
		d.searchOperations(c, q)
		return
	}
	req := &statementdatav1.ListFeedRequest{}
	if q.Has("before") {
		var ok bool
		req.Before, ok = requestMoment(c, q.Get("before"), "before")
		if !ok {
			return
		}
	}
	ctx, cancel := unary(c)
	defer cancel()
	in, err := d.Operations.ListFeed(ctx, req)
	if err != nil {
		c.Fail(err)
		return
	}
	conv := converter{loc: zone(in.GetTimeZone()), metrics: c.Metrics}
	out, ok := conv.operationFeed(in)
	if !ok {
		rest.Internal().Write(c.W)
		return
	}
	rest.WriteJSON(c.W, http.StatusOK, out)
}

func (d Deps) searchOperations(c *rest.Call, q url.Values) {
	if q.Has("all_time") && (q.Has("from") || q.Has("to")) {
		queryInvalid(c, "all_time")
		return
	}
	f := &commonv1.OperationsFilter{}
	req := &statementdatav1.SearchOperationsRequest{Filter: f}
	for _, field := range []string{"from", "to"} {
		if !q.Has(field) {
			continue
		}
		ts, ok := requestMoment(c, q.Get(field), field)
		if !ok {
			return
		}
		if field == "from" {
			f.From = ts
		} else {
			f.To = ts
		}
	}
	if q.Has("all_time") {
		value, err := strconv.ParseBool(q.Get("all_time"))
		if err != nil {
			queryInvalid(c, "all_time")
			return
		}
		req.AllTime = value
	}
	for _, field := range []struct {
		key    string
		target **string
	}{{"q", &f.Q}, {"category_id", &f.CategoryId}, {"selection", &f.Selection}} {
		if q.Has(field.key) {
			value := q.Get(field.key)
			*field.target = &value
		}
	}
	for _, field := range []struct {
		key    string
		target **int64
	}{{"amount_from", &f.AmountFromMinor}, {"amount_to", &f.AmountToMinor}} {
		if !q.Has(field.key) {
			continue
		}
		value, err := strconv.ParseInt(q.Get(field.key), 10, 64)
		if err != nil || value < 0 {
			queryInvalid(c, field.key)
			return
		}
		*field.target = &value
	}
	if q.Has("kind") {
		switch q.Get("kind") {
		case "expense":
			f.Kind = commonv1.KindFilter_KIND_FILTER_EXPENSE
		case "income":
			f.Kind = commonv1.KindFilter_KIND_FILTER_INCOME
		default:
			queryInvalid(c, "kind")
			return
		}
	}
	if q.Has("transfer_mode") {
		switch q.Get("transfer_mode") {
		case "with":
			f.TransferMode = commonv1.TransferMode_TRANSFER_MODE_WITH
		case "without":
			f.TransferMode = commonv1.TransferMode_TRANSFER_MODE_WITHOUT
		default:
			queryInvalid(c, "transfer_mode")
			return
		}
	}
	ctx, cancel := context.WithTimeout(c.R.Context(), SearchDeadline)
	defer cancel()
	in, err := d.Operations.SearchOperations(ctx, req, grpc.MaxCallRecvMsgSize(SearchMessageLimit))
	if err != nil {
		c.Fail(err)
		return
	}
	conv := converter{loc: zone(in.GetTimeZone()), metrics: c.Metrics}
	out, ok := conv.operationSearch(in)
	if !ok {
		rest.Internal().Write(c.W)
		return
	}
	rest.WriteJSON(c.W, http.StatusOK, out)
}

func (d Deps) getOperation(c *rest.Call) {
	ts, ok := requestMoment(c, c.R.URL.Query().Get("occurred_at"), "occurred_at")
	if !ok {
		return
	}
	ctx, cancel := unary(c)
	defer cancel()
	in, err := d.Operations.GetOperation(ctx, &statementdatav1.GetOperationRequest{Id: c.R.PathValue("id"), OccurredAt: ts})
	if err != nil {
		c.Fail(err)
		return
	}
	conv := converter{loc: zone(in.GetTimeZone()), metrics: c.Metrics}
	out, ok := conv.operation(in.GetOperation(), true)
	if !ok {
		rest.Internal().Write(c.W)
		return
	}
	rest.WriteJSON(c.W, http.StatusOK, out)
}

func (d Deps) changeCategory(c *rest.Call) {
	var body struct {
		CategoryID string `json:"category_id"`
		OccurredAt string `json:"occurred_at"`
	}
	if !decode(c, &body) {
		return
	}
	ts, ok := requestMoment(c, body.OccurredAt, "occurred_at")
	if !ok {
		return
	}
	ctx, cancel := unary(c)
	defer cancel()
	in, err := d.Operations.ChangeCategory(ctx, &statementdatav1.ChangeCategoryRequest{Id: c.R.PathValue("id"), CategoryId: body.CategoryID, OccurredAt: ts})
	if err != nil {
		c.Fail(err)
		return
	}
	conv := converter{loc: zone(in.GetTimeZone()), metrics: c.Metrics}
	if in == nil {
		c.Metrics.Untranslated("ChangeCategoryResponse", "absent")
		rest.Internal().Write(c.W)
		return
	}
	items, ok := conv.operationItems(in.GetItems(), true)
	if !ok {
		rest.Internal().Write(c.W)
		return
	}
	rest.WriteJSON(c.W, http.StatusOK, struct {
		Items      []operationJSON `json:"items"`
		ReplacedID *string         `json:"replaced_id,omitempty"`
	}{items, in.ReplacedId})
}

func (d Deps) listCategories(c *rest.Call) {
	ctx, cancel := unary(c)
	defer cancel()
	in, err := d.Categories.ListCategories(ctx, &statementdatav1.ListCategoriesRequest{})
	if err != nil {
		c.Fail(err)
		return
	}
	items := make([]categoryJSON, 0, len(in.GetCategories()))
	for _, item := range in.GetCategories() {
		if item == nil {
			c.Metrics.Untranslated("Category", "absent")
			rest.Internal().Write(c.W)
			return
		}
		kinds := make([]string, 0, len(item.GetKinds()))
		for _, kind := range item.GetKinds() {
			if code, ok := enumCode(kind); ok {
				kinds = append(kinds, code)
			} else {
				c.Metrics.Untranslated("Category.kinds", "unknown")
			}
		}
		items = append(items, categoryJSON{ID: item.GetId(), Name: item.GetName(), Icon: item.GetIcon(), IsSystem: item.GetIsSystem(), Kinds: kinds, Assignable: item.GetAssignable(), Note: item.Note})
	}
	rest.WriteJSON(c.W, http.StatusOK, struct {
		Categories []categoryJSON `json:"categories"`
	}{items})
}

func (d Deps) dismissRegularPayment(c *rest.Call) {
	ctx, cancel := unary(c)
	defer cancel()
	_, err := d.RegularPayments.DismissRegularPayment(ctx, &statementdatav1.DismissRegularPaymentRequest{RegularPaymentId: c.R.PathValue("id")})
	if err != nil {
		c.Fail(err)
		return
	}
	c.W.WriteHeader(http.StatusNoContent)
}

func (d Deps) restoreRegularPayment(c *rest.Call) {
	ctx, cancel := unary(c)
	defer cancel()
	_, err := d.RegularPayments.RestoreRegularPayment(ctx, &statementdatav1.RestoreRegularPaymentRequest{RegularPaymentId: c.R.PathValue("id")})
	if err != nil {
		c.Fail(err)
		return
	}
	c.W.WriteHeader(http.StatusNoContent)
}
