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
- SCRAM authentication and session pooling (Phase 4b): pgpilot now terminates
  authentication and pools backend connections.
  - `internal/scram` implements SCRAM-SHA-256 for both the client and server
    roles; `internal/backend` opens and authenticates connections to the primary
    with SCRAM, resets them for reuse (`ROLLBACK`, then `DISCARD ALL`), and
    manages one pool per `(user, database)`; `internal/config` loads pgpilot's
    JSON configuration.
  - The proxy authenticates each client with SCRAM-SHA-256, acquires a pooled
    backend for the client's `(user, database)`, replays the backend's startup
    parameters, relays the session while tracking transaction status, intercepts
    the client's Terminate, and resets and returns the backend to the pool on a
    clean disconnect (discarding it otherwise).
  - `cmd/pgpilot` now runs from a `-config <file>` (replacing `-primary`); see
    `pgpilot.example.json`.
  - SCRAM is validated against real PostgreSQL (backend) and real psql
    (client); `make itest` asserts psql through pgpilot matches psql direct.
    Transaction pooling and feature detection follow in Phase 4c. Rationale in
    `docs/adr/0004-auth-termination-and-pooling.md`.

### Dependencies

- Added `github.com/jackc/pgx/v5` v5.7.1 (pinned to keep the module's `go 1.22`
  floor) for its `pgproto3` wire-protocol codec.
- Promoted `golang.org/x/crypto` to a direct dependency for `pbkdf2`, used by
  the SCRAM implementation.
