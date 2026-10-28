package rest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"

	"github.com/fin-assist/bank-bff/internal/apispec"
)

// MinBodyLimit is the smallest JSON body budget the BFF may enforce: 64 KiB of wire
// UTF-8 including whitespace and escapes (FIN-339).
const MinBodyLimit = 64 << 10

// TooLarge is the refusal of a JSON body above the budget: 413 REQUEST_TOO_LARGE of
// FIN-339, answered before the idempotency key and never stored under it.
func TooLarge() Error {
	return Error{Status: http.StatusRequestEntityTooLarge, Code: "REQUEST_TOO_LARGE"}
}

// jsonEncodingRefusal implements FIN-339 only for its six agreed JSON operations.
// A repeated header is invalid even when both tokens are identity. HTTP OWS is
// normalized by net/http; trim SP/HTAB here as well for in-process callers.
// Accepted OWS is normalized in the shared request Header before schema validation.
func jsonEncodingRefusal(r *http.Request, op apispec.Operation) *Error {
	switch op.Route.Operation.OperationID {
	case "startPhoneVerification", "register", "refreshTokens", "logout", "grantAssistantConsent", "askAssistant":
	default:
		return nil
	}
	values := r.Header.Values("Content-Encoding")
	if len(values) == 0 {
		return nil
	}
	if len(values) == 1 && strings.EqualFold(strings.Trim(values[0], " \t"), "identity") {
		r.Header.Set("Content-Encoding", strings.Trim(values[0], " \t"))
		return nil
	}
	refusal := Validation(Detail{Field: "Content-Encoding", Code: DetailInvalidFormat, Message: detailMessage(DetailInvalidFormat)})
	return &refusal
}

// ReadBody reads the request body within limit. An operation without a request body
// accepts none: any body is a validation error, and at most one byte of it is read.
func ReadBody(r *http.Request, op apispec.Operation, limit int64) ([]byte, *Error) {
	if op.Route.Operation.RequestBody == nil {
		if r.ContentLength > 0 {
			e := Validation(Detail{Code: DetailInvalidFormat, Message: "request body is not allowed"})
			return nil, &e
		}
		var one [1]byte
		if n, _ := io.ReadFull(r.Body, one[:]); n > 0 {
			e := Validation(Detail{Code: DetailInvalidFormat, Message: "request body is not allowed"})
			return nil, &e
		}
		return nil, nil
	}
	if r.ContentLength > limit {
		e := TooLarge()
		return nil, &e
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if int64(len(body)) > limit {
		e := TooLarge()
		return nil, &e
	}
	if err != nil {
		// A body the client did not finish (timeout, reset): no response reaches it anyway.
		e := Validation(Detail{Code: DetailInvalidFormat, Message: "request body could not be read"})
		return nil, &e
	}
	return body, nil
}

var validationOptions = &openapi3filter.Options{
	MultiError:          true,
	SkipSettingDefaults: true,
	// Authorization is checked by the identity middleware before validation, in the
	// contract order; the schema check must not repeat it.
	AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
}

// Validate checks parameters, headers and body against the operation of openapi.yaml.
// It returns every violation found. Lengths are counted in Unicode code points, as
// docs/api.md defines «символов» (kin-openapi counts UTF-16 units).
func Validate(ctx context.Context, r *http.Request, op apispec.Operation, path map[string]string, body []byte) []Detail {
	clone := r.Clone(ctx)
	clone.Body = io.NopCloser(bytes.NewReader(body))
	clone.ContentLength = int64(len(body))
	err := openapi3filter.ValidateRequest(ctx, &openapi3filter.RequestValidationInput{
		Request: clone, PathParams: path, Route: op.Route, Options: validationOptions,
	})
	if err == nil {
		return nil
	}
	var details []Detail
	collect(err, &details)
	return dedupe(details)
}

func collect(err error, out *[]Detail) {
	// A RequestError unwraps to its schema MultiError, and a MultiError matches its
	// elements in errors.As, so neither can classify the error itself: the list is
	// matched by its own type to walk it element by element, each element keeping
	// its own parameter.
	if multi, ok := err.(openapi3.MultiError); ok { //nolint:errorlint // errors.As would match the MultiError a RequestError unwraps to.
		for _, e := range multi {
			collect(e, out)
		}
		return
	}
	var req *openapi3filter.RequestError
	if !errors.As(err, &req) {
		*out = append(*out, Detail{Code: DetailInvalidFormat, Message: detailMessage(DetailInvalidFormat)})
		return
	}
	field := ""
	if req.Parameter != nil {
		field = req.Parameter.Name
	}
	switch {
	case errors.Is(req.Err, openapi3filter.ErrInvalidRequired), errors.Is(req.Err, openapi3filter.ErrInvalidEmptyValue):
		*out = append(*out, Detail{Field: field, Code: DetailRequired, Message: detailMessage(DetailRequired)})
		return
	case req.Err == nil:
		*out = append(*out, Detail{Field: field, Code: DetailInvalidFormat, Message: detailMessage(DetailInvalidFormat)})
		return
	}
	schemaErrors(req.Err, field, req.Parameter == nil, out)
}

func schemaErrors(err error, field string, inBody bool, out *[]Detail) {
	if multi, ok := err.(openapi3.MultiError); ok { //nolint:errorlint // As in collect: the list itself, not a list an element wraps.
		for _, e := range multi {
			schemaErrors(e, field, inBody, out)
		}
		return
	}
	var se *openapi3.SchemaError
	if !errors.As(err, &se) {
		*out = append(*out, Detail{Field: field, Code: DetailInvalidFormat, Message: detailMessage(DetailInvalidFormat)})
		return
	}
	if lengthWithinCodePoints(se) {
		return
	}
	if inBody {
		field = strings.Join(se.JSONPointer(), ".")
	}
	code := DetailInvalidFormat
	switch se.SchemaField {
	case "required":
		code = DetailRequired
	case "minLength", "maxLength", "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "minItems", "maxItems":
		code = DetailOutOfRange
	}
	*out = append(*out, Detail{Field: field, Code: code, Message: detailMessage(code)})
}

// lengthWithinCodePoints reports a kin-openapi length error that does not hold when
// the string is measured in code points: astral characters count once, not twice.
func lengthWithinCodePoints(se *openapi3.SchemaError) bool {
	if se.SchemaField != "minLength" && se.SchemaField != "maxLength" || se.Schema == nil {
		return false
	}
	s, ok := se.Value.(string)
	if !ok {
		return false
	}
	n := uint64(utf8.RuneCountInString(s))
	if n < se.Schema.MinLength {
		return false
	}
	return se.Schema.MaxLength == nil || n <= *se.Schema.MaxLength
}

func dedupe(in []Detail) []Detail {
	seen := make(map[Detail]bool, len(in))
	out := in[:0]
	for _, d := range in {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Field < out[j].Field })
	return out
}

// ValidateInput is a route's input validation strategy. It may leave the request
// body unread for a streaming handler. Refusals precede validation details.
type ValidateInput func(*http.Request, apispec.Operation, map[string]string, int64) ([]byte, []Detail, *Error)

// JSONInput is the existing bounded JSON request pipeline.
func JSONInput(r *http.Request, op apispec.Operation, path map[string]string, limit int64) ([]byte, []Detail, *Error) {
	body, refusal := ReadBody(r, op, limit)
	if refusal != nil {
		return nil, nil, refusal
	}
	return body, Validate(r.Context(), r, op, path, body), nil
}

// StreamingInput validates only parameters and headers. The owning endpoint must
// validate its entire bounded body before completing the downstream operation.
func StreamingInput(r *http.Request, op apispec.Operation, path map[string]string, _ int64) ([]byte, []Detail, *Error) {
	options := *validationOptions
	options.ExcludeRequestBody = true
	// kin-openapi 0.133 security validation reads the entire body even with the
	// no-op authenticator. Identity already ran; give schema checks an empty body
	// while retaining the original headers/path/query and the real stream untouched.
	clone := r.Clone(r.Context())
	clone.Body = http.NoBody
	clone.ContentLength = 0
	clone.GetBody = nil
	err := openapi3filter.ValidateRequest(r.Context(), &openapi3filter.RequestValidationInput{
		Request: clone, PathParams: path, Route: op.Route, Options: &options,
	})
	if err == nil {
		return nil, nil, nil
	}
	var details []Detail
	collect(err, &details)
	return nil, dedupe(details), nil
}
