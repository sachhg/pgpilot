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
- Connection pooling (Phase 4): a bounded, health-checked pool (`internal/pool`)
  with backpressure; SCRAM-SHA-256 authentication on both sides
  (`internal/scram`, `internal/backend`), per-`(user, database)` pools, and a
  JSON config (`internal/config`); and session or transaction pooling selected
  by `pool.mode`, with `pg_query`-based detection (`internal/detect`) that pins
  a session using a feature transaction pooling would break. Validated against
  real PostgreSQL and psql in both modes. Rationale in ADRs 0004 and 0005.
- Query classification (Phase 5): `internal/classify` decides whether a query
  may be served by a replica (read) or must go to the primary (write) using
  `pg_query`, handling the cases that trip up naive routers. Table-driven tests
  cover every case. Rationale in `docs/adr/0006-query-classification.md`.
- Replica registry and health (Phase 6): `internal/registry` runs a background
  poller that queries the primary and each replica for recovery state, WAL
  position, and replay timestamp; it derives replication lag in bytes (from the
  LSN gap to the primary) and seconds (from the replay timestamp), and trips a
  per-backend circuit breaker with exponential backoff when polling fails,
  probing for recovery. The config gains `replicas` and `health` settings, and
  `SIGHUP` reloads the replica set without a restart. The registry runs in the
  binary (visible with `-log-level debug`) and is the health source the routing
  phase will consult. Rationale in
  `docs/adr/0007-replica-registry-and-health.md`.

### Dependencies

- Added `github.com/jackc/pgx/v5` v5.7.1 (pinned to keep the module's `go 1.22`
  floor) for its `pgproto3` wire-protocol codec.
- Promoted `golang.org/x/crypto` to a direct dependency for `pbkdf2`, used by
  the SCRAM implementation.
- Added `github.com/pganalyze/pg_query_go/v6` v6.2.2 (v6 rather than the v5
  BUILD.md names, because v5 no longer builds on recent macOS SDKs) for query
  feature detection and classification. pg_query is cgo, so the build now needs
  a C compiler. `google.golang.org/protobuf` is a direct dependency for the
  reflection-based parse-tree walk.
