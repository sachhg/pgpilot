package router

import (
	"sync/atomic"
	"time"
)

// RoundRobin hands out candidates in rotation, ignoring load and latency. It is
// the simplest fair policy: over a stable candidate set every replica receives
// an equal share of reads. Because the eligible set can change between calls
// (a replica may fall behind the fence or fail health checks), the rotation
// indexes into whatever candidates it is given rather than pinning positions.
type RoundRobin struct {
	next atomic.Uint64
}

// NewRoundRobin returns a ready round-robin policy.
func NewRoundRobin() *RoundRobin { return &RoundRobin{} }

// Choose returns the next candidate in rotation.
func (p *RoundRobin) Choose(candidates []Candidate, _ string) string {
	i := p.next.Add(1) - 1
	return candidates[i%uint64(len(candidates))].Addr
}

// Release is a no-op: round-robin keeps no per-backend state.
func (p *RoundRobin) Release(string, string, time.Duration) {}

// NeedsFingerprint reports false; round-robin ignores the query shape.
func (p *RoundRobin) NeedsFingerprint() bool { return false }

// Name identifies the policy.
func (p *RoundRobin) Name() string { return "round-robin" }
