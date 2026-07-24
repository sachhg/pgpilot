# 5. Transaction pooling with pg_query feature detection

Date: 2026-07-23

## Status

Accepted.

## Context

Phase 4b gave pgpilot session pooling: a client holds one backend for its whole
session. Transaction pooling multiplexes many clients onto fewer backends by
returning a backend to the pool between transactions — but that breaks any
feature whose state lives on the connection across transactions: a prepared
statement, a temporary table, a LISTEN registration, or a session-level GUC. The
phase requires supporting both modes and refusing transaction pooling when such a
feature appears.

## Decision

**Two pool modes, selected by `pool.mode` in the config: `session` (default) and
`transaction`.** In transaction mode the proxy acquires a backend for a
transaction and returns it to the pool once the transaction reaches an idle
`ReadyForQuery`, clearing it first with a single `DISCARD ALL` so the next client
sees a clean connection.

**Detect state-leaking statements with `pg_query`, never string matching.** The
`internal/detect` package parses each simple-query string with the real
PostgreSQL grammar and flags `PREPARE`, `CREATE TEMP TABLE`, `LISTEN`/`NOTIFY`/
`UNLISTEN`, and session-level `SET`/`RESET` (`SET LOCAL` is transaction-scoped and
allowed). Anything unparseable is treated as breaking, conservatively.

**Refuse transaction pooling by pinning, not erroring.** When a breaking feature
is detected, the session is pinned to its current backend for the rest of its
life (the backend is not returned to the pool between transactions), so the
feature works correctly — just without transaction-level multiplexing for that
session. This is the graceful reading of "refuse to enter transaction mode": the
client's session keeps working rather than failing. A pinned backend is reset
with `DISCARD ALL` when the session finally ends.

**Pin the extended query protocol too.** The extended protocol (Parse/Bind/
Describe/Execute/Sync) spans several messages before a response and commonly
carries named prepared statements. Rather than track that state machine at
transaction granularity, a session that uses it is pinned and handed to the
continuous relay. Transaction multiplexing therefore applies to the simple query
protocol, which covers autocommit statements and explicit `BEGIN`/`COMMIT`
blocks.

**Use `pg_query_go` v6, not v5.** BUILD.md names `pg_query_go/v5`, but its last
release (v5.1.0) fails to compile against the macOS 15.4+ SDK — Postgres's
bundled `strchrnul` port collides with the now-declared system symbol. v6.2.2
fixes the build, exposes the same parse API pgpilot needs, and keeps the module's
`go 1.22` floor. This is a necessary deviation.

## Consequences

- `make itest` validates both modes against real psql: identical results, a
  session GUC and a temporary table that persist across statements (proving
  pinning), and multiplexing several sessions across a two-connection pool.
- pg_query is a cgo dependency, so the build now requires a C compiler; CI's
  `ubuntu-latest` and a stock macOS toolchain both provide one.
- Detection covers the four features BUILD.md names. Session-level advisory locks
  and `WITH HOLD` cursors are not detected; a session that relies on them in
  transaction mode outside an explicit transaction would lose them between
  transactions. This is a documented limitation for a future pass.
- Cancel-request handling and TLS termination remain deferred (ADRs 0002, 0004).
