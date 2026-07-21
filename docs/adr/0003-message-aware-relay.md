# 3. Message-aware relay: frame and forward raw, decode selectively

Date: 2026-07-21

## Status

Accepted

## Context

Phase 2 relayed a session as an opaque byte stream. To route queries (Phase 5)
and fence reads (Phase 7), pgpilot has to understand the PostgreSQL v3 wire
protocol — at minimum, where each message begins and ends, its type, and the
transaction status the backend reports in ReadyForQuery. Phase 3 introduces that
protocol awareness. Several decisions had real alternatives.

## Decision

**Use `jackc/pgx/v5/pgproto3` for message bodies, not a hand-rolled codec.**
The wire protocol has dozens of message types with fiddly encodings; pgproto3 is
the reference Go implementation, already a project dependency, and reimplementing
it would be pure risk. pgpilot pins pgx **v5.7.1** — the newest release whose
`go` directive stays at 1.22, keeping the "Go 1.22+" floor the project targets.

**Frame and forward raw bytes; decode only what is consumed.** The relay reads
each message's five-byte header (type + length), forwards the message
byte-for-byte, and decodes just the messages it acts on (today, ReadyForQuery
for transaction status). The alternative — decode every message into a struct
and re-encode it onto the wire — makes transparency hostage to pgproto3's
encode/decode being bit-exact for every message, and lets a decode bug corrupt a
session. Forwarding raw means pgpilot can never garble a session it does not
fully interpret, while still parsing what it needs.

**Stream messages larger than 64 KiB instead of buffering them.** A wide DataRow
or a large CopyData can be up to ~2 GiB. The relay buffers small bodies (so it
can hand them to the decoder) but streams larger ones straight through with
`io.CopyN`, bounding memory regardless of message size.

**Recover from decoder panics.** A fuzz target on the decoder (required by this
phase) immediately found that pgproto3 *panics* on some truncated bodies — an
empty Query body indexes past the end of the slice. Because pgpilot decodes
bytes straight off the wire, possibly from a hostile client, the decoder wraps
every pgproto3 `Decode` in a recover and returns an error instead. A panic in a
per-connection goroutine would otherwise crash the whole proxy.

## Consequences

- The proxy is now protocol-aware: it frames every message and tracks each
  session's transaction status (`I`/`T`/`E`), logged at debug level. The router
  will read this status to pin a session to one backend for the life of a
  transaction.
- Round-trip tests cover all twelve message types this phase names, and the
  decoder is fuzzed; the panic it surfaced is now a regression test.
- Transparency is preserved: the psql-through-proxy integration test still shows
  byte-identical behavior, including SCRAM authentication, which flows through as
  ordinary framed messages.
- Full per-message decoding in the live path is deliberately deferred to the
  phase that needs it (query classification); today only consumed messages are
  decoded, to avoid paying for work nothing yet reads.
