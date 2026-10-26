// Package rest is the contract-neutral HTTP plumbing of the public BFF API: request
// identity and hints, maintenance and client-version gates, OpenAPI request validation,
// bounded bodies, JSON error responses, gRPC error translation and SSE streams.
package rest

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"

	"github.com/fin-assist/go-grpc/reqctx"
)

// uuidShape is a canonical UUID of any version: Idempotency-Key and X-Request-Id
// (format: uuid in openapi.yaml). kin-openapi does not check the uuid format itself.
var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsUUID reports whether v is a canonical UUID string.
func IsUUID(v string) bool { return uuidShape.MatchString(v) }

type hintsKey struct{}

// Hints are the request values bank-bff passes to internal services as metadata
// (grpc-api.md 4). They are computed or copied by the BFF itself, never taken from
// arbitrary client metadata.
type Hints struct {
	// ClientIP is the client address: the TCP peer, or the X-Forwarded-For element
	// added by a trusted proxy (ClientIP).
	ClientIP string
	// ClientVersion is the validated X-Client-Version header.
	ClientVersion string
	// ReadPrimary is true for X-Read-Primary: 1.
	ReadPrimary bool
	// IdempotencyKey is the validated Idempotency-Key of an operation that requires it.
	IdempotencyKey string
}

// WithHints stores hints for the outgoing call interceptors.
func WithHints(ctx context.Context, h Hints) context.Context {
	return context.WithValue(ctx, hintsKey{}, h)
}

// HintsFrom returns the hints of the request, zero when none.
func HintsFrom(ctx context.Context) Hints {
	h, _ := ctx.Value(hintsKey{}).(Hints)
	return h
}

// newRequestID returns a random UUIDv4.
func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// RequestID assigns X-Request-Id (the client's UUID or a new one), echoes it in the
// response and stores it for logs and the x-request-id metadata of outgoing calls.
// A value of another shape is replaced, not echoed: it is client input. It also
// stores the client address (ClientIP with the trusted proxies).
func RequestID(trusted []netip.Prefix, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if !IsUUID(id) {
			id = newRequestID()
		}
		w.Header().Set("X-Request-Id", id)
		ctx := reqctx.WithRequestID(r.Context(), id)
		h := Hints{ClientIP: ClientIP(r, trusted), ReadPrimary: r.Header.Get("X-Read-Primary") == "1"}
		next.ServeHTTP(w, r.WithContext(WithHints(ctx, h)))
	})
}

// maxForwardedHops bounds the X-Forwarded-For elements examined from the right.
const maxForwardedHops = 16

// ClientIP is the client address: the TCP peer, unless the peer is a trusted proxy.
// Then X-Forwarded-For is walked from the right: every proxy appends the address it
// saw, so only elements appended by trusted proxies are believed, and the first
// address outside the trusted networks is the client. Everything left of it is
// written by the client and never examined, so a malformed or long prefix cannot
// change the result. If an element appended by trusted proxies cannot be parsed, or
// more than 16 elements were examined, the address of the last trusted hop is kept.
// Forwarding headers of an untrusted peer are ignored.
func ClientIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return ""
	}
	hop, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	hop = hop.Unmap()
	if !isTrusted(hop, trusted) {
		return hop.String()
	}
	examined := 0
	values := r.Header.Values("X-Forwarded-For")
	for i := len(values) - 1; i >= 0; i-- {
		elements := strings.Split(values[i], ",")
		for j := len(elements) - 1; j >= 0; j-- {
			if examined++; examined > maxForwardedHops {
				return hop.String()
			}
			addr, err := netip.ParseAddr(strings.TrimSpace(elements[j]))
			if err != nil {
				return hop.String()
			}
			hop = addr.Unmap()
			if !isTrusted(hop, trusted) {
				return hop.String()
			}
		}
	}
	return hop.String()
}

func isTrusted(addr netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
