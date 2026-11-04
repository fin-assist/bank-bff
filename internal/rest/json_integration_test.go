package rest

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIntegrationWriteJSONEncodingFailureBeforeHTTPCommit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusCreated, map[string]float64{"value": math.NaN()})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	failure := decodeError(t, string(body))
	if resp.StatusCode != http.StatusInternalServerError || failure.Code != "INTERNAL_ERROR" || failure.Retryable == nil || *failure.Retryable {
		t.Fatalf("HTTP committed an invalid success: %d %s", resp.StatusCode, body)
	}
}
