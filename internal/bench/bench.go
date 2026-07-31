// Package bench is pgpilot's benchmark harness: a concurrent, read-heavy
// workload generator and the statistics and chart helpers that turn its raw
// per-transaction latencies into comparable results. It exists to measure
// pgpilot against a direct connection and against pgbouncer — throughput,
// latency percentiles, and, for pgpilot, the fence-fallback rate under lag.
package bench

import (
	"sort"
	"time"
)

// Result summarizes one benchmark run against one target.
type Result struct {
	// Target names what was benchmarked ("direct", "pgpilot", "pgbouncer").
	Target string `json:"target"`
	// Duration is the wall-clock time the workload ran.
	Duration time.Duration `json:"duration"`
	// Transactions is the count of successful transactions.
	Transactions int `json:"transactions"`
	// Errors is the count of failed transactions (excluded from latency stats).
	Errors int `json:"errors"`
	// TPS is successful transactions per second.
	TPS float64 `json:"tps"`
	// Mean and the percentiles are of successful-transaction latency.
	Mean time.Duration `json:"mean"`
	P50  time.Duration `json:"p50"`
	P95  time.Duration `json:"p95"`
	P99  time.Duration `json:"p99"`
	// FenceFallbackRate is the fraction of reads pgpilot routed to the primary
	// for lack of an eligible replica, from its metrics. Zero for other targets.
	FenceFallbackRate float64 `json:"fence_fallback_rate"`
}

// Percentile returns the p-quantile (0..1) of an ascending-sorted slice via
// nearest-rank. It returns zero for an empty slice.
func Percentile(sorted []time.Duration, p float64) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	rank := int(float64(n)*p + 0.999999) // ceil(n*p)
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}

// Summarize builds a Result from raw successful-transaction latencies (in any
// order), the error count, and the wall-clock duration of the run.
func Summarize(target string, latencies []time.Duration, errors int, wall time.Duration) Result {
	sorted := make([]time.Duration, len(latencies))
	copy(sorted, latencies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var sum time.Duration
	for _, l := range sorted {
		sum += l
	}
	n := len(sorted)
	r := Result{
		Target:       target,
		Duration:     wall,
		Transactions: n,
		Errors:       errors,
		P50:          Percentile(sorted, 0.50),
		P95:          Percentile(sorted, 0.95),
		P99:          Percentile(sorted, 0.99),
	}
	if wall > 0 {
		r.TPS = float64(n) / wall.Seconds()
	}
	if n > 0 {
		r.Mean = sum / time.Duration(n)
	}
	return r
}
