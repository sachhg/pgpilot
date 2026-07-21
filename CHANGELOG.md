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
  and `make smoke` asserts that a row written to the primary is replayed and
  served by both replicas. Rationale recorded in
  `docs/adr/0001-dev-cluster-replication.md`.
- Transparent proxy (Phase 2): `internal/proxy` accepts client connections,
  refuses SSL/GSS negotiation with `N`, forwards the startup and authentication
  exchange to the primary untouched, and pipes bytes bidirectionally with
  graceful, leak-free shutdown. `cmd/pgpilot` now runs the proxy (`-listen`,
  `-primary`). An integration test (`make itest`) asserts that psql through the
  proxy behaves identically to psql direct. Rationale recorded in
  `docs/adr/0002-transparent-proxy-ssl-refusal.md`.
- Protocol codec (Phase 3): `internal/protocol` decodes the wire messages
  pgpilot routes on (Query, Parse, Bind, Describe, Execute, Sync, Terminate,
  ReadyForQuery, CommandComplete, ErrorResponse, RowDescription, DataRow) via
  `jackc/pgx/v5/pgproto3`, with round-trip tests and a panic-safe, fuzzed
  decoder. The proxy now relays messages frame-by-frame instead of as opaque
  bytes and tracks each session's transaction status from ReadyForQuery
  (`I`/`T`/`E`). `cmd/pgpilot` gains a `-log-level` flag. Rationale recorded in
  `docs/adr/0003-message-aware-relay.md`.

### Dependencies

- Added `github.com/jackc/pgx/v5` v5.7.1 (pinned to keep the module's `go 1.22`
  floor) for its `pgproto3` wire-protocol codec.
