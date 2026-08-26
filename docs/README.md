# dMem Documentation

dMem is a local Linux daemon backed by SQLite. Its current product surface
includes direct memory operations, raw-event processing and review, scoped
multi-channel retrieval, entities and relations, explicit conflict evolution,
feedback-derived ranking, reviewed aggregates, backup and restore, and a Go
library for logical import and export.

Schema v1 has no migration path. When the executable schema changes, initialize
a new data directory instead of reusing an incompatible database.

## Product Topics

- [API usage](api/usage.md): transport, headers, direct agent operations, and the broader resource API.
- [Architecture and data model](architecture/overview.md): authority, revisions, scopes, jobs, providers, and retrieval.
- [Entities, relations, conflicts, and evolution](architecture/evolution.md): reviewed changes and conflict resolution.
- [Feedback, ranking, and aggregation](architecture/feedback-and-aggregation.md): append-only feedback, ranking signals, and reviewed scene/topic memories.
- [Logical portability](storage/logical-portability.md): Format v1 export/import library and its consistency boundary.
- [Operations](operations/runbook.md): configuration, health, backup, restore, rebuilds, and failure handling.
- [Testing](development/testing-and-evaluation.md): current build profile and executable checks.

## Executable Contracts

- [OpenAPI 3.1](api/openapi.yaml) defines the HTTP API served over the Unix socket.
- [SQLite Schema v1](storage/schema-v1.sql) defines new-database initialization.

When prose and an executable contract disagree, use the executable contract.
[Architecture decisions](adr/README.md) preserve decision history and rationale;
they do not override the current contracts or implementation.
