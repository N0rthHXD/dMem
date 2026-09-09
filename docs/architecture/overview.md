# Architecture And Data Model

dMem runs as one Go process owned by one Linux user. A file lock permits one
writer per data directory. The API is HTTP/JSON over a user-only Unix socket,
and SQLite is the authority for domain data, idempotency, audit history, jobs,
and index metadata.

## Authority And Derivation

Raw events are committed before optional automatic processing. Memories have a
stable logical ID, immutable revisions, append-only state events, evidence, and
a current projection. FTS rows, embeddings, semantic corpus metadata,
entity/relation/graph index metadata, feedback aggregates, and aggregate freshness are derived and
rebuildable. Derived data must never overwrite authority.

A confirmed memory revision is the stable input for knowledge-graph extraction;
the raw event remains provenance/evidence. Extraction produces entity, alias,
mention, entity-fact, and memory-relation proposals, which are deterministically
validated, risk-classified, and either auto-confirmed (recorded with an
auto-review rule version) or queued for review. Entity facts (`entity_facts`)
describe typed relationships among resolved entities and are distinct from
revision-level `memory_relations`.

Memory content is text. Raw events can contain text, JSON, or an external
reference; binary content and managed attachment storage are not implemented.
An accepted external reference records metadata only and does not copy or
guarantee the referenced resource.

## Spaces And Lifecycle

Every write targets an explicit space. Spaces form a typed hierarchy and recall
can include ancestors, but sibling spaces are not implicitly visible. Scope is
applied before relevance scoring or relation traversal.

Current lifecycle states include `active`, `archived`, `contested`, `expired`,
`superseded`, `retracted`, and `deleted`. Revisions are never edited in place.
Optimistic updates carry an expected revision, and frozen memories reject
automatic changes. Logical deletion removes a memory from retrieval while
retaining revisions, evidence, state, and audit records.

## Processing And Review

Event ingestion creates durable extraction work in the same transaction. The
default processor is a no-op, so event capture remains useful without an
external provider. An explicitly configured OpenAI-compatible processing
provider can produce typed candidates. Candidates remain independent review
objects until confirmed or rejected; model confidence cannot autonomously
revise or delete a memory.

Processing and embedding providers are separately configured, independently
rate-limited, and disabled by default. Provider calls run outside SQLite write
transactions. Provider/model and processing identities are stored with work so
results remain attributable.

## Configuration And Policy

All daemon settings come from one strict TOML file selected by XDG default or a
single `--config` locator. Setting-specific flags and environment overrides are
removed. The file stores provider endpoints, models, secret references, limits,
policies, queue thresholds, and paths. Secret values never enter the file,
database, status, or logs; only typed environment references are persisted.

A reload (API `POST /v1/config/reload`, `dmemd reload`, or `SIGHUP`) reads,
strictly decodes, and atomically activates one complete file revision. A valid
change publishes one immutable effective snapshot with monotonic activation,
configuration, and policy generations. An invalid or no-op change leaves the
previous snapshot untouched. Restart-bound fields (paths, worker count, server
timeout, connection limit) reject the whole candidate rather than being silently
ignored.

Provider policy is a versioned set of egress decisions. Assignments target exact
spaces, stable space attributes, or ancestors with descendant inheritance.
Current-space overrides win; otherwise the nearest ancestor wins; explicit deny
wins at equal specificity; unmatched spaces deny by default; siblings never
inherit. Each space receives a current-generation decision before provider work
is evaluated, so spaces created after startup do not need their UUID listed in
the file.

Policy that denies a target moves the job to `blocked_policy` without resolving a
secret or making a network call. Correcting the policy creates a durable
reconciliation run that re-evaluates blocked work in bounded batches with a
crash-safe cursor. Reconciliation only changes jobs, eligibility projections, and
index metadata; it never mutates authority records. Compatible embeddings are
reused after policy changes; incompatible identities trigger a rebuild.

## Jobs And Recovery

SQLite stores durable jobs for extraction, embedding, index rebuilds, feedback
aggregation, and aggregate rebuilds. Workers use leases, attempts, heartbeats,
bounded retries, and generation fencing. Only the current lease owner and
generation may commit a result. Startup fences expired work and returns it to a
valid retry or terminal state. `blocked_policy` jobs are not selected by the
normal retry scheduler and do not consume provider retries; reconciliation
re-evaluates them under the current policy generation.

The queue applies soft and hard limits. A soft-limit write can be accepted with
deferred work; a hard-limit or disk-hard-limit write is rejected atomically,
rather than storing authority without its required job.

## Retrieval

Keyword retrieval uses SQLite FTS5. Semantic retrieval stores float32 embedding
BLOBs in SQLite and uses statically linked sqlite-vec for exact cosine search.
Authority, scope, policy, identity, and validity filters run before top-k.
`semantic_index_version` is a monotonic generation of the live semantic corpus,
not an immutable vector snapshot: projection eligibility and compatible vector
membership changes advance it transactionally.

Retrieval has three top-level channels: `keyword`, `semantic`, and `graph`.
Entity resolution is query understanding (retrieval pre-processing), not a
separate channel: deterministic alias longest-match maps natural-language
mentions to resolved entity IDs. The graph channel internally distinguishes
signals — `direct_entity_mention` (0-hop), `exact_predicate_fact` / `one_hop_fact`
(1-hop), `two_hop_path` (bounded 2-hop), and `memory_relation` /
`graph_conflict` — and expands from a strict unified seed set of keyword
candidates, semantic candidates, and resolved-entity direct mentions.
Requests select explicit `channels`; channels are fused with versioned
reciprocal-rank fusion (`rrf-v3`), each authority evidence contributing at
most once, and bounded feedback aggregates act only as a post-ranking signal.

Cursors bind the authority corpus generation, live semantic corpus generation,
and relevant ranking/index versions and generations. A generation change
invalidates an old cursor rather than continuing over changed membership. Before a
result is returned, dMem rechecks its space, current revision, lifecycle state,
valid time, and result/character budgets. Responses report degraded channels
instead of silently claiming full retrieval.

See [evolution](evolution.md) for reviewed entity/relation/conflict changes and
[feedback and aggregation](feedback-and-aggregation.md) for ranking feedback and
derived memories.
