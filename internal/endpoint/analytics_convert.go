package endpoint

import (
	analyticsv1 "github.com/fin-assist/contracts/gen/go/finassist/analytics/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
)

type analyticsResponse struct {
	HasData    bool                 `json:"has_data"`
	Params     *AnalyticsParams     `json:"params,omitempty"`
	Currency   string               `json:"currency,omitempty"`
	Period     *analyticsPeriod     `json:"period,omitempty"`
	Navigation *analyticsNavigation `json:"navigation,omitempty"`
	Blocks     *[]analyticsBlock    `json:"blocks,omitempty"`
	State      *analyticsState      `json:"state,omitempty"`
}

type analyticsPeriod struct {
	Range     *Range   `json:"range"`
	IsCurrent bool     `json:"is_current"`
	Coverage  string   `json:"coverage"`
	DataFrom  *string  `json:"data_from,omitempty"`
	DataTo    *string  `json:"data_to,omitempty"`
	Gaps      []*Range `json:"gaps"`
}

type analyticsNavigation struct {
	Previous *string `json:"previous"`
	Next     *string `json:"next"`
}

type analyticsState struct {
	Stale            bool    `json:"stale"`
	LastOperationAt  *string `json:"last_operation_at,omitempty"`
	DataFrom         string  `json:"data_from"`
	DataTo           string  `json:"data_to"`
	FullMonths       int32   `json:"full_months"`
	UnreadLinesCount int32   `json:"unread_lines_count"`
	CalculatedAt     string  `json:"calculated_at"`
	Recalculating    bool    `json:"recalculating"`
}

type analyticsPeriodItem struct {
	Key      string `json:"key"`
	Range    *Range `json:"range"`
	Coverage string `json:"coverage"`
}

// analyticsBlock keeps the common catalog open: data and string codes pass
// through without inspecting nested primitives, locks or actions (grpc-api 5.3).
type analyticsBlock struct {
	ID        string           `json:"id,omitempty"`
	Type      string           `json:"type"`
	V         int32            `json:"v"`
	Status    string           `json:"status,omitempty"`
	Lock      *analyticsLock   `json:"lock,omitempty"`
	Title     *string          `json:"title,omitempty"`
	AltText   *string          `json:"alt_text,omitempty"`
	EmptyText *string          `json:"empty_text,omitempty"`
	Data      any              `json:"data,omitempty"`
	Action    *analyticsAction `json:"action,omitempty"`
}

type analyticsLock struct {
	Reason        string  `json:"reason"`
	Text          string  `json:"text"`
	Required      *int32  `json:"required,omitempty"`
	Available     *int32  `json:"available,omitempty"`
	AvailableFrom *string `json:"available_from,omitempty"`
}

type analyticsAction struct {
	Type      string            `json:"type"`
	Filter    *OperationsFilter `json:"filter,omitempty"`
	PaymentID *string           `json:"payment_id,omitempty"`
	Label     *string           `json:"label,omitempty"`
}

func coverageCode(v commonv1.Coverage) string {
	if value, ok := enumCode(v); ok {
		return value
	}
	return "partial"
}

func (c converter) analyticsBlock(b *commonv1.Block) analyticsBlock {
	if b == nil {
		return analyticsBlock{}
	}
	out := analyticsBlock{ID: b.GetId(), Type: b.GetType(), V: b.GetV(), Status: b.GetStatus(), Title: b.Title, AltText: b.AltText, EmptyText: b.EmptyText}
	if b.Data != nil {
		out.Data = b.Data.AsMap()
	}
	if l := b.GetLock(); l != nil {
		text := l.GetText()
		if text == "" {
			text = "Пока недоступно"
			c.metrics.EmptyRequired("Block.lock.text")
		}
		out.Lock = &analyticsLock{Reason: l.GetReason(), Text: text, Required: l.Required, Available: l.Available, AvailableFrom: optionalMoment(l.AvailableFrom, c.loc)}
	}
	if a := b.GetAction(); a != nil {
		out.Action = &analyticsAction{Type: a.GetType(), Filter: operationsFilter(a.Filter, c.loc), PaymentID: a.PaymentId, Label: a.Label}
	}
	return out
}

func (c converter) analytics(resp *analyticsv1.GetAnalyticsResponse) (analyticsResponse, bool) {
	out := analyticsResponse{HasData: resp.GetHasData()}
	if !out.HasData {
		return out, true
	}
	params, ok := analyticsParams(resp.GetParams())
	if !ok {
		c.metrics.Untranslated("GetAnalyticsResponse.params.period", "unknown")
		return analyticsResponse{}, false
	}
	out.Params = params
	out.Currency = c.currency(resp.GetCurrency(), "GetAnalyticsResponse.currency")
	if resp.GetPeriod() == nil || resp.GetState() == nil || resp.GetPeriod().GetRange() == nil {
		return analyticsResponse{}, false
	}
	p := resp.GetPeriod()
	bounds := p.GetBounds()
	out.Period = &analyticsPeriod{Range: timeRange(p.GetRange(), c.loc), IsCurrent: p.GetIsCurrent(), Coverage: coverageCode(bounds.GetCoverage()), DataFrom: optionalMoment(bounds.GetDataFrom(), c.loc), DataTo: optionalMoment(bounds.GetDataTo(), c.loc), Gaps: []*Range{}}
	for _, gap := range bounds.GetGaps() {
		out.Period.Gaps = append(out.Period.Gaps, timeRange(gap, c.loc))
	}
	n := resp.GetNavigation()
	out.Navigation = &analyticsNavigation{}
	if n != nil {
		out.Navigation.Previous, out.Navigation.Next = n.Previous, n.Next
	}
	blocks := make([]analyticsBlock, 0, len(resp.GetBlocks()))
	for _, block := range resp.GetBlocks() {
		blocks = append(blocks, c.analyticsBlock(block))
	}
	out.Blocks = &blocks
	s := resp.GetState()
	out.State = &analyticsState{Stale: s.GetStale(), LastOperationAt: optionalMoment(s.GetLastOperationAt(), c.loc), DataFrom: moment(s.GetDataFrom(), c.loc), DataTo: moment(s.GetDataTo(), c.loc), FullMonths: s.GetFullMonths(), UnreadLinesCount: s.GetUnreadLinesCount(), CalculatedAt: moment(s.GetCalculatedAt(), c.loc), Recalculating: s.GetRecalculating()}
	return out, true
}
