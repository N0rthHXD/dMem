# Testing

Linux tests use a C compiler, `CGO_ENABLED=1`, and the `sqlite_fts5` build tag.
Vector storage statically links sqlite-vec
(`github.com/asg017/sqlite-vec-go-bindings` `v0.1.6`); its C source is compiled
into the same binary via `sqlite3_auto_extension`, so no external `.so` or
runtime extension loading is required. Tests use deterministic providers or
local `httptest` servers; they do not need live provider credentials.

## Core Checks

```sh
go mod download
make build
make test
make test-race
make vet
```

`make schema-check` verifies that the documented SQL exactly matches the embedded
schema and initializes a temporary database.

## Gateway And Protocol Tests

- Gateway module tests use narrow fake ports for only the capabilities a module
  declares. They cover semantic validation, error classification, cancellation,
  idempotency, orchestration, and invariants without HTTP or SQLite where
  possible.
- `internal/gateway/arch_test.go` enforces the gateway dependency rules: no
  `net/http`, no protocol package, no storage or jobs import, no sibling-module
  import, and no exported contract that exposes a `storage.*` selector.
- `internal/protocol/httpapi` tests use fake gateway modules and cover strict
  single-value JSON decoding, DTO conversion, route conformance (caller identity
  and idempotency policy), response encoding, gateway-error mapping, panic
  recovery, cancellation, and safe errors.
- HTTP/2 compatibility and multiplexed concurrency are proven with a test-only
  `httptest.Server` configured with `EnableHTTP2`. No production listener, TLS,
  or h2c setup is added by the tests.
- `internal/protocol/httpapi/arch_test.go` enforces that the protocol tree
  imports no storage, jobs, provider, config, retrieval, or application package
  and never owns a listener or inspects the HTTP protocol version.

## Recovery And Quality

```sh
make schema-check
make restore-drill
make portability-drill
make quality
make performance
make offline-default
make config-check
```

- `restore-drill` creates and restores a real consistent snapshot, validates
  authority and checkpoints, fences interrupted work, and checks derived-state
  recovery.
- `portability-drill` checks fresh-instance logical import, mappings,
  provenance, retrieval equivalence, checkpoints, throughput, and temporary
  space bounds. Full package tests cover malformed and malicious inputs,
  rollback, cancellation, idempotency, and stale generations.
- `quality` runs fixed corpora for keyword, semantic, hybrid, scope isolation,
  conflict references, feedback poisoning, and stale-revision behavior. It also
  enforces broad shared Linux CI quality thresholds.
- Knowledge-graph tests cover graph extraction scheduling/idempotency, provider
  output validation (unknown fields, rune ranges, illegal predicates, NaN
  confidence, self relations), build-time entity resolution, auto-confirm vs
  pending review, fact temporal evolution, entity-fact/list/proposal behavior,
  query-time entity resolution (English and Chinese aliases), 0-hop/1-hop/2-hop
  and memory-relation recall, graph cursor invalidation, and concurrent fact
  confirmation.
- `performance` checks broad shared Linux CI bounds for latency, allocation,
  rebuild cost, portability throughput, and storage growth. These are regression
  gates, not production capacity promises. Wall-clock and allocation gates are
  skipped by the default and race suites; they run only with
  `DMEM_PERFORMANCE=1`, which the `performance` target sets.
- `offline-default` verifies that disabled providers do not read secrets or make
  HTTP requests.
- `config-check` verifies that `docs/operations/config.example.toml` validates.

### Configuration And Reconciliation

Configuration reload and reconciliation tests live in `internal/daemon` and
`internal/storage`. They use the managed daemon (`StartManaged` with
`config.NewManager`) and the in-package `reloadConfig` helper to write strict TOML
files that toggle provider enablement and policy assignments without live
provider credentials.

- `TestManagedDaemonReloadsCapabilitiesAtomically` and
  `TestSC014ReloadBecomesEffectiveWithinTenSeconds` verify atomic reload,
  no-change, rejection, and the 10-second SC-014 timing bound.
- `TestPolicyCorrectionRecoversBlockedProcessing` and
  `TestPolicyExpansionDoesNotDuplicateCompletedWork` prove policy expansion
  flips `blocked_policy` jobs to `pending` without changing the processing
  compatibility identity or duplicating completed work.
- `TestReconciliationResumesAfterDaemonRestart` proves effective configuration
  identity, blocked work, reconciliation cursor, and semantic provider state
  survive a daemon restart and resume correctly.
- `TestSC004ReconciliationEnqueuesEligibleJobsPromptly` verifies the 30-second
  SC-004 bound: 95% of newly eligible blocked jobs enter reconciliation promptly
  while completed jobs are not duplicated.

Deterministic provider drills use the in-process `heldProcessor` (blocks until a
release channel is closed) and `httptest.NewTLSServer` stubs; they never contact
live endpoints. Reconciliation recovery is driven either by the daemon's
maintenance loop (1-second interval) or by direct repository calls
(`ClaimReconciliationRun` + `Reconcile`) for precise timing control.

The fixed retrieval fixtures and versioned gate files live under
`internal/storage/testdata`. Changes to corpora, ranking, index formats,
portability format, scale, or thresholds must update the corresponding identity
and record a reproducible baseline.

Vulnerability scanning is separate because it downloads vulnerability data:

```sh
go install golang.org/x/vuln/cmd/govulncheck@v1.1.4
make govulncheck
```

`make check` runs the full required suite except the separately invoked
`performance` and `govulncheck` targets. Performance gates are also skipped by
`test` and `test-race`; a controlled runner or CI job opts in with
`DMEM_PERFORMANCE=1`.
