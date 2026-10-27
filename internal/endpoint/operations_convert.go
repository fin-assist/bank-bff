package endpoint

import (
	"time"

	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	statementdatav1 "github.com/fin-assist/contracts/gen/go/finassist/statementdata/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type operationAccount struct {
	TypeName string  `json:"type_name"`
	Mask     *string `json:"mask,omitempty"`
	Spoken   *string `json:"spoken,omitempty"`
}
type operationJSON struct {
	ID               string            `json:"id"`
	Kind             string            `json:"kind"`
	OccurredAt       string            `json:"occurred_at"`
	Amount           int64             `json:"amount"`
	Currency         string            `json:"currency"`
	Title            string            `json:"title"`
	CategoryID       string            `json:"category_id"`
	CategoryName     string            `json:"category_name"`
	CategoryIcon     string            `json:"category_icon"`
	IsCategoryManual bool              `json:"is_category_manual"`
	Note             *string           `json:"note,omitempty"`
	Status           string            `json:"status"`
	Account          *operationAccount `json:"account,omitempty"`
	FromAccount      *operationAccount `json:"from_account,omitempty"`
	ToAccount        *operationAccount `json:"to_account,omitempty"`
	Description      *string           `json:"description,omitempty"`
	PostedAt         *string           `json:"posted_at,omitempty"`
	NoteDetails      *string           `json:"note_details,omitempty"`
	BankCategory     *string           `json:"bank_category,omitempty"`
	MCC              *string           `json:"mcc,omitempty"`
	SourceName       *string           `json:"source_name,omitempty"`
}
type categoryJSON struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Icon       string   `json:"icon"`
	IsSystem   bool     `json:"is_system"`
	Kinds      []string `json:"kinds"`
	Assignable bool     `json:"assignable"`
	Note       *string  `json:"note,omitempty"`
}
type monthSummaryJSON struct {
	Month   string  `json:"month"`
	Range   *Range  `json:"range"`
	Expense int64   `json:"expense"`
	Income  int64   `json:"income"`
	DataTo  *string `json:"data_to,omitempty"`
}
type feedStateJSON struct {
	Stale           bool    `json:"stale"`
	DataTo          *string `json:"data_to,omitempty"`
	LastOperationAt *string `json:"last_operation_at,omitempty"`
	LastUploadedAt  *string `json:"last_uploaded_at,omitempty"`
}
type feedJSON struct {
	Items      []operationJSON    `json:"items"`
	Summary    []monthSummaryJSON `json:"summary"`
	State      *feedStateJSON     `json:"state"`
	Range      *Range             `json:"range"`
	NextBefore *string            `json:"next_before,omitempty"`
}
type searchJSON struct {
	Items        []operationJSON `json:"items"`
	TotalCount   int32           `json:"total_count"`
	Range        *Range          `json:"range,omitempty"`
	HasOlderData *bool           `json:"has_older_data,omitempty"`
}

func accountJSON(a *commonv1.AccountRef, details bool) *operationAccount {
	if a == nil {
		return nil
	}
	out := &operationAccount{TypeName: a.GetTypeName(), Mask: a.Mask}
	if details {
		spoken := a.GetSpoken()
		out.Spoken = &spoken
	}
	return out
}
func (c converter) operation(in *statementdatav1.Operation, details bool) (operationJSON, bool) {
	if in == nil {
		c.metrics.Untranslated("Operation", "absent")
		return operationJSON{}, false
	}
	kind, okKind := enumCode(in.GetKind())
	status, okStatus := enumCode(in.GetStatus())
	if !okKind {
		c.metrics.Untranslated("Operation.kind", "unknown")
	}
	if !okStatus {
		c.metrics.Untranslated("Operation.status", "unknown")
	}
	if !okKind || !okStatus || !c.analyticsMoment(in.GetOccurredAt(), "Operation.occurred_at") {
		return operationJSON{}, false
	}
	if in.GetCurrency() == "" {
		c.metrics.Untranslated("Operation.currency", "absent")
		return operationJSON{}, false
	}
	if in.GetAmountMinor() < 0 {
		c.metrics.Untranslated("Operation.amount", "invalid")
		return operationJSON{}, false
	}
	out := operationJSON{ID: in.GetId(), Kind: kind, Status: status, OccurredAt: moment(in.GetOccurredAt(), c.loc), Amount: in.GetAmountMinor(), Currency: in.GetCurrency(), Title: in.GetTitle(), CategoryID: in.GetCategory().GetId(), CategoryName: in.GetCategory().GetName(), CategoryIcon: in.GetCategory().GetIcon(), IsCategoryManual: in.GetIsCategoryManual(), Note: in.Note, Account: accountJSON(in.GetAccount(), details), FromAccount: accountJSON(in.GetFromAccount(), details), ToAccount: accountJSON(in.GetToAccount(), details)}
	if details {
		if !c.analyticsOptionalMoment(in.PostedAt, "Operation.posted_at") {
			return operationJSON{}, false
		}
		out.Description = in.Description
		out.PostedAt = optionalMoment(in.PostedAt, c.loc)
		out.NoteDetails = in.NoteDetails
		out.BankCategory = in.BankCategory
		out.MCC = in.Mcc
		source := in.GetSourceName()
		out.SourceName = &source
	}
	return out, true
}
func (c converter) operationItems(in []*statementdatav1.Operation, details bool) ([]operationJSON, bool) {
	out := make([]operationJSON, 0, len(in))
	for _, item := range in {
		converted, ok := c.operation(item, details)
		if !ok {
			return nil, false
		}
		out = append(out, converted)
	}
	return out, true
}
func (c converter) operationFeed(in *statementdatav1.ListFeedResponse) (feedJSON, bool) {
	items, ok := c.operationItems(in.GetItems(), false)
	if !ok {
		return feedJSON{}, false
	}
	if in.GetState() == nil {
		c.metrics.Untranslated("ListFeedResponse.state", "absent")
		return feedJSON{}, false
	}
	if !c.analyticsRange(in.GetRange(), "ListFeedResponse.range") {
		return feedJSON{}, false
	}
	state := in.GetState()
	for _, field := range []struct {
		value *timestamppb.Timestamp
		name  string
	}{{in.NextBefore, "ListFeedResponse.next_before"}, {state.DataTo, "FeedState.data_to"}, {state.LastOperationAt, "FeedState.last_operation_at"}, {state.LastUploadedAt, "FeedState.last_uploaded_at"}} {
		if !c.analyticsOptionalMoment(field.value, field.name) {
			return feedJSON{}, false
		}
	}
	out := feedJSON{Items: items, Summary: make([]monthSummaryJSON, 0, len(in.GetSummary())), Range: timeRange(in.GetRange(), c.loc), NextBefore: optionalMoment(in.NextBefore, c.loc), State: &feedStateJSON{Stale: state.GetStale(), DataTo: optionalMoment(state.DataTo, c.loc), LastOperationAt: optionalMoment(state.LastOperationAt, c.loc), LastUploadedAt: optionalMoment(state.LastUploadedAt, c.loc)}}
	for _, s := range in.GetSummary() {
		// Month is a calendar key supplied by the service, not a date to convert.
		if _, err := time.Parse("2006-01", s.GetMonth()); err != nil {
			c.metrics.Untranslated("MonthSummary.month", "invalid")
			return feedJSON{}, false
		}
		if !c.analyticsRange(s.GetRange(), "MonthSummary.range") || !c.analyticsOptionalMoment(s.DataTo, "MonthSummary.data_to") {
			return feedJSON{}, false
		}
		out.Summary = append(out.Summary, monthSummaryJSON{Month: s.GetMonth(), Range: timeRange(s.GetRange(), c.loc), Expense: s.GetExpenseMinor(), Income: s.GetIncomeMinor(), DataTo: optionalMoment(s.DataTo, c.loc)})
	}
	return out, true
}
func (c converter) operationSearch(in *statementdatav1.SearchOperationsResponse) (searchJSON, bool) {
	if in == nil {
		c.metrics.Untranslated("SearchOperationsResponse", "absent")
		return searchJSON{}, false
	}
	items, ok := c.operationItems(in.GetItems(), false)
	if !ok {
		return searchJSON{}, false
	}
	if in.Range != nil && !c.analyticsRange(in.Range, "SearchOperationsResponse.range") {
		return searchJSON{}, false
	}
	return searchJSON{Items: items, TotalCount: in.GetTotalCount(), Range: timeRange(in.Range, c.loc), HasOlderData: in.HasOlderData}, true
}
