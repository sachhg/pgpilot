package router

import (
	"sync"
	"testing"
	"time"
)

// lagCand builds a candidate with a given replication lag.
func lagCand(addr string, lagSeconds float64) Candidate {
	return Candidate{Addr: addr, LagSeconds: lagSeconds}
}

func TestScored_ColdStartPrefersIdle(t *testing.T) {
	p := NewScored()
	cands := addrs("a", "b")
	// First read: tie at equal cold estimates -> first candidate.
	if got := p.Choose(cands, "fp"); got != "a" {
		t.Fatalf("cold first choice = %q, want a", got)
	}
	// "a" now has one in flight; "b" is idle -> "b" is cheaper.
	if got := p.Choose(cands, "fp"); got != "b" {
		t.Fatalf("second choice = %q, want b (a is loaded)", got)
	}
}

func TestScored_SteersExpensiveShapeToFasterReplica(t *testing.T) {
	p := NewScored()
	cands := addrs("fast", "slow")

	// Teach the policy that fingerprint "big" is cheap on "fast" and dear on
	// "slow" by observing several completions of each.
	for i := 0; i < 20; i++ {
		p.Choose([]Candidate{{Addr: "fast"}}, "big")
		p.Release("fast", "big", 5*time.Millisecond)
		p.Choose([]Candidate{{Addr: "slow"}}, "big")
		p.Release("slow", "big", 200*time.Millisecond)
	}

	// With both idle, a "big" read should now go to the fast replica.
	got := map[string]int{}
	for i := 0; i < 10; i++ {
		addr := p.Choose(cands, "big")
		got[addr]++
		p.Release(addr, "big", 5*time.Millisecond) // keep them idle between picks
	}
	if got["fast"] == 0 || got["slow"] > got["fast"] {
		t.Errorf("expensive shape not steered to fast replica: %v", got)
	}
}

func TestScored_ShapeAwareNotGlobal(t *testing.T) {
	p := NewScored()
	// "slow" is dear for shape "big" but cheap for shape "small".
	for i := 0; i < 20; i++ {
		p.Choose([]Candidate{{Addr: "slow"}}, "big")
		p.Release("slow", "big", 200*time.Millisecond)
		p.Choose([]Candidate{{Addr: "slow"}}, "small")
		p.Release("slow", "small", 1*time.Millisecond)
		p.Choose([]Candidate{{Addr: "fast"}}, "big")
		p.Release("fast", "big", 5*time.Millisecond)
		p.Choose([]Candidate{{Addr: "fast"}}, "small")
		p.Release("fast", "small", 5*time.Millisecond)
	}
	cands := addrs("fast", "slow")
	// A "small" read should favor "slow" (1ms) over "fast" (5ms), proving the
	// estimate is per shape and not a single per-replica number.
	got := map[string]int{}
	for i := 0; i < 10; i++ {
		addr := p.Choose(cands, "small")
		got[addr]++
		p.Release(addr, "small", 1*time.Millisecond)
	}
	if got["slow"] <= got["fast"] {
		t.Errorf("small shape should favor slow replica for that shape: %v", got)
	}
}

func TestScored_PenalizesLag(t *testing.T) {
	// Equal load and no latency history: the laggier replica should lose.
	p := NewScored()
	cands := []Candidate{lagCand("behind", 2.0), lagCand("current", 0.0)}
	if got := p.Choose(cands, "fp"); got != "current" {
		t.Errorf("lag not penalized: chose %q, want current", got)
	}
}

func TestScored_ReleaseDrainsInFlight(t *testing.T) {
	p := NewScored()
	cands := addrs("a", "b")
	for i := 0; i < 50; i++ {
		addr := p.Choose(cands, "fp")
		p.Release(addr, "fp", time.Millisecond)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.inFlight) != 0 {
		t.Errorf("in-flight not drained: %v", p.inFlight)
	}
}

func TestScored_Metadata(t *testing.T) {
	p := NewScored()
	if !p.NeedsFingerprint() {
		t.Error("scored policy should need the fingerprint")
	}
	if p.Name() != "scored" {
		t.Errorf("Name = %q, want scored", p.Name())
	}
}

func TestScored_ConcurrentIsRace(t *testing.T) {
	p := NewScored()
	cands := addrs("a", "b", "c")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				addr := p.Choose(cands, "fp")
				p.Release(addr, "fp", time.Duration(seed+j)*time.Microsecond)
			}
		}(i)
	}
	wg.Wait()
}
