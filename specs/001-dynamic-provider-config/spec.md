# Feature Specification: Dynamic Provider Configuration

**Feature Branch**: `not-created`

**Created**: 2026-08-27

**Status**: Draft

**Input**: User description: "1.减少或降低安全管控程序；2.优化启动方式，现在的参数过多。尤其是LLM和Embedding的provider，这些是否能做成动态加载配置。"

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Load and Reload One Configuration File (Priority: P1)

As the local dMem operator, I can define daemon, processing provider, embedding provider, policy, limit, path, and worker settings in one configuration file. The daemon loads that file at startup and atomically reloads valid changes while remaining available.

**Why this priority**: Configuration is currently spread across a long startup command and environment. A single durable source makes startup repeatable and allows provider changes without rebuilding command lines or restarting the daemon.

**Independent Test**: Put all settings in one configuration file, start dMem without individual setting arguments, change and reload the processing and embedding sections independently, verify valid changes take effect without restart, and verify an invalid edit leaves the last valid configuration active.

**Acceptance Scenarios**:

1. **Given** a valid configuration file with processing enabled, **When** the daemon starts, **Then** new eligible processing work uses that provider without provider-specific startup arguments.
2. **Given** independently active processing and embedding providers, **When** the operator changes only the embedding section and requests or triggers reload, **Then** embedding adopts the new valid configuration while processing keeps its effective configuration.
3. **Given** an active provider, **When** the operator disables that capability in the file and reloads it, **Then** dMem stops new external calls for that capability while local authority writes and unaffected capabilities continue.
4. **Given** a changed file that is invalid or cannot be activated safely, **When** reload is attempted, **Then** the complete previous effective configuration remains active and the operator receives an actionable, content-safe explanation.
5. **Given** a daemon restart, **When** dMem reads the same configuration file, **Then** the last saved valid non-secret settings and secret references are restored without reconstructing a startup command.

---

### User Story 2 - Authorize Dynamic Spaces Without Restart (Priority: P1)

As an operator or agent creating spaces during normal use, I can rely on explicit, understandable provider policy inheritance or runtime authorization so that newly created eligible spaces can use automatic processing and semantic embedding without daemon restarts.

**Why this priority**: Dynamic space creation is a core workflow. Exact startup-time UUID lists make provider capabilities unusable for new project, task, and session spaces.

**Independent Test**: Authorize a parent scope, create child spaces after daemon startup, verify the documented inheritance result, change one child's effective authorization at runtime, and confirm no daemon restart is required.

**Acceptance Scenarios**:

1. **Given** a parent space with provider use explicitly allowed and inheritance enabled, **When** an eligible child space is created, **Then** the child receives the effective policy before its first provider-backed job is evaluated.
2. **Given** a child space whose effective policy is inherited, **When** the operator applies an explicit child override, **Then** the override takes effect without changing sibling spaces.
3. **Given** a space that is not authorized, **When** it creates authority data, **Then** the data is stored normally, no prohibited external call occurs, and provider work is recorded as policy-blocked rather than falsely successful.
4. **Given** an authorization policy change, **When** the new policy becomes effective, **Then** affected pending or policy-blocked work is reconciled without interrupting unrelated spaces.

---

### User Story 3 - Recover Work After Configuration Changes (Priority: P1)

As an operator, I can correct a provider configuration or policy and have previously blocked processing and embedding work recover online, without changing unrelated processing identities or restarting the daemon.

**Why this priority**: Dynamic loading has little value if previous policy decisions remain terminal or semantic indexes remain blocked after the configuration is corrected.

**Independent Test**: Cause processing and embedding work to be blocked by policy, apply a policy update that makes the work eligible, and verify the work is re-evaluated and the semantic channel recovers while unrelated completed results are not duplicated.

**Acceptance Scenarios**:

1. **Given** processing work blocked by a prior policy, **When** a newer effective policy permits it, **Then** the work can be re-evaluated without inventing a new processing algorithm version.
2. **Given** one memory excluded from embedding, **When** other memories remain eligible, **Then** eligible memories can still form and use a semantic index.
3. **Given** an embedding policy is expanded, **When** previously excluded memories become eligible, **Then** their embeddings and affected semantic indexes are scheduled for recovery automatically.
4. **Given** a provider configuration changes incompatibly, **When** it becomes effective, **Then** derived results whose compatibility identity changed are marked for rebuild while authoritative data remains available.

---

### User Story 4 - Apply Proportionate Controls (Priority: P2)

As a user of automatic processing and semantic retrieval, I receive the best permitted result when only part of a request is ineligible, rather than losing an entire capability because one context item or one memory cannot be sent externally.

**Why this priority**: Security controls should prevent prohibited egress at the smallest safe scope. Rejecting unrelated eligible work reduces availability without improving protection.

**Independent Test**: Submit an allowed processing target with mixed allowed and disallowed context, and maintain a semantic corpus containing both eligible and excluded memories; verify prohibited content is never sent while eligible processing and retrieval continue.

**Acceptance Scenarios**:

1. **Given** an allowed processing target with disallowed optional context, **When** processing runs, **Then** disallowed context is omitted, the allowed target is processed, and omission counts and reasons are auditable without exposing content.
2. **Given** a target itself is disallowed, **When** processing is evaluated, **Then** no external call occurs and the work enters an explicit policy-blocked state.
3. **Given** a search over multiple spaces, **When** the same space set is supplied in a different order, **Then** authorization and semantic availability produce the same result.
4. **Given** a semantic query and an embedding corpus, **When** egress policy is evaluated, **Then** query authorization is distinguished from stored-memory source classification.

---

### User Story 5 - Operate with a Minimal, Inspectable Startup (Priority: P2)

As an operator, I can start the default instance without individual configuration arguments, select a non-default configuration file with at most one locator, inspect the effective redacted configuration, and diagnose why a provider or policy is inactive without exposing secrets, memory content, or query text.

**Why this priority**: Reducing startup arguments improves repeatability only if the resulting configuration remains visible, attributable, and safe to troubleshoot.

**Independent Test**: Start a default instance without setting arguments, start a separate instance by selecting one alternative configuration file, inspect the effective configuration and provider states, restart it, and verify the same effective settings and safe diagnostics are available.

**Acceptance Scenarios**:

1. **Given** an initialized instance with a configuration file in the standard location, **When** the daemon starts, **Then** the operator does not need to supply paths, provider endpoints, models, policy lists, limits, worker settings, or secret-reference arguments.
2. **Given** an active configuration, **When** the operator inspects it, **Then** the output shows capability state, configuration identity, policy identity, activation time, and safe health information but never secret values or protected content.
3. **Given** no provider has been configured, **When** the daemon starts, **Then** it remains local and offline by default and local-only workflows remain usable.
4. **Given** an operator needs a non-default instance, **When** the daemon starts with one configuration-file locator, **Then** all operational settings still come exclusively from that selected file.

### Edge Cases

- Two operators attempt conflicting provider updates concurrently; only one complete configuration becomes effective and neither sees a partially applied state.
- The configuration file changes more than once while a reload is in progress; only a complete, stable file revision is validated and activated.
- The configuration file is temporarily absent, partially written, unreadable, or replaced during editing; the last valid effective configuration continues running and the failure is reported safely.
- An operator requests reload when the file has not changed; no compatibility identity changes and no work is unnecessarily reconciled.
- The provider is unreachable during validation or after activation; authoritative writes continue and work remains recoverable under bounded retry behavior.
- A secret reference is missing, empty, or changes independently of non-secret configuration; no secret value is persisted or disclosed.
- The active embedding configuration changes model behavior or output dimension while retaining a similar display name; incompatible derived data is not treated as current.
- A policy is narrowed while provider calls or jobs are already in flight; stale work cannot publish results under the newer policy generation.
- A parent policy changes while descendants have explicit overrides; inherited descendants update, while overridden descendants retain their explicit decision.
- A space is created concurrently with a policy update; its effective policy is deterministic and auditable.
- A configuration update is valid but would exclude part of the current semantic corpus; eligible retrieval continues and excluded records are clearly accounted for.
- The selected configuration file is unavailable or contains an invalid revision; the daemon does not silently activate an unvalidated provider configuration.
- The operator restores an earlier valid file revision; reload records it as a new activation event and triggers the same compatibility and reconciliation checks as any update.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST allow the daemon to start and provide local authority, manual-memory, and keyword-retrieval workflows without any external provider configuration.
- **FR-002**: The system MUST use one selected configuration file as the sole source of daemon paths, worker settings, operational limits, processing provider settings, embedding provider settings, and provider policies.
- **FR-003**: The system MUST load the selected configuration file at startup and MUST allow authorized local operators to validate and atomically reload it while the daemon is running.
- **FR-004**: Persisted provider configuration MUST contain secret references only; secret values MUST NOT be persisted, returned by inspection, written to audit records, or included in errors.
- **FR-005**: A configuration file revision MUST become effective atomically only after the complete file, all required fields, limits, provider identity, policy identity, and secret references are validated.
- **FR-006**: A startup with an invalid configuration MUST fail before service becomes available; a failed runtime reload MUST leave the complete previous effective configuration unchanged and MUST provide an actionable, content-safe reason.
- **FR-007**: Processing and embedding configurations MUST be independently enabled, disabled, changed, and attributed so failure or maintenance of one capability does not stop the other.
- **FR-008**: Every effective provider configuration MUST have a stable non-secret compatibility identity and every effective egress policy MUST have a distinct generation or identity.
- **FR-009**: Configuration identities MUST change whenever a setting changes in a way that can alter provider output compatibility, policy eligibility, or reproducibility.
- **FR-010**: The system MUST keep external processing, embedding, network access, and provider telemetry disabled until an operator explicitly activates the relevant capability.
- **FR-011**: Egress policy MUST support runtime authorization based on explicit space, stable space attributes, or ancestor-derived scope, without requiring all future internal UUIDs to be known at daemon startup.
- **FR-012**: Every space MUST have a deterministic effective provider policy derived from explicit policy assignment, documented inheritance, and explicit overrides; sibling spaces MUST NOT become authorized implicitly.
- **FR-013**: Space creation and policy updates MUST produce an auditable effective-policy result before provider work for the affected space is evaluated.
- **FR-014**: Policy checks MUST prevent prohibited content from being sent externally before secrets are resolved or network activity begins.
- **FR-015**: If a processing target is allowed but optional context is not allowed, the system MUST omit the disallowed context and continue within remaining quality and size bounds.
- **FR-016**: If a processing target itself is not allowed, the system MUST make no provider call and MUST record the work as policy-blocked rather than completed successfully.
- **FR-017**: Policy-blocked work MUST remain distinguishable from successful no-result work, permanent invalid work, and transient provider failure.
- **FR-018**: When configuration or policy identity changes, the system MUST reconcile affected policy-blocked, pending, and derived work without restarting the daemon and without duplicating unaffected completed results.
- **FR-019**: Semantic indexing MUST continue for policy-eligible memories even when other searchable memories are policy-excluded.
- **FR-020**: A single policy-excluded embedding MUST NOT cancel or indefinitely block rebuilding the eligible semantic corpus.
- **FR-021**: When an embedding policy expands, newly eligible memories MUST be identified and scheduled for embedding; when it narrows, excluded records MUST cease to participate in the effective semantic corpus.
- **FR-022**: Semantic query authorization MUST be evaluated as a query-specific egress action over the complete normalized requested scope, independent of stored-memory source classifications and input ordering.
- **FR-023**: The system MUST expose safe status for each provider capability, including enabled state, effective configuration identity, policy identity, last activation outcome, blocked-work count, and reconciliation state.
- **FR-024**: Provider configuration changes, activation failures, policy changes, effective-policy decisions, rollback, and reconciliation MUST be auditable without recording secrets, raw event or memory content, query text, authorization headers, or provider responses.
- **FR-025**: Configuration changes MUST NOT mutate authoritative events, memories, revisions, evidence, or state history; affected derived data MUST remain rebuildable.
- **FR-026**: Work started under an obsolete configuration or policy generation MUST NOT publish results as though it ran under the current generation.
- **FR-027**: The normal daemon startup interface MUST accept no individual settings; it MAY accept one configuration-file locator, while all effective values come from the selected file.
- **FR-028**: The system MUST provide a redacted effective-configuration view suitable for startup verification and troubleshooting.
- **FR-029**: Setting-specific startup arguments and environment-variable configuration MUST be removed as part of this feature, without a compatibility or migration layer.
- **FR-030**: The system MUST retain strict output validation, bounded provider requests, concurrency controls, retry limits, lease fencing, revision conflict detection, local-only default exposure, and explicit authority review boundaries.
- **FR-031**: The system MUST normalize policy-relevant content classifications so equivalent standard representations are evaluated consistently.
- **FR-032**: Input egress limits, generated-result limits, and aggregate-input limits MUST be independently expressible and reported so changing one capability does not unintentionally disable another.
- **FR-033**: Individual operational settings MUST NOT be accepted from startup arguments or environment variables once file-based configuration migration is complete; startup MAY accept only the daemon action and one configuration-file locator for non-default instances.
- **FR-034**: If no configuration-file locator is supplied, the daemon MUST use a documented standard location so the default instance can start without setting arguments.
- **FR-035**: The system MUST detect or accept an explicit request to load file changes without requiring daemon restart, and MUST report the loaded file revision and activation outcome.
- **FR-036**: Runtime reload MUST evaluate the complete configuration as one revision; it MUST NOT combine sections from different file revisions or partially activate a changed file.
- **FR-037**: A file revision whose effective values are unchanged MUST NOT create a new compatibility identity or trigger unnecessary reconciliation.
- **FR-038**: The configuration format MUST reject unknown or misspelled settings with a location-specific validation message rather than silently ignoring them.

### Key Entities

- **Configuration File Revision**: One complete, validated revision containing all daemon and provider settings, identified independently from its filesystem modification time.
- **Provider Configuration**: A versioned, non-secret section for one processing or embedding capability, including provider identity, model identity, connection settings, operational limits, secret references, lifecycle state, and compatibility identity.
- **Provider Policy**: A versioned set of explicit egress decisions covering capability, space scope, content classification, source classification, sensitivity, inheritance behavior, and bounded-use rules.
- **Policy Assignment**: The binding between a provider policy and a space or stable space attribute, including whether descendants inherit it and whether a child has an explicit override.
- **Effective Provider State**: The currently active configuration and policy identity for one capability, including activation status, health, and reconciliation state.
- **Configuration Activation**: An auditable attempt to validate and atomically make one complete file revision effective, including success, failure, rollback, actor, and time.
- **Policy-Blocked Work**: Processing or embedding work that was not externally executed because its target was ineligible under a specific policy generation and that may become eligible after an explicit policy change.
- **Embedding Eligibility**: The effective determination that a memory revision belongs to the semantic corpus for a specific embedding configuration and policy identity.
- **Reconciliation Run**: A bounded, auditable evaluation of work and derived data affected by a configuration or policy transition.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: An operator can start the default initialized daemon using no individual setting arguments and complete startup configuration verification in under 2 minutes.
- **SC-002**: An operator can activate, update, disable, or roll back either provider capability without a daemon restart, and 100% of successful changes become effective atomically with no partial configuration visible.
- **SC-003**: A space created after daemon startup can receive its intended effective provider policy and begin eligible automatic work without restart or manual UUID-list editing.
- **SC-004**: After a corrected policy becomes effective, 95% of newly eligible policy-blocked jobs enter reconciliation within 30 seconds, while unaffected completed jobs are not duplicated.
- **SC-005**: In a corpus containing both eligible and policy-excluded memories, 100% of eligible memories can complete semantic indexing without sending excluded content and without an excluded memory blocking the eligible index.
- **SC-006**: Reordering the same set of requested spaces changes neither the authorization outcome nor semantic-channel availability in all conformance tests.
- **SC-007**: During mixed-context processing tests, 100% of prohibited optional context is omitted before external activity, while allowed targets continue processing whenever at least one valid bounded request can be formed.
- **SC-008**: Across restart tests, 100% of last valid enabled configurations restore their non-secret effective state without repeating startup arguments, and no secret value appears in persisted configuration, status, audit, or errors.
- **SC-009**: In incompatible embedding-configuration change tests, 100% of affected derived records are detected as non-current before they can be represented as compatible search results.
- **SC-010**: Local authority writes, manual memory management, and keyword retrieval remain available in 100% of tests where providers are absent, disabled, invalid, unreachable, or being reconfigured.
- **SC-011**: At least 90% of operators in a documented usability test can identify why a provider is inactive or work is blocked on the first inspection, without access to protected content or secret values.
- **SC-012**: In all startup conformance tests, removed setting-specific arguments and environment variables have no supported compatibility path and cannot override the selected configuration file.
- **SC-013**: In all configuration conformance tests, 100% of effective operational values come from one selected file revision, and unknown or misspelled settings are never silently accepted.
- **SC-014**: A valid configuration-file change becomes effective without daemon restart within 10 seconds of an explicit reload request or configured change detection, while invalid or partial files cause zero effective-setting changes.

## Assumptions

- dMem remains a local, single-user daemon; this feature does not introduce a network-facing administration mode or a new multi-user authorization model.
- The local operator who can already control the private daemon endpoint is authorized to manage provider configurations and policies.
- Dynamic loading means applying validated configuration while the daemon remains running; it does not mean loading executable provider plugins or arbitrary code.
- Processing and embedding continue to use independently controlled provider capabilities even when they happen to reference the same external service.
- Provider use remains opt-in and offline by default. Reducing security controls means replacing static, global, and unrecoverable restrictions with proportionate, scoped, auditable controls, not permitting unrestricted egress.
- Secret values remain supplied by an external secret source; dMem persists only references and reports missing references safely.
- The project is in its initialization stage. Existing setting-specific startup arguments and environment variables are removed directly; no compatibility, precedence, import, or migration behavior is required.
- The default instance uses a documented standard configuration-file location. A non-default instance may be selected by one file locator, which identifies the source but does not override values inside it.
- Runtime reload may be initiated explicitly or by safe file-change detection; either path has identical full-file validation and atomic activation semantics.
- Policy inheritance is explicit and applies through ancestor relationships only when enabled; sibling spaces never inherit from one another.
- Configuration and policy transitions preserve authoritative data and may trigger bounded background reconciliation of derived work.
- Existing accepted architectural requirements for immutable revisions, review before destructive memory change, scope isolation, bounded retrieval, private local exposure, and deterministic verification remain in force.

## Scope Boundaries

- This feature includes loading all daemon, processing, embedding, policy, limit, path, and worker settings from one selected configuration file.
- This feature includes runtime validation and atomic reload of processing and embedding provider settings, egress policies, policy inheritance, activation history, effective status, and recovery of affected work.
- This feature directly removes setting-specific startup arguments and environment-variable configuration; it retains only daemon actions and an optional configuration-file locator.
- This feature includes correcting policy granularity where one disallowed optional item currently blocks unrelated eligible work.
- This feature does not remove provider output validation, bounded calls, lease fencing, revision conflict checks, local-only defaults, or review requirements for authoritative changes.
- This feature does not add arbitrary executable provider plugins, remote administration, multi-tenant ACLs, or autonomous destructive memory updates.
- Broad maintenance, restore, schema migration, filesystem-path, and conflict-case recovery improvements identified by the wider audit are separate features unless required directly for provider-configuration migration.
