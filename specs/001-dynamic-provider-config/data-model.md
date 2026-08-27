# Data Model: Dynamic Provider Configuration

## Overview

The model separates operator-owned file revisions, effective runtime generations, policy decisions, durable work, and rebuildable derived artifacts. Configuration records never contain secret values. Authority memory entities remain unchanged by configuration activation or reconciliation.

## Configuration File Revision

Represents one stable read of the selected TOML file.

| Field | Rules |
|-------|-------|
| `revision_id` | UUID generated for the load attempt |
| `source_identity` | SHA-256 of exact bounded file bytes |
| `effective_identity` | SHA-256 of canonical normalized non-secret values; nullable when parsing fails |
| `config_version` | Exactly `1` when valid |
| `loaded_at` | UTC timestamp |
| `redacted_document` | Canonical allowlisted status representation; nullable when invalid |

Multiple source identities may share one effective identity. Secret values are never represented.

## Configuration Activation

Append-only record of an explicit reload, SIGHUP, or startup load.

| Field | Rules |
|-------|-------|
| `activation_id` | UUID |
| `revision_id` | References the attempted file revision when readable |
| `previous_activation_id` | Previous successful activation, nullable on first startup |
| `trigger` | `startup`, `api`, or `sighup` |
| `requested_by` | Logical local caller or daemon identity |
| `outcome` | `activated`, `no_change`, `rejected`, or `failed` |
| `error_code` | Stable safe code; nullable on success |
| `diagnostics` | Field paths, line/column, codes; no values or source excerpts |
| `started_at`, `finished_at` | UTC timestamps |

Only `activated` changes effective generations. `no_change` may update the active source identity but creates no compatibility generation or reconciliation.

## Effective Configuration State

Singleton current pointer and reload status.

| Field | Rules |
|-------|-------|
| `activation_id` | Current successful activation |
| `source_identity` | Most recently loaded source identity |
| `effective_identity` | Current canonical effective identity |
| `activation_generation` | Positive monotonic integer |
| `activated_at` | UTC timestamp |
| `last_attempt_id` | Latest activation attempt |

The process also holds an immutable in-memory snapshot corresponding exactly to this effective state.

## Capability State

One current record for `processing` and one for `embedding`.

| Field | Rules |
|-------|-------|
| `capability` | Primary key: `processing` or `embedding` |
| `enabled` | Boolean |
| `configuration_identity` | Canonical non-secret capability hash; nullable when disabled |
| `configuration_generation` | Monotonic per capability |
| `compatibility_identity` | Output/artifact compatibility hash; nullable when disabled |
| `policy_identity` | Current canonical policy hash; nullable when disabled |
| `policy_generation` | Monotonic per capability |
| `activation_id` | Activation that established the state |
| `health_state` | `disabled`, `ready`, `degraded`, or `unavailable` |
| `reconciliation_state` | `idle`, `queued`, `running`, or `failed` |

Configuration generation changes when effective capability behavior changes. Policy generation changes when policy becomes a new effective transition, including reactivating earlier policy content. A no-op changes neither.

## Provider Policy

Immutable canonical rules for one capability.

| Field | Rules |
|-------|-------|
| `capability` | `processing` or `embedding` |
| `policy_identity` | Content-derived primary identity |
| `default_decision` | Must be `deny` |
| `allowed_content_types` | Normalized MIME type set |
| `allowed_source_types` | Closed source-type set |
| `allowed_sensitivities` | `normal`, optionally `sensitive` |
| `query_allowed` | Embedding-only semantic query action |
| `created_at` | UTC timestamp |

Policy identity is generated, not operator-authored. Optional display labels do not affect correctness.

## Policy Assignment

Immutable rule connecting a policy to a stable space selector.

| Field | Rules |
|-------|-------|
| `assignment_id` | UUID |
| `policy_identity` | Owning policy |
| `selector_kind` | `space`, `space_type`, or `external_id` |
| `space_id` | Required only for exact-space selector |
| `space_type` | Required for type selector; existing closed enum |
| `external_id` | Required for stable external-ID selector |
| `effect` | `allow` or `deny` |
| `include_descendants` | Boolean; meaningful for exact space selector |
| `priority` | Deterministic integer for attribute rules |

Resolution order is current-space exact assignment, nearest ancestor assignment with inheritance, then stable attribute assignment. Explicit deny wins at equal specificity. No match means deny. Sibling assignment never participates.

## Effective Space Policy

Rebuildable projection explaining one space decision for one generation.

| Field | Rules |
|-------|-------|
| `capability`, `space_id`, `policy_generation` | Composite primary identity |
| `decision` | `allow` or `deny` |
| `source_assignment_id` | Assignment that determined the result, nullable for default deny |
| `source_space_id` | Ancestor or current space, nullable for attribute/default |
| `inheritance_depth` | Zero for current space, positive for ancestor, nullable otherwise |
| `reason_code` | Safe stable code |
| `evaluated_at` | UTC timestamp |

Missing current-generation projection cannot authorize by falling back to an older generation. The execution path must synchronously resolve current rules or keep work blocked.

## Durable Job Changes

Existing jobs gain configuration-aware intent and state.

| Field | Rules |
|-------|-------|
| `status` | Adds `blocked_policy` and `superseded` |
| `capability` | Nullable for local-only jobs |
| `configuration_generation` | Desired generation for provider work |
| `policy_generation` | Last desired/evaluated policy generation |
| `compatibility_identity` | Derived artifact or processing identity |
| `blocked_reason_code` | Safe code only when blocked |
| `reconciliation_run_id` | Nullable lineage |
| `provider_attempt_count` | Separate from lease attempts; policy block does not increment it |

### Job State Transitions

```text
pending/waiting/retrying -> running
running -> succeeded
running -> retrying -> running
running -> failed
running -> blocked_policy
running -> superseded
blocked_policy -> pending       policy/config now permits work
blocked_policy -> blocked_policy current policy still denies
blocked_policy -> superseded    target no longer applicable
running -> pending              stale generation fenced and re-evaluated
```

`blocked_policy` is not selected by the normal retry scheduler and does not consume provider retries.

## Job Attempt Changes

| Field | Rules |
|-------|-------|
| `configuration_generation` | Captured before external work |
| `policy_generation` | Captured before external work |
| `effective_decision_id` | Policy evidence used for target |
| `compatibility_identity` | Captured identity |
| `external_call_started_at` | Null proves no external call |
| `outcome` | Adds `blocked_policy` and `stale_generation` |
| `omitted_context_count` | Non-negative count |
| `omission_summary` | Classification/reason counts only; no content |

Publication requires current lease, configuration generation, policy generation, target revision/content, and current eligibility.

## Embedding Eligibility

Rebuildable projection independent from keyword searchability.

| Field | Rules |
|-------|-------|
| `memory_id`, `revision_id` | Current memory revision |
| `configuration_generation`, `policy_generation` | Decision context |
| `eligible` | Boolean |
| `reason_code` | Safe classification |
| `effective_space_policy` | Decision provenance |
| `evaluated_at` | UTC timestamp |

The current semantic corpus includes only current searchable revisions with current eligibility and compatible embeddings.

## Memory Embedding Changes

| Field | Rules |
|-------|-------|
| `memory_id`, `revision_id`, `content_hash` | Source identity |
| `compatibility_identity` | Provider/model/dimension/preprocessing identity |
| `provider_identity`, `model_identity` | Audit fields |
| `configuration_identity` | Non-secret producer identity |
| `dimension`, `vector`, `processed_at` | Existing validated derived value |

Primary identity includes content hash and compatibility identity, not policy generation. Compatible vectors can be reused after policy changes.

## Semantic Index Generation

| Field | Rules |
|-------|-------|
| `index_version` | Positive generation |
| `compatibility_identity` | Must match query embedding identity |
| `policy_generation` | Eligibility generation represented |
| `state` | `building`, `ready`, `stale`, or `degraded` |
| `dimension` | Positive when corpus requires vectors |
| `eligible_revision_count` | Current eligible authority count |
| `embedded_revision_count` | Compatible vector count |
| `excluded_revision_count` | Searchable but policy-excluded count |
| `corpus_fingerprint` | Canonical eligible revision/content set hash |
| `created_at`, `activated_at` | UTC timestamps |

An empty eligible corpus is valid and ready. Policy narrowing prevents excluded records from search immediately through eligibility filtering, before physical index compaction.

## Reconciliation Run

Durable bounded scan created by an activation diff.

| Field | Rules |
|-------|-------|
| `run_id` | UUID |
| `capability` | Processing or embedding |
| `activation_id` | Triggering activation |
| `change_class` | `policy_expand`, `policy_narrow`, `compatibility`, `enable`, `disable`, or `runtime_limits` |
| `from/to_configuration_generation` | Generation range |
| `from/to_policy_generation` | Generation range |
| `state` | `pending`, `running`, `completed`, or `failed` |
| `cursor`, `scan_high_watermark` | Crash-safe bounded progress |
| `affected_count`, `scheduled_count`, `blocked_count` | Non-negative metrics |
| timestamps | Created, started, updated, finished |

Activation commits run intents, not a full corpus scan. Each bounded batch and cursor update commit atomically.

## Core Invariants

1. No secret value is stored in any entity.
2. Failed activation changes no effective pointer or generation.
3. No-op source changes create no compatibility/policy generation or reconciliation.
4. Policy evaluation occurs before secret resolution and network activity.
5. Optional context denial cannot become target denial.
6. Old configuration or policy generations cannot publish.
7. Searchable authority and embedding eligibility remain distinct.
8. Excluded records cannot appear in current semantic results even if an old vector remains on disk.
9. Policy changes do not invalidate a compatible vector; compatibility changes do.
10. Reconciliation never mutates memory authority.
