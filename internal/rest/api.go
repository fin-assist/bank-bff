package rest

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/fin-assist/bank-bff/internal/apispec"
)

// Route is one contract operation served by the BFF.
type Route struct {
	// Method and Path are the OpenAPI method and path template ("/v1/consents/{type}").
	Method, Path string
	// Protected reports whether the request needs a verified Authorization header.
	// Nil means never; the consent document decides by its path parameter.
	Protected func(*http.Request) bool
	// Idempotent marks operations that require Idempotency-Key (docs/api.md, «Идемпотентность»).
	Idempotent bool
	// Handle serves a request that passed every BFF check.
	Handle func(*Call)
}

// Always is Protected for operations that always require the access token.
func Always(*http.Request) bool { return true }

// Call is a validated request of one operation.
type Call struct {
	W http.ResponseWriter
	R *http.Request
	// Op is the contract operation (declared statuses are used by Fail).
	Op apispec.Operation
	// Body is the validated JSON body, nil for operations without one.
	Body []byte
	// Metrics counts translation defects.
	Metrics *Metrics
	// Log is the request logger; it never receives tokens, bodies or personal data.
	Log *slog.Logger
	// Streams limits concurrent SSE streams.
	Streams *StreamLimiter
	// Clock is "now" for fresh retry moments.
	Clock func() time.Time
}

// Fail answers an internal call error by its registry reason.
func (c *Call) Fail(err error) {
	e := c.Metrics.FromGRPC(err, c.Op.Statuses)
	if e.Code == "INTERNAL_ERROR" {
		c.Log.WarnContext(c.R.Context(), "internal call failed", "grpc_code", codeOf(err))
	}
	e.Write(c.W)
}

// Config is what the public API needs besides its routes.
type Config struct {
	Spec      *apispec.Spec
	Gates     Gates
	BodyLimit int64
	// Authenticate wraps a protected handler: identity.HTTP.Middleware.
	Authenticate func(http.Handler) http.Handler
	Metrics      *Metrics
	Streams      *StreamLimiter
	Log          *slog.Logger
	Clock        func() time.Time
}

// Handler builds the public handler: request ID and access log for every path, the
// maintenance and client-version gates for /v1, then one route per operation and a
// JSON 404 for any other /v1 path or method. The public mux never serves
// operational endpoints.
func Handler(cfg Config, routes []Route) (http.Handler, error) {
	mux := http.NewServeMux()
	for _, route := range routes {
		op, err := cfg.Spec.Operation(route.Method, route.Path)
		if err != nil {
			return nil, err
		}
		mux.Handle(route.Method+" "+route.Path, routeHandler(cfg, route, op))
	}
	notFound := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { NotFound().Write(w) })
	mux.Handle("/v1/", notFound)
	gated := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client, refusal := cfg.Gates.Check(r)
		if refusal != nil {
			refusal.Write(w)
			return
		}
		h := HintsFrom(r.Context())
		h.ClientVersion = r.Header.Get("X-Client-Version")
		if rec, ok := w.(*recorder); ok {
			rec.client = client.Label()
		}
		mux.ServeHTTP(w, r.WithContext(WithHints(r.Context(), h)))
	})
	root := http.NewServeMux()
	root.Handle("/v1/", gated)
	root.Handle("/", http.NotFoundHandler())
	return RequestID(accessLog(cfg, root)), nil
}

func routeHandler(cfg Config, route Route, op apispec.Operation) http.Handler {
	checked := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rec, ok := w.(*recorder); ok {
			rec.route = route.Path
		}
		body, refusal := ReadBody(r, op, cfg.BodyLimit)
		if refusal != nil {
			refusal.Write(w)
			return
		}
		path := pathValues(route.Path, r)
		details := Validate(r.Context(), r, op, path, body)
		key := ""
		if route.Idempotent {
			values := r.Header.Values("Idempotency-Key")
			switch {
			case len(values) == 0:
				details = append(details, Detail{Field: "Idempotency-Key", Code: DetailRequired, Message: detailMessage(DetailRequired)})
			case len(values) > 1 || !IsUUID(values[0]):
				details = append(details, Detail{Field: "Idempotency-Key", Code: DetailInvalidFormat, Message: detailMessage(DetailInvalidFormat)})
			default:
				key = values[0]
			}
		}
		if details = dedupe(details); len(details) > 0 {
			Validation(details...).Write(w)
			return
		}
		ctx := r.Context()
		if key != "" {
			h := HintsFrom(ctx)
			h.IdempotencyKey = key
			ctx = WithHints(ctx, h)
		}
		route.Handle(&Call{W: w, R: r.WithContext(ctx), Op: op, Body: body, Metrics: cfg.Metrics,
			Log: cfg.Log, Streams: cfg.Streams, Clock: cfg.Clock})
	})
	protected := cfg.Authenticate(checked)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rec, ok := w.(*recorder); ok {
			rec.route = route.Path
		}
		if route.Protected != nil && route.Protected(r) {
			protected.ServeHTTP(w, r)
			return
		}
		checked.ServeHTTP(w, r)
	})
}

// pathValues returns the path parameters of a template from the matched request.
func pathValues(template string, r *http.Request) map[string]string {
	values := map[string]string{}
	for _, part := range strings.Split(template, "/") {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			name := part[1 : len(part)-1]
			values[name] = r.PathValue(name)
		}
	}
	return values
}
