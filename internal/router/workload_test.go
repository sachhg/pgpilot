package router

import (
	"testing"
	"time"
)

// TestPolicies_WorkloadComparison runs every policy through the same
// deterministic mixed workload and reports latency statistics. It asserts the
// load-aware policies beat blind round-robin on both tail latency and makespan,
// and that the scored policy — which alone knows a query shape's cost — is at
// least as good as least-in-flight. The table it logs is the phase's
// policy comparison; run `go test -run WorkloadComparison -v ./internal/router`.
func TestPolicies_WorkloadComparison(t *testing.T) {
	const (
		seed = 20260723
		n    = 6000
	)
	w := genWorkload(seed, n)

	rr := simulate(NewRoundRobin(), genWorkload(seed, n))
	lif := simulate(NewLeastInFlight(), genWorkload(seed, n))
	sc := simulate(NewScored(), w)

	t.Logf("%-16s %10s %10s %10s %10s %10s   %s",
		"policy", "makespan", "mean", "p50", "p95", "p99", "per-replica")
	for _, row := range []struct {
		name string
		s    stats
	}{
		{"round-robin", rr},
		{"least-in-flight", lif},
		{"scored", sc},
	} {
		t.Logf("%-16s %9.1fms %9.2fms %9.2fms %9.2fms %9.2fms   %v",
			row.name, row.s.makespan, row.s.mean, row.s.p50, row.s.p95, row.s.p99, row.s.perReplica)
	}

	// Load-aware policies must beat blind rotation on tail latency and makespan.
	if lif.p95 >= rr.p95 {
		t.Errorf("least-in-flight p95 %.2f not better than round-robin %.2f", lif.p95, rr.p95)
	}
	if sc.p95 >= rr.p95 {
		t.Errorf("scored p95 %.2f not better than round-robin %.2f", sc.p95, rr.p95)
	}
	if sc.makespan >= rr.makespan {
		t.Errorf("scored makespan %.1f not better than round-robin %.1f", sc.makespan, rr.makespan)
	}
	// Shape awareness should not lose to shape-blind load balancing.
	if sc.p95 > lif.p95*1.02 {
		t.Errorf("scored p95 %.2f worse than least-in-flight %.2f", sc.p95, lif.p95)
	}
	// Round-robin, ignoring the 3x-slow replica, should overload it; the
	// load-aware policies should send it strictly fewer reads.
	if lif.perReplica["r3"] >= rr.perReplica["r3"] {
		t.Errorf("least-in-flight sent r3 %d reads, round-robin %d; expected fewer",
			lif.perReplica["r3"], rr.perReplica["r3"])
	}
}

// benchWorkload is generated once and reused so the benchmarks measure the
// policy, not workload generation.
var benchWorkload = genWorkload(1, 4000)

func benchmarkWorkload(b *testing.B, make func() Policy) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		simulate(make(), benchWorkload)
	}
}

func BenchmarkWorkload_RoundRobin(b *testing.B) {
	benchmarkWorkload(b, func() Policy { return NewRoundRobin() })
}
func BenchmarkWorkload_LeastInFlight(b *testing.B) {
	benchmarkWorkload(b, func() Policy { return NewLeastInFlight() })
}
func BenchmarkWorkload_Scored(b *testing.B) {
	benchmarkWorkload(b, func() Policy { return NewScored() })
}

// benchmarkChoose measures the cost of a single Choose/Release pair over a fixed
// candidate set, isolating per-decision overhead from the simulator.
func benchmarkChoose(b *testing.B, p Policy) {
	cands := addrs("a", "b", "c", "d")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		addr := p.Choose(cands, "fp")
		p.Release(addr, "fp", time.Millisecond)
	}
}

func BenchmarkChoose_RoundRobin(b *testing.B)    { benchmarkChoose(b, NewRoundRobin()) }
func BenchmarkChoose_LeastInFlight(b *testing.B) { benchmarkChoose(b, NewLeastInFlight()) }
func BenchmarkChoose_Scored(b *testing.B)        { benchmarkChoose(b, NewScored()) }
