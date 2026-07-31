//go:build integration

package integration

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sachhg/pgpilot/internal/config"
	"github.com/sachhg/pgpilot/internal/metrics"
	"github.com/sachhg/pgpilot/internal/router"
)

// TestProxy_MetricsReflectTraffic drives a read and a write through a real
// routing proxy and asserts the Prometheus metrics record where each went. A
// series in the exposition output exists only once its labels have been
// observed, so the presence of read/replica and write/primary series is proof
// the traffic was classified and routed, and instrumented, correctly.
func TestProxy_MetricsReflectTraffic(t *testing.T) {
	compose := requireCluster(t)
	resumeReplication(t, compose)
	m := metrics.New()
	port := startRoutingProxy(t, config.FenceRelaxed, router.NewRoundRobin(), m)

	if _, err := runPsql(compose, "host.docker.internal", port, "SELECT 1"); err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := runPsql(compose, "host.docker.internal", port,
		"CREATE TABLE IF NOT EXISTS metrics_probe (id int)"); err != nil {
		t.Fatalf("write: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()

	want := []string{
		`pgpilot_routing_decisions_total{reason="read",target="replica"}`,
		`pgpilot_routing_decisions_total{reason="write",target="primary"}`,
		`pgpilot_query_duration_seconds_count{target="replica"}`,
		`pgpilot_query_duration_seconds_count{target="primary"}`,
		"pgpilot_sessions_opened_total",
	}
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("metrics missing %q", w)
		}
	}
}
