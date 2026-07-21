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
Today pgpilot is a **protocol-aware transparent proxy**: it forwards every
connection to a single primary, but it now frames and decodes the wire protocol
and tracks each session's transaction status. Read/write routing and fencing
arrive in later phases.

## Roadmap

| Phase | Focus                                                        | Status |
| ----: | ------------------------------------------------------------ | ------ |
|     0 | Repo hygiene, CI, licensing                                  | done   |
|     1 | Dev cluster: primary + 2 streaming replicas (docker-compose) | done   |
|     2 | Transparent proxy (byte-level passthrough)                   | done   |
|     3 | Protocol codec (typed frontend/backend messages)            | done   |
|     4 | Connection pooling (session + transaction)                  | next   |
|     5 | Query classification (read vs. write via pg_query)          |        |
|     6 | Replica registry, health polling, circuit breakers          |        |
|     7 | LSN fencing                                                  |        |
|     8 | Routing policy engine                                        |        |
|     9 | Observability (Prometheus, structured logs, pprof)          |        |
|    10 | Fault-injection harness                                      |        |
|    11 | Benchmarks vs. direct connection and pgbouncer              |        |
|    12 | Docs and the v0.1.0 release                                 |        |

## Technology

- Go 1.22+, standard library first
- [`jackc/pgx/v5/pgproto3`](https://github.com/jackc/pgx) — wire protocol codec
- [`pganalyze/pg_query_go/v5`](https://github.com/pganalyze/pg_query_go) — real
  Postgres parsing and query fingerprinting
- [`prometheus/client_golang`](https://github.com/prometheus/client_golang) —
  metrics

## Quick start

```sh
make build   # compile the binary into bin/
make test    # run tests with the race detector
make lint    # run golangci-lint
make up      # bring up the local primary + replica cluster
make smoke   # assert the cluster replicates (run after `make up`)
make itest   # assert psql through the proxy matches psql direct
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
with `pg_basebackup` on its first start, then streams WAL to stay current. The
seeded schema lives in `docker/primary/initdb/`.

```sh
make up                       # primary + 2 replicas, waits until healthy
make smoke                    # a write on the primary is served by both replicas
psql -h localhost -p 55432 -U pgpilot pgpilot   # connect to the primary
make down                     # stop and delete the cluster's volumes
```

The smoke test (`test/smoke`) writes a unique row to the primary, waits for each
replica to replay past the write's LSN, and asserts the row is readable there —
the same read-your-writes invariant pgpilot will enforce automatically once LSN
fencing lands in Phase 7.

The design decisions behind the cluster are recorded in
[`docs/adr/0001-dev-cluster-replication.md`](docs/adr/0001-dev-cluster-replication.md).

## Running the proxy

pgpilot accepts client connections and forwards them to a single upstream
primary. It refuses TLS (replying `N` to `SSLRequest`) and passes the startup
and authentication exchange through untouched, so any auth method the backend
uses — including SCRAM — works end-to-end.

```sh
make up                                   # backends on 55432–55434
make build && ./bin/pgpilot \
  -listen 127.0.0.1:6432 \
  -primary 127.0.0.1:55432 \
  -log-level debug                        # proxy on 6432 -> primary on 55432

# Connect through the proxy exactly as you would connect directly:
psql "host=localhost port=6432 dbname=pgpilot user=pgpilot sslmode=prefer"
```

Because TLS is refused for now, clients must permit a cleartext connection to
pgpilot; libpq's default `sslmode=prefer` falls back automatically, whereas
`sslmode=require` will fail until TLS termination arrives (see
[`docs/adr/0002-transparent-proxy-ssl-refusal.md`](docs/adr/0002-transparent-proxy-ssl-refusal.md)).
`make itest` asserts that a session through the proxy is byte-for-byte
equivalent to a direct one.

### Protocol awareness

Rather than piping opaque bytes, pgpilot frames every message on the wire and
decodes the ones it routes on, using `jackc/pgx`'s `pgproto3` (see
[`internal/protocol`](internal/protocol)). It tracks each session's transaction
status from the backend's ReadyForQuery indicator (`I`/`T`/`E`) — the signal the
router will use to pin a session to one backend for the life of a transaction.
Run with `-log-level debug` to watch the transitions:

```
level=DEBUG msg="transaction status" session=1 status=idle
level=DEBUG msg="transaction status" session=1 status="in transaction"
level=DEBUG msg="transaction status" session=1 status=idle
```

The message decoder is fuzzed and hardened against malformed input; the design
is recorded in
[`docs/adr/0003-message-aware-relay.md`](docs/adr/0003-message-aware-relay.md).

## License

[MIT](LICENSE)
