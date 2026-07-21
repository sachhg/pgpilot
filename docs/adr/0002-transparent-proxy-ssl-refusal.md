# 2. Transparent proxy: refuse TLS and pass startup/auth through untouched

Date: 2026-07-21

## Status

Accepted

## Context

Phase 2 puts pgpilot in the connection path for the first time. Before it can
route anything, it must accept a client the way PostgreSQL would, get that client
authenticated against a backend, and relay the session's bytes. Two decisions
here have real alternatives.

### TLS negotiation

A libpq client (psql with the default `sslmode=prefer`) opens a connection by
sending an `SSLRequest` — a startup packet whose version field is the sentinel
`80877103` — before any StartupMessage. Some clients send a `GSSENCRequest`
(`80877104`) first as well. The server answers with a single byte: `S` to
proceed under TLS, or `N` to continue in cleartext.

Options:

1. **Refuse with `N`** and let the client fall back to cleartext.
2. **Terminate TLS at the proxy** — present a certificate, decrypt, and forward
   cleartext (or re-encrypt) to the backend.
3. **Pass TLS through** to the backend transparently.

### Startup and authentication

PostgreSQL authenticates with SCRAM-SHA-256 by default: a multi-message
challenge/response bound to the exact StartupMessage parameters. A proxy that
inserts itself into that exchange must either implement it or relay it.

## Decision

**Refuse SSL/GSS with `N` for now, and document it.** The proxy replies `N` to
every `SSLRequest`/`GSSENCRequest` and then reads the client's real
StartupMessage. Keeping the wire path in cleartext lets later phases parse the
protocol (Phase 3) without first solving TLS termination. This is a deliberate
Phase-2 limitation, not the end state — option 2 (terminate TLS, likely with SNI
and per-backend policy) is the intended direction, which is exactly why the
negotiation is handled explicitly rather than piped blindly.

**Pass the startup packet and the whole authentication exchange through to the
primary untouched.** The proxy forwards the client's StartupMessage byte-for-byte
and then relays raw bytes in both directions, so SCRAM runs directly between the
client and the backend. The proxy never sees or needs credentials, and it cannot
corrupt an exchange it does not interpret. Parsing individual messages is
deferred to Phase 3, where it is needed to classify queries and track
transaction state.

## Consequences

- Clients must allow a non-TLS connection to pgpilot. With `sslmode=prefer` (the
  libpq default) the fallback is automatic; `sslmode=require` will fail until TLS
  termination lands. The README calls this out.
- Because authentication is end-to-end, pgpilot works with any auth method the
  backend is configured for — including SCRAM — without implementing any of it.
- Forwarding is byte-transparent, so `CancelRequest` packets and every startup
  parameter flow through unmodified.
- Each session runs two copy goroutines plus a watcher bound to the server's
  context. On shutdown the context is cancelled, both connections are closed, and
  `Serve` waits for every session to drain, so no goroutine leaks — covered by a
  race-enabled shutdown test.
