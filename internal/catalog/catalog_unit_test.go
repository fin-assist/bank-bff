package catalog

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	commonv1 "github.com/fin-assist/contracts/gen/go/finassist/common/v1"
	"github.com/fin-assist/contracts/gen/go/finerr"
)

// safeText applies the UserMessage rules of docs/api.md: 1–1000 code points, at
// least one non-White_Space, plain text without control characters or markup.
func safeText(s string) bool {
	n := utf8.RuneCountInString(s)
	if n < 1 || n > 1000 || strings.TrimFunc(s, unicode.IsSpace) == "" || strings.ContainsAny(s, "<>`*[]") {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func TestUnitCatalogCoversTheRegistryWithSafeTexts(t *testing.T) {
	for _, value := range commonv1.ErrorReason_value {
		name := commonv1.ErrorReason(value).String()
		if _, inRegistry := finerr.Lookup(name); !inRegistry {
			continue
		}
		if _, known := REST(name); !known {
			t.Errorf("registry code %s has no catalog text", name)
		}
	}
	for _, code := range []string{"MAINTENANCE", "CLIENT_VERSION_UNSUPPORTED", "REQUEST_TOO_LARGE"} {
		if _, known := REST(code); !known {
			t.Errorf("BFF code %s has no catalog text", code)
		}
	}
	entries := []Entry{unknownREST, unknownAnswer}
	for _, c := range RESTCodes() {
		e, _ := REST(c)
		entries = append(entries, e)
	}
	for _, c := range AnswerCodes() {
		e, _ := Answer(c)
		entries = append(entries, e)
	}
	for _, e := range entries {
		if !safeText(e.Text) || e.Diagnostic == "" || e.Severity != Info && e.Severity != Warning && e.Severity != Error {
			t.Errorf("unsafe entry %+v", e)
		}
	}
	if e, known := REST("FUTURE_CODE"); known || e != unknownREST {
		t.Fatal("unknown REST code")
	}
	if e, known := Answer("INTERNAL_ERROR"); known || e != unknownAnswer {
		t.Fatal("assistant fallback")
	}
	if e, _ := Answer("MODEL_ERROR"); e.Text != "Не получилось ответить. Вопрос не потрачен" {
		t.Fatal("FIN-159 text of MODEL_ERROR changed")
	}
	if !safeText("x") || safeText(" 　") || safeText(strings.Repeat("я", 1001)) || safeText("a\x00") {
		t.Fatal("rule check")
	}
}
