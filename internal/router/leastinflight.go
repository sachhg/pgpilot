package router

import (
	"sync"
	"time"
)

// LeastInFlight sends each read to the candidate currently serving the fewest
// outstanding reads, spreading load by observed occupancy rather than by blind
// rotation. It adapts to replicas that are slow or briefly overloaded — a
// backend whose reads are piling up stops being chosen until it drains — without
// needing to know anything about the query itself.
//
// In-flight counts are kept per address, incremented on Choose and decremented
// on the paired Release. An address absent from the map is serving zero reads.
type LeastInFlight struct {
	mu       sync.Mutex
	inFlight map[string]int
}

// NewLeastInFlight returns a ready least-in-flight policy.
func NewLeastInFlight() *LeastInFlight {
	return &LeastInFlight{inFlight: make(map[string]int)}
}

// Choose returns the candidate with the fewest reads in flight, breaking ties in
// favor of the earlier candidate so selection is deterministic for a stable
// candidate order.
func (p *LeastInFlight) Choose(candidates []Candidate, _ string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	best := candidates[0].Addr
	bestN := p.inFlight[best]
	for _, c := range candidates[1:] {
		if n := p.inFlight[c.Addr]; n < bestN {
			best, bestN = c.Addr, n
		}
	}
	p.inFlight[best]++
	return best
}

// Release decrements the in-flight count for addr. Latency is ignored;
// least-in-flight weighs occupancy, not duration.
func (p *LeastInFlight) Release(addr, _ string, _ time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inFlight[addr] <= 1 {
		delete(p.inFlight, addr)
		return
	}
	p.inFlight[addr]--
}

// NeedsFingerprint reports false; least-in-flight ignores the query shape.
func (p *LeastInFlight) NeedsFingerprint() bool { return false }

// Name identifies the policy.
func (p *LeastInFlight) Name() string { return "least-in-flight" }
