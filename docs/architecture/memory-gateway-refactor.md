# Memory Gateway Architecture Refactor

## Status

This document is the design source for the interface and application-layer
refactor tracked in `specs/003-api-layer-refactor/tasks.md`.

The repository is pre-release. This refactor replaced the former UDS HTTP API
without preserving its routes, transport details, OpenAPI contract, or
socket-specific configuration for compatibility. Phases 0-6 are implemented:
the Memory Gateway is the semantic core and `internal/protocol/httpapi` is the
first Agent-neutral REST+JSON adapter. Durable authority and behavioral
guarantees were preserved.

## Goals

- Make Memory Gateway the protocol-independent semantic core used by every
  inbound protocol.
- Divide the gateway into cohesive modules with narrow Go interfaces rather
  than one repository-shaped facade.
- Implement an Agent-neutral REST+JSON protocol adapter that can be hosted by
  HTTP/2.
- Leave room for later MCP, REST+SSE, and WebSocket adapters without changing
  gateway contracts.
- Prevent protocol code from importing storage implementations or persistence
  DTOs.
- Keep the current SQLite, FTS5, sqlite-vec, relational graph, job, policy, and
  metadata implementation while exposing it through logical capability ports.
- Preserve atomic authority writes, idempotency, audit history, durable jobs,
  generation fencing, provider-policy enforcement, and offline defaults.
- Remove the old UDS API and all of its code, tests, documentation, dependencies,
  and configuration after replacement coverage exists.
- Delete the current evaluation subsystem before implementation and rebuild it
  against the new protocol only after the refactor is complete.

## Non-Goals

- Designing or implementing Agent skills.
- Creating Agent-specific adapters or route variants.
- Implementing a production network listener, TLS, h2c, authentication,
  authorization at the network edge, rate limiting, or certificate management.
- Replacing SQLite with a vector database, graph database, key-value store, or
  distributed database during this refactor.
- Preserving the old UDS HTTP API as a compatibility endpoint.
- Adding dual reads, dual writes, deprecated DTOs, route aliases, or temporary
  compatibility packages.
- Rebuilding LoCoMo, LongMemEval, calibration, or other evaluation tooling until
  the new gateway and HTTP adapter are complete.

## Target Architecture

```text
+-------------------------------------------------------------+
|                       Agent Clients                         |
+-------------------------------+-----------------------------+
                                |
                  Standard Interaction Protocols
             HTTP/2 REST+JSON / MCP / REST+SSE / WebSocket
                                |
                                v
+-------------------------------------------------------------+
|                    Protocol Adapters                        |
|                                                             |
|  HTTP API          MCP            SSE          WebSocket    |
|  (this refactor)   (later)        (later)      (later)      |
+-------------------------------+-----------------------------+
                                | internal Go interfaces
                                v
+-------------------------------------------------------------+
|                       Memory Gateway                        |
|                                                             |
|  Memory      Retrieval      Extraction      Consolidation   |
|  Scope       Feedback       Knowledge       Policy          |
+-------------------------------+-----------------------------+
                                | capability ports
                                v
+-------------------------------------------------------------+
|                     Runtime Adapters                        |
|                                                             |
|  SQLite persistence  Jobs  Processing provider  Embedding   |
|  FTS5                sqlite-vec              Configuration   |
+-------------------------------------------------------------+
```

The target dependency direction is:

```text
Agent client
  -> protocol adapter
  -> gateway module interface
  -> gateway service
  -> capability port
  -> runtime adapter
```

The following dependency directions are forbidden:

```text
protocol -> storage
protocol -> jobs
gateway -> net/http
gateway -> protocol DTO
gateway contract -> storage DTO
storage -> gateway service
module -> another module's concrete service
```

## Layer Responsibilities

### Protocol Adapters

Protocol adapters translate a wire protocol into gateway calls. They own:

- method, path, message, or stream routing;
- representation-level parsing and strict decoding;
- request body and message size limits;
- protocol metadata to gateway call-context conversion;
- request identifiers and cancellation propagation;
- gateway command/query construction;
- gateway result serialization;
- gateway error to protocol error mapping;
- panic recovery at the protocol boundary.

Protocol adapters do not own:

- domain validation;
- memory lifecycle rules;
- scope visibility semantics;
- retrieval planning, fusion, or context selection;
- extraction or consolidation decisions;
- provider egress policy;
- transaction management;
- repository calls.

The first adapter is an Agent-neutral REST+JSON handler under
`internal/protocol/httpapi`. It returns an `http.Handler`. It must not create a
listener or require `r.ProtoMajor == 2`; the eventual server assembly is
responsible for enforcing HTTP/2.

### Memory Gateway

Memory Gateway owns application semantics and orchestration. It is a set of
modules, not a single large `Gateway` interface. Each module publishes its own
commands, queries, results, service interface, and required capability ports.

A module may consume a narrow interface implemented by another capability, but
it must not retain another module's concrete service. Interfaces belong to the
consumer whenever practical. This keeps a module independently testable and
prevents package dependency cycles.

### Runtime Adapters

Runtime adapters implement gateway capability ports. During this refactor, one
SQLite-backed implementation may satisfy many ports. Logical separation does
not imply separate physical databases.

Provider adapters remain typed, in-process ports. Provider network calls remain
outside SQLite write transactions and publish results through existing lease,
configuration, policy, and revision fences.

## Shared Gateway Contracts

Only cross-cutting value types belong directly in `internal/gateway`:

```go
type CallContext struct {
    RequestID     string
    CorrelationID string
    Caller        CallerIdentity
    Idempotency   *Idempotency
}

type CallerIdentity struct {
    SubjectID string
    ClientID  string
}

type Idempotency struct {
    Key string
}
```

The gateway computes canonical command hashes. A protocol adapter must not make
HTTP method or route part of core idempotency semantics. A write identity is
formed from:

```text
caller identity + operation name + idempotency key + canonical command hash
```

Stable operation names include `memory.remember`, `memory.forget`,
`feedback.append`, and `extraction.ingest`.

Gateway errors are protocol-neutral:

```go
type ErrorCode string

const (
    ErrorInvalidInput          ErrorCode = "invalid_input"
    ErrorNotFound              ErrorCode = "not_found"
    ErrorConflict              ErrorCode = "conflict"
    ErrorIdempotencyConflict   ErrorCode = "idempotency_conflict"
    ErrorPermissionDenied      ErrorCode = "permission_denied"
    ErrorResourceExhausted     ErrorCode = "resource_exhausted"
    ErrorCapabilityUnavailable ErrorCode = "capability_unavailable"
    ErrorInternal              ErrorCode = "internal"
)
```

Gateway modules translate storage and provider failures into this stable error
set. Protocol adapters never inspect `storage.Err*` values.

## Gateway Modules

### Memory

The Memory module owns direct authority operations:

```go
type Service interface {
    Remember(context.Context, RememberCommand) (RememberResult, error)
    Get(context.Context, GetQuery) (Memory, error)
    Forget(context.Context, ForgetCommand) (ForgetResult, error)
}
```

It owns semantic validation, defaults that are independent of a wire protocol,
scope checks, idempotency, authority mutation, audit records, and atomic creation
of required derived-work jobs.

### Retrieval

The Retrieval module owns:

- query planning;
- lexical, semantic, and graph capability selection;
- degraded execution reporting;
- candidate fusion and ranking;
- temporal interpretation;
- context selection and budgets;
- cursor semantics;
- explain results;
- bounded, content-safe telemetry.

Its public surface starts with:

```go
type Service interface {
    Recall(context.Context, Query) (Result, error)
}
```

Pure algorithms currently under `internal/retrieval` remain reusable. Search
orchestration moves out of `storage.Repository`; SQL, FTS5, sqlite-vec, and graph
queries stay in the SQLite adapter.

### Extraction

The Extraction module owns raw event ingestion and extraction work semantics:

```go
type Service interface {
    Ingest(context.Context, IngestCommand) (IngestResult, error)
}
```

It preserves the existing execution boundary:

```text
transactional event and job creation
  -> short preparation read
  -> provider call outside a write transaction
  -> fenced transactional commit
```

Worker-facing prepare, commit, block, and fail ports remain separate from the
inbound protocol-facing service.

### Consolidation

The first consolidation boundary represents existing behavior: duplicate,
reinforce, supplement, conflict, candidate confirmation, and evolution proposal
decisions. It does not claim that time-decay or retention cleanup already exists.

Candidate confirmation remains one atomic use case. A pure classifier may be
separated from persistence, but evaluation and mutation must not become two
externally visible, race-prone steps.

Future decay and retention behavior belongs in an explicit retention service,
not in an overloaded confirmation API.

### Policy

The package may be named `policy` rather than broad `privacy` because the first
implementation covers concrete authorization and disclosure responsibilities:

```go
type Authorizer interface {
    Authorize(context.Context, Request) (Decision, error)
}

type DisclosureFilter interface {
    Filter(context.Context, DisclosureRequest) (DisclosureResult, error)
}
```

Decisions include authority writes and reads, recall, provider processing,
embedding, semantic queries, evidence disclosure, and sensitive export.

Filtering must occur before provider calls and before candidate admission where
required. It must not exist only as response redaction in the HTTP adapter.

### Scope

Scope owns the existing space hierarchy and stable external identity:

```go
type Service interface {
    Create(context.Context, CreateCommand) (Scope, error)
    Resolve(context.Context, ResolveQuery) (Scope, error)
    Get(context.Context, GetQuery) (Scope, error)
    List(context.Context, ListQuery) (Page, error)
}
```

Internal storage can continue to use the term `Space` during migration. Public
HTTP terminology must choose either `scope` or `space` and use it consistently.

### Feedback

Feedback is separate from Memory because it has its own purpose/type rules,
decision context, aggregate projection, and optional corrective proposal:

```go
type Service interface {
    Append(context.Context, AppendCommand) (AppendResult, error)
    Aggregate(context.Context, AggregateQuery) (AggregateResult, error)
}
```

### Knowledge

Knowledge owns entity, relation, fact, graph, proposal review, and conflict
resolution semantics. Read and review surfaces are separate so a protocol can
expose graph reads without gaining mutation or review authority.

The initial Agent HTTP API need not expose review or maintenance operations.

## Storage Capability Ports

Ports describe capabilities, not database products:

```text
AuthorityStore
ScopeStore
EventStore
RecallStore
LexicalIndex
SemanticIndex
KnowledgeGraphStore
JobStore
PolicyStore
FeedbackStore
TelemetryStore
MetadataStore
```

The reference diagram's Vector Store, Graph DB, and Key-Value Store are possible
future deployments, not current components. Today:

- vectors are float32 BLOBs queried with sqlite-vec;
- graph authority and projections are relational SQLite tables;
- FTS5 is a first-class lexical index;
- jobs, idempotency, policy, configuration, and metadata are relational tables,
  not a key-value store.

One SQLite adapter initially implements all required ports. Physical splitting
is a separate future project requiring explicit authority, outbox,
reconciliation, cursor-generation, backup, and degradation contracts.

## Transactions And Unit Of Work

Splitting interfaces must not split atomic behavior. Cross-capability writes use
a SQLite-backed unit of work. Operations that require one transaction include:

- raw event plus extraction job creation;
- memory revision plus derived-work scheduling;
- candidate review plus memory/evolution/audit mutation;
- configuration activation plus capability and policy state;
- feedback plus aggregate-work scheduling;
- idempotency result publication with the authoritative write.

A unit-of-work interface may expose transaction-scoped capability writers. It
must not become a second general repository with every read and write method.

## Initial HTTP REST+JSON Surface

The initial adapter is protocol-focused and Agent-neutral. Its intended public
surface is deliberately small:

```text
POST /v1/memories
GET  /v1/memories/{memory_id}
POST /v1/memories/{memory_id}/forget
POST /v1/memories/{memory_id}/feedback
POST /v1/recall
POST /v1/events
GET  /v1/events/{event_id}
POST /v1/scopes
GET  /v1/scopes/{scope_id}
```

The exact paths and DTOs are frozen in the HTTP contract task, not by this
illustrative list. Candidate review, index rebuild, job control, configuration
reload, graph maintenance, and other administrative operations are not part of
the initial Agent-facing protocol surface.

The adapter must use strict JSON decoding, bounded bodies, one JSON value per
request, propagated request cancellation, safe errors, and route-level policy.
It must be compatible with HTTP/2 multiplexing and must not depend on
connection-specific HTTP/1.1 behavior.

## Runtime Assembly

The current daemon mixes composition, workers, storage, Unix socket lifecycle,
and HTTP serving. The target runtime is a composition root:

```text
configuration
  -> SQLite store and capability adapters
  -> gateway module services
  -> provider adapters and workers
  -> protocol handler construction
```

During this refactor the runtime exposes the protocol handler but does not start
a production listener. Listener and security design is deferred.

The runtime may retain the `Daemon` name temporarily to minimize unrelated
renaming, but its completed state contains no Unix listener, socket file,
`http.Server`, or old API handler.

## Testing Strategy

Existing core tests are retained until replacement coverage is verified. Tests
are removed by responsibility, not all at once.

### Gateway Tests

Each gateway module uses fakes for only its declared ports. Tests cover semantic
validation, error classification, cancellation, idempotency, orchestration, and
invariants without HTTP or SQLite where possible.

### SQLite Adapter Tests

Storage tests continue to protect schema, authority, transaction, recovery,
retrieval, policy, and projection behavior. They migrate to capability-adapter
tests as package boundaries change.

### Protocol Tests

HTTP tests use fake gateway modules and cover strict decoding, DTO conversion,
route policy, response encoding, gateway-error mapping, panic recovery,
cancellation, concurrency, and HTTP/2 compatibility.

### Runtime Tests

Runtime tests cover composition, worker lifecycle, configuration application,
shutdown, and the absence of UDS/socket lifecycle behavior.

### Evaluation

The former evaluation tree, its Python package metadata, CI job, Makefile
target, and documentation links were removed before the refactor. No benchmark
or evaluation compatibility layer is retained. A new evaluation subsystem is a
post-refactor project and must consume the new HTTP contract rather than the old
UDS API.

## Migration Policy

- Migrate one vertical slice at a time.
- Add replacement tests before deleting the old tests for that slice.
- Do not add compatibility wrappers between old handlers and new gateway
  modules.
- A temporary adapter may delegate to existing storage implementation through a
  narrow port, but its exported contract must not contain storage types.
- Do not physically split persistence during this refactor.
- Delete obsolete code and docs as soon as their replacement is verified.
- Do not mark a phase complete while old and new ownership coexist without an
  explicit next task removing the old path.

## Completion Gates

The refactor is complete only when all of the following hold:

- The former in-process HTTP API package is absent.
- No Unix socket listener, socket cleanup, socket permission, socket client
  helper, or socket path constant remains.
- No protocol package imports `internal/storage` or `internal/jobs`.
- No gateway package imports `net/http` or a protocol package.
- No gateway public interface exposes a `storage.*` type.
- No HTTP handler stores or receives `*storage.Repository`.
- The runtime does not own `net.Listener`, `http.Server`, or an old API handler.
- The initial HTTP adapter has contract and HTTP/2 compatibility tests.
- Gateway modules have focused tests against narrow fake ports.
- SQLite integration tests preserve authority, idempotency, audit, recovery,
  retrieval, policy, and transaction guarantees.
- The evaluation subsystem, its CI job, Python project metadata, Makefile
  targets, and docs references are absent.
- Old UDS/OpenAPI docs and tests are absent or replaced with the new HTTP
  contract.
- README, architecture, operations, configuration examples, and CI describe
  only the completed architecture.
- Repository-wide searches for the former socket client helper, socket path,
  socket CLI flag, Unix listener call, and removed API handler types return no
  current-code or current-documentation references.
- Formatting, build, focused tests, full tests, race tests, vet, and
  `git diff --check` pass under the supported build profile.
