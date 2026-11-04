package rest

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUnitWriteJSONEncodingFailureIsAnInternalError(t *testing.T) {
	for name, value := range map[string]any{"NaN": math.NaN(), "positive infinity": math.Inf(1), "negative infinity": math.Inf(-1), "unsupported type": make(chan int)} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			WriteJSON(w, http.StatusCreated, map[string]any{"private": "synthetic-private-value", "value": value})
			if w.Code != http.StatusInternalServerError || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("encoding failure was sent as success: %d %v %s", w.Code, w.Header(), w.Body.String())
			}
			e := decodeError(t, w.Body.String())
			if e.Code != "INTERNAL_ERROR" || e.Retryable == nil || *e.Retryable || strings.Contains(w.Body.String(), "synthetic-private-value") {
				t.Fatalf("unsafe encoding failure response: %s", w.Body.String())
			}
		})
	}
}

func TestUnitWriteJSONKeepsSuccessfulStatusAndEncoding(t *testing.T) {
	w := httptest.NewRecorder()
	WriteJSON(w, http.StatusCreated, map[string]string{"text": "<safe>"})
	if w.Code != http.StatusCreated || w.Body.String() != "{\"text\":\"\\u003csafe\\u003e\"}\n" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("success changed: %d %v %q", w.Code, w.Header(), w.Body.String())
	}
}
