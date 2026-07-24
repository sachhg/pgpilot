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

On top of fencing sits a pluggable **routing policy** that scores replicas by
replication lag, in-flight load, and a per-query-fingerprint latency estimate
learned online, so expensive query shapes are steered away from
already-loaded replicas.

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
Today pgpilot is an **authenticating connection pooler** that also tracks the
health of the cluster: it verifies each client with SCRAM-SHA-256, hands it a
pooled connection (session or transaction mode), and runs a background poller
that reports each backend's role and replication lag. The read/write classifier
and the health registry that will drive routing are built and tested; wiring
them into LSN fencing and a policy engine comes next.

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
|     7 | LSN fencing                                                  | next   |
|     8 | Routing policy engine                                        |        |
|     9 | Observability (Prometheus, structured logs, pprof)          |        |
|    10 | Fault-injection harness                                      |        |
|    11 | Benchmarks vs. direct connection and pgbouncer              |        |
|    12 | Docs and the v0.1.0 release                                 |        |

## Technology

- Go 1.22+, standard library first
- [`jackc/pgx/v5/pgproto3`](https://github.com/jackc/pgx) — wire protocol codec
- [`pganalyze/pg_query_go`](https://github.com/pganalyze/pg_query_go) — the real
  Postgres parser, for query feature detection and read/write classification.
  Uses v6 rather than v5, which no longer builds on recent macOS SDKs; this makes
  the build require a C compiler (cgo).
- [`prometheus/client_golang`](https://github.com/prometheus/client_golang) —
  metrics

## Quick start

```sh
make build   # compile the binary into bin/
make test    # run tests with the race detector
make lint    # run golangci-lint
make up      # bring up the local primary + replica cluster
make smoke   # assert the cluster replicates (run after `make up`)
make itest   # assert psql through the pooler matches psql direct
make down    # tear the cluster down
make bench   # run benchmarks
```

## Development cluster

`make up` brings up a real replication topology with docker-compose: one
Postgres 16 primary and two streaming replicas.

| Service    | Host port | Role                          |
| ---------- | --------: | ----------------------------- |
| `primary`  |     55432 | accepts writes                |
| `replica1` |     55433 | read-only hot standby         |
| `replica2` |     55434 | read-only hot standby         |

(Host ports use a high range so they never collide with a local Postgres on
5432; the backends are internal to the proxy and exposed only for convenience.)

The primary is configured for physical streaming replication with a dedicated
replication slot per standby. Each replica bootstraps by cloning the primary
with `pg_basebackup`, then streams WAL to stay current. The design decisions are
recorded in
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
  "health": {"interval": "1s", "failure_threshold": 3, "base_backoff": "1s", "max_backoff": "30s"}
}
```

```sh
make up                                       # backends on 55432–55434
make build && ./bin/pgpilot \
  -config pgpilot.example.json \
  -log-level debug

# Connect through pgpilot exactly as you would connect directly:
psql "host=localhost port=6432 dbname=pgpilot user=pgpilot sslmode=prefer"
```

pgpilot **verifies the client** with SCRAM-SHA-256 against the password in the
config, then hands the session a pooled connection it opened to the primary
(also with SCRAM). Connections are keyed by `(user, database)` and drawn from a
bounded pool that applies backpressure. Because TLS is refused for now, clients
must permit a cleartext connection (`sslmode=prefer` falls back automatically).
See [`docs/adr/0004-auth-termination-and-pooling.md`](docs/adr/0004-auth-termination-and-pooling.md).

### Pool modes

`pool.mode` selects how long a client keeps a backend:

- **`session`** (default) — a client holds one backend for its whole session.
- **`transaction`** — a backend returns to the pool *between transactions*, with
  `pg_query`-based detection that **pins** a session using a feature transaction
  pooling would break (prepared statements, temp tables, `LISTEN`/`NOTIFY`,
  session GUCs). See
  [`docs/adr/0005-transaction-pooling-and-feature-detection.md`](docs/adr/0005-transaction-pooling-and-feature-detection.md).

### Health and replication lag

A background poller (`internal/registry`) queries the primary and every replica
for recovery state and replication lag — in **bytes** (the WAL gap to the
primary) and **seconds** (age of the last replayed transaction) — and trips a
per-backend **circuit breaker** with exponential backoff when a backend stops
responding, probing for its recovery. `SIGHUP` reloads the replica set without a
restart. Run with `-log-level debug` to watch the polls; the design is recorded
in
[`docs/adr/0007-replica-registry-and-health.md`](docs/adr/0007-replica-registry-and-health.md).

### Read/write classification

`internal/classify` decides, from a query's parse tree, whether it may be served
by a replica or must run on the primary — correctly handling `SELECT ... FOR
UPDATE`, data-modifying CTEs, volatile functions, `EXPLAIN ANALYZE`,
multi-statement queries, and explicit transactions. It is the engine the routing
phases will consume; see
[`docs/adr/0006-query-classification.md`](docs/adr/0006-query-classification.md).

## License

[MIT](LICENSE)
