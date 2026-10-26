package rest

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/fin-assist/bank-bff/internal/catalog"
)

// Detail is one violation of VALIDATION_ERROR (docs/api.md, «Ошибки»).
type Detail struct {
	Field   string `json:"field,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Detail codes of the contract.
const (
	DetailRequired      = "REQUIRED"
	DetailInvalidFormat = "INVALID_FORMAT"
	DetailOutOfRange    = "OUT_OF_RANGE"
)

// Error is one REST error response. The JSON body has exactly one error object; its
// user_message, severity and diagnostic message come from the catalog by Code.
type Error struct {
	// Status is the HTTP status.
	Status int
	// Code is the registry code.
	Code string
	// Details are the violations of VALIDATION_ERROR.
	Details []Detail
	// RetryAt, ResetsAt and Until are the RFC 3339 moments of RATE_LIMITED,
	// LIMIT_EXCEEDED and MAINTENANCE.
	RetryAt, ResetsAt, Until string
	// RetryAfter is the fresh wait of the Retry-After header; zero omits the header.
	RetryAfter time.Duration
	// Retryable permits replay of the whole original REST request, not a new operation attempt.
	// Nil has no permission; Write emits explicit false for the new BFF contract.
	Retryable *bool
	// serviceRequestRetryable is only the decoded original-RPC permission, never sufficient by itself.
	serviceRequestRetryable *bool
}

type errorBody struct {
	Error errorObject `json:"error"`
}

type errorObject struct {
	Code        string   `json:"code"`
	Message     string   `json:"message"`
	UserMessage string   `json:"user_message"`
	Severity    string   `json:"severity"`
	Details     []Detail `json:"details,omitempty"`
	RetryAt     string   `json:"retry_at,omitempty"`
	ResetsAt    string   `json:"resets_at,omitempty"`
	Until       string   `json:"until,omitempty"`
	Retryable   *bool    `json:"retryable,omitempty"`
}

// Internal is the generic 500 INTERNAL_ERROR.
func Internal() Error { return Error{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR"} }

// Validation is 400 VALIDATION_ERROR with the given violations.
func Validation(details ...Detail) Error {
	return Error{Status: http.StatusBadRequest, Code: "VALIDATION_ERROR", Details: details}
}

// NotFound is 404 NOT_FOUND.
func NotFound() Error { return Error{Status: http.StatusNotFound, Code: "NOT_FOUND"} }

// InvalidToken is 401 INVALID_TOKEN of the Authorization header check.
func InvalidToken() Error { return Error{Status: http.StatusUnauthorized, Code: "INVALID_TOKEN"} }

// RetryAfterSeconds rounds a positive wait up to whole seconds, at least one.
func RetryAfterSeconds(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	s := int64(math.Ceil(d.Seconds()))
	if s < 1 {
		s = 1
	}
	return s
}

// Write sends the error. The Date header is set by net/http for every response, so
// replayed and failed answers carry a fresh Date too.
func (e Error) Write(w http.ResponseWriter) {
	entry, _ := catalog.REST(e.Code)
	retryable := false
	if e.Retryable != nil {
		retryable = *e.Retryable
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	if s := RetryAfterSeconds(e.RetryAfter); s > 0 {
		h.Set("Retry-After", strconv.FormatInt(s, 10))
	}
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(errorBody{errorObject{
		Code: e.Code, Message: entry.Diagnostic, UserMessage: entry.Text, Severity: entry.Severity,
		Details: e.Details, RetryAt: e.RetryAt, ResetsAt: e.ResetsAt, Until: e.Until, Retryable: &retryable,
	}})
}

// WriteJSON sends a success body.
func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// WriteNoContent sends 204.
func WriteNoContent(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
