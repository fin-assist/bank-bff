package rest

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/fin-assist/bank-bff/internal/apispec"
)

func TestUnitCalendarQueryExactErrorsBeforeSchema(t *testing.T) {
	spec, err := apispec.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	const chart = "/v1/analytics/charts/monthly_chart"
	const config = "/v1/statements/config"
	for _, tc := range []struct{ name, path, query, field, code string }{
		{"missing cursor", chart, "", "cursor", DetailRequired},
		{"empty cursor", chart, "cursor=", "cursor", DetailRequired},
		{"repeated cursor", chart, "cursor=a&cursor=b", "cursor", DetailInvalidFormat},
		{"oversize cursor", chart, "cursor=" + strings.Repeat("x", 2049), "cursor", DetailInvalidFormat},
		{"invalid UTF8", chart, "cursor=%FF", "cursor", DetailInvalidFormat},
		{"unknown query", chart, "cursor=opaque&transfer_mode=without", "transfer_mode", DetailInvalidFormat},
		{"header in query", chart, "cursor=opaque&X-Read-Primary=1", "X-Read-Primary", DetailInvalidFormat},
		{"long from", config, "period_from=2026-01-010&period_to=2026-02-01", "period_from", DetailInvalidFormat},
		{"long to", config, "period_from=2026-01-01&period_to=2026-02-010", "period_to", DetailInvalidFormat},
		{"invalid date", config, "period_from=2026-02-30", "period_from", DetailInvalidFormat},
		{"year zero", config, "period_to=0000-01-01", "period_to", DetailInvalidFormat},
		{"short date", config, "period_to=2026-2-01", "period_to", DetailInvalidFormat},
		{"empty date", config, "period_to=", "period_to", DetailInvalidFormat},
		{"repeated date", config, "period_to=2026-01-01&period_to=2026-01-02", "period_to", DetailInvalidFormat},
		{"UTF8 code point limit", chart, "cursor=" + url.QueryEscape(strings.Repeat("😀", 2048)), "", ""},
		{"first calendar year", config, "period_from=0001-01-01&period_to=0001-01-01", "", ""},
		{"other routes remain open", config, "legacy=1", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			op, e := spec.Operation("GET", tc.path)
			if e != nil {
				t.Fatal(e)
			}
			r := httptest.NewRequest("GET", tc.path+"?"+tc.query, nil)
			r.Header.Set("X-Client-Version", "android/0.2.16+1")
			r.Header.Set("X-Read-Primary", "1")
			got := Validate(t.Context(), r, op, nil, nil)
			if tc.code == "" {
				if len(got) != 0 {
					t.Fatal(got)
				}
			} else if len(got) != 1 || got[0].Field != tc.field || got[0].Code != tc.code {
				t.Fatalf("want one %s/%s, got %+v", tc.field, tc.code, got)
			}
		})
	}
}
