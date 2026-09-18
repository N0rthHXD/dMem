# Quickstart: Validate Dynamic Provider Configuration

> **Historical record.** This quickstart was superseded by
> `specs/003-api-layer-refactor`. The `make openapi` target, the
> `dmemd status`/`dmemd reload` subcommands, and the Unix-socket API it exercises
> were removed. See `docs/development/testing.md` for the current gates.

## Prerequisites

- Linux with Go 1.24+, CGO, and SQLite FTS5 prerequisites.
- Repository root as the working directory.
- The feature implemented according to [plan.md](plan.md).
- Two local deterministic HTTPS test providers or the repository's provider test server fixtures.
- A private temporary XDG config, data, and runtime location.

## 1. Build and Baseline Tests

```sh
make build
make test
make vet
make openapi
make schema-check
```

Expected:

- All commands pass.
- Existing authority, memory, keyword, and review behavior remains available.
- No test requires live provider credentials.

Run `make test-race` before declaring implementation complete.

## 2. Create the Offline-Default File

Copy `contracts/config.example.toml` to the test XDG config location and replace the example absolute paths with private test directories.

Validate it:

```sh
./dmemd --config /absolute/test/config.toml config validate
```

Expected:

- Validation succeeds.
- Both providers are disabled.
- Output includes source/effective identities but no secret values.

Verify strictness by adding `max_concurent = 2` under a provider limits table.

Expected:

- Validation fails with `config_unknown_field` and a field location.
- No setting is silently ignored.

## 3. Initialize and Start with No Setting Flags

```sh
./dmemd --config /absolute/test/config.toml init
./dmemd --config /absolute/test/config.toml daemon
```

Expected:

- The daemon starts from the selected file.
- Local authority writes, manual memory, and keyword retrieval work.
- Removed setting-specific flags and `DMEM_*` setting overrides are unsupported.
- Provider secret environment variables are not needed while providers are disabled.

## 4. Enable Processing at Runtime

Create a user/project hierarchy, then update the processing provider section using the grammar in [config-schema.md](contracts/config-schema.md). Use an exact project assignment with descendant inheritance and set the referenced test secret environment variable in the daemon environment.

```sh
./dmemd --config /absolute/test/config.toml reload
./dmemd --config /absolute/test/config.toml status
```

Expected:

- Reload returns `activated` without daemon restart.
- Processing is ready and embedding remains disabled.
- New child task/session spaces inherit the project decision.
- Sibling projects remain denied.
- Status contains generated identities/generations and secret availability, never the secret value.

## 5. Reject an Invalid Full Revision

Edit both provider sections, but make one endpoint invalid, then reload.

Expected:

- Reload is rejected.
- Neither processing nor embedding changes.
- Previous processing work continues under the previous complete snapshot.
- Authority and keyword operations remain available.

Restore valid content and change only comments/formatting, then reload.

Expected:

- Outcome is `no_change`.
- Capability generations do not change.
- No reconciliation run is created.

## 6. Verify Policy-Proportionate Processing

Configure an allowed target source and a denied optional context source. Ingest an allowed target with mixed context.

Expected:

- Denied context is omitted before secret resolution or network activity.
- The allowed target is processed.
- Attempt status reports omission count/reason only.

Create a target in a denied sibling space.

Expected:

- No external call occurs.
- The job becomes `blocked_policy`, not succeeded or retrying.

Expand the policy to allow that sibling and reload.

Expected:

- A reconciliation run queues the blocked job within 30 seconds.
- The job executes under the new policy generation.

## 7. Verify Eligible Semantic Corpus

Enable embedding with a policy that allows one project and denies another. Create searchable memories in both.

Expected:

- Allowed memories receive compatible embeddings.
- Denied memories receive explicit ineligible decisions and no provider call.
- The semantic index reaches ready for the eligible corpus; denied memories do not block it.
- Keyword retrieval can still find policy-excluded memories within normal scope rules.

Expand embedding policy and reload.

Expected:

- Newly eligible memories are scheduled.
- Existing compatible vectors are reused.
- Semantic retrieval remains available for the existing eligible corpus during reconciliation.

Change embedding dimension/model and reload.

Expected:

- Old vectors are not represented as current-compatible.
- The new eligible corpus rebuilds under a new compatibility identity.
- Hybrid retrieval reports keyword degradation until compatible semantic readiness.

## 8. Verify Complete Query Scope

Run the same semantic search with the same spaces in different orders.

Expected:

- Authorization and semantic availability are identical.
- Query egress is evaluated as `semantic_query`, not as source type `manual`.
- If any requested scope is denied, semantic-only search is explicitly unavailable and hybrid explicitly degrades rather than silently dropping spaces.

## 9. Verify In-Flight Generation Fence

Pause a deterministic provider response after the external call starts. Narrow policy or change capability identity and reload before releasing the response.

Expected:

- Reload activates without waiting for the old request.
- The old result cannot publish under the new generation.
- The attempt records `stale_generation` and the logical work is re-evaluated as pending, blocked, or superseded.
- No authority revision is mutated by reconciliation.

## 10. Verify SIGHUP

Save a valid provider change and send `SIGHUP` to the daemon.

Expected:

- The same activation pipeline runs.
- Status reports trigger `sighup` and the same activated/no-change/rejected semantics.
- Repeated signals are bounded rather than producing an unbounded queue.

## Completion Gate

The feature is ready for task completion only when:

- Strict configuration and CLI contract tests pass.
- OpenAPI and both schema copies match.
- Invalid/no-op/concurrent reload integration tests pass.
- Processing, embedding, aggregate, semantic query, and generation-fence tests pass under `go test -race`.
- Recovery tests prove reconciliation resumes after daemon restart.
- Logs, status, audit, and errors pass secret/content leak assertions.
