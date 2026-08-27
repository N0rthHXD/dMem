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
| `POST /v1/recall` | Searches one or more spaces with a result-count and character budget. Defaults to ancestor inclusion, hybrid mode, active/contested states, 10 results, and 12,000 characters. |
| `POST /v1/memories/{memory_id}/feedback` | Appends feedback to the memory's current revision and can request a corrective evolution proposal. |
| `POST /v1/memories/{memory_id}/forget` | Logically deletes the memory, removes it from recall, and retains authority and audit history. |

`remember` defaults `memory_type` to `fact`, `sensitivity` to `normal`,
`source_name` to the caller, and `occurred_at` to the current time. Supported
memory types are `fact`, `preference`, `constraint`, `decision`, `task`,
`event`, `project_context`, and `agent_experience`.

`recall` supports `keyword`, `semantic`, and `hybrid` modes. Semantic execution
requires a configured embedding provider and a usable semantic index. Responses
state the requested and executed modes, degradation reasons, ranking/index
versions, cursor, truncation, and character use. Results are revalidated against
scope, current revision, state, and valid time before return.

Feedback types are `useful`, `irrelevant`, `correct`, `supplement`, and
`contest`. A proposal request is accepted only for a corrective type and must
include `source_event_id` and `proposed_content`.

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
- entity and relation proposals, review, query, and index rebuilds;
- evolution proposals and conflict-case resolution;
- feedback listing and feedback-aggregate health/rebuild;
- aggregate proposals, review, query, and provenance.

Entity merge is explicitly unsupported. Logical export/import and backup/restore
are not HTTP operations; see [logical portability](../storage/logical-portability.md)
and [operations](../operations/runbook.md).
