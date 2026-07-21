# 1. Development cluster uses physical streaming replication

Date: 2026-07-21

## Status

Accepted

## Context

Phase 1 needs a local cluster that mirrors the topology pgpilot routes across:
one primary that accepts writes and two replicas that serve reads. The cluster
must exhibit real replication lag and expose a real replayed-LSN position,
because later phases (LSN fencing, lag-based routing, fault injection) are
meaningless against a fake or synchronously-consistent backend.

Several decisions had real alternatives.

## Decision

**Physical streaming replication, not logical.** The replicas are byte-for-byte
copies of the primary kept current by streaming WAL. This is what production
read-replica deployments use, and it is the only form that exposes a single,
cluster-wide `pg_last_wal_replay_lsn()` that Phase 7 can fence reads against.
Logical replication ships row changes per publication, has no whole-database
replay LSN to fence on, and does not replicate DDL — a poor fit for
read-your-writes routing.

**Official `postgres:16` image with an explicit standby entrypoint, not a
turnkey image.** The replica entrypoint clones the primary with `pg_basebackup
--write-recovery-conf` and then hands off to the stock postgres entrypoint. We
rejected opinionated images (e.g. Bitnami) that hide replication behind
environment variables: the project's goal is to make every mechanism
inspectable and defensible, and replication is exactly the mechanism the rest of
the system depends on.

**Dedicated physical replication slots, one per standby.** Slots make the
primary retain WAL until each standby has consumed it, so a briefly-disconnected
replica catches up by streaming instead of needing a full reclone. The
alternative, `wal_keep_size`, bounds retention by size and silently breaks a
replica that falls far enough behind.

**`trust` auth for replication, scoped to the compose network.** The containers
are reachable only on the isolated `pgnet` bridge and no replication port is
published, so password auth would add ceremony without adding safety here.
Production would use `scram-sha-256` over TLS with a per-standby credential.

**High host ports (55432–55434).** A local Postgres on 5432 is common — the
author's machine has one — and binding it would make `make up` fail. The
backends are internal to the proxy; the host mappings exist only for psql and
the smoke test.

## Consequences

- `make up` produces a real primary plus two streaming standbys; `make smoke`
  asserts that a primary write is replayed and served by both replicas.
- The cluster reproduces genuine replication lag, which Phases 6–10 rely on.
- `trust` replication auth must never be copied into a non-local deployment;
  this ADR records that boundary explicitly.
- Re-cloning a standby is just removing its volume (`make down -v`); the
  entrypoint re-bootstraps it on the next `make up`.
