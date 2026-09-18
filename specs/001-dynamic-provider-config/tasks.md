---

description: "Implementation tasks for dynamic provider configuration"
---

# Tasks: Dynamic Provider Configuration

> **Historical record.** These tasks document the completed feature 001. The
> Unix-socket API, OpenAPI contract, former API package, `status`/`reload`
> subcommands, and `make openapi` target they reference were removed by
> `specs/003-api-layer-refactor`. Current contracts live in
> `docs/api/http-rest-contract.md`.

**Input**: Design documents from `specs/001-dynamic-provider-config/`

**Prerequisites**: `plan.md`, `spec.md`, `research.md`, `data-model.md`, `contracts/`, `quickstart.md`

**Tests**: Automated tests are required by the dMem constitution for configuration, API, storage, durable jobs, recovery, processing, and retrieval behavior. Write each listed test first and confirm it fails for the intended reason before implementation.

**Organization**: Tasks are grouped by user story so each story produces a separately testable increment. Shared schema, identity, and snapshot infrastructure is completed in the foundational phase.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel because it changes different files and does not depend on another incomplete task in the same phase.
- **[Story]**: Maps the task to a user story in `spec.md`.
- Every task names the concrete file or files it changes.

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Establish the strict TOML dependency, canonical example, and project gates used by later work.

- [X] T001 Add the selected strict TOML decoder as a direct dependency in `go.mod` and `go.sum`
- [X] T002 [P] Add the production offline-default configuration example at `docs/operations/config.example.toml` based on `specs/001-dynamic-provider-config/contracts/config.example.toml`
- [X] T003 [P] Add configuration fixture directories for valid, invalid, unknown-field, duplicate-field, enabled-processing, and enabled-embedding documents under `internal/config/testdata/`
- [X] T004 Update configuration-related Makefile gates to validate the default TOML file and removed setting flags in `Makefile`

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Build the configuration grammar, immutable identities, persistence model, and runtime snapshot boundaries required by every story.

**CRITICAL**: No user story work begins until this phase is complete.

### Foundational Tests

- [X] T005 [P] Add failing strict TOML tests for closed tables, duplicate keys, required fields, durations, absolute paths, bounds, disabled/enabled provider sections, and no setting environment overrides in `internal/config/config_test.go`
- [X] T006 [P] Add failing canonical identity tests for source, effective, capability, compatibility, policy, set normalization, formatting-only changes, and secret exclusion in `internal/config/identity_test.go`
- [X] T007 [P] Add failing schema contract tests for configuration activations, capability states, policy entities, blocked jobs, eligibility, semantic generations, and reconciliation runs in `internal/storage/schema_contract_test.go`
- [X] T008 [P] Add failing provider secret-reference tests for unavailable, empty, rotated, and non-leaking environment secrets in `internal/provider/client_test.go`

### Foundational Implementation

- [X] T009 Replace flag/environment configuration parsing with the versioned file document and normalized runtime model in `internal/config/config.go` and `internal/config/file.go`
- [X] T010 Implement layered file safety, strict decoding, field diagnostics, conditional provider validation, MIME normalization, and restart-bound field comparison in `internal/config/validate.go`
- [X] T011 Implement canonical non-secret serialization and source/effective/capability/compatibility/policy hashes in `internal/config/identity.go`
- [X] T012 [P] Implement typed environment secret references and request-time resolution in `internal/provider/secret.go` and adapt `internal/provider/client.go`
- [X] T013 Define immutable effective and capability snapshots plus redacted allowlist serialization in `internal/config/snapshot.go`
- [X] T014 Replace schema v1 directly with configuration revision/activation, capability generation, policy assignment/decision, blocked job, eligibility, semantic generation, and reconciliation entities in `internal/storage/schema-v1.sql` and `docs/storage/schema-v1.sql`
- [X] T015 Adapt shared domain and storage scan models to the new job, attempt, embedding, activation, policy, and reconciliation fields in `internal/domain/models.go`, `internal/storage/jobs.go`, and `internal/storage/semantic.go`
- [X] T016 Implement a serialized configuration manager with immutable snapshot reads, activation attempt status, generation allocation, and atomic publication in `internal/config/manager.go`
- [X] T017 Add repository transactions that atomically persist successful activation pointers, capability generations, policy definitions, and reconciliation intents in `internal/storage/configuration.go`
- [X] T018 Re-run and complete the foundational tests in `internal/config/config_test.go`, `internal/config/identity_test.go`, `internal/provider/client_test.go`, and `internal/storage/schema_contract_test.go`

**Checkpoint**: A strict file can be loaded into one immutable snapshot, identities are deterministic, and the new schema can persist activation generations without yet exposing runtime reload.

---

## Phase 3: User Story 1 - Load and Reload One Configuration File (Priority: P1) MVP

**Goal**: Start from one TOML file and atomically enable, update, disable, and independently replace processing and embedding providers without restart.

**Independent Test**: Start with both providers disabled, activate processing, update only embedding, disable one capability, reject a mixed invalid revision without any partial change, and confirm the same valid file loads after daemon restart.

### Tests for User Story 1

- [X] T019 [P] [US1] Add failing manager tests for activated, no-change, rejected, concurrent, restart-required, and previous-snapshot-preservation outcomes in `internal/config/manager_test.go`
- [X] T020 [P] [US1] Add failing daemon integration tests for independent processing/embedding enable, update, disable, and invalid full-file reload in `internal/daemon/config_reload_test.go`
- [X] T021 [P] [US1] Add failing in-flight provider tests proving one operation uses one immutable snapshot and old generations cannot publish as current in `internal/jobs/service_test.go` and `internal/storage/semantic_test.go`
- [X] T022 [P] [US1] Add failing aggregate tests proving provider, policy, processing version, and aggregate limits are captured from one processing snapshot in `internal/aggregate/service_test.go`

### Implementation for User Story 1

- [X] T023 [US1] Refactor daemon assembly to own the configuration manager and dynamic provider capability snapshots in `internal/daemon/daemon.go`
- [X] T024 [US1] Refactor processing dispatch to capture provider, limits, identities, and generations once per job in `internal/jobs/provider.go` and `internal/jobs/service.go`
- [X] T025 [US1] Refactor aggregate building to capture one processing capability snapshot and separate extraction-context from aggregate-input limits in `internal/aggregate/service.go`
- [X] T026 [US1] Refactor embedding generation and semantic query execution to use one captured embedding snapshot instead of mutable repository provider fields in `internal/storage/repository.go`, `internal/storage/semantic.go`, and `internal/storage/semantic_search.go`
- [X] T027 [US1] Enforce configuration/policy generation alongside lease, revision, and content fences in processing, embedding, aggregate, and semantic publication transactions in `internal/storage/jobs.go`, `internal/storage/semantic.go`, and `internal/storage/aggregates.go`
- [X] T028 [US1] Implement normalized configuration diff classes and enqueue only required post-activation reconciliation intents in `internal/config/manager.go` and `internal/storage/configuration.go`
- [X] T029 [US1] Complete US1 manager, daemon, jobs, semantic, and aggregate tests in `internal/config/manager_test.go`, `internal/daemon/config_reload_test.go`, `internal/jobs/service_test.go`, `internal/storage/semantic_test.go`, and `internal/aggregate/service_test.go`

**Checkpoint**: One complete file revision is observable atomically, processing and embedding change independently, invalid revisions preserve the prior snapshot, and stale generation results cannot publish.

---

## Phase 4: User Story 2 - Authorize Dynamic Spaces Without Restart (Priority: P1)

**Goal**: Resolve deterministic runtime policy for newly created spaces using explicit assignments, stable selectors, ancestor inheritance, and child overrides.

**Independent Test**: Allow one project with descendant inheritance, create task/session children after startup, override one child, deny a sibling, and update the policy while space creation is concurrent without restarting the daemon.

### Tests for User Story 2

- [X] T030 [P] [US2] Add failing policy resolution unit tests for exact space, nearest ancestor, explicit child override, stable selectors, deny tie-break, default deny, and sibling isolation in `internal/storage/policy_test.go`
- [X] T031 [P] [US2] Add failing space creation integration tests for effective policy generation and concurrent activation in `internal/daemon/spaces_events_test.go`
- [X] T032 [P] [US2] Add failing configuration tests for policy assignment grammar, invalid selector combinations, canonical ordering, and generated policy identities in `internal/config/config_test.go`

### Implementation for User Story 2

- [X] T033 [US2] Implement canonical provider policy and assignment conversion from TOML selectors in `internal/config/validate.go` and `internal/config/identity.go`
- [X] T034 [US2] Implement current-space, nearest-ancestor, stable-attribute, deny-tie, and default-deny resolution with effective decision persistence in `internal/storage/policy.go`
- [X] T035 [US2] Integrate current-generation effective policy creation into the space creation transaction in `internal/storage/repository.go`
- [X] T036 [US2] Add policy generation lookup and current-decision enforcement to processing, embedding, aggregate, and semantic query preparation in `internal/storage/jobs.go`, `internal/storage/semantic.go`, `internal/storage/aggregates.go`, and `internal/storage/semantic_search.go`
- [X] T037 [US2] Complete US2 policy and dynamic-space tests in `internal/storage/policy_test.go`, `internal/daemon/spaces_events_test.go`, and `internal/config/config_test.go`

**Checkpoint**: A future space UUID is never required in startup configuration; every new space receives a deterministic current-generation decision before provider work is evaluated.

---

## Phase 5: User Story 3 - Recover Work After Configuration Changes (Priority: P1)

**Goal**: Represent policy-blocked work honestly and recover affected processing, embedding, and semantic work online through durable bounded reconciliation.

**Independent Test**: Block processing and embedding under one generation, expand policy, restart during reconciliation, and verify newly eligible work resumes without duplicate completed results or a daemon restart.

### Tests for User Story 3

- [X] T038 [P] [US3] Add failing job state-machine tests for `blocked_policy`, `stale_generation`, retry budgets, re-evaluation, supersession, and active-job uniqueness in `internal/storage/jobs_test.go`
- [X] T039 [P] [US3] Add failing reconciliation recovery tests for durable cursors, bounded batches, no-op activation, crash restart, and capability-specific diff classes in `internal/storage/configuration_test.go`
- [X] T040 [P] [US3] Add failing semantic tests for eligible-only rebuild, valid empty corpus, policy expansion vector reuse, policy narrowing exclusion, and compatibility rebuild in `internal/storage/semantic_test.go`
- [X] T041 [P] [US3] Add failing daemon integration tests for policy correction without processing-version changes or daemon restart in `internal/daemon/config_reconciliation_test.go`

### Implementation for User Story 3

- [X] T042 [US3] Implement `blocked_policy` and `superseded` transitions, attempt outcomes, blocked reason codes, and provider-attempt accounting in `internal/storage/jobs.go`
- [X] T043 [US3] Replace policy rejection success handling with blocked job completion and current-generation re-evaluation in `internal/jobs/service.go`
- [X] T044 [US3] Implement durable reconciliation claim, cursor, batch, completion, and restart recovery operations in `internal/storage/configuration.go`
- [X] T045 [US3] Run reconciliation alongside existing workers and dispatch policy/configuration diff classes in `internal/jobs/service.go` and `internal/daemon/daemon.go`
- [X] T046 [US3] Separate embedding eligibility from searchable projections and update embedding scheduling in `internal/storage/memories.go`, `internal/storage/indexes.go`, and `internal/storage/semantic.go`
- [X] T047 [US3] Rebuild and activate semantic generations against current eligible compatible records only, preserving immediate narrowing filters and partial expansion availability in `internal/storage/semantic.go` and `internal/storage/semantic_search.go`
- [X] T048 [US3] Complete US3 job, reconciliation, semantic, and daemon recovery tests in `internal/storage/jobs_test.go`, `internal/storage/configuration_test.go`, `internal/storage/semantic_test.go`, and `internal/daemon/config_reconciliation_test.go`

**Checkpoint**: Policy-blocked work is not success or retry failure, correction triggers durable online recovery, and one excluded memory cannot block the eligible semantic corpus.

---

## Phase 6: User Story 4 - Apply Proportionate Controls (Priority: P2)

**Goal**: Reject prohibited egress at the smallest safe scope while preserving allowed targets and deterministic complete-scope semantic behavior.

**Independent Test**: Process an allowed target with denied optional context, deny a target before secret/network activity, and execute equivalent multi-space semantic searches in different orders with identical authorization outcomes.

### Tests for User Story 4

- [X] T049 [P] [US4] Add failing provider tests for policy-aware context omission, omission summaries, MIME normalization, target denial before secret resolution, and separate input/output limits in `internal/provider/client_test.go`
- [X] T050 [P] [US4] Add failing processing integration tests for allowed targets with mixed event/memory context and ancestor read-only context in `internal/jobs/service_test.go`
- [X] T051 [P] [US4] Add failing semantic search tests for normalized complete scopes, input-order invariance, separate `semantic_query` policy, and explicit hybrid degradation in `internal/storage/semantic_test.go`

### Implementation for User Story 4

- [X] T052 [US4] Move target policy decisions before minimization and omit denied optional context with safe aggregate counters in `internal/provider/client.go` and `internal/jobs/provider.go`
- [X] T053 [US4] Normalize MIME policy keys and split egress-input, candidate-output, response, context, and aggregate bounds in `internal/provider/client.go`, `internal/provider/contracts.go`, and `internal/config/config.go`
- [X] T054 [US4] Mark ancestor processing memories with proposal capability and prevent read-only ancestor context from causing late batch commit failure in `internal/jobs/validation.go` and `internal/storage/jobs.go`
- [X] T055 [US4] Implement all-or-nothing `semantic_query` authorization over normalized expanded search scope in `internal/storage/semantic_search.go` and `internal/storage/policy.go`
- [X] T056 [US4] Complete US4 provider, processing, and semantic tests in `internal/provider/client_test.go`, `internal/jobs/service_test.go`, and `internal/storage/semantic_test.go`

**Checkpoint**: Denied optional data never leaves the daemon and no longer blocks allowed work; target denial remains fail-closed; multi-space semantic authorization is order-independent.

---

## Phase 7: User Story 5 - Operate with a Minimal, Inspectable Startup (Priority: P2)

**Goal**: Expose the reduced CLI, strict offline validation, UDS reload/status contracts, SIGHUP, redacted health, and operational documentation.

**Independent Test**: Start the default instance without setting arguments, select a second instance with one `--config`, validate/reload/status it, reject removed setting flags, and inspect safe activation diagnostics without exposing secrets or content.

### Tests for User Story 5

- [X] T057 [P] [US5] Add failing CLI tests for required commands, XDG default, one `--config`, removed setting flags/env, maintenance operation arguments, validate/status/reload, and exit codes in `cmd/dmemd/main_test.go`
- [X] T058 [P] [US5] Add failing API handler tests for config status, idempotent reload, expected identity conflict, strict bodies, safe diagnostics, and error mapping in `internal/daemon/config_api_test.go`
- [X] T059 [P] [US5] Add failing OpenAPI contract assertions for `/v1/config` and `/v1/config/reload` in the former `internal/daemon/openapi_test.go` (removed by `specs/003-api-layer-refactor`)
- [X] T060 [P] [US5] Add failing SIGHUP and secret/content leak tests for activation attempts, coalescing, health summary, logs, and status in `internal/daemon/daemon_test.go`

### Implementation for User Story 5

- [X] T061 [US5] Replace implicit daemon and setting-specific CLI parsing with explicit commands, one global locator, offline validation, status, reload, and documented exit codes in `cmd/dmemd/main.go`
- [X] T062 [US5] Implement authenticated redacted status and idempotent reload handlers with expected-identity conflict handling in the former API package (removed by `specs/003-api-layer-refactor`)
- [X] T063 [US5] Add minimal non-secret configuration state to health without exposing paths or references in the former API health handler (removed by `specs/003-api-layer-refactor`)
- [X] T064 [US5] Wire SIGHUP to the serialized manager pipeline with bounded coalescing and current shutdown grace in `cmd/dmemd/main.go` and `internal/daemon/daemon.go`
- [X] T065 [US5] Merge the configuration API schemas and error responses into the former OpenAPI contract (removed by `specs/003-api-layer-refactor`) and update API usage in `docs/api/usage.md`
- [X] T066 [US5] Document the TOML grammar, atomic file publication, reload/status/SIGHUP, secret rotation, restart-required fields, and failure recovery in `docs/operations/runbook.md`
- [X] T067 [US5] Replace flag-based startup examples with default and `--config` workflows in `README.md`
- [X] T068 [US5] Complete US5 CLI, API, OpenAPI, SIGHUP, health, and leak tests in `cmd/dmemd/main_test.go`, `internal/daemon/config_api_test.go`, the former `internal/daemon/openapi_test.go`, and `internal/daemon/daemon_test.go`

**Checkpoint**: Operators use one file, one optional locator, and explicit reload/status workflows; all inspection is actionable and redacted.

---

## Phase 8: Polish & Cross-Cutting Concerns

**Purpose**: Complete documentation synchronization, recovery validation, performance checks, and repository-wide quality gates.

- [X] T069 [P] Add configuration and policy architecture documentation plus accepted ADR updates in `docs/architecture/overview.md`, `docs/adr/0022-explicit-openai-compatible-providers.md`, and a new ADR under `docs/adr/`
- [X] T070 [P] Update testing guidance and deterministic provider/reconciliation drill documentation in `docs/development/testing.md`
- [X] T071 Add a restart drill proving active configuration identity, blocked work, reconciliation cursor, and semantic generation recover correctly in `internal/daemon/config_reconciliation_test.go`
- [X] T072 Add target-scale reload and policy reconciliation performance assertions matching SC-004 and SC-014 in `internal/storage/configuration_test.go` and `internal/daemon/config_reload_test.go`
- [X] T073 Run the end-to-end scenarios in `specs/001-dynamic-provider-config/quickstart.md` and record any implementation-specific command corrections in that file
- [X] T074 Run `make build`, `make test`, `make test-race`, `make vet`, `make schema-check`, `make restore-drill`, `make portability-drill`, `make quality`, and `make offline-default`, fixing failures only in feature-touched files
- [X] T075 Verify `docs/storage/schema-v1.sql` matches `internal/storage/schema-v1.sql`, `docs/api/http-rest-contract.md` matches implemented routes, and all secret/content leak assertions pass in `internal/storage/schema_contract_test.go` and `internal/protocol/httpapi`

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 Setup**: No dependencies.
- **Phase 2 Foundational**: Depends on Phase 1 and blocks every user story.
- **Phase 3 US1**: Depends on Phase 2 and establishes runtime snapshot/reload behavior.
- **Phase 4 US2**: Depends on Phase 2; integrates with US1 snapshots for runtime activation.
- **Phase 5 US3**: Depends on US1 generation activation and US2 policy decisions.
- **Phase 6 US4**: Depends on US2 policy resolution; can proceed in parallel with later US3 reconciliation work after policy interfaces stabilize.
- **Phase 7 US5**: Depends on US1 manager/status behavior; CLI tests and OpenAPI contract work can begin while US2-US4 internals proceed.
- **Phase 8 Polish**: Depends on all selected user stories.

### User Story Dependencies

```text
Foundational
    |
    +--> US1 Dynamic file loading/reload (MVP)
    |       |
    |       +---------------------> US5 Minimal CLI/status
    |
    +--> US2 Dynamic space policy
            |
            +--> US3 Online blocked-work recovery
            |
            +--> US4 Proportionate controls
```

- **US1** is independently demonstrable with provider reload and prior-snapshot preservation.
- **US2** is independently demonstrable using policy resolution and dynamic child creation, with providers otherwise stubbed.
- **US3** requires the generation and policy interfaces from US1/US2 but has an independent blocked-work recovery acceptance suite.
- **US4** requires policy decisions from US2 but independently validates context filtering and semantic query scope.
- **US5** requires the US1 manager but independently validates all operator-facing commands and contracts.

### Within Each User Story

- Write listed tests first and confirm they fail for the intended behavior.
- Complete state/data models before service logic.
- Complete service logic before API/CLI exposure.
- Enforce generation and authority fences in the same transaction as publication.
- Run each story's named tests at its checkpoint before starting dependent work.

## Parallel Opportunities

- T002, T003, and T004 can run in parallel after T001 dependency selection is known.
- T005-T008 are independent test suites and can run in parallel.
- T012 can proceed in parallel with storage schema implementation after the secret-reference contract is fixed.
- Within US1, T019-T022 can be authored in parallel; implementation then follows shared snapshot interfaces.
- Within US2, T030-T032 can be authored in parallel.
- Within US3, T038-T041 can be authored in parallel; T046-T047 can proceed alongside reconciliation worker work after T044 interfaces stabilize.
- Within US4, T049-T051 can be authored in parallel.
- Within US5, T057-T060 can be authored in parallel; T065-T067 can proceed after route/CLI names are stable.
- US3 reconciliation, US4 provider minimization, and US5 interface work can proceed concurrently after US1/US2 shared interfaces stabilize.
- T069 and T070 are independent documentation updates.

## Parallel Examples

### User Story 1

```text
Task T019: Manager activation outcome tests in internal/config/manager_test.go
Task T020: Daemon reload integration tests in internal/daemon/config_reload_test.go
Task T021: Generation fence tests in internal/jobs/service_test.go and internal/storage/semantic_test.go
Task T022: Aggregate snapshot tests in internal/aggregate/service_test.go
```

### User Story 2

```text
Task T030: Policy resolution tests in internal/storage/policy_test.go
Task T031: Dynamic space tests in internal/daemon/spaces_events_test.go
Task T032: Policy grammar tests in internal/config/config_test.go
```

### User Story 3

```text
Task T038: Blocked job tests in internal/storage/jobs_test.go
Task T039: Reconciliation recovery tests in internal/storage/configuration_test.go
Task T040: Eligible semantic corpus tests in internal/storage/semantic_test.go
Task T041: Online recovery tests in internal/daemon/config_reconciliation_test.go
```

### User Story 4

```text
Task T049: Provider minimization tests in internal/provider/client_test.go
Task T050: Mixed-context processing tests in internal/jobs/service_test.go
Task T051: Complete-scope semantic tests in internal/storage/semantic_test.go
```

### User Story 5

```text
Task T057: CLI contract tests in cmd/dmemd/main_test.go
Task T058: Config API tests in internal/daemon/config_api_test.go
Task T059: OpenAPI contract tests in the former `internal/daemon/openapi_test.go` (removed by `specs/003-api-layer-refactor`)
Task T060: SIGHUP and leak tests in internal/daemon/daemon_test.go
```

## Implementation Strategy

### MVP First

1. Complete Setup and Foundational phases.
2. Complete US1 through T029.
3. Stop and demonstrate one-file startup plus processing/embedding atomic runtime reload.
4. Do not expose the final CLI/API as complete until US5, but use direct manager integration tests for the MVP checkpoint.

### Incremental Delivery

1. **US1**: One strict source and safe provider reload.
2. **US2**: Dynamic spaces become policy-addressable without future UUID lists.
3. **US3**: Corrected policy recovers blocked work and eligible semantic indexing.
4. **US4**: Policy becomes proportionate at context/query scope.
5. **US5**: Operators receive the minimal CLI and safe status/reload interface.
6. **Polish**: Documentation, recovery, performance, and all constitutional quality gates.

### Direct-Replacement Rule

- Do not add compatibility flags, deprecated aliases, environment precedence, old-schema migration, or fallback parsing.
- When an old concept conflicts with this task list, replace it and update its tests rather than wrapping it.
- Do not modify unrelated existing worktree changes.

## Notes

- `[P]` marks tasks safe for concurrent execution after their phase prerequisites.
- Story labels provide requirement traceability.
- Every task includes concrete repository paths.
- Use the project-required `sqlite_fts5` build tag through Makefile gates.
- Commit only when explicitly requested; task completion does not imply automatic commits.

---

## Phase 9: Convergence

- [X] T076 Implement durable `superseded` job and attempt transitions, enforce active-job uniqueness across blocked and reconciliation transitions, and add state-machine coverage in `internal/storage/jobs.go` and `internal/storage/jobs_test.go` per FR-017, FR-018, and FR-026 (partial)
- [X] T077 Record configuration and policy generations, external-call start time, and provider-attempt counts before every embedding egress; prove policy blocks do not consume the provider budget in `internal/jobs/service.go` and `internal/storage/jobs_test.go` per FR-017 (contradicts)
- [X] T078 Complete durable bounded multi-batch reconciliation, crash/restart resumption, no-change behavior, diff-class coverage, and online policy-correction recovery tests in `internal/storage/configuration.go`, `internal/storage/configuration_test.go`, `internal/jobs/service.go`, and `internal/daemon/config_reconciliation_test.go` per FR-018 and SC-004 (partial)
- [X] T079 Create daemon integration coverage proving policy expansion recovers blocked processing and embedding without changing processing identity or duplicating completed work, including restart during reconciliation, in `internal/daemon/config_reconciliation_test.go` per US3/AC1-4 (missing)
- [X] T080 Populate and persist from/to policy generations for reconciliation runs, consume the target policy generation during reconciliation, and assert lineage in `internal/daemon/daemon.go`, `internal/storage/configuration.go`, and `internal/storage/configuration_test.go` per plan: activation/reconciliation (partial)
- [X] T081 Make embedding eligibility an explicit current-generation projection before every scheduling or execution decision, remove the global empty-table eligibility fallback, and test mixed eligible/excluded corpora in `internal/storage/memories.go`, `internal/storage/indexes.go`, `internal/storage/semantic.go`, and `internal/storage/semantic_test.go` per FR-019, FR-020, and FR-021 (partial)
- [X] T082 Allow an initially empty eligible semantic corpus to activate as ready and add tests for eligible-only rebuilds, vector reuse on expansion, immediate narrowing exclusion, and incompatible compatibility rebuilds in `internal/storage/semantic.go`, `internal/storage/semantic_search.go`, and `internal/storage/semantic_test.go` per FR-019, FR-020, and FR-021 (partial)
- [X] T083 Split aggregate-input, egress-input, candidate/generated-output, response, and context limits through configuration, identity, provider contracts, enforcement, status, and tests in `internal/config/config.go`, `internal/config/file.go`, `internal/provider/contracts.go`, `internal/provider/client.go`, and `internal/provider/client_test.go` per FR-031 and FR-032 (partial)
- [X] T084 Add processing integration tests for mixed allowed and denied event/memory context and ancestor read-only context, proving safe omissions and allowed-target processing in `internal/jobs/service_test.go` per US4/AC1-2 (missing)
- [X] T085 Add semantic tests for normalized duplicate/order-invariant scopes, all-or-nothing `semantic_query` authorization, query/source-classification separation, and explicit hybrid degradation reasons in `internal/storage/semantic_test.go` per US4/AC3-4 (missing)
- [X] T086 Implement documented CLI exit-code classification for daemon-unavailable, rejected/conflict, and temporary-overload outcomes, and complete command/XDG/locator/removed-settings/status/reload tests in `cmd/dmemd/main.go` and `cmd/dmemd/main_test.go` per FR-027, FR-033, and FR-034 (partial)
- [X] T087 Expose redacted per-capability health, configuration and policy generations, blocked-work counts, and durable reconciliation state from storage through the former `/v1/config` API, with API and contract redaction tests in the former API package, `internal/config/snapshot.go`, `internal/daemon/config_api_test.go`, and the former `internal/daemon/openapi_test.go` per FR-023 and FR-028 (partial)
- [X] T088 Add SIGHUP reload, repeated-signal coalescing, shutdown-grace, activation-diagnostic, and secret/content/query non-leak tests in `internal/daemon/daemon_test.go` per US5/AC2 (missing)
- [X] T089 Synchronize architecture and ADR documentation with strict file activation and removal of setting-specific flags, document deterministic provider/reconciliation drills, add SC-004/SC-014 restart and timing assertions, and execute quickstart and required feature quality gates in `docs/architecture/overview.md`, `docs/adr/0022-explicit-openai-compatible-providers.md`, `docs/adr/`, `docs/development/testing.md`, `internal/storage/configuration_test.go`, `internal/daemon/config_reload_test.go`, and `specs/001-dynamic-provider-config/quickstart.md` per plan: documentation and SC-004 and SC-014 (partial)

---

## Phase 10: Convergence

- [X] T090 Prevent a `blocked_policy` `embed_memory` job from indefinitely blocking `rebuild_semantic` by excluding `blocked_policy` from the rebuild claim/promote gate in `internal/storage/jobs.go` (`ClaimJob` and `PromoteWaitingJobs`) and superseding now-ineligible `blocked_policy` embed jobs during embedding reconciliation in `internal/storage/configuration.go`, with a test that blocks an embed job by policy and verifies the eligible semantic rebuild still completes, per FR-020, FR-021, US3/AC2, and SC-005 (partial)
- [X] T091 Durably audit rejected and failed runtime reloads by recording `configuration_activations` rows with `outcome` of `rejected`/`failed` and populated `diagnostics` for non-activated outcomes in `internal/daemon/daemon.go`, and add a daemon-level test asserting a rejected `POST /v1/config/reload` persists an auditable activation row with diagnostics, per FR-024 (partial)
- [X] T092 Preserve the complete previous effective configuration when runtime `applySnapshot` fails by reverting the manager snapshot to `previous` (or applying before publishing) and making capability application atomic/rollback-safe across processing and embedding in `internal/daemon/daemon.go` and `internal/config/manager.go`, with a test injecting an `applySnapshot` failure that asserts the previous effective config remains active, per FR-006, SC-002, and FR-036 (partial)
- [X] T093 Add tests exercising the SIGHUP signal handler wiring (`handleSIGHUPReloads`) and repeated-signal coalescing end-to-end in `cmd/dmemd/main_test.go` and `internal/daemon/daemon_test.go`, per FR-035, US5/AC2, and Constitution V (partial)
- [X] T094 Add redaction leak tests for an enabled provider covering the persisted `configuration_revisions.redacted_document` audit row and the `/v1/config` response (asserting secret name, endpoint, and query instructions are excluded) in `internal/storage/configuration_test.go` and `internal/daemon/config_api_test.go`, per FR-004 and FR-028 (partial)
