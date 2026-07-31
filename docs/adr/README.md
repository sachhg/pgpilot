# Architecture Decision Records

Each ADR captures one decision with real alternatives: the context, what was
chosen, and what was rejected and why. They are the record of *why* pgpilot is
the way it is — the code shows the what.

| # | Decision | Phase |
| --: | --- | --: |
| [0001](0001-dev-cluster-replication.md) | Local dev cluster: a primary and two streaming replicas | 1 |
| [0002](0002-transparent-proxy-ssl-refusal.md) | A transparent proxy that refuses TLS/GSS with `N` | 2 |
| [0003](0003-message-aware-relay.md) | A message-aware relay over `pgproto3` | 3 |
| [0004](0004-auth-termination-and-pooling.md) | Terminate auth at the proxy; pool per `(user, database)` | 4 |
| [0005](0005-transaction-pooling-and-feature-detection.md) | Transaction pooling with pg_query feature detection | 4 |
| [0006](0006-query-classification.md) | Read/write classification from the parse tree | 5 |
| [0007](0007-replica-registry-and-health.md) | Replica registry, health polling, circuit breakers | 6 |
| [0008](0008-lsn-fencing.md) | LSN fencing for read-your-writes routing | 7 |
| [0009](0009-routing-policy-engine.md) | Routing policy engine (round-robin, least-in-flight, scored) | 8 |
| [0010](0010-observability.md) | Observability: metrics, logging, profiling | 9 |
| [0011](0011-benchmark-methodology.md) | Benchmark methodology | 11 |
| [0012](0012-fault-tolerance.md) | Fault tolerance: read failover and fault injection | 10 |

## Format

Each record has **Context** (the forces at play), **Decision** (what was chosen,
in the present tense), and **Consequences** (the results, including the rejected
alternatives). New decisions get the next number and are never rewritten — a
superseded decision is marked as such and a new ADR replaces it.
