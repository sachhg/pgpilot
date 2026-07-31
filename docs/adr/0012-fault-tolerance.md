# 12. Fault tolerance: read failover and fault injection

Date: 2026-07-31

## Status

Accepted.

## Context

A router sits in the data path, so a backend failure must not become a client
failure when the router has somewhere else to send the work. Until now a routed
read that could not reach its chosen replica dropped the client — a reject if the
connection could not be acquired, a silent close if a pooled connection had been
severed — even with another replica or the primary healthy. This phase makes
reads survive a backend failure and builds the harness that proves it.

## Decision

**Fail a read over before any byte reaches the client.** `dispatchRead` acquires
a backend and writes the query inside a retry loop. If the acquire fails, or the
write fails before the response has begun, it excludes that backend and selects
the next eligible replica, then finally the primary, giving up only when every
candidate is gone. Because nothing has been streamed yet, the retry is invisible
to the client. Selection reuses the existing eligibility and policy logic with an
*exclude* set, so failover respects the fence and the routing policy.

**The pool already heals a severed idle connection.** On acquire, the pool
health-checks a reused connection and, on failure, discards it and dials a fresh
one. So a connection severed while idle is repaired by a re-dial to the same
(reachable) backend, and failover is reserved for a backend that is actually
unreachable.

**Writes and in-transaction statements do not fail over.** A write must go to the
primary and a pinned session must stay on its backend; neither has a healthy
alternative, so a failure there legitimately drops.

**A fault that strikes mid-response drops, by necessity.** Once relaying has
begun the client already holds part of the answer; pgpilot cannot re-run the
query elsewhere any more than a direct connection could. That is the boundary of
what failover can promise, and it is deliberate.

**Inject faults in-process, with no external tool.** `internal/faultproxy` is a
TCP forwarder that blackholes (refuses new connections — a backend down), severs
(drops live connections — a backend cut mid-query), and adds latency. Tests point
pgpilot's backends at fault proxies and toggle faults directly, with a long
health-poll interval so a fault does not immediately flip the registry —
exercising the failover path rather than plain health avoidance.

## Consequences

- Four invariants are asserted end to end: no read dropped while a healthy
  backend exists; severed connections lose no read; strict fencing never serves a
  stale read even with a replica down and the rest frozen; and no connection or
  goroutine leak under churn. Each failover increments `read_failovers_total`.
- Leak detection uses `runtime.NumGoroutine` settling to a post-startup baseline
  and the pool's own in-use count returning to zero, rather than adding a
  goroutine-leak dependency — the connection-count check is the exact and
  deterministic one, and it needs nothing new in go.mod.
- Failover adds no cost to the common path: the retry loop runs a single
  iteration when the first backend answers.
- Rejected: an external fault injector such as toxiproxy (a dependency and a
  process to manage for what a small in-process forwarder does); killing replica
  containers as the only fault (racy — the registry may notice before the test
  can, hiding the failover path); and a goroutine-leak library (the pool's in-use
  count already proves the property that matters).
