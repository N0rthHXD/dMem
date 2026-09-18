# dMem

dMem is a local, single-user memory daemon for agents and applications. It
stores versioned, structured memories and their evidence in SQLite, retrieves
scoped keyword and semantic candidates with gated typed-graph features, and
exposes its semantics through a protocol-independent Memory Gateway. External
processing and embedding providers are disabled by default.

Schema v1 has no migration path. Databases with an incompatible schema must be
replaced by a newly initialized data directory.

## Architecture

The repository is organized around one semantic core and thin inbound protocol
adapters:

- **Memory Gateway** (`internal/gateway`) owns application semantics:
  `memory`, `scope`, `feedback`, `retrieval`, `extraction`, `policy`,
  `consolidation`, and `knowledge`. Modules publish protocol-neutral commands,
  queries, results, errors, and narrow capability ports. They never import
  `net/http`, a protocol package, or a storage implementation.
- **Protocol adapters** (`internal/protocol/httpapi`) translate a wire protocol
  into gateway calls. The first adapter is an Agent-neutral REST+JSON
  `http.Handler` over a frozen nine-route `/v1` surface. Adapters own strict
  decoding, body limits, headers, routing, and error mapping; they never own
  domain rules or repository calls.
- **Runtime adapters** (`internal/storage`, `internal/jobs`,
  `internal/provider`) implement the gateway's capability ports over the
  existing SQLite, FTS5, sqlite-vec, relational graph, job, policy, and
  metadata implementation. Logical ports do not imply separate databases.
- **Composition root** (`internal/daemon`) assembles configuration, the SQLite
  adapters, gateway services, providers, workers, and the protocol handler.

The dependency direction is:

```text
agent client -> protocol adapter -> gateway module -> capability port -> runtime adapter
```

The current runtime **exposes the protocol handler but does not start a
listener**. There is no production HTTP/2 server, TLS, h2c, authentication,
authorization at the network edge, rate limiting, or socket lifecycle in this
repository yet. The HTTP contract is frozen and exercised through in-process
tests; a server layer is separate future work.

## Build And Run

Linux, Go 1.24 or newer, a C compiler, and SQLite FTS5 support are required.

```sh
go mod download
CGO_ENABLED=1 go build -tags sqlite_fts5 -o dmemd ./cmd/dmemd
install -d -m 0700 "${XDG_CONFIG_HOME:-$HOME/.config}/dmem"
cp docs/operations/config.example.toml "${XDG_CONFIG_HOME:-$HOME/.config}/dmem/config.toml"
# Edit the example's absolute data_dir path for this user.
./dmemd config validate
./dmemd init
./dmemd daemon
```

The default configuration file is
`${XDG_CONFIG_HOME:-$HOME/.config}/dmem/config.toml`. Use
`--config /absolute/path/config.toml` to select a different instance. The file
chooses the data directory and all daemon settings; it is the only source of
daemon settings. Providers are disabled in the shipped example.

`dmemd daemon` starts the SQLite adapters and the durable workers, applies the
configuration snapshot, and waits for shutdown. It does not open a socket or a
TCP port. `SIGHUP` requests a serialized configuration reload.

## HTTP Contract

The frozen contract is [docs/api/http-rest-contract.md](docs/api/http-rest-contract.md).
It defines nine Agent-neutral routes:

```text
POST /v1/memories
GET  /v1/memories/{memory_id}
POST /v1/memories/{memory_id}/forget
POST /v1/memories/{memory_id}/feedback
POST /v1/recall
POST /v1/events
GET  /v1/events/{event_id}
POST /v1/scopes
GET  /v1/scopes/{scope_id}
```

Every route requires caller identity (`X-Caller-Subject-ID` and/or
`X-Caller-Client-ID`), and every write requires an `Idempotency-Key`. Requests
are strict single-value JSON with per-route body limits, and every response
carries `X-Request-ID`. The adapter returns an `http.Handler`; serving it is the
responsibility of a future server layer. Because no listener exists yet, the
contract is validated by the adapter's route, decoding, error, and HTTP/2
compatibility tests rather than by a running endpoint.

## Documentation

- [Documentation index](docs/README.md)
- [HTTP REST+JSON contract](docs/api/http-rest-contract.md)
- [Architecture and data model](docs/architecture/overview.md)
- [Memory Gateway refactor design](docs/architecture/memory-gateway-refactor.md)
- [Entities, relations, conflicts, and evolution](docs/architecture/evolution.md)
- [Feedback, ranking, and aggregation](docs/architecture/feedback-and-aggregation.md)
- [Logical portability](docs/storage/logical-portability.md)
- [Operations](docs/operations/runbook.md)
- [Testing](docs/development/testing.md)
- [Architecture decisions](docs/adr/README.md)
