# Operations

## Runtime

`dmemd` runs as a local single-user daemon. The data directory must be owned by
the current user, must be mode `0700`, and must not resolve through unsafe
symlinks. A lock in the data directory prevents a second writer.

The daemon reads exactly one strict TOML file. Its default location is
`${XDG_CONFIG_HOME:-$HOME/.config}/dmem/config.toml`; `--config PATH` is the
only setting-related command-line option. Begin from
`docs/operations/config.example.toml`, replace its example absolute paths, and
publish edits by atomically renaming a private temporary file over the selected
file. The file contains no secret values: enabled providers reference an
environment variable that is resolved only after policy approval for a request.

`dmemd daemon` assembles the SQLite capability adapters, gateway services,
providers, durable workers, and the Agent-neutral REST+JSON `http.Handler`, then
waits for shutdown. **It does not open a Unix socket or a TCP port.** There is no
production listener, TLS, h2c, authentication, or network security boundary in
this repository yet; serving the handler is separate future work. Operator
visibility therefore comes from process logs and the durable state in SQLite,
not from a live health endpoint.

Important defaults are represented in that file:

| Setting | Default |
| --- | --- |
| shutdown grace | 15 seconds |
| workers/read connections | 2 |
| queue soft/hard limits | 10,000 / 20,000 |
| job lease / timeout / attempts | 30 seconds / 2 minutes / 3 |
| disk soft/hard free thresholds | 1 GiB / 256 MiB |
| context result/token/char budgets | 20 results / 4,000 tokens / 16,000 chars |

HTTP request body limits are per route and are declared in the frozen contract
and in `internal/protocol/httpapi` route metadata; they are not daemon settings.

Providers require explicit enablement, endpoint, model, default-deny policy, and
an environment secret reference. Disabled providers do not resolve secrets or
make HTTP requests. `instance.data_dir`, `instance.database_filename`, and
`jobs.worker_count` are restart-bound; a reload that changes one is rejected as a
whole.

Use `dmemd [--config PATH] config validate` to validate offline, `init` to
initialize a new database, and `daemon` to run. There is no `status` or `reload`
subcommand: `SIGHUP` requests a serialized reload pipeline and repeated signals
are coalesced. Failed or rejected reloads preserve the prior complete snapshot.
Correct a rejected file and signal again.

## Health And Failures

Because the runtime exposes a handler but no listener, health is observed
through logs and durable state rather than an HTTP endpoint. Treat authority
failures differently from derived-index failures:

- Stop writes and preserve the data directory when SQLite integrity, schema, or
  authority checks fail.
- Keep authority intact and re-run the corresponding rebuild when a derived
  index is stale or unavailable.
- Track an already-active rebuild through the durable job rather than creating a
  rebuild storm.
- Let startup lease recovery fence interrupted workers; never edit job state or
  generation directly.
- At the disk hard threshold, expect writes to fail while read-only queries can
  remain available.

The SQLite integrity check runs in the background after startup so a large
database (or FTS5 index) does not block readiness. On failure the daemon logs an
error and storage becomes unavailable. Restart or stop the daemon and preserve
the data directory, then diagnose from a copy before resuming writes.

Derived-index rebuilds (FTS, semantic, graph, and feedback aggregates) are
durable internal jobs. They are not part of the initial Agent HTTP contract.

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
queues semantic recovery. After restore, start the daemon, inspect logs and
durable jobs, then run representative scoped recalls through the gateway or the
HTTP adapter tests. Semantic state can remain absent until a configured
embedding provider completes its work.

Logical export/import is a separate library workflow documented in
[logical portability](../storage/logical-portability.md); no export/import CLI or
HTTP route exists.

## Privacy

The default deployment is local and offline. Logs must not contain secrets,
Authorization headers, raw memory/event content, feedback notes, query text, or
provider responses. Provider allowlists restrict spaces, source/content types,
and sensitivities before egress. Backups and logical packages require the same
filesystem protection as the authority database.
