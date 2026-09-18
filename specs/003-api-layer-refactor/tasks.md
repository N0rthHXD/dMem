---
description: "Phased tasks for the Memory Gateway and protocol adapter refactor"
---

# Tasks: Memory Gateway And Protocol Adapters

**Design source**: `docs/architecture/memory-gateway-refactor.md`

**Scope**: Replace the current UDS HTTP API with protocol-independent Memory
Gateway modules, logically decouple storage through capability ports, implement
an Agent-neutral REST+JSON adapter suitable for HTTP/2, and remove all obsolete
UDS/API/evaluation assets.

**Compatibility policy**: dMem is pre-release. Do not preserve old UDS routes,
socket behavior, HTTP DTOs, OpenAPI status matrices, configuration fields,
Python evaluation clients, or route-based idempotency identities. Do not add
route aliases, deprecated fields, dual handlers, compatibility decoders, or
legacy client packages.

**Persistence policy**: Keep one SQLite database and the current FTS5,
sqlite-vec, relational graph, jobs, policy, telemetry, idempotency, and audit
implementation. Introduce logical ports without physically splitting stores.

**Testing policy**: Do not delete all Go tests before implementation. Add or
migrate replacement tests for each vertical slice, verify them, then delete the
obsolete API/daemon tests for that slice. The `evaluation/` Python subsystem is
the exception: remove it and all of its build, CI, packaging, and documentation
references in Phase 0, then rebuild evaluation only after this plan is complete.

## Task Format

Tasks use:

```text
[ID] [P?] [Area] Description
```

- `[P]` means the task can run in parallel with other `[P]` tasks in the same
  group after their shared dependencies are complete.
- Areas are `CLEAN`, `CONTRACT`, `STORE`, `MEM`, `SCOPE`, `FEEDBACK`, `SEARCH`,
  `EXTRACT`, `POLICY`, `CONSOL`, `KNOW`, `HTTP`, `RUNTIME`, `TEST`, or `DOC`.
- Mark a task `[X]` only after implementation, focused verification, cleanup,
  and documentation required by that task are complete.

## New-Session Protocol

Every implementation session must:

1. Read this file and `docs/architecture/memory-gateway-refactor.md`.
2. Run `git status --short` and preserve unrelated worktree changes.
3. Select the first incomplete task whose dependencies are complete unless the
   user explicitly selects another unblocked task.
4. Inspect current implementation and executable contracts before editing.
5. Add or migrate focused tests before changing behavior, except for the
   explicitly approved evaluation deletion tasks.
6. Implement the smallest complete boundary change without compatibility code.
7. Run focused tests, formatting, and `git diff --check`.
8. Delete the replaced old path and its obsolete tests in the same task or the
   immediately following cleanup task named below.
9. Mark only verified tasks `[X]`.
10. At phase boundaries, run every checkpoint command and report the exact next
    unblocked task.

Recommended continuation prompt:

```text
Continue the Memory Gateway refactor from
specs/003-api-layer-refactor/tasks.md. Read the design source and worktree first,
implement the next unblocked task without compatibility code, preserve core
behavior with focused tests, remove replaced old paths, run the phase checks,
and report the next task ID.
```

## Global Rules

- Protocol packages do not import `internal/storage` or `internal/jobs`.
- Gateway packages do not import `net/http` or protocol packages.
- Gateway public contracts do not expose `storage.*` types.
- Gateway modules depend on narrow consumer-owned ports, not concrete services.
- One SQLite adapter may implement many logical capability ports.
- Interface separation must not split existing atomic writes. Use a unit of work
  where authority, jobs, audit, policy, or idempotency must commit together.
- Provider calls remain outside SQLite write transactions and remain fenced.
- Scope, policy, lifecycle, current revision, sensitivity, and valid-time rules
  remain enforced before disclosure or provider egress.
- No task may weaken authority, audit, idempotency, recovery, offline-default,
  or rebuildability guarantees merely to simplify a package boundary.
- No production listener, TLS, h2c, authentication, or network security design
  is added in this plan.
- Agent-specific routes, DTOs, adapters, or skills are out of scope.

## Phase 0: Remove Evaluation And Capture The Baseline

**Purpose**: Remove the approved disposable evaluation subsystem while keeping
the Go behavior baseline needed for a safe refactor.

**Dependencies**: None.

- [X] T001 [CLEAN] Record the pre-refactor Go build and focused core test status;
  document any existing failure without fixing unrelated behavior
- [X] T002 [CLEAN] Delete the complete `evaluation/` tree, including generated
  caches or untracked evaluation artifacts
- [X] T003 [CLEAN] Delete evaluation-only root Python packaging such as
  `pyproject.toml` when no non-evaluation Python package remains
- [X] T004 [CLEAN] Remove `python-test`, evaluation variables, and evaluation-only
  phony targets from `Makefile`
- [X] T005 [CLEAN] Remove the Python evaluation CI job and evaluation-only setup
  from `.github/workflows/ci.yml`
- [X] T006 [CLEAN] Remove current documentation and README links whose only
  purpose is the deleted evaluation subsystem; leave historical specs untouched
  until the final documentation sweep if they are needed as implementation
  history
- [X] T007 [CLEAN] Verify no active build, CI, package, or current documentation
  path references `evaluation/`, `dmem_eval`, `dmem_locomo`,
  `dmem_longmemeval`, or `dmem_calibration`

**Checkpoint**:

```sh
CGO_ENABLED=1 go build -tags sqlite_fts5 ./...
CGO_ENABLED=1 go test -tags sqlite_fts5 ./internal/domain ./internal/retrieval ./internal/config ./internal/provider ./internal/jobs ./internal/storage
git diff --check
```

---

## Phase 1: Gateway Foundation

**Purpose**: Establish protocol-neutral identity, error, and module contracts
before moving handlers.

**Dependencies**: Phase 0.

- [X] T008 [CONTRACT] Create `internal/gateway` shared call-context, caller,
  idempotency, operation identity, paging, and base result contracts without
  HTTP or storage imports
- [X] T009 [CONTRACT] Define the stable gateway error codes, typed error,
  retryability metadata, safe public details, and error inspection helpers
- [X] T010 [CONTRACT] Define canonical command hashing and operation names that
  do not depend on HTTP method or route
- [X] T011 [P] [CONTRACT] Define Memory service commands, queries, results, and
  narrow persistence ports under `internal/gateway/memory`
- [X] T012 [P] [CONTRACT] Define Scope service contracts and ports under
  `internal/gateway/scope`
- [X] T013 [P] [CONTRACT] Define Feedback service contracts and ports under
  `internal/gateway/feedback`
- [X] T014 [P] [CONTRACT] Define Retrieval service contracts and initial coarse
  recall-store port under `internal/gateway/retrieval`
- [X] T015 [P] [CONTRACT] Define Extraction ingress and worker-facing contracts
  under `internal/gateway/extraction`
- [X] T016 [P] [CONTRACT] Define narrow Policy authorization and disclosure
  contracts under `internal/gateway/policy`
- [X] T017 [P] [CONTRACT] Define Consolidation confirmation and classifier
  contracts under `internal/gateway/consolidation`
- [X] T018 [P] [CONTRACT] Define Knowledge read and separate Review mutation
  contracts under `internal/gateway/knowledge`
- [X] T019 [TEST] Add architecture tests or static checks proving gateway
  packages do not import `net/http`, protocol packages, or expose `storage.*`
  in public interfaces

**Checkpoint**:

```sh
CGO_ENABLED=1 go test -tags sqlite_fts5 ./internal/gateway/...
CGO_ENABLED=1 go build -tags sqlite_fts5 ./...
git diff --check
```

---

## Phase 2: Logical Storage Ports And Unit Of Work

**Purpose**: Adapt current SQLite behavior to gateway-owned contracts without a
physical storage split.

**Dependencies**: Phase 1.

- [X] T020 [STORE] Inventory cross-table atomic operations and define the
  minimal SQLite unit-of-work boundary for authority, jobs, audit, policy, and
  idempotency writers
- [X] T021 [STORE] Implement Memory capability ports by adapting current
  repository behavior without exporting storage DTOs to the gateway
- [X] T022 [P] [STORE] Implement Scope capability ports
- [X] T023 [P] [STORE] Implement Feedback capability ports
- [X] T024 [P] [STORE] Implement the initial coarse RecallStore port while
  retaining FTS5, sqlite-vec, graph, cursor, and telemetry behavior
- [X] T025 [P] [STORE] Implement Event and extraction-work capability ports
- [X] T026 [P] [STORE] Implement Policy authorization data ports
- [X] T027 [P] [STORE] Implement Consolidation transaction and classifier data
  ports without splitting candidate confirmation atomicity
- [X] T028 [P] [STORE] Implement Knowledge and Review capability ports
- [X] T029 [STORE] Add compile-time interface assertions for the SQLite-backed
  adapters and remove any gateway contract dependency on `storage.*`
- [X] T030 [TEST] Migrate focused storage tests to prove equivalent transaction,
  idempotency, audit, cursor, retrieval, policy, and fencing behavior through
  the new ports

**Checkpoint**:

```sh
CGO_ENABLED=1 go test -tags sqlite_fts5 ./internal/storage ./internal/gateway/...
CGO_ENABLED=1 go test -race -tags sqlite_fts5 ./internal/storage ./internal/gateway/...
git diff --check
```

---

## Phase 3: Core Gateway Services

**Purpose**: Move application semantics and orchestration out of HTTP handlers
and the repository-shaped application path.

**Dependencies**: Phase 2.

### Lower-Risk Vertical Slices

- [X] T031 [MEM] Implement Memory remember/get/forget service semantics,
  validation, defaults, idempotency, and stable error mapping
- [X] T032 [SCOPE] Implement Scope create/resolve/get/list semantics and stable
  external identity behavior
- [X] T033 [FEEDBACK] Implement feedback combination validation, decision-context
  handling, persistence, aggregate scheduling, and error mapping
- [X] T034 [TEST] Add focused unit tests for T031-T033 using only narrow fake
  ports and verify cancellation and idempotency propagation
- [X] T035 [CLEAN] Remove replaced logic from `internal/application/memory.go`
  while retaining only temporary code still required by an incomplete slice

### Retrieval

- [ ] T036 [SEARCH] Move query parsing defaults that are protocol-independent
  into the Retrieval module
- [ ] T037 [SEARCH] Move query planning and capability degradation orchestration
  out of `storage.Repository`
- [ ] T038 [SEARCH] Move fusion, ranking, temporal interpretation, and context
  selection orchestration into Retrieval while reusing pure algorithms
- [ ] T039 [SEARCH] Preserve cursor generation binding, explain policy, bounded
  telemetry, and pre-top-k scope/policy/lifecycle filtering
- [ ] T040 [TEST] Migrate search, ranking, context, graph-quality, semantic, and
  cursor tests to the Retrieval service and SQLite capability adapter boundaries

### Extraction And Policy

- [ ] T041 [EXTRACT] Implement raw-event ingestion with atomic durable job
  creation through the unit of work
- [ ] T042 [EXTRACT] Adapt job preparation and fenced commit to extraction-owned
  contracts while keeping provider calls outside transactions
- [ ] T043 [POLICY] Implement policy authorization and disclosure services over
  current effective-policy data
- [ ] T044 [POLICY] Route processing, embedding, semantic query, and disclosure
  decisions through narrow policy interfaces without changing offline defaults
- [ ] T045 [TEST] Migrate processing, graph extraction, policy, blocked-work,
  reconciliation, and provider-isolation tests

### Consolidation And Knowledge

- [ ] T046 [CONSOL] Extract deterministic consolidation classification and
  provider-result validation from storage implementation details
- [ ] T047 [CONSOL] Implement atomic candidate confirmation and evolution
  behavior through the consolidation unit-of-work port
- [ ] T048 [KNOW] Implement entity, relation, fact, and graph read services
- [ ] T049 [KNOW] Implement separate proposal review and conflict-resolution
  services without exposing them through the initial Agent HTTP surface
- [ ] T050 [TEST] Migrate consolidation, candidate, entity, relation, graph,
  evolution, and conflict tests to the new service and adapter boundaries

**Checkpoint**:

```sh
CGO_ENABLED=1 go test -tags sqlite_fts5 ./internal/gateway/... ./internal/storage ./internal/jobs ./internal/provider
CGO_ENABLED=1 go test -race -tags sqlite_fts5 ./internal/gateway/... ./internal/jobs
CGO_ENABLED=1 go build -tags sqlite_fts5 ./...
git diff --check
```

---

## Phase 4: HTTP REST+JSON Adapter

**Purpose**: Provide the first Agent-neutral protocol implementation without a
production listener.

**Dependencies**: Phase 3 service needed by each route before that route starts.

- [ ] T051 [HTTP] Freeze the initial route, request, response, error, pagination,
  request-ID, caller, and idempotency contract in an HTTP-specific contract
  document
- [ ] T052 [HTTP] Create `internal/protocol/httpapi` with dependency injection
  only for gateway module interfaces
- [ ] T053 [HTTP] Implement declarative route metadata for operation name,
  caller requirement, write/idempotency policy, body limit, and handler
- [ ] T054 [HTTP] Implement strict single-value JSON decoding, bounded bodies,
  common JSON responses, request IDs, panic recovery, and gateway error mapping
- [ ] T055 [P] [HTTP] Implement Memory routes and DTO conversion
- [ ] T056 [P] [HTTP] Implement Recall route and DTO conversion
- [ ] T057 [P] [HTTP] Implement Feedback route and DTO conversion
- [ ] T058 [P] [HTTP] Implement Event ingress/detail routes and DTO conversion
- [ ] T059 [P] [HTTP] Implement Scope create/detail routes and DTO conversion
- [ ] T060 [HTTP] Add route-conformance tests proving every write requires an
  idempotency key and every non-public route requires caller identity
- [ ] T061 [HTTP] Add fake-gateway contract tests for all routes, statuses,
  strict decoding, cancellation, panic recovery, and safe errors
- [ ] T062 [HTTP] Add HTTP/2 compatibility and multiplexed concurrency tests
  using test-only transport; do not add a production listener or TLS setup
- [ ] T063 [TEST] Add static dependency checks proving protocol packages do not
  import storage, jobs, or concrete gateway services

**Checkpoint**:

```sh
CGO_ENABLED=1 go test -tags sqlite_fts5 ./internal/protocol/httpapi
CGO_ENABLED=1 go test -race -tags sqlite_fts5 ./internal/protocol/httpapi
CGO_ENABLED=1 go build -tags sqlite_fts5 ./...
git diff --check
```

---

## Phase 5: Runtime Recomposition And Old API Removal

**Purpose**: Make the new gateway and protocol handler the only inbound path and
remove UDS/server ownership.

**Dependencies**: Phase 4.

- [ ] T064 [RUNTIME] Recompose startup around SQLite capability adapters,
  gateway services, providers, workers, and the HTTP protocol handler
- [ ] T065 [RUNTIME] Expose the HTTP handler from runtime assembly without
  creating a production listener
- [ ] T066 [RUNTIME] Preserve worker startup/shutdown, configuration snapshots,
  provider updates, generation fences, integrity checks, and graceful close
- [ ] T067 [CLEAN] Remove `internal/api` after all replacement routes and gateway
  tests pass
- [ ] T068 [CLEAN] Remove Unix listener creation, socket permission management,
  stale socket cleanup, socket identity tracking, connection limiter, and UDS
  client helpers
- [ ] T069 [CLEAN] Remove `http.Server` ownership and old API handler fields from
  the daemon/runtime
- [ ] T070 [CLEAN] Remove socket-only and old server-only configuration fields,
  validation, examples, and command behavior without compatibility aliases
- [ ] T071 [TEST] Replace daemon API/OpenAPI/UDS tests with runtime composition,
  Gateway integration, and HTTP adapter tests, then delete obsolete tests
- [ ] T072 [TEST] Update maintenance, portability, configuration, and release
  smoke tests so they call stable runtime/gateway boundaries rather than UDS
- [ ] T073 [CLEAN] Remove dependencies used only by old OpenAPI runtime tests or
  the deleted UDS API, then run `go mod tidy`

**Checkpoint**:

```sh
CGO_ENABLED=1 go test -tags sqlite_fts5 ./...
CGO_ENABLED=1 go test -race -tags sqlite_fts5 ./...
CGO_ENABLED=1 go vet -tags sqlite_fts5 ./...
CGO_ENABLED=1 go build -tags sqlite_fts5 ./...
git diff --check
```

---

## Phase 6: Documentation And Residual Cleanup

**Purpose**: Ensure the repository describes and contains only the new
architecture.

**Dependencies**: Phase 5.

- [ ] T074 [DOC] Replace the old OpenAPI contract with the new HTTP REST+JSON
  contract or another machine-readable contract selected in T051
- [ ] T075 [DOC] Rewrite README build, architecture, and usage sections for
  Gateway plus protocol adapters; do not document a production listener that
  does not exist
- [ ] T076 [DOC] Rewrite architecture overview, operations, testing guidance,
  and configuration examples; mark superseded UDS ADRs accordingly
- [ ] T077 [DOC] Update or remove earlier specs and task references that would
  otherwise present deleted evaluation or UDS interfaces as current work
- [ ] T078 [CLEAN] Delete empty packages, dead DTOs, old error mappers, obsolete
  route helpers, stale fixtures, and generated artifacts
- [ ] T079 [CLEAN] Verify current code and docs contain no `internal/api`,
  `api.Handler`, `HandlerDependencies`, `unixClient`, `dmem.sock`,
  `--unix-socket`, or `net.Listen("unix", ...)` references
- [ ] T080 [CLEAN] Verify there is no `evaluation/`, Python evaluation package,
  evaluation CI job, evaluation Makefile target, or current-doc evaluation link
- [ ] T081 [CLEAN] Verify no protocol package imports storage/jobs, no gateway
  package imports HTTP/protocol, and no gateway public API exposes storage types
- [ ] T082 [TEST] Run the complete verification matrix and record any explicitly
  accepted residual risk; no known old-architecture residue may remain

**Final Checkpoint**:

```sh
gofmt -w <changed-go-files>
CGO_ENABLED=1 go build -tags sqlite_fts5 ./...
CGO_ENABLED=1 go test -tags sqlite_fts5 ./...
CGO_ENABLED=1 go test -race -tags sqlite_fts5 ./...
CGO_ENABLED=1 go vet -tags sqlite_fts5 ./...
go mod tidy
git diff --check
git status --short
```

## Post-Refactor Work

The following work starts only after T082:

- design the production HTTP/2 listener and TLS or h2c policy;
- design authentication, authorization, rate limiting, and network exposure;
- rebuild evaluation against the new protocol contract;
- add MCP, REST+SSE, or WebSocket protocol adapters;
- evaluate physical vector, graph, or metadata storage separation using measured
  scale and consistency requirements.
