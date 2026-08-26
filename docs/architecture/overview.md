# Architecture And Data Model

dMem runs as one Go process owned by one Linux user. A file lock permits one
writer per data directory. The API is HTTP/JSON over a user-only Unix socket,
and SQLite is the authority for domain data, idempotency, audit history, jobs,
and index metadata.

## Authority And Derivation

Raw events are committed before optional automatic processing. Memories have a
stable logical ID, immutable revisions, append-only state events, evidence, and
a current projection. FTS rows, embeddings, vector indexes, entity/relation
index metadata, feedback aggregates, and aggregate freshness are derived and
rebuildable. Derived data must never overwrite authority.

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

## Jobs And Recovery

SQLite stores durable jobs for extraction, embedding, index rebuilds, feedback
aggregation, and aggregate rebuilds. Workers use leases, attempts, heartbeats,
bounded retries, and generation fencing. Only the current lease owner and
generation may commit a result. Startup fences expired work and returns it to a
valid retry or terminal state.

The queue applies soft and hard limits. A soft-limit write can be accepted with
deferred work; a hard-limit or disk-hard-limit write is rejected atomically,
rather than storing authority without its required job.

## Retrieval

Keyword retrieval uses SQLite FTS5. Semantic retrieval uses versioned
embeddings and a Go vector index. Hybrid retrieval combines independent
channels using versioned reciprocal-rank fusion. Entity and relation channels
can add candidates, and bounded feedback aggregates act only as a post-ranking
signal.

Cursors bind the corpus generation and relevant ranking/index versions. A
version change invalidates an old cursor rather than mixing snapshots. Before a
result is returned, dMem rechecks its space, current revision, lifecycle state,
valid time, and result/character budgets. Responses report degraded channels
instead of silently claiming full retrieval.

See [evolution](evolution.md) for reviewed entity/relation/conflict changes and
[feedback and aggregation](feedback-and-aggregation.md) for ranking feedback and
derived memories.
