package endpoint

import (
	"fmt"

	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	statementdatav1 "github.com/fin-assist/contracts/gen/go/finassist/statementdata/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"

	"github.com/fin-assist/bank-bff/internal/catalog"
)

// ImportConfig is the import screen configuration (REST 3.1).
type ImportConfig struct {
	DownloadURL      string         `json:"download_url"`
	LoginURL         string         `json:"login_url"`
	MaxFileSizeBytes int64          `json:"max_file_size_bytes"`
	AcceptedFormats  []string       `json:"accepted_formats"`
	SuggestedPeriod  *Range         `json:"suggested_period"`
	Loaded           *LoadedSummary `json:"loaded,omitempty"`
}

// LoadedSummary describes the existing operations, without calculating business data.
type LoadedSummary struct {
	FirstOperationAt string `json:"first_operation_at"`
	LastOperationAt  string `json:"last_operation_at"`
	OperationCount   int64  `json:"operation_count"`
}

// Uploads is the upload history with its optional summary (REST 3.4).
type Uploads struct {
	Summary *UploadsSummary `json:"summary,omitempty"`
	Uploads []Upload        `json:"uploads"`
}

// UploadsSummary is the service-computed aggregate over retained uploads.
type UploadsSummary struct {
	UploadCount      int32   `json:"upload_count"`
	OperationCount   int64   `json:"operation_count"`
	FirstOperationAt string  `json:"first_operation_at"`
	LastOperationAt  string  `json:"last_operation_at"`
	Gaps             []Range `json:"gaps"`
}

// Upload is one history item. Unknown status follows REST's explicit done fallback.
type Upload struct {
	UploadID             string  `json:"upload_id"`
	FileName             string  `json:"file_name"`
	Status               string  `json:"status"`
	UploadedAt           string  `json:"uploaded_at"`
	OperationCount       int64   `json:"operation_count"`
	UnreadCount          int32   `json:"unread_count"`
	SourceName           string  `json:"source_name"`
	ResolvedUnreadCount  int32   `json:"resolved_unread_count,omitempty"`
	ResolvedByUploadedAt *string `json:"resolved_by_uploaded_at,omitempty"`
	PeriodFrom           *string `json:"period_from,omitempty"`
	PeriodTo             *string `json:"period_to,omitempty"`
}

// ImportAccepted is the 202 accepted file; its name is preserved byte-for-byte.
type ImportAccepted struct {
	UploadID string `json:"upload_id"`
	FileName string `json:"file_name"`
}

// ImportProgress is a nonterminal parsing progress event (REST 3.3).
type ImportProgress struct {
	Stage   string `json:"stage"`
	Percent int32  `json:"percent"`
}

// ImportError is a terminal result error, never a refusal of the SSE request.
type ImportError struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	UserMessage string `json:"user_message"`
	Severity    string `json:"severity"`
}

// ImportResult is the complete event. All business numbers come from the service.
type ImportResult struct {
	OperationCount       int64             `json:"operation_count"`
	NewCount             int64             `json:"new_count"`
	DuplicateCount       int64             `json:"duplicate_count"`
	UnreadCount          int32             `json:"unread_count"`
	FirstOperationAt     *string           `json:"first_operation_at,omitempty"`
	LastOperationAt      *string           `json:"last_operation_at,omitempty"`
	IsFirstImport        bool              `json:"is_first_import"`
	Totals               *ImportTotals     `json:"totals,omitempty"`
	Accounts             []ImportAccount   `json:"accounts"`
	CategorizedCount     int64             `json:"categorized_count"`
	OwnTransferCount     int64             `json:"own_transfer_count"`
	UncategorizedCount   int64             `json:"uncategorized_count"`
	UncategorizedFilters *OperationsFilter `json:"uncategorized_filters,omitempty"`
	Currency             string            `json:"currency"`
	Coverage             ImportCoverage    `json:"coverage"`
	Notices              []ImportNotice    `json:"notices"`
}

// ImportTotals carries minor units, with a presentation scope, not a recomputed sum.
type ImportTotals struct {
	Scope   string `json:"scope"`
	Expense int64  `json:"expense"`
	Income  int64  `json:"income"`
}

// ImportAccount contains only the display account type and masked number.
type ImportAccount struct {
	TypeName    string  `json:"type_name"`
	Mask        *string `json:"mask,omitempty"`
	IsOtherBank bool    `json:"is_other_bank"`
}

// ImportCoverage carries service decisions about available analytics.
type ImportCoverage struct {
	FullMonths       int32                 `json:"full_months"`
	FullMonthsBefore int32                 `json:"full_months_before"`
	NewlyFullMonths  []string              `json:"newly_full_months"`
	IncompleteMonths []IncompleteMonth     `json:"incomplete_months"`
	Features         []FeatureAvailability `json:"features"`
}

// IncompleteMonth describes the service-provided coverage facts.
type IncompleteMonth struct {
	Month      string  `json:"month"`
	Range      *Range  `json:"range"`
	DataFrom   string  `json:"data_from"`
	DataTo     string  `json:"data_to"`
	Gaps       []Range `json:"gaps"`
	InProgress bool    `json:"in_progress"`
}

// FeatureAvailability adds only fixed presentation labels to service decisions.
type FeatureAvailability struct {
	Feature                string  `json:"feature"`
	FeatureName            string  `json:"feature_name"`
	Open                   bool    `json:"open"`
	OpenedNow              bool    `json:"opened_now"`
	RequiredFullMonths     *int32  `json:"required_full_months,omitempty"`
	RequiredMonthsWithData *int32  `json:"required_months_with_data,omitempty"`
	Text                   *string `json:"text,omitempty"`
}

// ImportNotice is a fixed presentation of a service-selected notice.
type ImportNotice struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

// UnreadLines contains only metadata, never raw transaction content.
type UnreadLines struct {
	Lines []UnreadLine `json:"lines"`
}

// UnreadLine describes one unresolved line (REST 3.6).
type UnreadLine struct {
	UploadID   string  `json:"upload_id"`
	FileName   string  `json:"file_name"`
	LineNumber int32   `json:"line_number"`
	Date       *string `json:"date,omitempty"`
	Reason     string  `json:"reason"`
	ReasonName string  `json:"reason_name"`
}

func (c converter) importConfig(m *statementdatav1.GetImportConfigResponse) ImportConfig {
	out := ImportConfig{DownloadURL: m.GetDownloadUrl(), LoginURL: m.GetLoginUrl(), MaxFileSizeBytes: m.GetMaxFileSizeBytes(),
		AcceptedFormats: append([]string{}, m.GetAcceptedFormats()...), SuggestedPeriod: timeRange(m.GetSuggestedPeriod(), c.loc)}
	if m.GetLoaded() != nil {
		l := m.GetLoaded()
		out.Loaded = &LoadedSummary{moment(l.GetFirstOperationAt(), c.loc), moment(l.GetLastOperationAt(), c.loc), l.GetOperationCount()}
	}
	return out
}

func (c converter) ranges(in []*commonv1.TimeRange) []Range {
	out := make([]Range, 0, len(in))
	for _, r := range in {
		if v := timeRange(r, c.loc); v != nil {
			out = append(out, *v)
		}
	}
	return out
}

func (c converter) uploads(m *statementdatav1.ListUploadsResponse) Uploads {
	out := Uploads{Uploads: []Upload{}}
	if s := m.GetSummary(); s != nil {
		out.Summary = &UploadsSummary{s.GetUploadCount(), s.GetOperationCount(), moment(s.GetFirstOperationAt(), c.loc), moment(s.GetLastOperationAt(), c.loc), c.ranges(s.GetGaps())}
	}
	for _, u := range m.GetUploads() {
		status, ok := enumCode(u.GetStatus())
		if !ok {
			status = "done"
			c.metrics.Untranslated("Upload.status", "unknown")
		}
		item := Upload{UploadID: u.GetUploadId(), FileName: u.GetFileName(), Status: status, UploadedAt: moment(u.GetUploadedAt(), c.loc), OperationCount: u.GetOperationCount(), UnreadCount: u.GetUnreadCount(), SourceName: u.GetSourceName(), ResolvedUnreadCount: u.GetResolvedUnreadCount()}
		if u.GetResolvedUnreadCount() > 0 {
			item.ResolvedByUploadedAt = optionalMoment(u.GetResolvedByUploadedAt(), c.loc)
		}
		if u.PeriodFrom != nil && u.PeriodTo != nil {
			item.PeriodFrom = u.PeriodFrom
			item.PeriodTo = u.PeriodTo
		}
		out.Uploads = append(out.Uploads, item)
	}
	return out
}

var featureLabels = map[string]string{
	"comparison": "Сравнение с прошлым месяцем", "monthly_chart": "Динамика по месяцам", "typical": "Обычные расходы",
	"regular_payments": "Регулярные платежи", "notable_spending": "Заметные траты", "small_frequent": "Мелкие частые траты", "year_forecast": "Прогноз на год",
}

func (c converter) coverage(m *statementdatav1.ImportCoverage) ImportCoverage {
	out := ImportCoverage{FullMonths: m.GetFullMonths(), FullMonthsBefore: m.GetFullMonthsBefore(), NewlyFullMonths: append([]string{}, m.GetNewlyFullMonths()...), IncompleteMonths: []IncompleteMonth{}, Features: []FeatureAvailability{}}
	for _, v := range m.GetIncompleteMonths() {
		out.IncompleteMonths = append(out.IncompleteMonths, IncompleteMonth{v.GetMonth(), timeRange(v.GetRange(), c.loc), moment(v.GetDataFrom(), c.loc), moment(v.GetDataTo(), c.loc), c.ranges(v.GetGaps()), v.GetInProgress()})
	}
	for _, v := range m.GetFeatures() {
		code, known := enumCode(v.GetFeature())
		label, ok := featureLabels[code]
		if !known || !ok {
			c.metrics.Untranslated("FeatureAvailability.feature", "unknown")
			continue
		}
		item := FeatureAvailability{Feature: code, FeatureName: label, Open: v.GetOpen(), OpenedNow: v.GetOpenedNow(), RequiredFullMonths: v.RequiredFullMonths, RequiredMonthsWithData: v.RequiredMonthsWithData}
		if !item.Open {
			var text string
			switch {
			case v.RequiredFullMonths != nil:
				text = fmt.Sprintf("%s — нужно полных месяцев: %d, есть %d", label, *v.RequiredFullMonths, m.GetFullMonths())
			case v.RequiredMonthsWithData != nil:
				text = fmt.Sprintf("%s — нужно месяцев с данными: %d", label, *v.RequiredMonthsWithData)
			}
			if text != "" {
				item.Text = &text
			}
		}
		out.Features = append(out.Features, item)
	}
	return out
}

func (c converter) importResult(m *statementdatav1.ImportResult) ImportResult {
	out := ImportResult{OperationCount: m.GetOperationCount(), NewCount: m.GetNewCount(), DuplicateCount: m.GetDuplicateCount(), UnreadCount: m.GetUnreadCount(), FirstOperationAt: optionalMoment(m.GetFirstOperationAt(), c.loc), LastOperationAt: optionalMoment(m.GetLastOperationAt(), c.loc), IsFirstImport: m.GetIsFirstImport(), Accounts: []ImportAccount{}, CategorizedCount: m.GetCategorizedCount(), OwnTransferCount: m.GetOwnTransferCount(), UncategorizedCount: m.GetUncategorizedCount(), UncategorizedFilters: operationsFilter(m.GetUncategorizedFilters(), c.loc), Currency: "RUB", Coverage: c.coverage(m.GetCoverage()), Notices: []ImportNotice{}}
	if t := m.GetTotals(); t != nil {
		scope, ok := enumCode(t.GetScope())
		if !ok {
			scope = "unknown"
			c.metrics.Untranslated("ImportTotals.scope", "unknown")
		}
		out.Totals = &ImportTotals{scope, t.GetExpenseMinor(), t.GetIncomeMinor()}
	}
	for _, a := range m.GetAccounts() {
		out.Accounts = append(out.Accounts, ImportAccount{a.GetTypeName(), a.Mask, a.GetIsOtherBank()})
	}
	for _, n := range m.GetNotices() {
		switch n {
		case statementdatav1.ImportNotice_IMPORT_NOTICE_EMPTY_STATEMENT:
			out.Notices = append(out.Notices, ImportNotice{"empty_statement", "В файле нет операций."})
		case statementdatav1.ImportNotice_IMPORT_NOTICE_NO_EXPENSES:
			out.Notices = append(out.Notices, ImportNotice{"no_expenses", "В файле нет расходов. Возможно, выгружен не тот счёт."})
		default:
			c.metrics.Untranslated("ImportResult.notices", "unknown")
		}
	}
	return out
}

func (c converter) importError(m *statementdatav1.ImportStreamError) ImportError {
	code := "PROCESSING_FAILED"
	if m.GetStatus() == nil {
		if legacy, ok := enumName(m.GetCode()); ok {
			code = legacy
		} else {
			c.metrics.Untranslated("ImportStreamError.code", "unknown")
		}
	} else if result, valid := finerr.DecodeResultStatus(m.GetStatus()); valid {
		code = result.Reason
		if alias, known := enumName(m.GetCode()); known && alias != code {
			c.metrics.Untranslated("ImportStreamError.status", "legacy_mismatch")
		}
	} else {
		c.metrics.Untranslated("ImportStreamError.status", "invalid")
	}
	entry, known := catalog.Import(code)
	if !known {
		c.metrics.Untranslated("ImportStreamError.reason", "unknown")
	}
	return ImportError{Code: code, Message: entry.Diagnostic, UserMessage: entry.Text, Severity: entry.Severity}
}

func (c converter) importEvent(m *statementdatav1.WatchUploadResponse) (string, any, bool, bool) {
	switch e := m.GetEvent().(type) {
	case *statementdatav1.WatchUploadResponse_Progress:
		stage, ok := enumCode(e.Progress.GetStage())
		if !ok {
			stage = "unknown"
			c.metrics.Untranslated("ImportProgress.stage", "unknown")
		}
		return "progress", ImportProgress{stage, e.Progress.GetPercent()}, false, true
	case *statementdatav1.WatchUploadResponse_Complete:
		return "complete", c.importResult(e.Complete), true, true
	case *statementdatav1.WatchUploadResponse_Error:
		return "error", c.importError(e.Error), true, true
	default:
		c.metrics.Untranslated("WatchUploadResponse.event", "unknown")
		return "", nil, false, false
	}
}

func (c converter) unread(m *statementdatav1.ListUnreadLinesResponse) UnreadLines {
	out := UnreadLines{Lines: []UnreadLine{}}
	for _, v := range m.GetLines() {
		code, known := enumCode(v.GetReason())
		if !known {
			c.metrics.Untranslated("UnreadLine.reason", "unknown")
			continue
		}
		out.Lines = append(out.Lines, UnreadLine{v.GetUploadId(), v.GetFileName(), v.GetLineNumber(), optionalMoment(v.GetDate(), c.loc), code, v.GetReasonName()})
	}
	return out
}
