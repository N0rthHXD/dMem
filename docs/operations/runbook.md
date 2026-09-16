# Operations

## Runtime

`dmemd` runs as a local single-user daemon. The data and runtime directories
must be owned by the current user and must not resolve through unsafe symlinks.
The runtime directory is mode `0700`, the socket is mode `0600`, and a lock in
the data directory prevents a second writer.

The daemon reads exactly one strict TOML file. Its default location is
`${XDG_CONFIG_HOME:-$HOME/.config}/dmem/config.toml`; `--config PATH` is the
only setting-related command-line option. Begin from
`docs/operations/config.example.toml`, replace its example absolute paths, and
publish edits by atomically renaming a private temporary file over the selected
file. The file contains no secret values: enabled providers reference an
environment variable that is resolved only after policy approval for a request.

Important defaults are represented in that file:

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

Providers require explicit enablement, endpoint, model, default-deny policy, and
an environment secret reference. Disabled providers do not resolve secrets or
make HTTP requests. Paths, worker count, connection limits, and server timeout
are restart-bound; a reload that changes one is rejected as a whole.

Use `dmemd [--config PATH] config validate` to validate offline, `status` to
inspect redacted state, and `reload` to synchronously reload. `SIGHUP` requests
the same serialized reload pipeline; repeated signals are coalesced. Failed or
rejected reloads preserve the prior complete snapshot. Correct a rejected file
and reload it, then use status to inspect safe diagnostics and reconciliation
state.

## Health And Failures

`GET /v1/health` requires no caller header. It reports readiness, write/query
availability, storage and schema state, queue depth, disk pressure, processor
identity, FTS, semantic, entity, relation, graph, feedback, and aggregate state,
plus a `decision` block with the effective identity, generation, and state of
consolidation, quality and duplicate projections, semantic calibration, graph
scoring, the feature ranker, context selection, and feedback aggregation. Treat
authority failures differently from derived-index failures:

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

The SQLite integrity check runs in the background after startup so a large
database (or FTS5 index) does not block readiness. `storage_integrity_state`
reports `off`, `pending`, `ready`, or `failed`; on `failed` the daemon logs an
error and `storage_state` becomes `unavailable`. Restart or stop the daemon and
preserve the data directory, then diagnose from a copy before resuming writes.

Rebuild operations are `POST /v1/indexes/fts/rebuild`,
`POST /v1/indexes/semantic/rebuild`, `POST /v1/indexes/graph/rebuild`, and
`POST /v1/feedback/aggregates/rebuild`. They create durable jobs and require the
normal caller and idempotency headers.

Semantic vectors are stored as compact `float32` blobs in the authority table
`memory_embeddings` and retrieved with sqlite-vec exact cosine. The daemon
verifies the sqlite-vec extension at startup (`vec_version()` plus a
`vec_length()` sanity probe) and refuses to start if it is unavailable, so a
missing or corrupted native build fails loudly rather than returning wrong
retrieval results. To diagnose the linked extension on an operator box, run
`SELECT vec_version()` against the authority database with the same `sqlite_fts5`
build tag.

## Initialize, Backup, And Restore

Initialize only a new database:

```sh
dmemd init
```

There is no schema migration command. A database with an incompatible schema is
rejected and must be replaced by a newly initialized data directory.

Backup uses SQLite's online backup API and may run while the daemon is active.
The target must not exist and its parent must be a private, current-user-owned
directory:

```sh
install -d -m 0700 "$HOME/dmem-backups"
dmemd backup \
  --target "$HOME/dmem-backups/backup-$(date +%Y%m%dT%H%M%S)"
```

Restore replaces the complete data directory. Stop the daemon first:

```sh
dmemd restore \
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
