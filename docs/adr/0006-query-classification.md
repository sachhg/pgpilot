# 6. Query classification for read/write routing

Date: 2026-07-23

## Status

Accepted.

## Context

To route a read to a replica and a write to the primary, pgpilot has to decide,
per query, which it is. The decision has to be correct on the cases that trip up
naive routers — a `SELECT ... FOR UPDATE`, a `SELECT` that writes through a CTE,
a `SELECT` that calls a volatile function, `EXPLAIN ANALYZE`, a multi-statement
simple query, and an explicit transaction block. This phase builds the
classifier; the router that consumes it comes later.

## Decision

**Classify from the parse tree with `pg_query`, never string matching.** BUILD.md
forbids regex or prefix classification, and for good reason: `SELECT` can hide a
write and `INSERT` can appear inside a read-shaped CTE. `internal/classify`
parses each statement with the real PostgreSQL grammar and inspects the tree.

**A routing class, not a data-modification fact, and conservative by default.**
`Class` is `Read` (may go to a replica) or `Write` (must go to the primary).
Anything the classifier cannot prove is a safe read — an unparseable string, an
unfamiliar statement type — is a `Write`, so a write is never sent to a replica.
A multi-statement query is a `Write` if any statement is.

**The tricky cases:**

- **Row locks** — a `SELECT` with a `FOR UPDATE`/`FOR SHARE`/`FOR NO KEY UPDATE`
  locking clause is a `Write`; locks must be taken on the primary.
- **Data-modifying CTEs** — a generic depth-first walk of the parse tree (via
  protobuf reflection) finds any `INSERT`/`UPDATE`/`DELETE`/`MERGE` node,
  wherever it hides, so `WITH x AS (INSERT ...) SELECT ...` is a `Write`.
- **Volatile functions** — the same walk finds function calls and matches them
  against a curated set (`nextval`, `setval`, `random`, …). True volatility lives
  in the catalog (`pg_proc.provolatile`); a curated list avoids a catalog lookup
  while covering the functions that matter, above all the sequence functions that
  must run on the primary. `SELECT nextval('s')` is a `Write`.
- **EXPLAIN** — `EXPLAIN` only plans, so it is a `Read`; `EXPLAIN ANALYZE`
  executes its statement, so it takes that statement's class.
- **Explicit transactions** — `BEGIN`/`COMMIT`/`ROLLBACK`/`SAVEPOINT` classify as
  `Write`, sending an explicit transaction to the primary. Combined with the
  transaction-status tracking from Phase 3, this keeps the whole transaction
  pinned to one backend until it ends.

## Consequences

- The classifier is a pure, table-driven-tested library covering every case
  above; it is ready for the routing engine (Phase 8) and is not yet wired into
  the live path, since there is nothing to route to until the replica registry
  (Phase 6) exists.
- Volatility detection is a curated heuristic, not a catalog lookup: a volatile
  user-defined function outside the list would be misrouted to a replica. A
  catalog-backed check is possible later.
- Explicit transactions always go to the primary, even read-only ones. Routing a
  `BEGIN READ ONLY` transaction to a replica is a future optimization.
