package main

import (
	"strings"
	"testing"
	"time"

	"github.com/sachhg/pgpilot/internal/bench"
)

func TestSortTargets_CanonicalOrder(t *testing.T) {
	in := []bench.Result{
		{Target: "pgpilot-relaxed"},
		{Target: "direct"},
		{Target: "pgpilot-strict"},
		{Target: "pgbouncer"},
	}
	got := sortTargets(in)
	want := []string{"direct", "pgbouncer", "pgpilot-strict", "pgpilot-relaxed"}
	for i, w := range want {
		if got[i].Target != w {
			t.Errorf("position %d = %q, want %q", i, got[i].Target, w)
		}
	}
}

func TestMarkdownTable(t *testing.T) {
	rs := []bench.Result{
		{Target: "direct", TPS: 15000, P50: 400 * time.Microsecond, P95: time.Millisecond, P99: 2 * time.Millisecond},
		{Target: "pgpilot-strict", TPS: 7000, P50: time.Millisecond, P95: 2 * time.Millisecond, P99: 3 * time.Millisecond, FenceFallbackRate: 0.99},
	}
	table := markdownTable(rs)
	for _, want := range []string{"| target |", "| direct | 15000 |", "99.0%", "| pgpilot-strict | 7000 |"} {
		if !strings.Contains(table, want) {
			t.Errorf("table missing %q:\n%s", want, table)
		}
	}
	// A zero fence-fallback rate renders as a dash, not 0%.
	if !strings.Contains(table, "| — |") {
		t.Errorf("expected dash for zero fallback:\n%s", table)
	}
}

func TestLatencyChart_RendersTargets(t *testing.T) {
	rs := []bench.Result{
		{Target: "direct", P50: time.Millisecond, P95: 2 * time.Millisecond, P99: 3 * time.Millisecond},
	}
	svg := latencyChart("readonly", rs)
	for _, want := range []string{"<svg", "Latency — readonly", "p50", "p95", "p99", "direct"} {
		if !strings.Contains(svg, want) {
			t.Errorf("chart missing %q", want)
		}
	}
}
