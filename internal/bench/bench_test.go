package bench

import (
	"testing"
	"time"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestPercentile(t *testing.T) {
	// 1..100 ms.
	sorted := make([]time.Duration, 100)
	for i := range sorted {
		sorted[i] = ms(i + 1)
	}
	cases := []struct {
		p    float64
		want time.Duration
	}{
		{0.50, ms(50)},
		{0.95, ms(95)},
		{0.99, ms(99)},
		{1.0, ms(100)},
	}
	for _, c := range cases {
		if got := Percentile(sorted, c.p); got != c.want {
			t.Errorf("Percentile(%.2f) = %v, want %v", c.p, got, c.want)
		}
	}
}

func TestPercentile_Empty(t *testing.T) {
	if got := Percentile(nil, 0.95); got != 0 {
		t.Errorf("Percentile(empty) = %v, want 0", got)
	}
}

func TestSummarize(t *testing.T) {
	// 10 successful transactions of 1..10ms over a 1s wall clock, 3 errors.
	lat := []time.Duration{ms(5), ms(1), ms(10), ms(3), ms(7), ms(2), ms(9), ms(4), ms(8), ms(6)}
	r := Summarize("pgpilot", lat, 3, time.Second)

	if r.Target != "pgpilot" {
		t.Errorf("Target = %q", r.Target)
	}
	if r.Transactions != 10 || r.Errors != 3 {
		t.Errorf("counts = %d ok / %d err, want 10 / 3", r.Transactions, r.Errors)
	}
	if r.TPS != 10 {
		t.Errorf("TPS = %v, want 10", r.TPS)
	}
	if r.Mean != ms(55)/10 { // sum 55ms / 10 = 5.5ms
		t.Errorf("Mean = %v, want 5.5ms", r.Mean)
	}
	if r.P50 != ms(5) || r.P95 != ms(10) || r.P99 != ms(10) {
		t.Errorf("percentiles = %v/%v/%v, want 5ms/10ms/10ms", r.P50, r.P95, r.P99)
	}
}

func TestSummarize_EmptyIsSafe(t *testing.T) {
	r := Summarize("direct", nil, 0, time.Second)
	if r.Transactions != 0 || r.TPS != 0 || r.Mean != 0 || r.P50 != 0 {
		t.Errorf("empty summary = %+v, want zeros", r)
	}
}
