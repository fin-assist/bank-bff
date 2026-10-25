package rest

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// outgoingHints appends the allowlisted client hints of grpc-api.md 4 to the outgoing
// metadata: x-client-ip, x-client-version, x-read-primary and idempotency-key. It runs
// after the go-grpc client interceptor (which starts fresh metadata with the service
// token and x-request-id) and after the identity interceptor (which adds only the
// verified user token). Nothing else from the HTTP request reaches the service.
func outgoingHints(ctx context.Context) context.Context {
	h := HintsFrom(ctx)
	var kv []string
	if h.ClientIP != "" {
		kv = append(kv, "x-client-ip", h.ClientIP)
	}
	if h.ClientVersion != "" {
		kv = append(kv, "x-client-version", h.ClientVersion)
	}
	if h.ReadPrimary {
		kv = append(kv, "x-read-primary", "1")
	}
	if h.IdempotencyKey != "" {
		kv = append(kv, "idempotency-key", h.IdempotencyKey)
	}
	if len(kv) == 0 {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, kv...)
}

// UnaryHints is the client interceptor that forwards the request hints.
func UnaryHints() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return invoker(outgoingHints(ctx), method, req, reply, cc, opts...)
	}
}

// StreamHints is the stream client interceptor that forwards the request hints.
func StreamHints() grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		return streamer(outgoingHints(ctx), desc, cc, method, opts...)
	}
}
