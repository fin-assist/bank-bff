package rest

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Fields of finassist_empty_required_field_total that this BFF serves
// (grpc-api.md 5.3); each gets a zero series at startup. MetricLock.text is a
// legacy typed metric, no longer served here, so it remains zero. Nested TileLock
// fields inside opaque Struct data are never inspected or counted by the BFF.
var emptyRequiredFields = []string{"Block.lock.text", "MetricLock.text", "GetAnalyticsResponse.currency", "Message.currency", "AnswerSnapshot.currency"}

// Metrics are the BFF metrics of the public API. Labels are bounded: routes are
// patterns, codes are registry names or "other", client versions are capped.
// Metrics is safe for concurrent use.
type Metrics struct {
	requests     *prometheus.CounterVec
	duration     *prometheus.HistogramVec
	unmapped     *prometheus.CounterVec
	untranslated *prometheus.CounterVec
	empty        *prometheus.CounterVec
	streams      *prometheus.GaugeVec
	rejected     *prometheus.CounterVec
	clients      *boundedLabels
	reasons      *boundedLabels
}

// NewMetrics registers the public API metrics in reg.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "finassist_http_requests_total",
			Help: "Public API responses by route pattern, HTTP status and client platform/MAJOR.MINOR (capped, then other).",
		}, []string{"route", "status", "client"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "finassist_http_request_duration_seconds",
			Help:    "Public API handling time by route pattern; SSE routes measure the whole stream.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 300},
		}, []string{"route"}),
		unmapped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "finassist_unmapped_error_total",
			Help: "Internal errors answered 500 because they are not complete registry errors of the operation.",
		}, []string{"reason", "grpc_code"}),
		untranslated: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "finassist_untranslated_code_total",
			Help: "Service values bank-bff cannot translate to a REST code or text.",
		}, []string{"field", "code"}),
		empty: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "finassist_empty_required_field_total",
			Help: "Empty required service fields replaced by a documented default.",
		}, []string{"field"}),
		streams: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "finassist_sse_streams",
			Help: "Open SSE streams by route pattern.",
		}, []string{"route"}),
		rejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "finassist_sse_rejected_total",
			Help: "Streaming requests refused by the BFF concurrency limits, by route pattern.",
		}, []string{"route"}),
		clients: newBoundedLabels(32),
		reasons: newBoundedLabels(32),
	}
	for _, c := range []prometheus.Collector{m.requests, m.duration, m.unmapped, m.untranslated, m.empty, m.streams, m.rejected} {
		if err := reg.Register(c); err != nil {
			return nil, err
		}
	}
	for _, f := range emptyRequiredFields {
		m.empty.WithLabelValues(f)
	}
	return m, nil
}

// Untranslated counts a service value without a REST code or text. code must be bounded.
func (m *Metrics) Untranslated(field, code string) {
	if m != nil {
		m.untranslated.WithLabelValues(field, code).Inc()
	}
}

// EmptyRequired counts an empty required field that got its documented default.
func (m *Metrics) EmptyRequired(field string) {
	if m != nil {
		m.empty.WithLabelValues(field).Inc()
	}
}

func (m *Metrics) unmappedError(reason, grpcCode string) {
	if m != nil {
		m.unmapped.WithLabelValues(m.reasons.value(reason), grpcCode).Inc()
	}
}

// boundedLabels admits at most limit distinct values; later ones become "other".
type boundedLabels struct {
	mu    sync.Mutex
	limit int
	seen  map[string]bool
}

func newBoundedLabels(limit int) *boundedLabels {
	return &boundedLabels{limit: limit, seen: make(map[string]bool)}
}

func (b *boundedLabels) value(v string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.seen[v] {
		return v
	}
	if len(b.seen) >= b.limit {
		return "other"
	}
	b.seen[v] = true
	return v
}

// StreamRejected counts a streaming request refused by its concurrency budget.
func (m *Metrics) StreamRejected(route string) {
	if m != nil {
		m.rejected.WithLabelValues(route).Inc()
	}
}

// StreamOpened counts an open stream until the returned function is called.
func (m *Metrics) StreamOpened(route string) func() {
	if m == nil {
		return func() {}
	}
	g := m.streams.WithLabelValues(route)
	g.Inc()
	return g.Dec
}
