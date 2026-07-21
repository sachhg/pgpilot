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

## Roadmap

| Phase | Focus                                                        |
| ----: | ------------------------------------------------------------ |
|     0 | Repo hygiene, CI, licensing                                  |
|     1 | Dev cluster: primary + 2 streaming replicas (docker-compose) |
|     2 | Transparent proxy (byte-level passthrough)                   |
|     3 | Protocol codec (typed frontend/backend messages)            |
|     4 | Connection pooling (session + transaction)                  |
|     5 | Query classification (read vs. write via pg_query)          |
|     6 | Replica registry, health polling, circuit breakers          |
|     7 | LSN fencing                                                  |
|     8 | Routing policy engine                                        |
|     9 | Observability (Prometheus, structured logs, pprof)          |
|    10 | Fault-injection harness                                      |
|    11 | Benchmarks vs. direct connection and pgbouncer              |
|    12 | Docs and the v0.1.0 release                                 |

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
make down    # tear the cluster down
make bench   # run benchmarks
```

## License

[MIT](LICENSE)
