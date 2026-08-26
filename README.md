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
./dmemd init --data-dir "$HOME/.local/share/dmem"
./dmemd daemon
```

By default the API socket is `${XDG_RUNTIME_DIR}/dmem/dmem.sock`, or
`/tmp/dmem-$UID/dmem/dmem.sock` when `XDG_RUNTIME_DIR` is unset. The data
directory defaults to `${XDG_DATA_HOME}/dmem`, or
`$HOME/.local/share/dmem` when `XDG_DATA_HOME` is unset.

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
- [Entities, relations, conflicts, and evolution](docs/architecture/evolution.md)
- [Feedback, ranking, and aggregation](docs/architecture/feedback-and-aggregation.md)
- [Logical portability](docs/storage/logical-portability.md)
- [Operations](docs/operations/runbook.md)
- [Testing](docs/development/testing-and-evaluation.md)
- [LongMemEval evaluation](docs/development/longmemeval.md)
- [Architecture decisions](docs/adr/README.md)
