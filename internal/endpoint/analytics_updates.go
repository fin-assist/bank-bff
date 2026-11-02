package endpoint

import (
	"net/http"
	"regexp"
	"time"
	"unicode/utf8"

	analyticsv1 "github.com/fin-assist/contracts/gen/go/finassist/analytics/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	statementdatav1 "github.com/fin-assist/contracts/gen/go/finassist/statementdata/v1"
	"google.golang.org/protobuf/proto"

	"github.com/fin-assist/bank-bff/internal/rest"
)

// validSourceZone refuses a missing required or unresolvable source calendar.
// A profile/default zone must not silently rename a service-owned snapshot.
func validSourceZone(name string, required bool) bool {
	if name == "" {
		return !required
	}
	if len(name) > 128 || name == "Local" {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

func importConfigRequest(c *rest.Call) (*statementdatav1.GetImportConfigRequest, bool) {
	q := c.R.URL.Query()
	req := &statementdatav1.GetImportConfigRequest{}
	for _, field := range []string{"period_from", "period_to"} {
		if !q.Has(field) {
			continue
		}
		values := q[field]
		if len(values) != 1 || len(values[0]) != 10 {
			queryInvalid(c, field)
			return nil, false
		}
		day, err := time.Parse("2006-01-02", values[0])
		if err != nil || day.Year() < 1 || day.Format("2006-01-02") != values[0] {
			queryInvalid(c, field)
			return nil, false
		}
		if field == "period_from" {
			req.PeriodFrom = &values[0]
		} else {
			req.PeriodTo = &values[0]
		}
	}
	// Pairing, ordering and today's limit belong to BSD, which owns the calendar.
	return req, true
}

func dismissalParams(c *rest.Call) (*commonv1.AnalyticsParams, bool) {
	q := c.R.URL.Query()
	for _, field := range []string{"date", "transfer_mode"} {
		if !q.Has(field) {
			rest.Validation(rest.Detail{Field: field, Code: rest.DetailRequired, Message: "is required"}).Write(c.W)
			return nil, false
		}
		if len(q[field]) != 1 {
			queryInvalid(c, field)
			return nil, false
		}
	}
	date := q.Get("date")
	if !analyticsMonthKey.MatchString(date) || date[:4] == "0000" {
		queryInvalid(c, "date")
		return nil, false
	}
	mode := commonv1.TransferMode_TRANSFER_MODE_WITH
	switch q.Get("transfer_mode") {
	case "with":
	case "without":
		mode = commonv1.TransferMode_TRANSFER_MODE_WITHOUT
	default:
		queryInvalid(c, "transfer_mode")
		return nil, false
	}
	return &commonv1.AnalyticsParams{Period: commonv1.PeriodType_PERIOD_TYPE_MONTH, PeriodKey: date, TransferMode: mode}, true
}

type analyticsBlockUpdate struct {
	Params              *AnalyticsParams `json:"params"`
	Currency            string           `json:"currency"`
	TimeZone            string           `json:"time_zone"`
	Blocks              []analyticsBlock `json:"blocks"`
	RequiredCalcVersion int32            `json:"required_calc_version"`
}

var analyticsCurrency = regexp.MustCompile(`^[A-Z]{3}$`)

// validDismissalProjection binds a source response to this authenticated command.
// Metric contents belong to BSD and analytics; the BFF never interprets financial details.
func validDismissalProjection(p *analyticsv1.MetricProjection, params *commonv1.AnalyticsParams, userID string) bool {
	source := p.GetParams()
	return p != nil && p.GetUserId() == userID && source != nil &&
		source.GetPeriod() == params.GetPeriod() && source.GetPeriodKey() == params.GetPeriodKey() &&
		source.GetTransferMode() == params.GetTransferMode() && proto.Size(p) <= 3<<20
}

func (d Deps) applyDismissal(c *rest.Call, params *commonv1.AnalyticsParams, target int32, projection *analyticsv1.MetricProjection) {
	ctx, cancel := unary(c)
	defer cancel()
	resp, err := d.Analytics.ApplyRegularPaymentDismissal(ctx, &analyticsv1.ApplyRegularPaymentDismissalRequest{
		RegularPaymentId: c.R.PathValue("id"), CalcVersion: target, Params: params, Projection: projection,
	})
	if err != nil {
		// BSD already committed: only authentication can still be a client refusal.
		// A composer validation/context/not-found error must never assert that the write did not occur.
		c.FailAs(err, map[int]bool{http.StatusUnauthorized: true, http.StatusInternalServerError: true})
		return
	}
	if resp.GetRequiredCalcVersion() != target || resp.GetTimeZone() != projection.GetTimeZone() || resp.GetCurrency() != projection.GetCurrency() ||
		!validSourceZone(resp.GetTimeZone(), true) || !analyticsCurrency.MatchString(resp.GetCurrency()) || resp.GetParams() == nil ||
		resp.GetParams().GetPeriod() != params.GetPeriod() || resp.GetParams().GetPeriodKey() != params.GetPeriodKey() || resp.GetParams().GetTransferMode() != params.GetTransferMode() ||
		len(resp.GetBlocks()) < 1 || len(resp.GetBlocks()) > 16 {
		c.Refuse(rest.Internal(), false)
		return
	}
	conv := converter{loc: zone(resp.GetTimeZone()), metrics: c.Metrics}
	out := analyticsBlockUpdate{Currency: resp.GetCurrency(), TimeZone: resp.GetTimeZone(), RequiredCalcVersion: target, Blocks: make([]analyticsBlock, 0, len(resp.GetBlocks()))}
	out.Params, _ = analyticsParams(resp.GetParams())
	ids := make(map[string]bool, len(resp.GetBlocks()))
	for _, b := range resp.GetBlocks() {
		if b.GetId() == "" || ids[b.GetId()] || b.GetType() == "" || b.GetV() < 1 || !updatedBlockDataPresent(b) {
			c.Refuse(rest.Internal(), false)
			return
		}
		ids[b.GetId()] = true
		out.Blocks = append(out.Blocks, conv.analyticsBlock(b))
	}
	if !ids["regular_payments"] {
		c.Refuse(rest.Internal(), false)
		return
	}
	rest.WriteJSON(c.W, http.StatusOK, out)
}

// updatedBlockDataPresent enforces only the primitive envelope, never financial contents.
func updatedBlockDataPresent(b *commonv1.Block) bool {
	if b.GetData() != nil {
		return true
	}
	return b.GetType() != "insight_card" && b.GetType() != "tile_group" &&
		b.GetStatus() != "" && b.GetStatus() != "ready" && b.GetStatus() != "tentative"
}

func (d Deps) analyticsChartPage(c *rest.Call) {
	q := c.R.URL.Query()
	if !q.Has("cursor") || q.Get("cursor") == "" {
		rest.Validation(rest.Detail{Field: "cursor", Code: rest.DetailRequired, Message: "is required"}).Write(c.W)
		return
	}
	if len(q["cursor"]) != 1 || !utf8.ValidString(q.Get("cursor")) || utf8.RuneCountInString(q.Get("cursor")) > 2048 {
		queryInvalid(c, "cursor")
		return
	}
	ctx, cancel := unary(c)
	defer cancel()
	resp, err := d.Analytics.GetChartPage(ctx, &analyticsv1.GetChartPageRequest{Cursor: q.Get("cursor")})
	if err != nil {
		c.Fail(err)
		return
	}
	b := resp.GetBlock()
	if !validSourceZone(resp.GetTimeZone(), true) || b == nil || b.GetId() != "monthly_chart" || b.GetType() != "chart" || b.GetV() != 1 || b.GetStatus() != "ready" || b.GetData() == nil {
		rest.Internal().Write(c.W)
		return
	}
	conv := converter{loc: zone(resp.GetTimeZone()), metrics: c.Metrics}
	rest.WriteJSON(c.W, http.StatusOK, struct {
		Block    analyticsBlock `json:"block"`
		TimeZone string         `json:"time_zone"`
	}{conv.analyticsBlock(b), resp.GetTimeZone()})
}
