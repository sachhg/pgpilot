# 10. Observability: metrics, logging, and profiling

Date: 2026-07-31

## Status

Accepted.

## Context

pgpilot makes routing decisions a client cannot see: which backend served a
read, whether a read fell back to the primary, how loaded each pool is, how far
each replica lags. Operating it — and defending its behavior — needs those made
visible. This phase adds Prometheus metrics, a metrics/pprof endpoint, and
per-session log correlation, without coupling the whole codebase to a metrics
library.

## Decision

**A private registry, not the global default.** `internal/metrics` builds its
own `prometheus.Registry` and registers the Go runtime and process collectors on
it. Nothing uses the global `DefaultRegisterer`, so there is no package-level
state, tests scrape an isolated instance, and two proxies could run in one
process without colliding.

**Two kinds of metric, collected two ways.** Events — sessions opened/closed,
routing decisions, fence fallbacks, query latencies — are counters and
histograms updated inline where they happen. Current state — backend health and
lag, pool occupancy — are gauges that a scrape-time `Collector` reads fresh from
a `StateSource` on every scrape. Pushing gauges on a timer would let them go
stale between ticks and add a goroutine; pulling on scrape keeps them exactly as
current as the scrape itself.

**Labels chosen to stay bounded.** Routing decisions carry `target` (primary or
replica) and `reason` (write, read, fence_fallback, pinned, extended); query
latency carries only `target`. Deliberately *not* a label: the pg_query
fingerprint. The scored router keys latency by fingerprint internally, but a
fingerprint has unbounded cardinality, and one Prometheus series per query shape
would blow up the registry. Per-shape latency stays inside the router; the
exported histogram aggregates by target.

**A narrow `StateSource` interface decouples the packages.** The state collector
depends on two small structs and an interface, not on `registry` or `backend`.
`cmd/pgpilot` supplies the adapter. So the metrics package imports neither, and
the collector is tested with a fake source.

**The endpoint is separate and optional.** Metrics and pprof are served by their
own HTTP server on `observability.metrics_addr`, never on the Postgres listener;
an empty address binds nothing, and pprof is a further opt-in. The server shuts
down with the process.

**Latency is measured once, used twice.** The proxy already timed a routed read
from dispatch to ReadyForQuery to feed the router's EWMA; that same measurement
now also feeds the query-latency histogram, and every simple query is timed, not
just routed reads. Each routing decision is additionally logged at debug with
the client session id, so a metric anomaly can be traced to specific sessions.

## Consequences

- Query latency and routing metrics only populate in routing mode (replicas
  configured). As a plain primary-only pooler, pgpilot still exports sessions,
  pool saturation, backend health, and the Go/process metrics.
- The `reason="fence_fallback"` bucket also captures reads sent to the primary
  because every replica was unhealthy, not only because of the fence — both mean
  "no replica could serve this read", which is the operationally useful signal.
- Metrics add negligible overhead: counter and histogram updates are atomic, and
  the state collector only runs work when Prometheus scrapes.
- prometheus/client_golang is pinned to v1.20.5, the newest release that keeps
  the module's `go 1.22` floor; v1.21+ would drag it to 1.23, and the current
  latest to 1.25. This is the same "hold the floor" stance taken for pg_query.
- Rejected: the global default registry (hidden state, awkward to test); a
  per-fingerprint latency label (cardinality explosion); and timer-pushed gauges
  (staleness plus an extra goroutine, for no benefit over pull-on-scrape).
