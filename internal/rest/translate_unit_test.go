package rest

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fin-assist/contracts/gen/go/finerr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newTestMetrics(t *testing.T) (*Metrics, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	m, err := NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	return m, reg
}

var allStatuses = map[int]bool{400: true, 401: true, 403: true, 404: true, 409: true, 413: true, 422: true, 429: true, 500: true}

func TestUnitFromGRPCTranslatesRegistryReasons(t *testing.T) {
	m, _ := newTestMetrics(t)
	resets := time.Date(2026, 10, 26, 0, 0, 0, 0, time.FixedZone("MSK", 3*3600))
	retry := time.Now().Add(90 * time.Second)
	for _, tc := range []struct {
		err    error
		status int
		code   string
		check  func(Error) bool
	}{
		{finerr.InvalidToken(), 401, "INVALID_TOKEN", nil},
		{finerr.TokenExpired(), 401, "TOKEN_EXPIRED", nil},
		{finerr.TokenRevoked(), 401, "TOKEN_REVOKED", nil},
		{finerr.ConsentRequired(), 403, "CONSENT_REQUIRED", nil},
		{finerr.LimitExceeded(resets), 403, "LIMIT_EXCEEDED", func(e Error) bool { return e.ResetsAt == "2026-10-26T00:00:00+03:00" }},
		{finerr.NotFound(), 404, "NOT_FOUND", nil},
		{finerr.IdempotencyConflict(), 409, "IDEMPOTENCY_CONFLICT", nil},
		{finerr.RequestInProgress(3 * time.Second), 409, "REQUEST_IN_PROGRESS", func(e Error) bool { return e.RetryAfter == 3*time.Second }},
		{finerr.ConsentOutdated(), 422, "CONSENT_OUTDATED", nil},
		{finerr.RetryNotAllowed(), 422, "RETRY_NOT_ALLOWED", nil},
		{finerr.RateLimited(retry), 429, "RATE_LIMITED", func(e Error) bool { return e.RetryAt != "" && e.RetryAfter > 80*time.Second }},
		{finerr.InternalError(), 500, "INTERNAL_ERROR", nil},
		{fmt.Errorf("wrapped: %w", finerr.NotFound()), 404, "NOT_FOUND", nil},
		{finerr.ValidationError(&errdetails.BadRequest_FieldViolation{Field: "phone", Reason: "INVALID_FORMAT", Description: "bad phone"}), 400, "VALIDATION_ERROR",
			func(e Error) bool {
				return len(e.Details) == 1 && e.Details[0] == Detail{Field: "phone", Code: "INVALID_FORMAT", Message: "bad phone"}
			}},
		{finerr.ValidationError(&errdetails.BadRequest_FieldViolation{Field: "text", Reason: "OUT_OF_RANGE", Description: strings.Repeat("x", 300)}), 400, "VALIDATION_ERROR",
			func(e Error) bool { return e.Details[0].Message == "is out of range" }},
		{finerr.ValidationError(&errdetails.BadRequest_FieldViolation{Reason: "REQUIRED"}), 400, "VALIDATION_ERROR",
			func(e Error) bool { return e.Details[0].Message == "is required" && e.Details[0].Field == "" }},
	} {
		got := m.FromGRPC(tc.err, allStatuses)
		if got.Status != tc.status || got.Code != tc.code || tc.check != nil && !tc.check(got) {
			t.Fatalf("%v -> %+v", tc.err, got)
		}
	}
}

func TestUnitFromGRPCFallsBackToInternal(t *testing.T) {
	m, reg := newTestMetrics(t)
	//nolint:forbidigo // A malformed peer error is what is being tested.
	foreign, _ := status.New(codes.NotFound, "x").WithDetails(&errdetails.ErrorInfo{Domain: "other", Reason: "NOT_FOUND"})
	//nolint:forbidigo // A malformed peer error is what is being tested.
	unknown, _ := status.New(codes.FailedPrecondition, "x").WithDetails(&errdetails.ErrorInfo{Domain: finerr.Domain, Reason: "FUTURE_REASON"})
	//nolint:forbidigo // A malformed peer error is what is being tested.
	shapeless, _ := status.New(codes.FailedPrecondition, "x").WithDetails(&errdetails.ErrorInfo{Domain: finerr.Domain, Reason: "bad reason\n"})
	//nolint:forbidigo // An incomplete registry error: RATE_LIMITED without retry_at.
	incomplete, _ := status.New(codes.ResourceExhausted, "x").WithDetails(&errdetails.ErrorInfo{Domain: finerr.Domain, Reason: "RATE_LIMITED"})
	for _, tc := range []struct {
		err    error
		reason string
	}{
		{status.Error(codes.Unavailable, "user is down: dial tcp 10.0.0.1"), "none"},
		{status.Error(codes.DeadlineExceeded, "slow"), "none"},
		{status.Error(codes.PermissionDenied, "foreign caller"), "none"},
		{foreign.Err(), "none"},
		{unknown.Err(), "FUTURE_REASON"},
		{shapeless.Err(), "invalid"},
		{incomplete.Err(), "RATE_LIMITED"},
		{fmt.Errorf("plain"), "none"},
	} {
		if got := m.FromGRPC(tc.err, allStatuses); got.Status != 500 || got.Code != "INTERNAL_ERROR" || got.Details != nil {
			t.Fatalf("%v -> %+v", tc.err, got)
		}
		if n := testutil.ToFloat64(m.unmapped.WithLabelValues(tc.reason, status.Code(tc.err).String())); n < 1 {
			t.Fatalf("%v not counted as %s", tc.err, tc.reason)
		}
	}
	// A registry error the operation does not declare is a service defect: 500.
	if got := m.FromGRPC(finerr.NotFound(), map[int]bool{401: true}); got.Code != "INTERNAL_ERROR" || testutil.ToFloat64(m.unmapped.WithLabelValues("NOT_FOUND", "NotFound")) != 1 {
		t.Fatalf("undeclared: %+v", got)
	}
	// A cancelled request is not a translation defect.
	before := testutil.CollectAndCount(reg, "finassist_unmapped_error_total")
	for _, err := range []error{context.Canceled, status.Error(codes.Canceled, "client left")} {
		if got := m.FromGRPC(err, allStatuses); got.Code != "INTERNAL_ERROR" {
			t.Fatal(got)
		}
	}
	if testutil.CollectAndCount(reg, "finassist_unmapped_error_total") != before {
		t.Fatal("cancellation counted")
	}
}

func TestUnitMetricsAreBoundedAndNilSafe(t *testing.T) {
	m, reg := newTestMetrics(t)
	for i := 0; i < 40; i++ {
		m.unmappedError(fmt.Sprintf("R%d", i), "Unknown")
	}
	if testutil.ToFloat64(m.unmapped.WithLabelValues("other", "Unknown")) != 8 {
		t.Fatal("reason labels are capped at 32")
	}
	m.Untranslated("Block", "content")
	m.EmptyRequired("Message.currency")
	m.StreamRejected("/r")
	done := m.StreamOpened("/r")
	if testutil.ToFloat64(m.streams.WithLabelValues("/r")) != 1 {
		t.Fatal("open stream")
	}
	done()
	if testutil.ToFloat64(m.streams.WithLabelValues("/r")) != 0 || testutil.ToFloat64(m.empty.WithLabelValues("AnswerSnapshot.currency")) != 0 {
		t.Fatal("gauge or zero series")
	}
	if _, err := NewMetrics(reg); err == nil {
		t.Fatal("duplicate registration accepted")
	}
	var nilMetrics *Metrics
	nilMetrics.Untranslated("a", "b")
	nilMetrics.EmptyRequired("a")
	nilMetrics.StreamRejected("a")
	nilMetrics.StreamOpened("a")()
	nilMetrics.unmappedError("a", "b")
}
