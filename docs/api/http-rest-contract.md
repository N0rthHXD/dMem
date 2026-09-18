# dMem HTTP REST+JSON Contract

## Status

This document is the frozen wire contract for the first Agent-neutral REST+JSON
protocol adapter. It is the design source for `internal/protocol/httpapi` and is
the only HTTP contract in the repository: the previous Unix-socket
`docs/api/openapi.yaml` has been removed. The route metadata in
`internal/protocol/httpapi` and its conformance tests are the executable form of
this contract.

The contract is frozen. Changing a route, DTO field, status code, header name, or
error code is a contract change and requires updating this document, the route
metadata in `internal/protocol/httpapi`, and the contract tests together.

The adapter returns an `http.Handler`. The runtime composes it but does not
serve it: there is no production listener, TLS, h2c, or socket lifecycle in the
current architecture.

## Scope

In scope:

- nine Agent-neutral routes under `/v1`;
- request and response DTOs for those routes;
- request identification, caller identity, and idempotency headers;
- strict JSON decoding rules and body limits;
- the unified error body and status mapping;
- the pagination convention reserved for later list routes;
- HTTP/2 compatibility expectations for the handler.

Out of scope, and deliberately absent from this contract:

- a production listener, TLS, h2c, or socket lifecycle;
- authentication, authorization at the network edge, rate limiting, or any
  network security boundary;
- candidate review, job control, index rebuild, configuration reload, graph
  maintenance, or any other administrative operation;
- Agent-specific routes, DTOs, or behavior for Claude, OpenCode, or any other
  client. Every route is caller-neutral and client-neutral.

## Transport

- Scheme: caller-defined. The handler is transport-agnostic.
- Protocol: HTTP/1.1 or HTTP/2. The handler must not branch on
  `r.ProtoMajor`; enforcing HTTP/2 belongs to the future server layer.
- Base path: `/v1`.
- Media type: `application/json`.
- No cookies, sessions, redirects, or connection-specific behavior. Every
  request is self-describing.

## Common Headers

Request headers:

| Header | Required | Rules |
| --- | --- | --- |
| `Content-Type` | writes | Must parse as `application/json`; optional parameters are ignored. |
| `X-Request-ID` | no | Opaque token, 1–128 visible ASCII characters. Generated when absent. |
| `X-Correlation-ID` | no | Opaque token, 1–128 visible ASCII characters. |
| `X-Caller-Subject-ID` | conditionally | Opaque token, 1–128 visible ASCII characters. |
| `X-Caller-Client-ID` | conditionally | Opaque token, 1–128 visible ASCII characters. |
| `Idempotency-Key` | writes | Opaque token, 1–128 visible ASCII characters. |

Caller identity: every route in this contract is non-public. At least one of
`X-Caller-Subject-ID` or `X-Caller-Client-ID` is required. When both are
present, the subject is the primary audit identity and the client identifies the
acting client. Neither header names nor semantics are Agent-specific.

Response headers:

| Header | Always | Meaning |
| --- | --- | --- |
| `X-Request-ID` | yes | Echoes the request ID, or the generated one. |
| `Content-Type` | yes | `application/json`. |
| `Idempotency-Replayed` | writes | `true` when the response was served from a previously committed idempotent write, otherwise `false`. |

An opaque token is a non-empty string of at most 128 bytes, each byte in the
range `0x21`–`0x7E` (no spaces or control characters).

### Request ID

- When `X-Request-ID` is absent, the adapter generates a UUIDv4-shaped value.
- When present, it is validated; an invalid value yields `422 invalid_request`.
- The response always carries `X-Request-ID`, including error responses.
- The request ID is carried in the gateway `CallContext.RequestID`. It is audit
  correlation only: it never participates in idempotency identity and never
  changes the canonical command hash.

### Idempotency

- Every write route requires `Idempotency-Key`.
- The adapter only transports the key. The gateway forms the write identity
  from caller identity + operation name + idempotency key + canonical command
  hash. HTTP method and route are never part of that identity.
- Reusing a key with an equivalent canonical command returns the original
  result with `Idempotency-Replayed: true`.
- Reusing a key with a different canonical command returns
  `409 idempotency_conflict`.
- Read routes ignore `Idempotency-Key` if it is present.

## Routes

All paths are relative to `/v1`.

| # | Operation | Method and path | Access | Caller | Idempotency | Body limit | Success |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | `memory.remember` | `POST /v1/memories` | write | required | required | 1 MiB | `201` |
| 2 | `memory.get` | `GET /v1/memories/{memory_id}` | read | required | not required | none | `200` |
| 3 | `memory.forget` | `POST /v1/memories/{memory_id}/forget` | write | required | required | 64 KiB | `200` |
| 4 | `feedback.append` | `POST /v1/memories/{memory_id}/feedback` | write | required | required | 256 KiB | `201` |
| 5 | `retrieval.recall` | `POST /v1/recall` | write | required | required | 256 KiB | `200` |
| 6 | `extraction.ingest` | `POST /v1/events` | write | required | required | 4 MiB | `202` |
| 7 | `extraction.event.get` | `GET /v1/events/{event_id}` | read | required | not required | none | `200` |
| 8 | `scope.create` | `POST /v1/scopes` | write | required | required | 64 KiB | `201` |
| 9 | `scope.get` | `GET /v1/scopes/{scope_id}` | read | required | not required | none | `200` |

Notes:

- `POST /v1/recall` is classified as a write because it records bounded recall
  telemetry and therefore requires an idempotency key. It does not mutate
  memory authority.
- Read routes reject a non-empty request body with `400 unexpected_body`.
- Path identifiers are opaque, non-empty strings of 1–128 characters. The
  adapter does not impose a UUID format; an unknown identifier is reported by
  the gateway as `404 not_found`.
- Route order in the metadata table is stable and is the declaration order.

## Naming

The public HTTP surface uses **scope** terminology consistently:
`scope_id`, `scope_ids`, `parent_scope_id`, `scope_type`, and the `/v1/scopes`
path. Internal storage may continue to use `space`. No response in this
contract emits a `space_id` field.

Resource bodies use the gateway domain JSON shape for nested protocol-neutral
values (memory revisions, context packages, feedback events, content). The
adapter owns the envelope and any field renamed for the public contract.

## DTOs

### `POST /v1/memories`

Request:

```json
{
  "scope_id": "string, required",
  "content": "string, required",
  "kind": "string, optional",
  "sensitivity": "string, optional",
  "valid_from": "RFC3339 string, optional",
  "valid_until": "RFC3339 string, optional",
  "source_name": "string, optional",
  "source_event_id": "string, optional",
  "source_stream_key": "string, optional",
  "sequence": "non-negative integer, optional",
  "occurred_at": "RFC3339 string, optional",
  "metadata": "JSON object, optional",
  "reason": "string, optional"
}
```

Response `201`:

```json
{
  "operation_id": "string",
  "replayed": false,
  "memory": { "memory_id": "string", "scope_id": "string", "kind": "fact", "current_state": "active", "frozen": false, "sensitivity": "normal", "current_revision": {}, "created_at": "string", "updated_at": "string" },
  "event_id": "string"
}
```

Semantic defaults (`kind`, `sensitivity`, `source_name`, `occurred_at`,
`metadata`, `reason`) are applied by the gateway, not by the adapter. Omitted
fields are omitted from the command and the gateway default applies.

### `GET /v1/memories/{memory_id}`

Response `200`:

```json
{ "memory": { "memory_id": "string", "scope_id": "string", "current_revision": {} } }
```

### `POST /v1/memories/{memory_id}/forget`

Request:

```json
{ "reason": "string, required, 1-1024 characters" }
```

Response `200`:

```json
{ "operation_id": "string", "replayed": false, "memory": { "memory_id": "string", "scope_id": "string" } }
```

### `POST /v1/memories/{memory_id}/feedback`

Request:

```json
{
  "scope_id": "string, required",
  "purpose": "memory_quality_feedback | retrieval_feedback | answer_feedback",
  "feedback_type": "string, required",
  "reason_code": "string, required",
  "note": "string, optional",
  "decision": "decision context object, optional",
  "create_proposal": false,
  "source_event_id": "string, optional",
  "proposed_content": "string, optional"
}
```

Response `201`:

```json
{ "operation_id": "string", "replayed": false, "feedback": {}, "proposal": { "status": "string" } }
```

Purpose, type, reason, decision-context, and proposal rules are enforced by the
Feedback gateway module, not by the adapter.

### `POST /v1/recall`

Request:

```json
{
  "query": "string, required",
  "scope_ids": ["string, required, at least one"],
  "include_ancestors": "boolean, optional",
  "memory_types": ["string, optional"],
  "states": ["string, optional"],
  "reference_time": "RFC3339 string, optional",
  "intent": "string, optional",
  "max_results": "integer, optional",
  "max_chars": "integer, optional",
  "max_context_tokens": "integer, optional",
  "include_evidence": "boolean, optional",
  "explain": "boolean, optional",
  "exclude_stale_aggregates": "boolean, optional",
  "cursor": "string, optional",
  "skip_diversification": "boolean, optional"
}
```

The initial Agent contract does not expose capability or graph-policy debug
overrides. Response `200`:

```json
{ "context": { "items": [], "tokens_used": 0, "chars_used": 0, "truncated": false, "next_cursor": null }, "execution": {} }
```

### `POST /v1/events`

Request:

```json
{
  "scope_id": "string, required",
  "source_type": "string, required",
  "source_name": "string, required",
  "source_event_id": "string, optional",
  "source_stream_key": "string, optional",
  "sequence": "non-negative integer, optional",
  "occurred_at": "RFC3339 string, optional",
  "content": { "kind": "text | json | reference", "media_type": "string", "text": "string", "value": "any JSON", "reference_uri": "string", "declared_size": 0, "declared_hash": "string" },
  "metadata": "JSON object, optional"
}
```

Content kind, required fields, and incompatible-field rules are enforced by the
Extraction gateway module. Response `202`:

```json
{ "operation_id": "string", "replayed": false, "event_id": "string", "status": "string", "job_ids": [], "accepted_at": "string" }
```

### `GET /v1/events/{event_id}`

Response `200`:

```json
{ "event": { "event_id": "string", "scope_id": "string", "source_type": "string", "content": {}, "processing_status": "string" } }
```

### `POST /v1/scopes`

Request:

```json
{
  "parent_scope_id": "string or null, optional",
  "scope_type": "user | agent | application | project | task | session",
  "external_id": "string, required, 1-128 characters",
  "display_name": "string, required, 1-256 characters"
}
```

Response `201`:

```json
{ "operation_id": "string", "replayed": false, "scope": { "scope_id": "string", "parent_scope_id": null, "scope_type": "string", "external_id": "string", "display_name": "string", "created_at": "string" } }
```

### `GET /v1/scopes/{scope_id}`

Response `200`:

```json
{ "scope": { "scope_id": "string", "parent_scope_id": null, "scope_type": "string", "external_id": "string", "display_name": "string", "created_at": "string" } }
```

## JSON Decoding Rules

Every write body is decoded strictly:

1. The body is bounded by the route body limit. Exceeding it yields
   `413 request_too_large`.
2. `Content-Type` must be `application/json`. Otherwise
   `415 unsupported_media_type`.
3. The body must contain exactly one top-level JSON object. An empty body, a
   non-object value (`null`, array, string, number, boolean), or a second JSON
   value yields `400 invalid_json`.
4. Unknown object fields are rejected with `400 invalid_json`.
5. Field type mismatches are rejected with `400 invalid_json`.
6. Numbers are decoded with `json.Number`, so integer values are never rounded
   through `float64`. Typed integer fields still reject fractional or
   exponent-form values.
7. JSON object values that carry arbitrary user data (`metadata`,
   `content.value`) preserve `json.Number` semantics.

Read routes do not decode a body. A non-empty body on a read route yields
`400 unexpected_body`.

## Error Model

Every error response uses one body shape:

```json
{
  "error": {
    "code": "string, stable",
    "message": "string, human-readable, not stable",
    "request_id": "string",
    "retryable": false,
    "retry_after_ms": 1000,
    "details": { "key": "string" }
  }
}
```

`retry_after_ms` and `details` are omitted when absent. Clients must branch on
`code` and `retryable`, never on `message`.

Status mapping:

| Code | Status | Source |
| --- | --- | --- |
| `invalid_json` | 400 | adapter |
| `unexpected_body` | 400 | adapter |
| `not_found` | 404 | adapter (unknown route) or gateway |
| `method_not_allowed` | 405 | adapter |
| `request_too_large` | 413 | adapter |
| `unsupported_media_type` | 415 | adapter |
| `invalid_request` | 422 | adapter (headers, path identifiers, pagination) |
| `invalid_input` | 422 | gateway |
| `permission_denied` | 403 | gateway |
| `conflict` | 409 | gateway |
| `idempotency_conflict` | 409 | gateway |
| `resource_exhausted` | 429 when retryable, otherwise 507 | gateway |
| `capability_unavailable` | 503 | gateway |
| `internal` | 500 | gateway or adapter |
| `canceled` | 499 | adapter (client cancellation; non-standard, best effort) |
| `deadline_exceeded` | 504 | adapter |

Safe errors:

- A gateway `*gateway.Error` contributes only its declared-safe message,
  retryability, retry hint, and string details.
- The wrapped internal cause is never serialized.
- A non-gateway error is reported as `500 internal` with a generic message; its
  text is never echoed.
- A recovered panic is reported as `500 internal` with a generic message; the
  recovered value is never echoed.

## Pagination

No route in this contract returns a page. The following convention is frozen
now so later list routes are consistent:

- Query parameters: `limit` (integer, `1`–`200`, default `50`) and `cursor`
  (opaque string, at most 512 characters).
- Invalid values yield `422 invalid_request`.
- List responses use `{"items": [...], "next_cursor": "string"|null}`.
- A cursor is opaque and is produced by the owning gateway module; clients must
  not construct or parse one.
- `internal/protocol/httpapi` exposes `ParsePageRequest` implementing this
  convention.

## HTTP/2 Compatibility

- The handler must not inspect `r.ProtoMajor`, `r.Proto`, or connection state.
- The handler must not create a listener, `http.Server`, TLS configuration, or
  h2c upgrade.
- All request state lives in the request, the `CallContext`, or the response.
- Compatibility is proven with a test-only HTTP/2 transport
  (`httptest.Server` with `EnableHTTP2`); no production TLS or listener is
  added by this contract.
