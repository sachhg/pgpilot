# Changelog

All notable changes to pgpilot are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Repository scaffolding (Phase 0): MIT license, README with problem statement
  and roadmap, Makefile (`build`, `test`, `lint`, `up`, `down`, `bench`),
  `golangci-lint` v2 configuration, and a GitHub Actions CI pipeline running
  build, `go test -race`, and lint on every push and pull request.
- `cmd/pgpilot` entrypoint skeleton.
- Local development cluster (Phase 1): a `docker-compose.yml` bringing up one
  Postgres 16 primary and two streaming replicas over real physical
  replication, with dedicated replication slots, a seeded test schema, and a
  standby bootstrap via `pg_basebackup`. `make up` produces a working cluster
  and `make smoke` asserts replication. Rationale in
  `docs/adr/0001-dev-cluster-replication.md`.
- Transparent proxy (Phase 2): `internal/proxy` accepts client connections,
  refuses SSL/GSS negotiation with `N`, and pipes bytes bidirectionally with
  graceful, leak-free shutdown. An integration test (`make itest`) asserts that
  psql through the proxy behaves identically to psql direct. Rationale in
  `docs/adr/0002-transparent-proxy-ssl-refusal.md`.
- Protocol codec (Phase 3): `internal/protocol` decodes the wire messages
  pgpilot routes on via `jackc/pgx/v5/pgproto3`, with round-trip tests and a
  panic-safe, fuzzed decoder. The proxy relays messages frame-by-frame and
  tracks each session's transaction status from ReadyForQuery. `cmd/pgpilot`
  gains a `-log-level` flag. Rationale in
  `docs/adr/0003-message-aware-relay.md`.
- Connection pool (Phase 4a): `internal/pool` is a bounded, health-checked pool
  of backend connections — configurable max size, acquire timeout, idle timeout
  with a background reaper, and per-connection health checks — that applies
  backpressure instead of queueing acquirers without bound.
- SCRAM authentication and session pooling (Phase 4b): `internal/scram`
  implements SCRAM-SHA-256 for both roles; `internal/backend` opens and
  authenticates connections to the primary with SCRAM, resets them for reuse,
  and manages one pool per `(user, database)`; `internal/config` loads pgpilot's
  JSON configuration. The proxy authenticates each client with SCRAM-SHA-256,
  acquires a pooled backend, replays startup parameters, and relays the session,
  resetting and reusing the backend on a clean disconnect. `cmd/pgpilot` runs
  from a `-config <file>`. Validated against real PostgreSQL and psql. Rationale
  in `docs/adr/0004-auth-termination-and-pooling.md`.
- Transaction pooling and feature detection (Phase 4c): a `pool.mode`
  configuration selects `session` or `transaction` pooling. In transaction mode
  a backend is returned to the pool between transactions (cleared with `DISCARD
  ALL`). `internal/detect` uses `pg_query` (the real PostgreSQL parser, never
  string matching) to flag statements that break transaction pooling — prepared
  statements, temporary tables, `LISTEN`/`NOTIFY`, and session GUCs — and pins
  such a session, and any extended-query-protocol session, to its backend so
  those features keep working. `make itest` validates both modes against real
  psql, including pinning and multiplexing. Rationale in
  `docs/adr/0005-transaction-pooling-and-feature-detection.md`.

### Dependencies

- Added `github.com/jackc/pgx/v5` v5.7.1 (pinned to keep the module's `go 1.22`
  floor) for its `pgproto3` wire-protocol codec.
- Promoted `golang.org/x/crypto` to a direct dependency for `pbkdf2`, used by
  the SCRAM implementation.
- Added `github.com/pganalyze/pg_query_go/v6` v6.2.2 for query feature
  detection. This uses **v6 rather than the v5** BUILD.md names because v5's last
  release fails to compile against recent macOS SDKs; v6 fixes the build and
  keeps the `go 1.22` floor. pg_query is cgo, so the build now needs a C
  compiler.
