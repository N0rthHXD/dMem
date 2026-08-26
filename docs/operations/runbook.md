# Operations

## Runtime

`dmemd` runs as a local single-user daemon. The data and runtime directories
must be owned by the current user and must not resolve through unsafe symlinks.
The runtime directory is mode `0700`, the socket is mode `0600`, and a lock in
the data directory prevents a second writer.

Important defaults are:

| Setting | Default |
| --- | --- |
| request timeout | 30 seconds |
| shutdown grace | 15 seconds |
| maximum connections | 64 |
| workers/read connections | 2 |
| maximum request body | 4 MiB |
| queue soft/hard limits | 10,000 / 20,000 |
| job lease / timeout / attempts | 30 seconds / 2 minutes / 3 |
| disk soft/hard free thresholds | 1 GiB / 256 MiB |

`DMEM_DATA_DIR`, `DMEM_RUNTIME_DIR`, `DMEM_DATABASE_PATH`, `DMEM_ENDPOINT`, and
`DMEM_PROCESSING_VERSION` provide environment defaults for their corresponding
flags. Other limits and provider settings are flag-only. Providers require
explicit enablement, endpoint, model, policy
allowlists, and secret environment-variable names. Disabled providers do not
resolve secrets or make HTTP requests.

Provider endpoints are full HTTPS request URLs. Processing endpoints must point
to the chat-completions route, and embedding endpoints must point to the
embeddings route; dMem does not append either path. Embedding configuration also
accepts `--embedding-provider-embedding-dim` and
`--embedding-provider-embedding-query-instructions`. A zero dimension is omitted
from provider requests, and query instructions are prepended only to semantic
search queries, not stored-memory embeddings. Empty optional request fields are
not sent.

## Health And Failures

`GET /v1/health` requires no caller header. It reports readiness, write/query
availability, storage and schema state, queue depth, disk pressure, processor
identity, and FTS, semantic, entity, relation, feedback, and aggregate state.
Treat authority failures differently from derived-index failures:

- Stop writes and preserve the data directory when SQLite integrity, schema, or
  authority checks fail.
- Keep authority intact and use the corresponding rebuild operation when a
  derived index is stale or unavailable.
- Track `job_already_active` through the returned existing job instead of
  creating a rebuild storm.
- Let startup lease recovery fence interrupted workers; never edit job state or
  generation directly.
- At the disk hard threshold, expect writes to fail while read-only queries can
  remain available.

Rebuild operations are `POST /v1/indexes/fts/rebuild`,
`POST /v1/indexes/semantic/rebuild`, `POST /v1/indexes/entities/rebuild`,
`POST /v1/indexes/relations/rebuild`, and
`POST /v1/feedback/aggregates/rebuild`. They create durable jobs and require the
normal caller and idempotency headers.

## Initialize, Backup, And Restore

Initialize only a new database:

```sh
dmemd init --data-dir "$DMEM_DATA_DIR"
```

There is no schema migration command. A database with an incompatible schema is
rejected and must be replaced by a newly initialized data directory.

Backup uses SQLite's online backup API and may run while the daemon is active.
The target must not exist and its parent must be a private, current-user-owned
directory:

```sh
install -d -m 0700 "$HOME/dmem-backups"
dmemd backup --data-dir "$DMEM_DATA_DIR" \
  --target "$HOME/dmem-backups/backup-$(date +%Y%m%dT%H%M%S)"
```

Restore replaces the complete data directory. Stop the daemon first:

```sh
dmemd restore --data-dir "$DMEM_DATA_DIR" \
  --source "$HOME/dmem-backups/backup-20260826T120000" \
  --confirm-replace
```

Restore validates checksums, exact schema, SQLite integrity, foreign keys,
authority counts, import checkpoints, and domain references in a same-filesystem
staging directory before publication. It fences interrupted jobs/imports,
rebuilds authority-derived projections, clears stale semantic activation, and
queues semantic recovery. After restore, start the daemon, inspect health and
jobs, then run representative scoped recalls. Semantic state can remain absent
until a configured embedding provider completes its work.

Logical export/import is a separate library workflow documented in
[logical portability](../storage/logical-portability.md); no export/import CLI or
HTTP route exists.

## Privacy

The default deployment is local and offline. Logs must not contain secrets,
Authorization headers, raw memory/event content, feedback notes, query text, or
provider responses. Provider allowlists restrict spaces, source/content types,
and sensitivities before egress. Backups and logical packages require the same
filesystem protection as the authority database.
