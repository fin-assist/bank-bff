package endpoint

import (
	"strings"
	"sync"
	"time"
	"unicode"

	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// defaultZone formats moments of responses without time_zone (grpc-api.md 8) and
// replaces an empty or unknown zone; the contract examples use Moscow.
const defaultZone = "Europe/Moscow"

var zones sync.Map // name -> *time.Location

// zone returns the IANA location of a response, Moscow when name is empty or unknown.
func zone(name string) *time.Location {
	if name == "" {
		name = defaultZone
	}
	if loc, ok := zones.Load(name); ok {
		return loc.(*time.Location)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		if name == defaultZone {
			return time.FixedZone("MSK", 3*60*60)
		}
		return zone(defaultZone)
	}
	zones.Store(name, loc)
	return loc
}

// moment formats a Timestamp in loc as RFC 3339 with an offset; fractions are kept
// when present so a value read back is exact.
func moment(ts *timestamppb.Timestamp, loc *time.Location) string {
	return ts.AsTime().In(loc).Format(time.RFC3339Nano)
}

// optionalMoment is moment for an optional field: nil stays absent.
func optionalMoment(ts *timestamppb.Timestamp, loc *time.Location) *string {
	if ts == nil {
		return nil
	}
	s := moment(ts, loc)
	return &s
}

// enumName returns the value name without the type prefix (TRANSFER_MODE_WITH ->
// WITH), mechanically by descriptor (grpc-api.md 8). The zero value and numbers
// unknown to this build give false: the field is not output.
func enumName(e protoreflect.Enum) (string, bool) {
	if e.Number() == 0 {
		return "", false
	}
	v := e.Descriptor().Values().ByNumber(e.Number())
	if v == nil {
		return "", false
	}
	prefix := upperSnake(string(e.Descriptor().Name())) + "_"
	name := string(v.Name())
	if !strings.HasPrefix(name, prefix) {
		return "", false
	}
	return strings.TrimPrefix(name, prefix), true
}

// enumCode is enumName in the lower case of REST codes (with, verified, ...).
func enumCode(e protoreflect.Enum) (string, bool) {
	name, ok := enumName(e)
	return strings.ToLower(name), ok
}

func upperSnake(camel string) string {
	var b strings.Builder
	for i, r := range camel {
		if unicode.IsUpper(r) && i > 0 {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

// Range is the REST half-open range.
type Range struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func timeRange(r *commonv1.TimeRange, loc *time.Location) *Range {
	if r == nil {
		return nil
	}
	return &Range{From: moment(r.GetStart(), loc), To: moment(r.GetEnd(), loc)}
}

// OperationsFilter is operations_filter (docs/api.md, «Общие объекты»).
type OperationsFilter struct {
	From          *string `json:"from,omitempty"`
	To            *string `json:"to,omitempty"`
	Q             *string `json:"q,omitempty"`
	CategoryID    *string `json:"category_id,omitempty"`
	Kind          *string `json:"kind,omitempty"`
	AmountFrom    *int64  `json:"amount_from,omitempty"`
	AmountTo      *int64  `json:"amount_to,omitempty"`
	TransferMode  *string `json:"transfer_mode,omitempty"`
	Selection     *string `json:"selection,omitempty"`
	SelectionName *string `json:"selection_name,omitempty"`
}

func operationsFilter(f *commonv1.OperationsFilter, loc *time.Location) *OperationsFilter {
	if f == nil {
		return nil
	}
	out := &OperationsFilter{
		From: optionalMoment(f.From, loc), To: optionalMoment(f.To, loc),
		Q: f.Q, CategoryID: f.CategoryId, AmountFrom: f.AmountFromMinor, AmountTo: f.AmountToMinor,
		Selection: f.Selection, SelectionName: f.SelectionName,
	}
	if kind, ok := enumCode(f.GetKind()); ok {
		out.Kind = &kind
	}
	// UNSPECIFIED means "without analytics rules": the field stays absent.
	if mode, ok := enumCode(f.GetTransferMode()); ok {
		out.TransferMode = &mode
	}
	return out
}

// AnalyticsParams is analytics_params.
type AnalyticsParams struct {
	Period       string `json:"period"`
	Date         string `json:"date"`
	TransferMode string `json:"transfer_mode"`
}

func analyticsParams(p *commonv1.AnalyticsParams) (*AnalyticsParams, bool) {
	if p == nil {
		return nil, false
	}
	period, ok := enumCode(p.GetPeriod())
	if !ok || p.GetPeriodKey() == "" {
		return nil, false
	}
	return &AnalyticsParams{Period: period, Date: p.GetPeriodKey(), TransferMode: transferMode(p.GetTransferMode())}, true
}

// transferMode is a required response transfer_mode: UNSPECIFIED and unknown values
// are "with", which is also how the client reads an unknown value (docs/api.md).
func transferMode(m commonv1.TransferMode) string {
	if code, ok := enumCode(m); ok {
		return code
	}
	return "with"
}
