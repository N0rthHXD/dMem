# API Usage

dMem exposes its semantics through an Agent-neutral REST+JSON adapter under
`internal/protocol/httpapi`. The exact routes, methods, DTO fields, status
codes, headers, and error codes are defined by the
[frozen HTTP REST+JSON contract](http-rest-contract.md). That document is the
only HTTP contract; the former Unix-socket OpenAPI description has been removed.

The adapter returns an `http.Handler`. The runtime composes it but does not
start a listener, so there is no live endpoint in this repository yet. The
behavior below is the semantics the handler implements and the adapter tests
enforce.

## Request Rules

- Every route requires caller identity. Send `X-Caller-Subject-ID` and/or
  `X-Caller-Client-ID`.
- Every write requires `Idempotency-Key`. Reusing a key with the same canonical
  command returns the original result with `Idempotency-Replayed: true`; reusing
  it for a different command returns `409 idempotency_conflict`.
- Every response carries `X-Request-ID`. When the request omits it, the adapter
  generates one.
- JSON decoding is strict and single-value: unknown fields, type mismatches,
  multiple JSON values, and oversized bodies are rejected.
- Branch on the stable `error.code` and `error.retryable` fields; never parse
  the human-readable `error.message`.

## Routes

| Route | Gateway module | Behavior |
| --- | --- | --- |
| `POST /v1/memories` | Memory | Atomically creates a manual source event, memory, initial immutable revision, evidence, active projection, audit data, and required index work. |
| `GET /v1/memories/{memory_id}` | Memory | Returns the current projection of one memory. |
| `POST /v1/memories/{memory_id}/forget` | Memory | Logically deletes the memory, removes it from recall, and retains authority and audit history. |
| `POST /v1/memories/{memory_id}/feedback` | Feedback | Appends feedback to the current revision and can request a corrective evolution proposal. |
| `POST /v1/recall` | Retrieval | Searches one or more scopes under result-count and context budgets and returns a selected context package plus execution metadata. |
| `POST /v1/events` | Extraction | Ingests a raw event and durably enqueues its extraction work in the same transaction. |
| `GET /v1/events/{event_id}` | Extraction | Returns one raw event and its processing status. |
| `POST /v1/scopes` | Scope | Creates a scope in the typed hierarchy. |
| `GET /v1/scopes/{scope_id}` | Scope | Returns one scope. |

## Memory Semantics

`remember` defaults `kind` to `fact`, `sensitivity` to `normal`, `source_name`
to the caller, and `occurred_at` to the current time. These defaults are applied
by the Memory gateway module, not by the adapter. Supported memory kinds are
`fact`, `event`, `preference`, `plan`, `procedure`, `scene`, `topic`, and
`timeline`.

A write returns the memory projection with its current revision. `forget`
requires a non-empty reason and keeps revisions, evidence, state, and audit
records.

## Recall Semantics

`recall` accepts natural-language or ordinary keyword text, not SQLite FTS5
expressions. The request expresses `intent`, an optional reference time, a
result limit, a context-token budget, and optional character budget. Keyword and
semantic are independent primary recall signals; entity resolution is query
understanding, and entity facts plus memory relations are internal graph
signals, not an independent channel.

The query plan selects graph `off`, `features_only`, `direct`, or
`bounded_path`; production defaults come from configuration rather than from the
request. Semantic execution requires a configured embedding provider and a
usable semantic index. Entity mentions inside a natural-language query are
resolved deterministically before retrieval, and an unambiguous resolution can
add candidates through direct mentions or a bounded typed path.

Responses report requested and executed capabilities, degradation reasons,
ranking/index/context/feedback identities, resolved entities, graph signals,
cursor, truncation, and character use. `explain: true` returns per-result
feature contributions, graph paths, evidence, and selection or rejection
reasons under the same scope, policy, and sensitivity rules as normal recall.
Results are revalidated against scope, current revision, state, and valid time
before return.

The initial Agent contract deliberately does not expose channel-selection or
graph-policy debug overrides; those remain internal diagnostics.

## Feedback Semantics

Feedback is separated by purpose: `memory_quality_feedback`,
`retrieval_feedback`, and `answer_feedback`. Feedback types are `useful`,
`irrelevant`, `correct`, `supplement`, and `contest`. A proposal request is
accepted only for a corrective type and must include `source_event_id` and
`proposed_content`. Retrieval feedback stores privacy-preserving decision
context (query hash and type, rank, selection state, matched signal types)
rather than query text.

## Scope Semantics

Public HTTP terminology uses **scope** consistently: `scope_id`, `scope_ids`,
`parent_scope_id`, `scope_type`, and the `/v1/scopes` path. Internal storage may
continue to use the term `space`; the adapter renames the field at the protocol
boundary. Scopes form a typed hierarchy; recall can include ancestors, but
sibling scopes are not implicitly visible.

## Operations Outside This Contract

Candidate review, job control, index rebuilds, configuration reload, graph
maintenance, and the broader administrative surface are not part of the initial
Agent-facing protocol. Logical export/import and backup/restore are not HTTP
operations; see [logical portability](../storage/logical-portability.md) and
[operations](../operations/runbook.md).
