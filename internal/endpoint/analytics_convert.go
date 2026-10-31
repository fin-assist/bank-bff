package endpoint

import (
	"regexp"

	analyticsv1 "github.com/fin-assist/contracts/gen/go/finassist/analytics/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
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
	Period    *string           `json:"period,omitempty"`
	Date      *string           `json:"date,omitempty"`
}

func (c converter) coverageCode(v commonv1.Coverage, field string) string {
	if value, ok := enumCode(v); ok {
		return value
	}
	c.metrics.Untranslated(field, "unknown")
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
		if key := a.PeriodKey; key != nil {
			if *key == "all_time" || analyticsKeyShape.MatchString(*key) {
				out.Action.Date = key
			} else {
				c.metrics.Untranslated("Block.action.period_key", "invalid")
			}
		}
		if a.Period != nil {
			if period, ok := enumCode(a.GetPeriod()); ok {
				out.Action.Period = &period
			} else {
				c.metrics.Untranslated("Block.action.period", "unknown")
			}
		}
	}
	return out
}

func (c converter) analytics(resp *analyticsv1.GetAnalyticsResponse) (analyticsResponse, bool) {
	out := analyticsResponse{HasData: resp.GetHasData()}
	if !out.HasData {
		return out, true
	}
	// grpc-api 5.3 counts each empty required field of a received response even
	// when another invalid field makes the final REST response fail closed.
	out.Currency = c.currency(resp.GetCurrency(), "GetAnalyticsResponse.currency")
	blocks := make([]analyticsBlock, 0, len(resp.GetBlocks()))
	for _, block := range resp.GetBlocks() {
		blocks = append(blocks, c.analyticsBlock(block))
	}
	out.Blocks = &blocks
	pms := resp.GetParams()
	if pms == nil {
		return c.analyticsInvalid("GetAnalyticsResponse.params", "absent")
	}
	if _, ok := enumCode(pms.GetPeriod()); !ok {
		return c.analyticsInvalid("GetAnalyticsResponse.params.period", "unknown")
	}
	if !c.analyticsPeriodKey(pms.GetPeriod(), pms.GetPeriodKey(), "GetAnalyticsResponse.params.period_key") {
		return analyticsResponse{}, false
	}
	out.Params, _ = analyticsParams(pms)
	if resp.GetPeriod() == nil {
		return c.analyticsInvalid("GetAnalyticsResponse.period", "absent")
	}
	if resp.GetState() == nil {
		return c.analyticsInvalid("GetAnalyticsResponse.state", "absent")
	}
	if !c.analyticsRange(resp.GetPeriod().GetRange(), "GetAnalyticsResponse.period.range") {
		return analyticsResponse{}, false
	}
	if resp.GetPeriod().GetBounds() == nil {
		return c.analyticsInvalid("GetAnalyticsResponse.period.bounds", "absent")
	}
	state := resp.GetState()
	for _, f := range []struct {
		value *timestamppb.Timestamp
		field string
	}{
		{state.GetDataFrom(), "GetAnalyticsResponse.state.data_from"},
		{state.GetDataTo(), "GetAnalyticsResponse.state.data_to"},
		{state.GetCalculatedAt(), "GetAnalyticsResponse.state.calculated_at"},
	} {
		if !c.analyticsMoment(f.value, f.field) {
			return analyticsResponse{}, false
		}
	}
	if !c.analyticsOptionals(resp) {
		return analyticsResponse{}, false
	}
	p := resp.GetPeriod()
	bounds := p.GetBounds()
	out.Period = &analyticsPeriod{Range: timeRange(p.GetRange(), c.loc), IsCurrent: p.GetIsCurrent(), Coverage: c.coverageCode(bounds.GetCoverage(), "GetAnalyticsResponse.period.bounds.coverage"), DataFrom: optionalMoment(bounds.GetDataFrom(), c.loc), DataTo: optionalMoment(bounds.GetDataTo(), c.loc), Gaps: []*Range{}}
	for _, gap := range bounds.GetGaps() {
		if !c.analyticsRange(gap, "GetAnalyticsResponse.period.bounds.gaps") {
			return analyticsResponse{}, false
		}
		out.Period.Gaps = append(out.Period.Gaps, timeRange(gap, c.loc))
	}
	n := resp.GetNavigation()
	out.Navigation = &analyticsNavigation{}
	if n != nil {
		for _, key := range []struct {
			value *string
			field string
		}{{n.Previous, "GetAnalyticsResponse.navigation.previous"}, {n.Next, "GetAnalyticsResponse.navigation.next"}} {
			if !c.analyticsNavigationKey(key.value, key.field) {
				return analyticsResponse{}, false
			}
		}
		out.Navigation.Previous, out.Navigation.Next = n.Previous, n.Next
	}
	s := resp.GetState()
	out.State = &analyticsState{Stale: s.GetStale(), LastOperationAt: optionalMoment(s.GetLastOperationAt(), c.loc), DataFrom: moment(s.GetDataFrom(), c.loc), DataTo: moment(s.GetDataTo(), c.loc), FullMonths: s.GetFullMonths(), UnreadLinesCount: s.GetUnreadLinesCount(), CalculatedAt: moment(s.GetCalculatedAt(), c.loc), Recalculating: s.GetRecalculating()}
	return out, true
}

func (c converter) analyticsInvalid(field, code string) (analyticsResponse, bool) {
	c.metrics.Untranslated(field, code)
	return analyticsResponse{}, false
}

// analyticsMoment rejects absent or invalid required instants instead of inventing
// Unix epoch through Timestamp.AsTime. Field labels come only from call-site constants.
func (c converter) analyticsMoment(value *timestamppb.Timestamp, field string) bool {
	if value == nil {
		c.metrics.Untranslated(field, "absent")
		return false
	}
	if value.CheckValid() != nil {
		c.metrics.Untranslated(field, "invalid")
		return false
	}
	// A valid UTC Timestamp at a calendar edge can leave RFC3339's four-digit
	// year range after formatting in the response zone. Refuse that projection too.
	local := value.AsTime()
	if c.loc != nil {
		local = local.In(c.loc)
	}
	if local.Year() < 0 || local.Year() > 9999 {
		c.metrics.Untranslated(field, "invalid")
		return false
	}
	return true
}

func (c converter) analyticsRange(value *commonv1.TimeRange, field string) bool {
	if value == nil {
		c.metrics.Untranslated(field, "absent")
		return false
	}
	return c.analyticsMoment(value.GetStart(), field+".start") && c.analyticsMoment(value.GetEnd(), field+".end")
}

// analyticsKeyShape is PeriodKey from the embedded OpenAPI. These are calendar
// labels, never instants; validating syntax does not convert or choose a period.
var analyticsKeyShape = regexp.MustCompile(`^[0-9]{4}(-(0[1-9]|1[0-2])|-Q[1-4])?$`)
var analyticsMonthKey = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)
var analyticsQuarterKey = regexp.MustCompile(`^[0-9]{4}-Q[1-4]$`)
var analyticsYearKey = regexp.MustCompile(`^[0-9]{4}$`)

func analyticsKeyMatches(period commonv1.PeriodType, value string) bool {
	switch period {
	case commonv1.PeriodType_PERIOD_TYPE_MONTH:
		return analyticsMonthKey.MatchString(value)
	case commonv1.PeriodType_PERIOD_TYPE_QUARTER:
		return analyticsQuarterKey.MatchString(value)
	case commonv1.PeriodType_PERIOD_TYPE_YEAR:
		return analyticsYearKey.MatchString(value)
	case commonv1.PeriodType_PERIOD_TYPE_ALL_TIME:
		return value == "all_time"
	default:
		return false
	}
}

func (c converter) analyticsPeriodKey(period commonv1.PeriodType, value, field string) bool {
	if value == "" {
		c.metrics.Untranslated(field, "absent")
		return false
	}
	if !analyticsKeyMatches(period, value) {
		c.metrics.Untranslated(field, "invalid")
		return false
	}
	return true
}

// analyticsNavigationKey distinguishes absent optional navigation (nil) from
// a present invalid calendar label, including the empty string.
func (c converter) analyticsNavigationKey(value *string, field string) bool {
	if value == nil {
		return true
	}
	if !analyticsKeyShape.MatchString(*value) {
		c.metrics.Untranslated(field, "invalid")
		return false
	}
	return true
}

func (c converter) analyticsOptionalMoment(value *timestamppb.Timestamp, field string) bool {
	return value == nil || c.analyticsMoment(value, field)
}

// analyticsOptionals validates only typed Timestamp fields owned by this new
// projection. Opaque Struct data and the shared assistant/import converters stay untouched.
func (c converter) analyticsOptionals(resp *analyticsv1.GetAnalyticsResponse) bool {
	for _, f := range []struct {
		value *timestamppb.Timestamp
		field string
	}{
		{resp.GetPeriod().GetBounds().GetDataFrom(), "GetAnalyticsResponse.period.bounds.data_from"},
		{resp.GetPeriod().GetBounds().GetDataTo(), "GetAnalyticsResponse.period.bounds.data_to"},
		{resp.GetState().GetLastOperationAt(), "GetAnalyticsResponse.state.last_operation_at"},
	} {
		if !c.analyticsOptionalMoment(f.value, f.field) {
			return false
		}
	}
	for _, block := range resp.GetBlocks() {
		for _, f := range []struct {
			value *timestamppb.Timestamp
			field string
		}{
			{block.GetLock().GetAvailableFrom(), "GetAnalyticsResponse.blocks.lock.available_from"},
			{block.GetAction().GetFilter().GetFrom(), "GetAnalyticsResponse.blocks.action.filter.from"},
			{block.GetAction().GetFilter().GetTo(), "GetAnalyticsResponse.blocks.action.filter.to"},
		} {
			if !c.analyticsOptionalMoment(f.value, f.field) {
				return false
			}
		}
	}
	return true
}
