package proxy

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sachhg/pgpilot/internal/classify"
	"github.com/sachhg/pgpilot/internal/metrics"
)

func scrapeMetrics(t *testing.T, m *metrics.Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)
	return rec.Body.String()
}

func TestRecordDecision_MapsEachCaseToAMetric(t *testing.T) {
	m := metrics.New()
	s := &session{metrics: m, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	const primary = "primary:5432"

	// Fresh write -> primary/write.
	s.recordDecision(true, classify.Write, primary, primary, false)
	// Fresh read routed to a replica -> replica/read.
	s.recordDecision(true, classify.Read, "replica:5432", primary, true)
	// Fresh read forced to the primary -> fence fallback.
	s.recordDecision(true, classify.Read, primary, primary, false)
	// In-transaction statement on the primary -> primary/pinned.
	s.recordDecision(false, classify.Write, primary, primary, false)
	// Pinned statement on a replica -> replica/pinned.
	s.recordDecision(false, classify.Read, "replica:5432", primary, false)

	body := scrapeMetrics(t, m)
	want := []string{
		`pgpilot_routing_decisions_total{reason="write",target="primary"} 1`,
		`pgpilot_routing_decisions_total{reason="read",target="replica"} 1`,
		`pgpilot_fence_fallbacks_total 1`,
		`pgpilot_routing_decisions_total{reason="fence_fallback",target="primary"} 1`,
		`pgpilot_routing_decisions_total{reason="pinned",target="primary"} 1`,
		`pgpilot_routing_decisions_total{reason="pinned",target="replica"} 1`,
	}
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("scrape missing %q", w)
		}
	}
}

func TestTargetLabel(t *testing.T) {
	if got := targetLabel("primary:5432", "primary:5432"); got != metrics.TargetPrimary {
		t.Errorf("targetLabel(primary) = %q, want primary", got)
	}
	if got := targetLabel("replica:5432", "primary:5432"); got != metrics.TargetReplica {
		t.Errorf("targetLabel(replica) = %q, want replica", got)
	}
}
