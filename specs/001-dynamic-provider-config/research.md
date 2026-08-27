# Research: Dynamic Provider Configuration

## Decision 1: One Strict TOML File

**Decision**: Use a versioned TOML file as the sole source for daemon paths, server settings, jobs, limits, processing, embedding, and egress policy. Decode into a dedicated file document, reject unknown and duplicate keys, then normalize and validate into the runtime model. Only `config_version = 1` is accepted.

**Rationale**: TOML is readable for operators, supports comments and clear sections, and has a smaller ambiguous syntax surface than YAML. A dedicated document keeps the public grammar independent from internal structs and distinguishes omitted values from explicit zero values.

**Alternatives considered**: JSON was rejected because long operator-owned documents need comments. YAML was rejected because aliases, merge keys, tags, multiple documents, and implicit scalar behavior require additional rejection rules. Multiple files, includes, and overrides were rejected because they violate the single-revision invariant.

## Decision 2: XDG Default and One Locator

**Decision**: The default file is `${XDG_CONFIG_HOME:-$HOME/.config}/dmem/config.toml`. The CLI accepts one global `--config PATH` only for selecting a non-default file. XDG variables locate the file but do not override settings. Provider secret environment variables remain secret sources, not configuration sources.

**Rationale**: This gives each process one inspectable source while matching the current Linux single-user model. The config file remains outside the authority data directory, avoiding a path-discovery cycle.

**Alternatives considered**: `/etc/dmem` was rejected as system-wide scope. A config file inside the data directory was rejected because the data path is itself configured. `DMEM_CONFIG`, setting flags, and setting environment overrides were rejected because they reintroduce precedence.

## Decision 3: No Compatibility or Migration

**Decision**: Delete setting-specific flags, `DMEM_*` setting environment reads, old provider allowlist syntax, aliases, and fallback behavior. Do not generate or import a file from old values. Rewrite schema v1 directly for the new state model.

**Rationale**: The project is in initialization and has no released compatibility obligation. Compatibility code would preserve the multiple-source problem and complicate every identity and state transition.

**Alternatives considered**: Deprecation windows, automatic conversion, and old/new precedence were rejected by explicit scope.

## Decision 4: Explicit Reload and SIGHUP, No Watcher in Version 1

**Decision**: `POST /v1/config/reload` and `dmemd reload` are the primary synchronous workflow. `SIGHUP` invokes the same pipeline asynchronously. The first implementation does not watch the file automatically.

**Rationale**: Explicit reload returns structured activated/no-change/rejected outcomes and avoids parsing editor intermediate files. It satisfies runtime loading while keeping one activation path and deterministic tests.

**Alternatives considered**: Parent-directory `fsnotify` with debounce is viable later, but adds remove/recreate, overflow, event storm, and partial-write semantics. SIGHUP alone was rejected because it cannot return validation results. Polling was rejected because it still requires stable-read semantics and introduces latency.

## Decision 5: Complete-File Atomic Activation

**Decision**: Read a bounded regular file through one descriptor, strictly decode and normalize the whole document, verify enabled secret references, construct all changed runtime clients, calculate identities, verify restart-only fields, persist the activation transition, then publish one immutable effective snapshot. Any failure leaves the old snapshot unchanged.

**Rationale**: Processing, embedding, policy, and limits must never come from mixed revisions. Preparing resources before publication makes rollback unnecessary for validation failures.

**Alternatives considered**: Section-level success and in-place client mutation were rejected because they create mixed revisions and races. Holding a global write lock across network work was rejected; activation performs no provider network probe.

## Decision 6: Startup-Bound and Reloadable Fields

**Decision**: Provider enablement, identity, model, secret reference, policy, request limits, processing version, queue thresholds, disk thresholds, and shutdown grace are reloadable. Data/database/runtime/socket paths, worker count, connection limit, and server/listener timeout are restart-bound in version 1. A candidate changing any restart-bound field is wholly rejected as `config_restart_required`.

**Rationale**: Current storage, listener, lock, and worker pools are constructed at startup. Silently ignoring changed fields would make the effective snapshot differ from the selected file; rebuilding all infrastructure online is disproportionate to provider hot loading.

**Alternatives considered**: All fields dynamic was rejected for excessive lifecycle complexity. All fields restart-only was rejected because it misses the provider goal.

## Decision 7: Immutable Snapshot and Atomic Publication

**Decision**: Maintain one immutable effective snapshot containing normalized redacted configuration, activation generation, source/effective identities, and immutable processing/embedding capability snapshots. Serialize activations with a mutex and publish with one atomic pointer swap. Each operation captures one capability snapshot once.

**Rationale**: Reads are frequent and reloads rare. A captured immutable snapshot prevents one request from combining an old provider with a new policy or identity and avoids locks across network calls.

**Alternatives considered**: A mutable config under an RW lock was rejected because repeated reads can still mix generations. Independent processing and embedding pointers were rejected because one file activation must be observable atomically.

## Decision 8: Separate Source, Effective, Capability, and Policy Identity

**Decision**: Hash exact file bytes for source identity; hash canonical normalized non-secret values for effective identity; separately hash output-compatibility fields for each capability and canonical policy rules for policy identity. Successful effective transitions receive monotonic activation/configuration/policy generations. Secret values never enter hashes.

**Rationale**: Formatting-only edits must be `no_change`, while restoring old content must still create a new activation event. Policy changes affect eligibility but should not force regeneration of compatible vectors.

**Alternatives considered**: Modification time, operator-entered versions, one raw file hash, and secret-inclusive hashes were rejected as unstable, incomplete, or unsafe.

## Decision 9: Typed Secret References

**Decision**: The file stores `{ source = "environment", name = "..." }`; version 1 supports environment references only. No interpolation, plaintext value, command, file-secret, or default credential chain is accepted. Activation checks availability without retaining the value; each external request resolves it once after policy validation.

**Rationale**: This preserves secret rotation and policy-before-secret behavior without turning environment variables into hidden configuration overrides.

**Alternatives considered**: Caching at reload, plaintext values, generic interpolation, and multiple secret backends were rejected for exposure or unnecessary scope.

## Decision 10: Generated Policy Identity and Explicit Inheritance

**Decision**: Define immutable canonical policy rules and generated policy identities. Assignments can target exact spaces, stable space type/external-id selectors, or ancestors with explicit descendant inheritance. Current-space overrides win; otherwise nearest matching ancestor wins; explicit deny wins at equal specificity; unmatched spaces deny by default; siblings never inherit.

**Rationale**: Future UUIDs cannot be listed at startup. Separating policy definition, assignment, and effective decision preserves provenance and allows deterministic child creation and bounded policy reconciliation.

**Alternatives considered**: Copying policy onto each child loses inheritance provenance. Arbitrary expressions or regex selectors were rejected as hard to audit. Exact UUID-only policy was the original blocker.

## Decision 11: `blocked_policy` Is a First-Class Job State

**Decision**: A disallowed target moves to `blocked_policy`, without resolving a secret, consuming provider retry budget, or making a network call. Disallowed optional context is omitted and counted. Policy reconciliation either leaves the job blocked with a newer evaluated generation, returns it to pending, or supersedes it if no longer applicable.

**Rationale**: Policy refusal is neither successful no-result nor transient provider failure. A durable blocked state supports status, safe online recovery, and accurate metrics.

**Alternatives considered**: `succeeded` with a result code was rejected as misleading. `waiting` was rejected because normal promotion would repeatedly execute it. Creating a fresh job for every generation was rejected because it obscures one logical intent.

## Decision 12: Configuration and Policy Generation Fence

**Decision**: Every provider attempt records configuration generation, policy generation, compatibility identity, effective space decision, and existing lease generation. Publication in the authority transaction verifies all current fences. An obsolete attempt becomes `stale_generation` and is re-evaluated; it cannot publish a candidate, embedding, aggregate, or index under the new identity.

**Rationale**: Context cancellation cannot guarantee a remote call stopped. Existing lease fencing protects ownership, not configuration changes. The database check is required to prevent stale results after reload or restart.

**Alternatives considered**: Waiting for calls to drain, context cancellation only, and in-memory checks were rejected as unsafe or unavailable after restart.

## Decision 13: Separate Searchable and Embedding-Eligible Corpora

**Decision**: Maintain embedding eligibility independently from memory searchability. The effective semantic corpus is the intersection of current searchable revisions, current-policy eligibility, and embeddings compatible with the active embedding identity. Policy narrowing excludes vectors from retrieval immediately; physical rebuild follows. Policy expansion reuses compatible vectors or schedules missing embeddings. An empty eligible corpus is a valid ready corpus.

**Rationale**: Local keyword visibility and permission to send content externally are different facts. A denied record must not block eligible records, and a policy change should not recreate identical vectors.

**Alternatives considered**: One vector index per space was rejected for orchestration complexity. Storing policy generation in every vector key was rejected because it duplicates compatible embeddings.

## Decision 14: Semantic Query Is a Separate Policy Action

**Decision**: Normalize, sort, and expand the complete requested scope, then authorize one `semantic_query` action. Use all-or-nothing scope semantics; hybrid may explicitly degrade to keyword. Do not classify query text as a manual memory source.

**Rationale**: Authorization cannot depend on the first input space or array order, and query egress differs from document embedding.

**Alternatives considered**: Silently dropping spaces was rejected because it misrepresents scope. One query embedding per space was rejected as redundant and difficult to rank consistently.

## Decision 15: Durable Diff-Class Reconciliation

**Decision**: Commit activation and bounded reconciliation intents together, then process them asynchronously with durable cursors. No-op changes do nothing; policy expansion re-evaluates blocked/excluded records; policy narrowing fences calls and excludes records immediately; embedding compatibility changes rebuild all eligible derived artifacts; unrelated capability changes do not rebuild them.

**Rationale**: Activation must be quick and crash-safe, while corpus scans may be large. Classifying the normalized diff minimizes provider calls and avoids duplicate completed processing results.

**Alternatives considered**: Full rebuild on every edit violates no-change and unaffected-work requirements. In-memory scans lose work on crash. Waiting for reconciliation before activation makes policy narrowing unsafe and reload latency unbounded.

## Decision 16: Safe Status and Diagnostics

**Decision**: Add authenticated UDS configuration status and reload routes. Serialize a dedicated redacted view by allowlist. Return identities, generations, state, secret reference availability, policy rule counts, blocked counts, and reconciliation state; never return secret values, content, query text, provider responses, or raw source snippets. Health receives only a minimal effective identity and reload outcome.

**Rationale**: Operators need actionable status, but existing health intentionally avoids local paths and secret-bearing details.

**Alternatives considered**: Serializing full config then deleting fields was rejected as fragile. Putting full status in unauthenticated health was rejected as unnecessary exposure.
