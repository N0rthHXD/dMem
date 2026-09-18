# Contract: dmemd CLI

> **Historical record.** This contract was superseded by
> `specs/003-api-layer-refactor`. The `status` and `reload` subcommands and the
> Unix-socket API it references no longer exist; reload is requested with
> `SIGHUP`, and the supported commands are `config validate`, `init`, `daemon`,
> `backup`, and `restore`.

## Grammar

```text
dmemd [--config PATH] <command> [command options]
```

Commands:

```text
init
daemon
status
reload
config validate
backup --target PATH
restore --source PATH --confirm-replace
```

- A command is required; bare `dmemd` is a usage error.
- `--config` may appear once and is the only setting-related global option.
- Removed flags and `DMEM_*` setting environment variables have no compatibility behavior.
- Backup/restore source, target, confirmation, output, and timeout controls are operation inputs, not persistent configuration overrides.
- Provider secret environment variables referenced by the file remain valid secret sources.

## Commands

### `config validate`

Reads and strictly validates the selected file without contacting a running daemon or an external provider. It verifies enabled secret-reference availability but never prints values.

Success output includes source/effective identities and redacted capability summaries.

### `daemon`

Loads the complete file before publishing the UDS endpoint. Invalid startup configuration fails before the service becomes available. `SIGHUP` requests reload of the same selected file; result is visible through `status`.

### `status`

Connects to the selected instance and prints the configuration status contract. Human-readable text is not stable; machine consumers use JSON output and enums.

### `reload`

Calls the UDS reload operation. It sends no config content and cannot select a different file. Outcomes are `activated` or `no_change`; validation, restart-required, conflict, and availability errors are non-zero.

## Exit Codes

| Code | Meaning |
|------|---------|
| `0` | Success, including `activated` and `no_change` |
| `1` | Internal operation failure |
| `2` | CLI grammar, locator, or offline config validation error |
| `3` | Selected daemon unavailable |
| `4` | Reload rejected by validation, restart requirement, or revision conflict |
| `5` | Temporary reload conflict or overload |

## Signals

- `SIGINT` and `SIGTERM` request shutdown.
- `SIGHUP` requests one reload through the same serialized pipeline as the API.
- Repeated `SIGHUP` during one reload is coalesced into at most one subsequent attempt.
- Signal-triggered diagnostics are obtained from `status`; they are not written with config values to logs.
