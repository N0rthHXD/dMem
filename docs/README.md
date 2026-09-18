# dMem Documentation

dMem is a local Linux daemon backed by SQLite. Its current product surface
includes direct memory operations, raw-event processing and review, scoped
keyword and semantic primary recall with gated typed-graph features and budgeted
context selection, entities, entity facts, and relations, explicit conflict
evolution, feedback-derived ranking, reviewed scene/topic/timeline aggregates,
decision telemetry and explain output, backup and restore, and logical import
and export.

Semantics live in protocol-independent Memory Gateway modules. The first inbound
protocol adapter is an Agent-neutral REST+JSON `http.Handler`. The runtime
composes that handler but does not start a production listener; transport, TLS,
authentication, and network exposure are separate future work.

Schema v1 has no migration path. When the executable schema changes, initialize
a new data directory instead of reusing an incompatible database.

## Product Topics

- [HTTP REST+JSON contract](api/http-rest-contract.md): the frozen wire contract,
  routes, DTOs, headers, decoding rules, and error model.
- [API usage](api/usage.md): how the gateway semantics map onto the contract.
- [Architecture and data model](architecture/overview.md): authority, revisions,
  scopes, jobs, providers, gateway modules, and retrieval.
- [Memory Gateway refactor design](architecture/memory-gateway-refactor.md):
  module boundaries, capability ports, and dependency rules.
- [Entities, relations, conflicts, and evolution](architecture/evolution.md):
  reviewed changes, entity facts, and conflict resolution.
- [Feedback, ranking, and aggregation](architecture/feedback-and-aggregation.md):
  append-only feedback, ranking signals, decision telemetry, and reviewed
  scene/topic/timeline memories.
- [Logical portability](storage/logical-portability.md): Format v1 export/import
  library and its consistency boundary.
- [Operations](operations/runbook.md): configuration, initialization, backup,
  restore, rebuilds, and failure handling.
- [Testing](development/testing.md): current build profile and executable checks.

## Executable Contracts

- [HTTP REST+JSON contract](api/http-rest-contract.md) defines the Agent-neutral
  `/v1` surface implemented by `internal/protocol/httpapi`.
- [SQLite Schema v1](storage/schema-v1.sql) defines new-database initialization.

When prose and an executable contract disagree, use the executable contract.
[Architecture decisions](adr/README.md) preserve decision history and rationale;
they do not override the current contracts or implementation.
