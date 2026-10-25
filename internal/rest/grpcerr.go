package rest

import (
	"context"
	"errors"
	"regexp"
	"unicode/utf8"

	"github.com/fin-assist/contracts/gen/go/finerr"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// reasonShape bounds reason labels to the registry naming rule (grpc-api.md 5.4).
var reasonShape = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,61}[A-Z0-9]$`)

// maxDetailMessage bounds a forwarded BadRequest description (diagnostics only).
const maxDetailMessage = 200

// FromGRPC translates an internal call error by its ErrorInfo.reason (finerr.ToREST,
// grpc-api.md 5). declared holds the statuses the operation declares in openapi.yaml.
// An incomplete or unregistered error, a bare status and a registry status the
// operation does not declare become 500 INTERNAL_ERROR and are counted; status
// messages never reach the client.
func (m *Metrics) FromGRPC(err error, declared map[int]bool) Error {
	if errors.Is(err, context.Canceled) || status.Code(err) == codes.Canceled {
		// The client went away; the response is written for completeness only.
		return Internal()
	}
	r, ok := finerr.ToREST(err)
	if !ok {
		m.unmappedError(reasonOf(err), status.Code(err).String())
		return Internal()
	}
	if !declared[r.Spec.HTTPStatus] {
		m.unmappedError(r.Code, status.Code(err).String())
		return Internal()
	}
	e := Error{Status: r.Spec.HTTPStatus, Code: r.Code, RetryAfter: r.RetryAfter,
		RetryAt: r.Metadata["retry_at"], ResetsAt: r.Metadata["resets_at"]}
	for _, v := range r.Violations {
		msg := v.GetDescription()
		if utf8.RuneCountInString(msg) > maxDetailMessage || !utf8.ValidString(msg) || msg == "" {
			msg = detailMessage(v.GetReason())
		}
		e.Details = append(e.Details, Detail{Field: v.GetField(), Code: v.GetReason(), Message: msg})
	}
	return e
}

// reasonOf returns a bounded label for an unmapped error: the shaped reason of its
// finassist ErrorInfo, "none" without one, "invalid" for a malformed reason.
func reasonOf(err error) string {
	st, ok := status.FromError(err)
	if !ok {
		return "none"
	}
	for _, d := range st.Details() {
		//nolint:forbidigo // Reading, not building, ErrorInfo for a bounded metric label.
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetDomain() == finerr.Domain {
			if reasonShape.MatchString(info.GetReason()) {
				return info.GetReason()
			}
			return "invalid"
		}
	}
	return "none"
}

// detailMessage is the diagnostic message of a violation code.
func detailMessage(code string) string {
	switch code {
	case DetailRequired:
		return "is required"
	case DetailOutOfRange:
		return "is out of range"
	default:
		return "has invalid format"
	}
}
