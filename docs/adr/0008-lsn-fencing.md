# 8. LSN fencing for read-your-writes routing

Date: 2026-07-23

## Status

Accepted.

## Context

pgpilot's reason to exist is routing reads to replicas *without* serving a stale
read of the session's own write. Lag-based routing ("only use a replica when lag
is low") is probabilistic. This phase makes it exact: after a session writes,
its subsequent reads may only go to a replica that has provably replayed that
write. It also turns pgpilot from a primary-only pooler into a router.

## Decision

**Route per query, classify with `internal/classify`.** When replicas are
configured, a session no longer holds one backend. Outside a transaction each
simple query is classified: writes go to the primary, reads go to an eligible
replica (or the primary if none qualifies). An explicit transaction is pinned to
one backend — `BEGIN` classifies as a write, so the whole transaction runs on the
primary — and a query that leaves session state (a session GUC, temp table,
prepared statement, LISTEN) or the extended query protocol pins the session to
its backend, reusing the transaction-pooling machinery.

**Advance a session fence after writes.** When a write-classified query commits
on the primary, pgpilot reads `pg_current_wal_lsn()` on that connection and
stores the maximum as the session's fence — the WAL position a later read must
not read behind. Reads never advance the fence, so a read that falls back to the
primary does not make later reads "sticky" to it.

**Three fencing modes.**

- **strict** (default) — a replica may serve a read only once its *replayed* LSN
  is at or past the fence. The registry's polled replay LSN is a safe lower
  bound (replicas only move forward), so if the registry says a replica has
  reached the fence, it truly has; the cost is that a read within one poll
  interval of a write may fall back to the primary.
- **bounded** — a replica within `bounded_ms` of replication lag may serve the
  read, trading strict read-your-writes for wider replica use.
- **relaxed** — any healthy replica may serve the read (lag-only routing).

## Consequences

- The acceptance test proves it: with replication paused via
  `pg_wal_replay_pause()`, a write followed by a read in the same session under
  strict mode returns the fresh value (the frozen replicas are behind the fence,
  so the read falls back to the primary), while a relaxed read is served the
  stale value from a frozen replica.
- Each write costs one extra `pg_current_wal_lsn()` round-trip. The registry's
  polled LSN, not a per-read query, decides replica eligibility, so reads add no
  round-trip; a fresher, per-read LSN check is a possible future refinement.
- Fencing is conservative: an explicit transaction or a bare `SET` also advances
  the fence even though it may not have written durable data. This is safe
  (never stale) and only sends some later reads to the primary; a data-write-
  precise fence is future work.
- Routing is active only when replicas are configured; without them pgpilot is
  the primary-only pooler of earlier phases. Which of several eligible replicas
  serves a read is "first eligible" for now — the routing policy engine (Phase 8)
  makes that choice smart.
