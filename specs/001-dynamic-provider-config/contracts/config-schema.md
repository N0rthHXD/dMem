# Contract: Configuration File

## Locator

- Default: `${XDG_CONFIG_HOME:-$HOME/.config}/dmem/config.toml`.
- `XDG_CONFIG_HOME` only locates the default file and must be absolute when set.
- One global `--config PATH` selects a non-default file and does not override any value.
- Stdin, multiple files, include directives, profiles, overlays, and setting environment variables are unsupported.
- The selected path is fixed for the process lifetime. Reload cannot switch files.

## File Safety

- The file must be a bounded regular file owned by the current user and must not be a symlink.
- The supported maximum size is 1 MiB.
- The loader reads from one opened descriptor and rejects a file that changes during the read.
- Operators should publish edits by writing a private temporary file in the same directory and atomically renaming it over the selected path.

## Grammar

- TOML with required integer `config_version = 1`.
- All tables are closed. Unknown or misspelled keys are errors.
- Duplicate keys or tables are errors.
- Strings are literal configuration values: no `$VAR`, `${VAR}`, `~`, shell, or template expansion.
- Paths are absolute. Database and socket filenames are basenames.
- Durations are explicit strings such as `250ms`, `30s`, and `2m`.
- Byte limits are decimal integers in fields ending `_bytes`.
- Enums are case-sensitive.
- Set-valued arrays are normalized by validation; input order and duplicates do not affect identity.

## Required Top-Level Tables

| Table | Required fields |
|-------|-----------------|
| root | `config_version` |
| `instance` | `data_dir`, `runtime_dir`, `database_filename`, `socket_filename` |
| `server` | `request_timeout`, `shutdown_grace_period`, `max_connections`, `max_request_bytes` |
| `jobs` | worker, queue, attempt, lease, timeout, and retry fields shown in the example |
| `storage` | soft and hard free-byte thresholds |
| `processing` | `version` |
| `processing.provider` | at least `enabled` |
| `embedding.provider` | at least `enabled` |

## Provider Rules

When `enabled = true`, the provider must include:

- `kind = "openai-compatible"`
- a complete HTTPS request endpoint without userinfo, query, or fragment
- stable model identity
- `telemetry_disabled = true`
- a typed secret reference
- complete capability-specific limits
- complete default-deny egress policy
- at least one assignment capable of allowing a space

When disabled, omitted provider details are accepted. Any supplied optional details are still validated.

Processing additionally requires an explicit non-noop processing version. Processing `max_egress_input_chars`, `max_aggregate_input_chars`, candidate output, response size, context count, concurrency, and rate limits are independent fields.

Embedding options include optional dimension and query instructions. Query instructions are output-affecting configuration but are redacted from status.

## Secret Reference

```toml
[processing.provider.secret]
source = "environment"
name = "DMEM_PROCESSING_API_KEY"
```

- Version 1 supports `environment` only.
- `name` uses a strict uppercase environment-variable identifier.
- A `value` field is unknown and rejected.
- Activation verifies that enabled-provider references are available and non-empty but never stores the value.
- Each external request resolves its secret after policy approval, so secret rotation does not require reload.

## Policy Assignment

Each assignment has `effect = "allow"` or `effect = "deny"` and one selector:

| Selector | Required fields | Optional behavior |
|----------|-----------------|-------------------|
| `space` | `space_id` | `include_descendants` |
| `space_type` | `space_type` | deterministic `priority` |
| `external_id` | `external_id` | deterministic `priority` |

Decision order:

1. Exact current-space assignment.
2. Nearest ancestor exact-space assignment with `include_descendants = true`.
3. Stable attribute assignments ordered by priority and canonical tie-break.
4. Default deny.

At equal specificity, deny wins. Siblings never participate. Policy identity is generated from normalized rules; the file does not contain an authoritative policy version string.

## Reloadability

| Fields | Runtime behavior |
|--------|------------------|
| Processing/embedding provider and policy fields | Hot reload |
| Processing version and provider limits | Hot reload |
| Queue thresholds, disk thresholds, shutdown grace | Hot reload |
| Data/database/runtime/socket paths | Whole candidate rejected as restart required |
| Worker count, max connections, server request timeout | Whole candidate rejected as restart required |
| Config version | Rejected unless exactly current version |

Reload never ignores a changed field and never activates only part of the file.

## Identities

- `source_identity`: SHA-256 of exact loaded bytes.
- `effective_identity`: SHA-256 of canonical normalized non-secret configuration.
- `configuration_identity`: per-capability behavior identity.
- `compatibility_identity`: per-capability output/artifact identity.
- `policy_identity`: normalized policy identity.

Secret values enter none of these identities. Formatting-only changes produce `no_change` and no reconciliation.

## Safe Diagnostics

Diagnostics expose stable code, TOML field path, and line/column when available. They never expose field values, source excerpts, secrets, provider responses, memory content, or query text.

Representative codes:

- `config_unavailable`
- `config_too_large`
- `config_invalid_syntax`
- `config_unknown_field`
- `config_duplicate_field`
- `config_invalid_value`
- `config_missing_field`
- `config_secret_unavailable`
- `config_restart_required`
