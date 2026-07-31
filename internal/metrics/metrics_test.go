package metrics_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sachhg/pgpilot/internal/metrics"
)

// scrape renders the current metrics as Prometheus text.
func scrape(t *testing.T, m *metrics.Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("scrape status = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

func TestMetrics_RecordsEvents(t *testing.T) {
	m := metrics.New()
	m.SessionOpened()
	m.SessionOpened()
	m.SessionOpened()
	m.SessionClosed()
	m.RoutingDecision(metrics.TargetPrimary, metrics.ReasonWrite)
	m.RoutingDecision(metrics.TargetReplica, metrics.ReasonRead)
	m.FenceFallback()
	m.ObserveQueryLatency(metrics.TargetReplica, 0.01)

	body := scrape(t, m)
	want := []string{
		"pgpilot_sessions_opened_total 3",
		"pgpilot_sessions_closed_total 1",
		"pgpilot_sessions_active 2",
		`pgpilot_routing_decisions_total{reason="write",target="primary"} 1`,
		`pgpilot_routing_decisions_total{reason="read",target="replica"} 1`,
		// FenceFallback counts both its own metric and a routing decision.
		"pgpilot_fence_fallbacks_total 1",
		`pgpilot_routing_decisions_total{reason="fence_fallback",target="primary"} 1`,
		`pgpilot_query_duration_seconds_count{target="replica"} 1`,
		// Go runtime collector is registered.
		"go_goroutines",
	}
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("scrape missing %q", w)
		}
	}
}

func TestMetrics_HistogramBucketsSpanRange(t *testing.T) {
	m := metrics.New()
	// A sub-millisecond read and a multi-second read must both land in buckets.
	m.ObserveQueryLatency(metrics.TargetPrimary, 0.0003)
	m.ObserveQueryLatency(metrics.TargetPrimary, 5.0)
	body := scrape(t, m)
	if !strings.Contains(body, `pgpilot_query_duration_seconds_count{target="primary"} 2`) {
		t.Errorf("expected two primary observations recorded:\n%s", firstLines(body, "pgpilot_query_duration_seconds"))
	}
}

func firstLines(body, prefix string) string {
	var out []string
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
