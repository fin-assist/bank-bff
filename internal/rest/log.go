package rest

import (
	"net/http"
	"strconv"
	"time"

	"github.com/fin-assist/go-grpc/reqctx"
	"google.golang.org/grpc/status"
)

// recorder captures the status for the access log and metrics. Unwrap keeps
// http.ResponseController (flush and deadlines of SSE) working through it.
type recorder struct {
	http.ResponseWriter
	status int
	route  string
	client string
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// accessLog writes one line per request and the request metrics. It logs the route
// pattern, never the raw path (it may carry IDs), headers, query or body.
func accessLog(cfg Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &recorder{ResponseWriter: w, route: "other", client: "none"}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		elapsed := time.Since(start)
		client := cfg.Metrics.clients.value(rec.client)
		cfg.Metrics.requests.WithLabelValues(rec.route, strconv.Itoa(rec.status), client).Inc()
		cfg.Metrics.duration.WithLabelValues(rec.route).Observe(elapsed.Seconds())
		cfg.Log.InfoContext(r.Context(), "request", "request_id", reqctx.RequestID(r.Context()), "method", r.Method, "route", rec.route,
			"status", rec.status, "duration_ms", elapsed.Milliseconds(), "client", client)
	})
}

func codeOf(err error) string { return status.Code(err).String() }
