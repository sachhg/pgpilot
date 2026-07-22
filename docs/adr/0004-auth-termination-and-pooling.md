# 4. Terminate authentication to enable connection pooling

Date: 2026-07-21

## Status

Accepted. Supersedes the connection-handling decision in
[ADR 0002](0002-transparent-proxy-ssl-refusal.md), which passed authentication
through untouched.

## Context

Session and transaction pooling reuse a backend connection across *different*
clients. A backend connection can only be reused if it is already authenticated,
which means the pooler — not the client — has to own the backend's
authentication, and therefore has to authenticate the client itself. ADR 0002's
pass-through model, where the client authenticates end-to-end with the backend,
makes reuse impossible: a pooled connection is already past its startup and
cannot replay a new client's handshake. Pooling forces this to change.

## Decision

**pgpilot terminates authentication on both sides, pgbouncer-style.**

- **Backend side (client role):** pgpilot opens its own connections to the
  primary and authenticates them with **SCRAM-SHA-256** (`internal/scram`
  client, driven by `internal/backend`). These connections are what the pool
  holds.
- **Client side (server role):** pgpilot verifies a connecting client with
  **SCRAM-SHA-256** (`internal/scram` server) before handing it a pooled
  backend. (This half is wired into the proxy in the next step of the phase.)

**Credentials come from a static config file** (`internal/config`, JSON): a list
of users with passwords, used both to verify clients and to open backend
connections. This is pgbouncer's "userlist" model. We rejected two alternatives:

- **`auth_query`** (fetch each user's SCRAM verifier from the backend at
  runtime) is more production-faithful but adds a bootstrap connection, verifier
  caching, and failure modes; overkill for this project's scope.
- **Cleartext client auth** would avoid implementing the SCRAM *server* but is a
  weak posture and would not exercise the interesting half of the protocol.

**Pools are keyed by `(user, database)`.** A connection authenticated as one user
against one database cannot serve another; separate pools keep them isolated,
matching how a client's identity maps to a backend session.

**Connections are reset before reuse** with `ROLLBACK` followed by `DISCARD ALL`,
sent as *separate* simple queries — combined into one multi-statement query they
would run inside an implicit transaction block, which `DISCARD ALL` forbids.
DISCARD ALL clears prepared statements, temp tables, LISTEN registrations, and
session GUCs, so a reused connection looks fresh. A connection that fails to
reset is discarded rather than reused.

## Consequences

- The SCRAM-SHA-256 implementation is validated against real PostgreSQL: the
  backend connector authenticates to the primary, and wrong passwords are
  rejected by the server.
- pgpilot now needs a config file (`-config`); the `-primary` flag is replaced.
  Config hot-reload is a Phase 6 concern.
- Because pgpilot authenticates clients itself, `sslmode=require` still fails
  (TLS termination remains future work, per ADR 0002), and cancel-request
  handling is deferred — a client's CancelRequest cannot yet be mapped to a
  pooled backend.
- Reset-based reuse assumes a well-behaved client that waits for its final
  ReadyForQuery before disconnecting; an abrupt mid-response disconnect causes
  the connection to be discarded rather than reused.
