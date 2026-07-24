// Package router chooses which eligible replica serves a read. Eligibility —
// whether a replica is healthy and fresh enough under the fencing rules — is
// decided upstream in the proxy; a Policy only ranks the replicas that already
// qualify and picks one, balancing load across them.
//
// Policies are self-contained and safe for concurrent use: a load-aware policy
// tracks its own in-flight counts and latency estimates across Choose/Release
// calls, so the package has no database dependency and every policy can be
// exercised against a simulated backend in a plain unit test.
package router

import "time"

// Candidate is a replica eligible to serve a read, carrying the live signals a
// Policy may weigh. In-flight load and per-fingerprint latency are not supplied
// here — a Policy that cares about them accumulates them itself as reads are
// chosen and released.
type Candidate struct {
	// Addr is the replica's network address and the Policy's return value.
	Addr string
	// LagBytes and LagSeconds are the replica's replication lag as of the most
	// recent health poll.
	LagBytes   int64
	LagSeconds float64
}

// Policy selects one candidate to serve a read. Implementations must be safe for
// concurrent use by many sessions. Every Choose must be paired with exactly one
// Release of the returned address so that load-aware policies keep an accurate
// in-flight count; stateless policies treat Release as a no-op.
type Policy interface {
	// Choose picks a candidate for a read whose query fingerprint is fingerprint
	// (empty when NeedsFingerprint reports false) and returns its address,
	// marking one read in flight on that backend. candidates is never empty.
	Choose(candidates []Candidate, fingerprint string) string
	// Release records that a read chosen earlier for addr has completed after
	// latency, clearing its in-flight mark and folding latency into any estimate.
	Release(addr, fingerprint string, latency time.Duration)
	// NeedsFingerprint reports whether the policy consults the query fingerprint,
	// so a caller can skip the cost of computing one when it does not.
	NeedsFingerprint() bool
	// Name identifies the policy in logs and metrics.
	Name() string
}
