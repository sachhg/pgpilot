package main

import "testing"

const sampleMetrics = `# HELP pgpilot_fence_fallbacks_total Reads sent to the primary.
# TYPE pgpilot_fence_fallbacks_total counter
pgpilot_fence_fallbacks_total 12
pgpilot_routing_decisions_total{reason="read",target="replica"} 88
pgpilot_routing_decisions_total{reason="write",target="primary"} 10
go_goroutines 21
`

func TestMetricValue(t *testing.T) {
	cases := map[string]float64{
		"pgpilot_fence_fallbacks_total":                                   12,
		`pgpilot_routing_decisions_total{reason="read",target="replica"}`: 88,
		"go_goroutines":          21,
		"pgpilot_missing_metric": 0,
	}
	for series, want := range cases {
		if got := metricValue(sampleMetrics, series); got != want {
			t.Errorf("metricValue(%q) = %v, want %v", series, got, want)
		}
	}
}

func TestFallbackRate(t *testing.T) {
	// 12 of (88 replica reads + 12 fallbacks) = 12/100 fell back.
	before := fallbackCounters{}
	after := fallbackCounters{replicaReads: 88, fallbacks: 12}
	if got := fallbackRate(before, after); got != 0.12 {
		t.Errorf("fallbackRate = %v, want 0.12", got)
	}
}

func TestFallbackRate_NoReads(t *testing.T) {
	if got := fallbackRate(fallbackCounters{}, fallbackCounters{}); got != 0 {
		t.Errorf("fallbackRate with no reads = %v, want 0", got)
	}
}
