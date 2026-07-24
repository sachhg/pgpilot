# pgpilot

> A transparent, LSN-fencing PostgreSQL connection router.

[![CI](https://github.com/sachhg/pgpilot/actions/workflows/ci.yml/badge.svg)](https://github.com/sachhg/pgpilot/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

## The problem

Read replicas scale reads, but they lag. The moment you route a read to a
replica you risk a *read-your-writes* violation: a user updates their profile,
the next page load lands on a replica that has not yet replayed that write, and
they see stale data. The usual mitigation — "only send reads to a replica when
its lag is low" — is probabilistic. It reduces the odds of a stale read; it does
not eliminate them.

## What pgpilot is

pgpilot is a proxy that speaks the PostgreSQL wire protocol, sits between
clients and a primary/replica cluster, and routes each query to the best
backend. Its distinguishing feature is **LSN fencing**: after a write commits,
pgpilot records the primary's WAL position (LSN) as that session's fence, and a
subsequent read is only sent to a replica that has *provably replayed at or past
that LSN*. Otherwise the read falls back to the primary. This delivers
read-your-writes consistency by construction rather than by hoping replication
lag stays low.

### Consistency modes

- **strict** — always fence; a read never observes a value older than this
  session's most recent write.
- **bounded** — allow staleness up to N milliseconds.
- **relaxed** — lag-only routing, no fencing.

## Non-goals

Sharding, query rewriting, multi-master, and a GUI are explicitly out of scope.
The goal is to do one thing — correct, observable read/write routing — well.

## Status

Early development, built in phases (see the roadmap). Not production-ready yet.
pgpilot now **routes**: it authenticates each client with SCRAM-SHA-256, pools
connections, classifies each query, and sends writes to the primary and reads to
a replica — enforcing read-your-writes with a per-session LSN fence — and
balances reads across eligible replicas with a selectable routing policy
(round-robin, least-in-flight, or latency-scored). Observability comes next.

## Roadmap

| Phase | Focus                                                        | Status |
| ----: | ------------------------------------------------------------ | ------ |
|     0 | Repo hygiene, CI, licensing                                  | done   |
|     1 | Dev cluster: primary + 2 streaming replicas (docker-compose) | done   |
|     2 | Transparent proxy (byte-level passthrough)                   | done   |
|     3 | Protocol codec (typed frontend/backend messages)            | done   |
|     4 | Connection pooling (session + transaction)                  | done   |
|     5 | Query classification (read vs. write via pg_query)          | done   |
|     6 | Replica registry, health polling, circuit breakers          | done   |
|     7 | LSN fencing                                                  | done   |
|     8 | Routing policy engine                                        | done   |
|     9 | Observability (Prometheus, structured logs, pprof)          | next   |
|    10 | Fault-injection harness                                      |        |
|    11 | Benchmarks vs. direct connection and pgbouncer              |        |
|    12 | Docs and the v0.1.0 release                                 |        |

## Technology

- Go 1.22+, standard library first
- [`jackc/pgx/v5/pgproto3`](https://github.com/jackc/pgx) — wire protocol codec
- [`pganalyze/pg_query_go`](https://github.com/pganalyze/pg_query_go) — the real
  Postgres parser, for query classification and feature detection. Uses v6
  rather than v5, which no longer builds on recent macOS SDKs; this makes the
  build require a C compiler (cgo).
- [`prometheus/client_golang`](https://github.com/prometheus/client_golang) —
  metrics

## Quick start

```sh
make build   # compile the binary into bin/
make test    # run tests with the race detector
make lint    # run golangci-lint
make up      # bring up the local primary + replica cluster
make smoke   # assert the cluster replicates (run after `make up`)
make itest   # assert psql through pgpilot matches psql direct, and fencing holds
make down    # tear the cluster down
```

## Development cluster

`make up` brings up a real replication topology with docker-compose: one
Postgres 16 primary and two streaming replicas (host ports 55432–55434). The
primary uses physical streaming replication with a dedicated slot per standby;
each replica bootstraps with `pg_basebackup`. Design in
[`docs/adr/0001-dev-cluster-replication.md`](docs/adr/0001-dev-cluster-replication.md).

## Running the proxy

pgpilot reads a JSON config file (see [`pgpilot.example.json`](pgpilot.example.json)):

```json
{
  "listen": "127.0.0.1:6432",
  "primary": "127.0.0.1:55432",
  "replicas": ["127.0.0.1:55433", "127.0.0.1:55434"],
  "users": [{"name": "pgpilot", "password": "pgpilot"}],
  "pool": {"mode": "session", "max_size": 10, "acquire_timeout": "5s", "idle_timeout": "5m"},
  "health": {"interval": "1s", "failure_threshold": 3, "base_backoff": "1s", "max_backoff": "30s"},
  "fencing": {"mode": "strict", "bounded_ms": 100},
  "routing": {"policy": "least-in-flight"}
}
```

```sh
make up
make build && ./bin/pgpilot -config pgpilot.example.json -log-level debug
psql "host=localhost port=6432 dbname=pgpilot user=pgpilot sslmode=prefer"
```

pgpilot verifies the client with SCRAM-SHA-256, then pools SCRAM-authenticated
connections to the backends, keyed by `(user, database)`. TLS is refused for now,
so clients must permit a cleartext connection (`sslmode=prefer` falls back
automatically). See
[`docs/adr/0004-auth-termination-and-pooling.md`](docs/adr/0004-auth-termination-and-pooling.md).

### Routing and LSN fencing

When `replicas` are configured, pgpilot **routes** each query: it classifies it
with `pg_query`, sends writes to the primary and reads to a replica, and pins an
explicit transaction to one backend. It enforces **read-your-writes** with a
per-session LSN fence — after a write commits on the primary, the session's fence
advances to the primary's WAL position, and a subsequent read only goes to a
replica that has replayed at or past that fence (else it falls back to the
primary). `fencing.mode` selects the trade-off:

- **`strict`** (default) — a replica serves a read only once it has replayed the
  fence.
- **`bounded`** — a replica within `bounded_ms` of lag may serve the read.
- **`relaxed`** — any healthy replica may (lag-only routing).

`make itest` includes an acceptance test that pauses replication with
`pg_wal_replay_pause()` and asserts a write followed by a read never observes a
stale value under strict mode. Design in
[`docs/adr/0008-lsn-fencing.md`](docs/adr/0008-lsn-fencing.md).

### Routing policies

Fencing decides which replicas *may* serve a read; `routing.policy` decides which
one *does* when more than one qualifies:

- **`round-robin`** — even rotation across eligible replicas.
- **`least-in-flight`** (default) — the eligible replica with the fewest reads
  outstanding, so a slow or overloaded replica sheds load until it drains.
- **`scored`** — ranks replicas by estimated completion time,
  `(inFlight + 1) * ewmaLatency(addr, shape) + lagPenalty * lag`, learning each
  query shape's cost per replica (keyed by pg_query fingerprint) so it steers
  expensive shapes away from busy replicas. It costs one fingerprint parse per
  read, which is why it is opt-in.

`go test -run WorkloadComparison -v ./internal/router` runs a deterministic
simulation comparing the policies on a synthetic mixed workload with no database.
Design in
[`docs/adr/0009-routing-policy-engine.md`](docs/adr/0009-routing-policy-engine.md).

### Health and replication lag

A background poller (`internal/registry`) tracks each backend's role and
replication lag (bytes and seconds) and trips a per-backend circuit breaker with
exponential backoff when a backend stops responding. `SIGHUP` reloads the replica
set without a restart. See
[`docs/adr/0007-replica-registry-and-health.md`](docs/adr/0007-replica-registry-and-health.md).

### Pool modes

`pool.mode` is `session` (a client holds one backend for its session) or
`transaction` (a backend returns to the pool between transactions), with
`pg_query`-based pinning of sessions that use a feature transaction pooling would
break. See
[`docs/adr/0005-transaction-pooling-and-feature-detection.md`](docs/adr/0005-transaction-pooling-and-feature-detection.md).

## License

[MIT](LICENSE)
