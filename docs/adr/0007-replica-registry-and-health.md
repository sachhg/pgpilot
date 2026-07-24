# 7. Replica registry, health polling, and circuit breaking

Date: 2026-07-23

## Status

Accepted.

## Context

Before pgpilot can route a read to a replica it has to know which backends are
up, which is the primary, and how far behind each replica is. This phase builds
that: a background poller that tracks each backend's recovery state and
replication lag, a circuit breaker that takes a failing backend out of rotation
and probes for its recovery, and config reload so the replica set can change
without a restart. The router that consumes this comes later.

## Decision

**Poll each backend directly with one query.** For every backend the poller runs
a single statement that works on both a primary and a standby:

```sql
SELECT pg_is_in_recovery(),
  COALESCE(CASE WHEN pg_is_in_recovery() THEN pg_last_wal_replay_lsn()
                ELSE pg_current_wal_lsn() END::text, '0/0'),
  CASE WHEN pg_is_in_recovery()
       THEN EXTRACT(EPOCH FROM (now() - pg_last_xact_replay_timestamp()))
       ELSE 0 END
```

The `CASE` avoids calling `pg_current_wal_lsn()` on a standby (it errors in
recovery) and `pg_last_wal_replay_lsn()` on the primary (it is NULL there).
Polling each backend for its own view is simpler than reading
`pg_stat_replication` on the primary, and it keeps working when the primary is
unreachable.

**Lag in both bytes and seconds.** Byte lag is the primary's current WAL position
minus the replica's replayed position, computed in the registry by parsing each
`pg_lsn` into a 64-bit offset. Seconds lag comes from the server:
`now() - pg_last_xact_replay_timestamp()`. The two answer different questions —
bytes is how much WAL is unreplayed, seconds is how old the last replayed
transaction is — and the seconds metric has a known quirk: on an idle,
fully-caught-up cluster it grows because no new transactions arrive, even though
byte lag is zero. The router will weigh both.

**A per-backend circuit breaker with exponential backoff.** Consecutive poll
failures increment a counter; at a threshold the backend is marked unhealthy
(the breaker trips) so the router will skip it. While failing, the poller backs
off exponentially (bounded) instead of hammering the backend, and a single
successful poll closes the breaker.

**Config reload without restart, scoped to the registry.** On `SIGHUP` pgpilot
re-reads the config file and reconfigures the poller's backend set, so replicas
can be added or removed live. Changing the listen address, users, or pool
settings still requires a restart; live-reloading those is deferred.

## Consequences

- The registry runs in the binary today (visible with `-log-level debug`) and is
  a tested library — unit-tested against a fake backend and integration-tested
  against the real primary and two replicas — ready for the routing engine.
- Health polling uses the first configured user's credentials and connects to a
  database of the same name.
- The seconds-lag idle quirk is documented; consumers should prefer byte lag for
  "how far behind" and treat seconds lag as a staleness bound.
