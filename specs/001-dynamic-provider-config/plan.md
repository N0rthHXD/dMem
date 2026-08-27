# Implementation Plan: Dynamic Provider Configuration

**Branch**: `001-dynamic-provider-config` | **Date**: 2026-08-27 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `specs/001-dynamic-provider-config/spec.md`

## Summary

Replace all setting-specific CLI flags and configuration environment variables with one strict TOML file selected from the XDG default location or one global `--config` locator. Add atomic runtime reload for processing and embedding providers and their policies through the private UDS API and `SIGHUP`, with immutable configuration snapshots, independent capability identities, generation-fenced work, dynamic space-policy inheritance, explicit `blocked_policy` jobs, and a semantic corpus limited to currently eligible memories. The project is pre-release: old configuration syntax, schemas, identities, and state machines are replaced directly without compatibility or migration code.

## Technical Context

**Language/Version**: Go 1.24

**Primary Dependencies**: Go standard library, `github.com/mattn/go-sqlite3`, `github.com/getkin/kin-openapi`, and a direct TOML decoder dependency with strict unknown-field diagnostics

**Storage**: One authoritative SQLite database per instance; one user-owned TOML configuration file; rebuildable on-disk vector index

**Testing**: Go unit and integration tests, deterministic local provider servers, OpenAPI contract validation, schema contract tests, race tests, and existing Makefile quality gates

**Target Platform**: Linux local single-user daemon over a private Unix domain socket

**Project Type**: Single Go CLI and local HTTP/JSON daemon

**Performance Goals**: Explicit reload returns an activated or no-change result within 10 seconds before background reconciliation; 95% of newly eligible blocked jobs are queued within 30 seconds; normal config reads add no lock contention or network calls to provider-backed work

**Constraints**: Offline by default; one configuration source; no secret values in the file, database, status, audit, or logs; complete-file atomic activation; old generations cannot publish; provider calls remain outside write transactions; local authority and keyword retrieval remain available during provider failure or reload

**Scale/Scope**: Two independently managed provider capabilities, up to 32 workers, queue limits up to 1,000,000 jobs, dynamic user/agent/application/project/task/session hierarchies, bounded reconciliation batches, and one active semantic index generation per instance

## Constitution Check

*GATE: Passed before research and re-checked after design.*

| Principle or constraint | Design response | Status |
|-------------------------|-----------------|--------|
| Durable Authority Before Derivation | Configuration changes never mutate events, memories, revisions, evidence, or state history. Reconciliation only changes jobs, eligibility projections, embeddings, and index metadata. | PASS |
| Versioned, Auditable Memory | Provider and policy generations are auditable; automatic processing still creates reviewable candidates/proposals and cannot perform destructive authority updates. | PASS |
| Local, Private, Offline by Default | Providers default disabled; reload and status use the private UDS; secrets remain references; policy evaluation precedes secret resolution and network activity. | PASS |
| Explicit Scope and Bounded Retrieval | Space policy uses explicit assignments and documented ancestor inheritance; siblings never inherit; semantic query policy evaluates the complete normalized scope. | PASS |
| Deterministic Verification | Strict TOML, canonical identities, generation fencing, fixed state transitions, OpenAPI/schema contracts, deterministic provider tests, and race tests are specified. | PASS |
| SQLite authority and one writer | Activation generations, blocked work, eligibility, and reconciliation cursors are committed through the existing single writer. | PASS |
| Provider calls outside write transactions | Snapshot acquisition and policy preparation precede calls; only fenced result publication uses a short transaction. | PASS |
| Durable worker leases and generation fencing | Existing lease fencing remains and is extended with configuration and policy generation checks. | PASS |
| Explicit compatibility lifecycle | This initialization-stage change directly replaces schema and configuration grammar; only current config version `1` is accepted, with no compatibility branch. | PASS |
| API and schema synchronization | New configuration endpoints are defined in `contracts/openapi-config.yaml`; implementation must update public OpenAPI plus embedded/documented schemas together. | PASS |

Post-design review found no constitutional violation. Complexity is required by explicit invariants: one immutable runtime snapshot avoids mixed configuration, while durable reconciliation ensures crash recovery without extending activation transactions.

## Project Structure

### Documentation (this feature)

```text
specs/001-dynamic-provider-config/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   ├── config.example.toml
│   ├── config-schema.md
│   ├── cli.md
│   └── openapi-config.yaml
└── tasks.md
```

### Source Code (repository root)

```text
cmd/dmemd/
├── main.go
└── main_test.go

internal/config/
├── config.go
├── file.go
├── identity.go
├── manager.go
├── snapshot.go
├── validate.go
└── *_test.go

internal/api/
├── api.go
├── config.go
└── health.go

internal/daemon/
├── daemon.go
├── daemon_test.go
└── openapi_test.go

internal/provider/
├── client.go
├── contracts.go
└── secret.go

internal/jobs/
├── provider.go
├── service.go
└── validation.go

internal/storage/
├── schema-v1.sql
├── repository.go
├── jobs.go
├── indexes.go
├── semantic.go
├── semantic_search.go
└── memories.go

internal/aggregate/
└── service.go

docs/api/openapi.yaml
docs/operations/runbook.md
README.md
```

**Structure Decision**: Keep the existing single Go module and package boundaries. Add the configuration file/identity/manager responsibilities under `internal/config`; expose runtime state through existing API/daemon assembly; extend existing storage/job/semantic packages rather than adding a parallel orchestration subsystem.

## Phase 0 Decisions

Research decisions and rejected alternatives are recorded in [research.md](research.md). All technical-context unknowns are resolved.

## Phase 1 Design

- [data-model.md](data-model.md) defines immutable configuration revisions, activation generations, space policy decisions, blocked jobs, embedding eligibility, semantic index generations, and durable reconciliation.
- [contracts/config-schema.md](contracts/config-schema.md) defines the strict single-file TOML grammar.
- [contracts/config.example.toml](contracts/config.example.toml) provides a complete offline-default example.
- [contracts/cli.md](contracts/cli.md) defines the reduced CLI and explicit reload behavior.
- [contracts/openapi-config.yaml](contracts/openapi-config.yaml) defines the UDS configuration status and reload API additions.
- [quickstart.md](quickstart.md) defines runnable end-to-end validation scenarios.

## Implementation Sequence

1. Replace flag/environment parsing with strict TOML loading, normalization, redaction, identities, and the reduced CLI grammar.
2. Replace schema-v1 job and embedding identities directly; add configuration activation, policy, eligibility, and reconciliation state.
3. Introduce immutable effective snapshots and serialized activation without yet changing provider behavior.
4. Route processing, embedding, aggregate, and semantic query operations through one captured capability snapshot and enforce configuration/policy generation at commit.
5. Implement explicit space-policy assignments and nearest-ancestor inheritance, then policy-aware context omission and `blocked_policy` transitions.
6. Separate searchable authority from embedding eligibility and rebuild only the eligible semantic corpus.
7. Add durable diff-class reconciliation for policy and compatibility changes.
8. Add UDS status/reload routes, `SIGHUP`, redacted health summary, CLI commands, OpenAPI, runbook, and README updates.
9. Run build, unit/integration, race, vet, OpenAPI, schema, recovery, and semantic quality gates.

## Complexity Tracking

No constitution violations require justification.
