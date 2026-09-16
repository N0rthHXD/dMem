# API Usage

The daemon serves HTTP/JSON under `/v1` over a Unix domain socket. It does not
listen on TCP. The exact methods, schemas, enums, status codes, and pagination
rules are defined by the [OpenAPI contract](openapi.yaml).

## Request Rules

- Send `X-Caller-ID` on every operation except `GET /v1/health`.
- Send `Idempotency-Key` on writes. Reusing a key with the same canonical
  request returns the original result and sets `Idempotent-Replay: true`;
  reusing it for a different request returns `idempotency_conflict`.
- JSON decoding is strict. Unknown fields and invalid values are rejected.
- Treat `error_code` and `retryable` as the stable error interface; do not parse
  human-readable `message` text.
- A successful asynchronous response means the authoritative request was
  accepted. Follow returned jobs through `GET /v1/jobs/{job_id}`.

## Direct Memory Operations

The direct operations are intended for agent integrations that do not need to
orchestrate raw events, revisions, and lifecycle endpoints separately.

| Operation | Behavior |
| --- | --- |
| `POST /v1/remember` | Atomically creates a manual source event, memory, initial immutable revision, evidence, active projection, audit data, and required index work. |
| `POST /v1/recall` | Searches one or more spaces with a result-count and context-token budget. Defaults to ancestor inclusion, keyword plus semantic primary recall with gated graph features, active/contested states, 20 results, and a 4,000-token context budget. |
| `POST /v1/memories/{memory_id}/feedback` | Appends feedback to the memory's current revision and can request a corrective evolution proposal. |
| `POST /v1/memories/{memory_id}/forget` | Logically deletes the memory, removes it from recall, and retains authority and audit history. |

`remember` defaults `kind` to `fact`, `sensitivity` to `normal`, `source_name`
to the caller, and `occurred_at` to the current time. Supported memory kinds are
`fact`, `event`, `preference`, `plan`, `procedure`, `scene`, `topic`, and
`timeline`.

`recall` accepts natural-language or ordinary keyword text, not SQLite FTS5
expressions; the request expresses intent (`intent`), a reference time, a result
limit, and a context-token budget. Keyword and semantic are independent primary
recall signals; entity resolution is query understanding, and entity facts plus
memory relations are internal graph signals, not an independent channel. The
query plan selects graph `off`, `features_only`, `direct`, or `bounded_path`;
production defaults to keyword plus semantic with graph `features_only` when the
graph is ready. Semantic execution requires a configured embedding provider and
a usable semantic index. Entity mentions inside a natural-language query are
resolved deterministically (alias longest-match) before retrieval, and an
unambiguous resolution can add candidates through direct mentions or a bounded
typed path.

Explicit channels are an evaluation/debug-only override: set
`debug.capabilities` to `["keyword"]`, `["semantic"]`, or `["graph"]`, plus the
optional `debug.graph_policy`, `debug.disable_diversification`, and
`debug.include_stale_aggregates` overrides. Stale aggregates are otherwise
excluded from recall.
Responses state the requested and executed capabilities, degradation reasons,
ranking/index/context/feedback identities, resolved entities, graph
signals/facts/paths, cursor, truncation, and character use. `explain: true`
returns per-result feature contributions, graph paths, evidence, and selection
or rejection reasons under the same scope, policy, and sensitivity rules as
normal recall. Results are revalidated against scope, current revision, state,
and valid time before return.

Feedback is separated by purpose: `memory_quality_feedback`,
`retrieval_feedback`, and `answer_feedback`. Feedback types are `useful`,
`irrelevant`, `correct`, `supplement`, and `contest`. A proposal request is
accepted only for a corrective type and must include `source_event_id` and
`proposed_content`. Retrieval feedback stores privacy-preserving decision
context (query hash and type, rank, selection state, matched signal types) rather
than query text.

Aggregate resource listings omit stale aggregates by default. Use
`freshness=stale` to select stale records or `exclude_stale=false` to include
both freshness states; recall remains stricter and requires
`debug.include_stale_aggregates=true`.

## Resource API

## Configuration API

`GET /v1/config` returns the active configuration identity, activation generation,
latest reload attempt, and redacted capability summaries. It never returns file
paths, secret references or values, request content, query text, or provider
responses.

`POST /v1/config/reload` reads the daemon's already-selected TOML file and
atomically activates it only when the complete candidate is valid. The request
requires an empty JSON object or an `expected_effective_identity`; a mismatch
returns `config_identity_conflict`. This is an idempotent write: reuse the same
key and body to receive the original successful result with
`Idempotent-Replay: true`.

The same contract exposes lower-level operations for:

- spaces, raw events, replay, memories, revisions, lifecycle state, and search;
- processing candidates and durable jobs;
- FTS and semantic rebuilds;
- entity and relation proposals, review, and query;
- entity facts (list/detail) and entity-fact proposals (create/list/review);
- graph index rebuild;
- evolution proposals and conflict-case resolution;
- feedback listing and feedback-aggregate health/rebuild;
- aggregate proposals, review, query, and provenance.

Entity merge is explicitly unsupported. Logical export/import and backup/restore
are not HTTP operations; see [logical portability](../storage/logical-portability.md)
and [operations](../operations/runbook.md).
