package bench

import "time"

// Workload configures the read-heavy benchmark: a mix of point SELECTs (which
// pgpilot routes to replicas) and point UPDATEs (which go to the primary and
// advance the session fence, so later reads on that session may fall back under
// lag). It is the workload where a read-router should shine over a plain pooler.
type Workload struct {
	// Duration is how long to drive load.
	Duration time.Duration
	// Connections is the number of concurrent client connections.
	Connections int
	// WriteRatio is the fraction of transactions that are UPDATEs, in [0, 1].
	WriteRatio float64
	// Rows is the size of the bench table (the id space reads and writes hit).
	Rows int
}

// isWrite decides whether the n-th transaction on a worker (0-based) is a write.
// Writes are spread deterministically by ratio — no RNG — so the read/write mix
// of a run is reproducible: a write occurs exactly when floor(n*ratio) advances.
func (w Workload) isWrite(n int) bool {
	if w.WriteRatio <= 0 {
		return false
	}
	if w.WriteRatio >= 1 {
		return true
	}
	return int(float64(n+1)*w.WriteRatio) != int(float64(n)*w.WriteRatio)
}
