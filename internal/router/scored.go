package router

import (
	"math"
	"sync"
	"time"
)

// ScoredWeights tunes the Scored policy. The defaults suit a cluster of
// comparable replicas; a deployment with wildly heterogeneous hardware or a
// staleness budget may want to raise LagPenaltyMs.
type ScoredWeights struct {
	// Alpha is the EWMA smoothing factor in (0, 1]: higher reacts faster to a
	// replica's recent latency, lower is steadier.
	Alpha float64
	// DefaultServiceMs is the assumed per-read service time, in milliseconds, for
	// a (replica, query-shape) pair not yet observed.
	DefaultServiceMs float64
	// LagPenaltyMs is the estimated cost, in milliseconds, added per second of a
	// replica's replication lag, discouraging reads to replicas that trail.
	LagPenaltyMs float64
}

// DefaultScoredWeights returns the weights NewScored uses.
func DefaultScoredWeights() ScoredWeights {
	return ScoredWeights{Alpha: 0.2, DefaultServiceMs: 1.0, LagPenaltyMs: 50.0}
}

// Scored ranks candidates by an estimate of how quickly each would finish a new
// read, then picks the lowest. The estimate multiplies the replica's current
// in-flight count (plus the incoming read) by an exponentially weighted moving
// average of how long this query shape has taken on that replica, and adds a
// penalty for replication lag:
//
//	score = (inFlight + 1) * ewmaLatency(addr, fingerprint) + lagPenalty * lagSeconds
//
// Keying latency by fingerprint is what lets it steer *expensive* shapes away
// from busy replicas: a costly query on a replica already running two of them
// scores far worse than the same query on an idle one, even when a cheap query
// would happily use the busy replica. It learns per-replica speed differences
// for free — a slower replica accrues higher EWMAs and wins fewer reads.
type Scored struct {
	w ScoredWeights

	mu       sync.Mutex
	inFlight map[string]int
	latency  map[fpKey]float64 // milliseconds, per (addr, fingerprint)
}

type fpKey struct {
	addr        string
	fingerprint string
}

// NewScored returns a scored policy with the default weights.
func NewScored() *Scored { return NewScoredWith(DefaultScoredWeights()) }

// NewScoredWith returns a scored policy with explicit weights.
func NewScoredWith(w ScoredWeights) *Scored {
	return &Scored{
		w:        w,
		inFlight: make(map[string]int),
		latency:  make(map[fpKey]float64),
	}
}

// Choose returns the candidate with the lowest estimated completion time for a
// read of the given fingerprint, breaking ties toward the earlier candidate.
func (p *Scored) Choose(candidates []Candidate, fingerprint string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	best := ""
	bestScore := math.Inf(1)
	for _, c := range candidates {
		service := p.serviceLocked(c.Addr, fingerprint)
		score := float64(p.inFlight[c.Addr]+1)*service + p.w.LagPenaltyMs*c.LagSeconds
		if score < bestScore {
			best, bestScore = c.Addr, score
		}
	}
	p.inFlight[best]++
	return best
}

// Release clears the in-flight mark for addr and folds the read's measured
// latency into the EWMA for its (addr, fingerprint) pair.
func (p *Scored) Release(addr, fingerprint string, latency time.Duration) {
	ms := float64(latency) / float64(time.Millisecond)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inFlight[addr] <= 1 {
		delete(p.inFlight, addr)
	} else {
		p.inFlight[addr]--
	}
	key := fpKey{addr, fingerprint}
	prev, ok := p.latency[key]
	if !ok {
		prev = p.w.DefaultServiceMs
	}
	p.latency[key] = p.w.Alpha*ms + (1-p.w.Alpha)*prev
}

// serviceLocked returns the current latency estimate for a (addr, fingerprint)
// pair, falling back to the default before any observation. Caller holds p.mu.
func (p *Scored) serviceLocked(addr, fingerprint string) float64 {
	if v, ok := p.latency[fpKey{addr, fingerprint}]; ok {
		return v
	}
	return p.w.DefaultServiceMs
}

// NeedsFingerprint reports true; the scored policy keys latency by query shape.
func (p *Scored) NeedsFingerprint() bool { return true }

// Name identifies the policy.
func (p *Scored) Name() string { return "scored" }
