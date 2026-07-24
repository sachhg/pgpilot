# 9. Routing policy engine

Date: 2026-07-23

## Status

Accepted.

## Context

Phase 7 made pgpilot route reads to replicas that have replayed past the
session's fence, but among the replicas that qualify it simply took the first
one. On a cluster with more than one eligible replica that concentrates every
read on the same backend, wasting the rest and ignoring that replicas differ in
load, speed, and how expensive a given query is on each. This phase decides
*which* eligible replica serves a read.

Eligibility (healthy, and fresh enough under the fencing rules) already lives in
the proxy and is a correctness question. Selection among the eligible is a
performance question with real alternatives, so it belongs behind an interface.

## Decision

**A `Policy` interface, separate from eligibility.** `internal/router` defines
`Policy.Choose(candidates, fingerprint) addr` over the replicas the proxy has
already deemed eligible. Because eligibility is decided upstream, a policy never
needs a database or the registry: it is pure Go, exercised in unit tests and a
deterministic simulator with no cluster. Every `Choose` is paired with one
`Release(addr, fingerprint, latency)` so a load-aware policy tracks in-flight
work and observed latency; the single policy instance is shared across all
sessions so it sees the whole proxy's load.

**Three implementations, increasing in sophistication.**

- **round-robin** — even rotation. Fair over a stable candidate set, keeps no
  state, ignores load and query cost.
- **least-in-flight** — send each read to the eligible replica with the fewest
  outstanding reads. Adapts to a slow or briefly overloaded replica for free,
  because its reads pile up and it stops being chosen until it drains. This is
  the default: strong, zero-config, and it costs no per-read parse.
- **scored** — rank candidates by estimated completion time,
  `(inFlight + 1) * ewmaLatency(addr, fingerprint) + lagPenalty * lagSeconds`,
  and take the minimum. Keying the EWMA by pg_query fingerprint is the point: it
  lets the policy steer an *expensive* query shape away from a replica already
  busy with that shape while still sending cheap queries there, and it learns
  per-replica speed differences as slower backends accrue higher averages.

**least-in-flight is the default, scored is opt-in.** The scored policy needs a
pg_query fingerprint per read — a cgo parse — and only pays off when replicas or
query costs are heterogeneous. Least-in-flight already handles slow and
overloaded replicas without that cost, so it is the safe default; `routing.policy`
selects another, validated at load time against the router's own names.

## Consequences

- A deterministic discrete-event simulator (seeded arrivals, a heavy-tailed
  shape mix, one 3x-slow replica, each replica a single FIFO server) compares
  the policies with no database. It shows the failure it is meant to: blind
  round-robin forces a third of all reads onto the slow replica and its queue
  explodes (p95 into the seconds), while least-in-flight and scored back off it —
  and scored, alone knowing heavy shapes are dear there, sends it the fewest
  reads and wins the tail. Micro-benchmarks confirm per-decision cost stays
  sub-microsecond.
- The scored policy trades slightly higher median latency for a better tail: it
  concentrates load on the fast replicas to spare the slow one, so the fast
  replicas queue a little more at p50 while p95/p99 improve. That is the right
  trade for a router, but it is a trade.
- The EWMA folds in queueing time, not pure service time, because that is what
  the proxy can measure — the latency from dispatch to ReadyForQuery. Under load
  the estimate therefore runs a little hot, which reinforces the correct steering
  but is not a clean per-shape cost model. A server-reported cost would be
  cleaner and is future work.
- The scored weights are constants tuned for comparable replicas. A cluster with
  a large staleness budget or very heterogeneous hardware may want them tunable
  from config; they are exported so that remains an additive change.
- Rejected: random choice (no load awareness), lag-only weighting (blind to load
  and query cost — the very thing that overloads a replica), and power-of-two-
  choices (a good cheap approximation of least-in-flight, but with only a handful
  of replicas exact least-in-flight is affordable and strictly better).
