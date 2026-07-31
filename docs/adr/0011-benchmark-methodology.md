# 11. Benchmark methodology

Date: 2026-07-31

## Status

Accepted.

## Context

pgpilot claims to route reads without stale-read violations. That claim needs
numbers: how much does the proxy hop cost, how does it compare to the standard
pooler (pgbouncer), and how does the read-your-writes machinery behave under
write load. The benchmark has to be reproducible and, above all, honest about
where pgpilot loses.

## Decision

**Two tools: pgbench and a custom generator.** pgbench (its default TPC-B,
write-heavy) is the recognizable baseline everyone knows. But TPC-B is the wrong
shape to show a read-router's behavior, and pgbench does not report the
fence-fallback rate or true percentiles. So the primary tool is a custom Go load
generator (`cmd/loadgen`) that drives a read-heavy point-SELECT/point-UPDATE mix,
records every transaction's latency for exact p50/p95/p99, and reads pgpilot's
`/metrics` to report the fraction of reads that fell back to the primary.

**The simple query protocol, everywhere.** pgpilot routes only simple queries —
the extended protocol pins a session to the primary by design (ADR 0008) — so an
extended-protocol client would measure pgpilot as a primary-only pooler. The
generator forces `QueryExecModeSimpleProtocol`; this also spares pgbouncer's
transaction pooling the server-side prepared statements it dislikes, keeping the
three targets comparable.

**Targets and scenarios.** Three targets — a direct connection to the primary,
pgbouncer (transaction pooling), and pgpilot in both strict and relaxed fencing —
across two scenarios: read-only (write ratio 0) and read-heavy mixed (write ratio
0.2). Read-only shows pgpilot routing every read to a replica; mixed shows strict
fencing pulling reads back to the primary after a write.

**Honest framing: one machine, shared CPU.** All backends and the proxy run on
one host, so the primary and both replicas contend for the same cores. This
setup *cannot* show the horizontal read-scaling win that separate replica
hardware would give — it isolates proxy overhead and the fencing mechanism. The
README says so plainly, and reports the cases where pgpilot loses.

## Consequences

- The headline result is that pgpilot costs throughput: on this hardware it runs
  reads at roughly a third to a half of a direct connection and below pgbouncer,
  because every query is parsed and classified, reads take an extra hop, and each
  write costs a `pg_current_wal_lsn()` round-trip to advance the fence. That is
  the price of routing and read-your-writes, and it is published, not hidden.
- The mixed scenario shows the fence-fallback rate near 100% under 20% writes in
  strict mode: read-your-writes, working as designed, sends most reads to the
  primary, so pgpilot behaves like a slower primary-only pooler there. Relaxed
  mode keeps reads on the replicas at the cost of staleness.
- Long-lived pgpilot and pgbouncer pools can exhaust the primary's connection
  slots mid-run, which silently corrupted an early run. The driver sizes pools,
  gives pgpilot a short idle timeout, and drains between targets to stay within
  budget; a real deployment would raise `max_connections`.
- Charts are generated as pure-Go SVG and checked in, so there is no plotting
  dependency and the comparison renders in the README on GitHub.
- Rejected: the extended protocol (would defeat pgpilot's routing and mismeasure
  it); pgbench alone (no percentiles, no fence-fallback rate, wrong workload
  shape); and an external plotting toolchain (a dependency for a picture).
