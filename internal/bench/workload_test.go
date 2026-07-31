package bench

import (
	"math"
	"testing"
	"time"
)

func TestWorkload_IsWrite_Ratio(t *testing.T) {
	cases := []float64{0, 0.1, 0.2, 0.5, 0.9, 1}
	const n = 10000
	for _, ratio := range cases {
		w := Workload{WriteRatio: ratio}
		writes := 0
		for i := 0; i < n; i++ {
			if w.isWrite(i) {
				writes++
			}
		}
		got := float64(writes) / n
		if math.Abs(got-ratio) > 0.01 {
			t.Errorf("ratio %.2f produced %.3f writes, want ~%.2f", ratio, got, ratio)
		}
	}
}

func TestWorkload_IsWrite_KnownPattern(t *testing.T) {
	// At ratio 0.5 the deterministic spread writes on every odd-indexed
	// transaction: floor((n+1)/2) advances exactly when n is odd.
	w := Workload{WriteRatio: 0.5}
	want := []bool{false, true, false, true, false, true}
	for i, exp := range want {
		if got := w.isWrite(i); got != exp {
			t.Errorf("isWrite(%d) = %v, want %v", i, got, exp)
		}
	}
}

func TestWorkload_IsWrite_Extremes(t *testing.T) {
	none := Workload{WriteRatio: 0}
	all := Workload{WriteRatio: 1}
	for i := 0; i < 20; i++ {
		if none.isWrite(i) {
			t.Errorf("ratio 0 wrote at %d", i)
		}
		if !all.isWrite(i) {
			t.Errorf("ratio 1 read at %d", i)
		}
	}
}

// Compile-time reminder that Workload carries the fields the runner reads.
var _ = Workload{Duration: time.Second, Connections: 1, WriteRatio: 0.2, Rows: 100}
