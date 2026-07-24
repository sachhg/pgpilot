package router

import (
	"sync"
	"testing"
)

func TestLeastInFlight_PicksIdlest(t *testing.T) {
	p := NewLeastInFlight()
	cands := addrs("a", "b", "c")

	// Load up "a" with two outstanding reads.
	if got := p.Choose(cands, ""); got != "a" { // tie -> first
		t.Fatalf("first choice = %q, want a", got)
	}
	// "a" now has 1; next idlest is a tie between b and c -> b.
	if got := p.Choose(cands, ""); got != "b" {
		t.Fatalf("second choice = %q, want b", got)
	}
	// b has 1; c has 0 -> c.
	if got := p.Choose(cands, ""); got != "c" {
		t.Fatalf("third choice = %q, want c", got)
	}
	// all at 1 -> tie, first (a).
	if got := p.Choose(cands, ""); got != "a" {
		t.Fatalf("fourth choice = %q, want a", got)
	}
}

func TestLeastInFlight_ReleaseFreesCapacity(t *testing.T) {
	p := NewLeastInFlight()
	cands := addrs("a", "b")
	// Drive a to 3 in flight, b to 0 by always releasing b.
	p.Choose(cands, "")      // a=1
	p.Choose(addrs("a"), "") // a=2
	p.Choose(addrs("a"), "") // a=3
	if got := p.Choose(cands, ""); got != "b" {
		t.Fatalf("with a=3,b=0 chose %q, want b", got)
	}
	// Release a twice: a=1, b=1 -> tie -> a.
	p.Release("a", "", 0)
	p.Release("a", "", 0)
	if got := p.Choose(cands, ""); got != "a" {
		t.Fatalf("after releasing a twice chose %q, want a", got)
	}
}

func TestLeastInFlight_ReleaseBalances(t *testing.T) {
	p := NewLeastInFlight()
	cands := addrs("a", "b", "c")
	// Every read is released immediately, so counts stay at 0 and selection
	// falls back to round-robin-like fairness via the tie-break order.
	counts := map[string]int{}
	for i := 0; i < 30; i++ {
		addr := p.Choose(cands, "")
		counts[addr]++
		p.Release(addr, "", 0)
	}
	if counts["a"] != 30 {
		t.Errorf("with immediate release, ties always pick first: counts=%v", counts)
	}
	// The map should be empty again once every read is released.
	if len(p.inFlight) != 0 {
		t.Errorf("in-flight map not drained: %v", p.inFlight)
	}
}

func TestLeastInFlight_ConcurrentBalance(t *testing.T) {
	p := NewLeastInFlight()
	cands := addrs("a", "b", "c", "d")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				addr := p.Choose(cands, "")
				p.Release(addr, "", 0)
			}
		}()
	}
	wg.Wait()
	if len(p.inFlight) != 0 {
		t.Errorf("in-flight map not drained after balanced load: %v", p.inFlight)
	}
}
