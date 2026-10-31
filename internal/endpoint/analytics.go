package endpoint

import (
	"net/http"
	"strconv"
	"strings"

	analyticsv1 "github.com/fin-assist/contracts/gen/go/finassist/analytics/v1"
	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"

	"github.com/fin-assist/bank-bff/internal/rest"
)

// periodType translates only periods supported by the generated contract. Unknown
// request codes cannot be represented by its enum and are refused before dialing.
func periodType(value string) (commonv1.PeriodType, bool) {
	switch value {
	case "", "month":
		return commonv1.PeriodType_PERIOD_TYPE_MONTH, true
	case "quarter":
		return commonv1.PeriodType_PERIOD_TYPE_QUARTER, true
	case "year":
		return commonv1.PeriodType_PERIOD_TYPE_YEAR, true
	case "rolling_year":
		return commonv1.PeriodType_PERIOD_TYPE_ROLLING_YEAR, true
	case "rolling_quarter":
		return commonv1.PeriodType_PERIOD_TYPE_ROLLING_QUARTER, true
	default:
		return 0, false
	}
}

func queryInvalid(c *rest.Call, field string) {
	rest.Validation(rest.Detail{Field: field, Code: rest.DetailInvalidFormat, Message: "has invalid format"}).Write(c.W)
}

func (d Deps) analytics(c *rest.Call) {
	q := c.R.URL.Query()
	period, ok := periodType(q.Get("period"))
	if !ok {
		queryInvalid(c, "period")
		return
	}
	req := &analyticsv1.GetAnalyticsRequest{Period: period, TransferMode: commonv1.TransferMode_TRANSFER_MODE_WITH}
	switch q.Get("transfer_mode") {
	case "", "with":
	case "without":
		req.TransferMode = commonv1.TransferMode_TRANSFER_MODE_WITHOUT
	default:
		queryInvalid(c, "transfer_mode")
		return
	}
	if date := q.Get("date"); date != "" {
		req.PeriodKey = &date
	}
	if supports := q.Get("supports"); supports != "" {
		for _, item := range strings.Split(supports, ",") {
			kind, version, found := strings.Cut(item, "@")
			v, err := strconv.ParseInt(version, 10, 32)
			if !found || kind == "" || err != nil || v < 1 {
				queryInvalid(c, "supports")
				return
			}
			req.Supports = append(req.Supports, &commonv1.BlockSupport{Type: kind, MaxVersion: int32(v)})
		}
	}
	if experiments := q.Get("exp"); experiments != "" {
		req.Experiments = map[string]string{}
		for _, item := range strings.Split(experiments, ",") {
			key, value, found := strings.Cut(item, ":")
			if !found || key == "" || value == "" {
				queryInvalid(c, "exp")
				return
			}
			req.Experiments[key] = value
		}
	}
	ctx, cancel := unary(c)
	defer cancel()
	resp, err := d.Analytics.GetAnalytics(ctx, req)
	if err != nil {
		c.Fail(err)
		return
	}
	conv := converter{loc: zone(resp.GetTimeZone()), metrics: c.Metrics}
	if resp.GetHasData() && (rollingPeriod(period) || rollingPeriod(resp.GetParams().GetPeriod())) && resp.GetParams().GetPeriod() != period {
		conv.metrics.Untranslated("GetAnalyticsResponse.params.period", "invalid")
		rest.Internal().Write(c.W)
		return
	}
	out, ok := conv.analytics(resp)
	if !ok {
		rest.Internal().Write(c.W)
		return
	}
	rest.WriteJSON(c.W, http.StatusOK, out)
}

func (d Deps) analyticsPeriods(c *rest.Call) {
	period, ok := periodType(c.R.URL.Query().Get("period"))
	if !ok {
		queryInvalid(c, "period")
		return
	}
	ctx, cancel := unary(c)
	defer cancel()
	resp, err := d.Analytics.ListPeriods(ctx, &analyticsv1.ListPeriodsRequest{Period: period})
	if err != nil {
		c.Fail(err)
		return
	}
	conv := converter{loc: zone(resp.GetTimeZone()), metrics: c.Metrics}
	items := make([]analyticsPeriodItem, 0, len(resp.GetPeriods()))
	for _, p := range resp.GetPeriods() {
		if !conv.analyticsResponseKey(period, p.GetKey(), "ListPeriodsResponse.periods.key") || !conv.analyticsRange(p.GetRange(), "ListPeriodsResponse.periods.range") {
			rest.Internal().Write(c.W)
			return
		}
		items = append(items, analyticsPeriodItem{Key: p.GetKey(), Range: timeRange(p.GetRange(), conv.loc), Coverage: conv.coverageCode(p.GetCoverage(), "ListPeriodsResponse.periods.coverage")})
	}
	rest.WriteJSON(c.W, http.StatusOK, struct {
		Periods []analyticsPeriodItem `json:"periods"`
	}{items})
}
