# dMem

dMem is a local, single-user memory daemon for agents and applications. It stores
versioned memories and their evidence in SQLite, serves HTTP/JSON over a Unix
domain socket, and supports scoped keyword, semantic, entity, and relation
retrieval. External processing and embedding providers are disabled by default.

Schema v1 has no migration path. Databases with an incompatible schema must be
replaced by a newly initialized data directory.

## Build And Run

Linux, Go 1.24 or newer, a C compiler, and SQLite FTS5 support are required.

```sh
go mod download
CGO_ENABLED=1 go build -tags sqlite_fts5 -o dmemd ./cmd/dmemd
install -d -m 0700 "${XDG_CONFIG_HOME:-$HOME/.config}/dmem"
cp docs/operations/config.example.toml "${XDG_CONFIG_HOME:-$HOME/.config}/dmem/config.toml"
# Edit the example's absolute data_dir and runtime_dir paths for this user.
./dmemd config validate
./dmemd init
./dmemd daemon
```

The default configuration file is `${XDG_CONFIG_HOME:-$HOME/.config}/dmem/config.toml`.
Use `--config /absolute/path/config.toml` to select a different instance. The
file chooses the API socket and data directory; it is the only source of daemon
settings. Providers are disabled in the shipped example.

All API requests except health require `X-Caller-ID`. Writes also require an
`Idempotency-Key`.

```sh
socket="${XDG_RUNTIME_DIR:-/tmp/dmem-$UID}/dmem/dmem.sock"

curl --unix-socket "$socket" http://localhost/v1/health

curl --unix-socket "$socket" -X POST http://localhost/v1/spaces \
  -H 'Content-Type: application/json' \
  -H 'X-Caller-ID: example' \
  -H 'Idempotency-Key: create-example-space' \
  -d '{"space_type":"user","external_id":"example","display_name":"Example"}'
```

Use the returned `space_id` with the agent-facing memory API:

```sh
curl --unix-socket "$socket" -X POST http://localhost/v1/remember \
  -H 'Content-Type: application/json' \
  -H 'X-Caller-ID: example' \
  -H 'Idempotency-Key: remember-1' \
  -d '{"space_id":"<space-id>","content":"The deployment region is eu-west-1."}'

curl --unix-socket "$socket" -X POST http://localhost/v1/recall \
  -H 'Content-Type: application/json' \
  -H 'X-Caller-ID: example' \
  -d '{"query":"deployment region","space_ids":["<space-id>"]}'
```

`POST /v1/memories/{memory_id}/feedback` appends feedback to the current
revision. `POST /v1/memories/{memory_id}/forget` logically deletes a memory and
removes it from recall. See [API usage](docs/api/usage.md) and the
[OpenAPI contract](docs/api/openapi.yaml) for request schemas and all endpoints.

## Documentation

- [Documentation index](docs/README.md)
- [Architecture and data model](docs/architecture/overview.md)
- [Memory and retrieval optimization plan](docs/architecture/memory-retrieval-optimization-plan.md)
- [Memory and retrieval implementation tasks](specs/002-memory-retrieval-optimization/tasks.md)
- [Entities, relations, conflicts, and evolution](docs/architecture/evolution.md)
- [Feedback, ranking, and aggregation](docs/architecture/feedback-and-aggregation.md)
- [Logical portability](docs/storage/logical-portability.md)
- [Operations](docs/operations/runbook.md)
- [Testing](docs/development/testing-and-evaluation.md)
- [Architecture decisions](docs/adr/README.md)
