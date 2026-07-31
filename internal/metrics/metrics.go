// Package metrics defines pgpilot's Prometheus instrumentation: the event
// counters and histograms the proxy updates inline (sessions, routing decisions,
// fence fallbacks, query latency) and, in state.go, a scrape-time collector for
// gauges that reflect current state (backend lag, pool saturation). Everything
// registers on a private registry so the collectors never touch global state and
// tests can scrape an isolated instance.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "pgpilot"

// Target label values: which kind of backend served a query or decision.
const (
	TargetPrimary = "primary"
	TargetReplica = "replica"
)

// Reason label values: why a query went where it did.
const (
	// ReasonWrite: a write, routed to the primary.
	ReasonWrite = "write"
	// ReasonRead: a read, routed to an eligible replica.
	ReasonRead = "read"
	// ReasonFenceFallback: a read sent to the primary because no replica was
	// eligible under the fence.
	ReasonFenceFallback = "fence_fallback"
	// ReasonPinned: a statement served by the session's already-pinned backend
	// (an open transaction, or a session-state statement).
	ReasonPinned = "pinned"
	// ReasonExtended: the extended query protocol, pinned to the primary.
	ReasonExtended = "extended"
)

// Metrics holds pgpilot's event instruments and the registry they live on.
type Metrics struct {
	reg *prometheus.Registry

	sessionsOpened prometheus.Counter
	sessionsClosed prometheus.Counter
	sessionsActive prometheus.Gauge
	routing        *prometheus.CounterVec
	fenceFallbacks prometheus.Counter
	queryLatency   *prometheus.HistogramVec
}

// New builds the metrics, registered on a fresh registry that also carries the
// Go runtime and process collectors (goroutines, memory, CPU, open FDs).
func New() *Metrics {
	m := &Metrics{
		reg: prometheus.NewRegistry(),
		sessionsOpened: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace, Name: "sessions_opened_total",
			Help: "Client sessions accepted since start.",
		}),
		sessionsClosed: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace, Name: "sessions_closed_total",
			Help: "Client sessions closed since start.",
		}),
		sessionsActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "sessions_active",
			Help: "Client sessions currently open.",
		}),
		routing: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "routing_decisions_total",
			Help: "Routing decisions by target backend and reason.",
		}, []string{"target", "reason"}),
		fenceFallbacks: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace, Name: "fence_fallbacks_total",
			Help: "Reads sent to the primary because no replica had replayed the fence.",
		}),
		queryLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Name: "query_duration_seconds",
			Help: "Simple-query round-trip latency, from dispatch to ReadyForQuery.",
			// ~0.5ms to ~16s, so p50/p95/p99 resolve across cheap and heavy reads.
			Buckets: prometheus.ExponentialBuckets(0.0005, 2, 16),
		}, []string{"target"}),
	}
	m.reg.MustRegister(
		m.sessionsOpened, m.sessionsClosed, m.sessionsActive,
		m.routing, m.fenceFallbacks, m.queryLatency,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// Registry returns the underlying registry, for wiring extra collectors or an
// alternate handler.
func (m *Metrics) Registry() *prometheus.Registry { return m.reg }

// Handler serves the metrics in Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{Registry: m.reg})
}

// SessionOpened records a newly accepted client session.
func (m *Metrics) SessionOpened() {
	m.sessionsOpened.Inc()
	m.sessionsActive.Inc()
}

// SessionClosed records a client session ending.
func (m *Metrics) SessionClosed() {
	m.sessionsClosed.Inc()
	m.sessionsActive.Dec()
}

// RoutingDecision records that a statement went to target for the given reason.
func (m *Metrics) RoutingDecision(target, reason string) {
	m.routing.WithLabelValues(target, reason).Inc()
}

// FenceFallback records a read that fell back to the primary for lack of an
// eligible replica. It also counts as a routing decision.
func (m *Metrics) FenceFallback() {
	m.fenceFallbacks.Inc()
	m.RoutingDecision(TargetPrimary, ReasonFenceFallback)
}

// ObserveQueryLatency records a simple query's round-trip latency in seconds
// against the target that served it.
func (m *Metrics) ObserveQueryLatency(target string, seconds float64) {
	m.queryLatency.WithLabelValues(target).Observe(seconds)
}
