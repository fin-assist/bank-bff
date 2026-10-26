package endpoint

import (
	"strings"
	"time"

	assistantv1 "github.com/fin-assist/contracts/gen/go/finassist/assistant/v1"

	"github.com/fin-assist/bank-bff/internal/catalog"
	"github.com/fin-assist/bank-bff/internal/rest"
)

// Block is the common REST block (docs/api.md, «Блоки»). The assistant uses text,
// chart and rows, version 1.
type Block struct {
	Type    string  `json:"type"`
	V       int     `json:"v"`
	Title   *string `json:"title,omitempty"`
	AltText *string `json:"alt_text,omitempty"`
	Data    any     `json:"data,omitempty"`
}

type textData struct {
	Text string `json:"text"`
}

type chartPoint struct {
	Range    Range    `json:"range"`
	Value    *float64 `json:"value,omitempty"`
	Coverage string   `json:"coverage"`
	DataFrom *string  `json:"data_from,omitempty"`
	DataTo   *string  `json:"data_to,omitempty"`
}

type chartData struct {
	Kind           string       `json:"kind"`
	ValueKind      string       `json:"value_kind"`
	Points         []chartPoint `json:"points"`
	HighlightIndex *int32       `json:"highlight_index,omitempty"`
}

type blockRow struct {
	Label     string  `json:"label"`
	Sublabel  *string `json:"sublabel,omitempty"`
	Value     any     `json:"value,omitempty"`
	ValueKind string  `json:"value_kind,omitempty"`
	Text      *string `json:"text,omitempty"`
}

type rowsData struct {
	Rows []blockRow `json:"rows"`
}

// unsupportedBlock keeps the position of a block bank-bff cannot translate: chunk
// indexes count blocks, so it must not disappear. The client shows alt_text for an
// unknown type or skips the block (docs/api.md, «Блоки»).
const unsupportedBlock = "unsupported"

type converter struct {
	loc     *time.Location
	metrics *rest.Metrics
}

func (c converter) block(b *assistantv1.Block) Block {
	switch content := b.GetContent().(type) {
	case *assistantv1.Block_Text:
		return Block{Type: "text", V: 1, AltText: b.AltText, Data: textData{Text: content.Text.GetText()}}
	case *assistantv1.Block_Chart:
		if data, ok := c.chart(content.Chart); ok {
			title := content.Chart.GetTitle()
			out := Block{Type: "chart", V: 1, AltText: b.AltText, Data: data}
			if title != "" {
				out.Title = &title
			}
			return out
		}
	case *assistantv1.Block_Rows:
		return Block{Type: "rows", V: 1, AltText: b.AltText, Data: c.rows(content.Rows)}
	}
	c.metrics.Untranslated("Block", "content")
	return Block{Type: unsupportedBlock, V: 1, AltText: b.AltText}
}

func (c converter) chart(ch *assistantv1.ChartBlock) (chartData, bool) {
	kind, okKind := enumCode(ch.GetKind())
	valueKind, okValue := enumCode(ch.GetValueKind())
	if !okKind || !okValue {
		return chartData{}, false
	}
	out := chartData{Kind: kind, ValueKind: valueKind, Points: []chartPoint{}, HighlightIndex: ch.HighlightIndex}
	for _, p := range ch.GetPoints() {
		r := timeRange(p.GetRange(), c.loc)
		if r == nil {
			return chartData{}, false
		}
		// An unknown coverage is shown as partial by the client (docs/api.md, Coverage).
		coverage, ok := enumCode(p.GetBounds().GetCoverage())
		if !ok {
			coverage = "partial"
		}
		out.Points = append(out.Points, chartPoint{Range: *r, Value: p.Value, Coverage: coverage,
			DataFrom: optionalMoment(p.GetBounds().DataFrom, c.loc), DataTo: optionalMoment(p.GetBounds().DataTo, c.loc)})
	}
	return out, true
}

func (c converter) rows(r *assistantv1.RowsBlock) rowsData {
	out := rowsData{Rows: []blockRow{}}
	for _, row := range r.GetRows() {
		item := blockRow{Label: row.GetLabel(), Sublabel: row.Sublabel}
		switch v := row.GetValue().(type) {
		case *assistantv1.BlockRow_AmountMinor:
			item.Value, item.ValueKind = v.AmountMinor, "amount"
		case *assistantv1.BlockRow_Count:
			item.Value, item.ValueKind = v.Count, "count"
		case *assistantv1.BlockRow_Percent:
			item.Value, item.ValueKind = v.Percent, "percent"
		case *assistantv1.BlockRow_Text:
			text := v.Text
			item.Text = &text
		}
		out.Rows = append(out.Rows, item)
	}
	return out
}

func (c converter) blocks(in []*assistantv1.Block) []Block {
	out := make([]Block, 0, len(in))
	for _, b := range in {
		out = append(out, c.block(b))
	}
	return out
}

// Chip is the transition under an answer (and the chip event).
type Chip struct {
	Label   string            `json:"label"`
	Screen  string            `json:"screen"`
	Filters *OperationsFilter `json:"filters,omitempty"`
	Params  *AnalyticsParams  `json:"params,omitempty"`
}

// chip returns false for a chip the client must not show: an unknown screen or a
// screen without its parameters.
func (c converter) chip(ch *assistantv1.Chip) (Chip, bool) {
	out := Chip{Label: ch.GetLabel()}
	switch ch.GetScreen() {
	case assistantv1.ChipScreen_CHIP_SCREEN_OPERATIONS:
		if ch.Filters == nil {
			break
		}
		out.Screen, out.Filters = "operations", operationsFilter(ch.Filters, c.loc)
		return out, true
	case assistantv1.ChipScreen_CHIP_SCREEN_ANALYTICS:
		params, ok := analyticsParams(ch.Params)
		if !ok {
			break
		}
		out.Screen, out.Params = "analytics", params
		return out, true
	}
	c.metrics.Untranslated("Chip.screen", "unknown")
	return Chip{}, false
}

func (c converter) chips(in []*assistantv1.Chip) []Chip {
	out := make([]Chip, 0, len(in))
	for _, ch := range in {
		if chip, ok := c.chip(ch); ok {
			out = append(out, chip)
		}
	}
	return out
}

// Source is the source line of an answer.
type Source struct {
	CalculatedAt     string   `json:"calculated_at"`
	DataRange        *Range   `json:"data_range,omitempty"`
	TransferMode     string   `json:"transfer_mode"`
	ChangedSince     []string `json:"changed_since"`
	ChangedSinceText *string  `json:"changed_since_text"`
}

// changedPhrases build changed_since_text (grpc-api.md 5.3: bank-bff assembles it).
var changedPhrases = map[string]string{"categories": "категории менялись", "uploads": "загружались выписки"}

func (c converter) source(s *assistantv1.AnswerSource) *Source {
	if s == nil {
		return nil
	}
	out := &Source{CalculatedAt: moment(s.GetCalculatedAt(), c.loc), DataRange: timeRange(s.DataRange, c.loc),
		TransferMode: transferMode(s.GetTransferMode()), ChangedSince: []string{}}
	var phrases []string
	unknown := false
	for _, ch := range s.GetChangedSince() {
		code, ok := enumCode(ch)
		if !ok {
			unknown = true
			c.metrics.Untranslated("AnswerSource.changed_since", "unknown")
			continue
		}
		out.ChangedSince = append(out.ChangedSince, code)
		phrases = append(phrases, changedPhrases[code])
	}
	if unknown {
		phrases = append(phrases, "данные менялись")
	}
	if len(phrases) > 0 {
		text := "с тех пор " + strings.Join(phrases, " и ")
		out.ChangedSinceText = &text
	}
	return out
}

// currency returns the required currency, RUB with a counted defect when empty
// (grpc-api.md 5.3).
func (c converter) currency(value, field string) string {
	if value == "" {
		c.metrics.EmptyRequired(field)
		return "RUB"
	}
	return value
}

// answerCode is the external code of a legacy AnswerErrorCode. An unknown or zero
// value is the conservative INTERNAL_ERROR: a code is never invented to enable retry
// (grpc-api.md 5.5).
func (c converter) answerCode(code assistantv1.AnswerErrorCode) string {
	if name, ok := enumName(code); ok {
		return name
	}
	c.metrics.Untranslated("AnswerError.code", "unknown")
	return "INTERNAL_ERROR"
}

// Presentation of a failed answer from the BFF catalog; service text is never copied.
func answerPresentation(code string) catalog.Entry {
	entry, _ := catalog.Answer(code)
	return entry
}

// Message is one dialogue message (7.3).
type Message struct {
	ID               string   `json:"id"`
	Role             string   `json:"role"`
	Currency         string   `json:"currency"`
	Text             *string  `json:"text,omitempty"`
	Status           *string  `json:"status,omitempty"`
	ErrorCode        *string  `json:"error_code,omitempty"`
	ErrorUserMessage *string  `json:"error_user_message,omitempty"`
	ErrorSeverity    *string  `json:"error_severity,omitempty"`
	ErrorText        *string  `json:"error_text,omitempty"`
	Retryable        *bool    `json:"retryable,omitempty"`
	Charged          *bool    `json:"charged,omitempty"`
	Blocks           *[]Block `json:"blocks,omitempty"`
	Chips            *[]Chip  `json:"chips,omitempty"`
	Source           *Source  `json:"source,omitempty"`
	CreatedAt        string   `json:"created_at"`
}

// message returns false for a role the client does not show (docs/api.md 7.3).
func (c converter) message(m *assistantv1.Message) (Message, bool) {
	out := Message{ID: m.GetId(), Currency: c.currency(m.GetCurrency(), "Message.currency"), CreatedAt: moment(m.GetCreatedAt(), c.loc)}
	switch m.GetRole() {
	case assistantv1.Role_ROLE_USER:
		out.Role = "user"
		text := m.GetText()
		out.Text = &text
		return out, true
	case assistantv1.Role_ROLE_ASSISTANT:
		out.Role = "assistant"
	default:
		c.metrics.Untranslated("Message.role", "unknown")
		return Message{}, false
	}
	status, ok := enumCode(m.GetStatus())
	if !ok {
		// An unknown status is shown as failed (docs/api.md 7.3); it has no code.
		c.metrics.Untranslated("Message.status", "unknown")
		status = "failed"
		entry := answerPresentation("INTERNAL_ERROR")
		out.Status = &status
		out.ErrorUserMessage, out.ErrorSeverity, out.ErrorText = &entry.Text, &entry.Severity, &entry.Text
		out.Blocks = &[]Block{}
		return out, true
	}
	out.Status = &status
	blocks := c.blocks(m.GetBlocks())
	out.Blocks = &blocks
	switch m.GetStatus() {
	case assistantv1.AnswerStatus_ANSWER_STATUS_COMPLETE:
		out.Charged = m.Charged
		chips := c.chips(m.GetChips())
		out.Chips = &chips
		out.Source = c.source(m.Source)
	case assistantv1.AnswerStatus_ANSWER_STATUS_FAILED:
		code := c.answerCode(m.GetErrorCode())
		entry := answerPresentation(code)
		out.ErrorCode = &code
		out.ErrorUserMessage, out.ErrorSeverity, out.ErrorText = &entry.Text, &entry.Severity, &entry.Text
		// The service's permission of a new attempt, with presence (grpc-api.md 5.3).
		out.Retryable = m.Retryable
		out.Blocks = &[]Block{}
	}
	return out, true
}
