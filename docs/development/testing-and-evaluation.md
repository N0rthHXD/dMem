# Testing And Evaluation

Linux tests use a C compiler, `CGO_ENABLED=1`, and the `sqlite_fts5` build tag.
Vector storage statically links sqlite-vec (`github.com/asg017/sqlite-vec-go-bindings`
`v0.1.6`); its C source is compiled into the same binary via `sqlite3_auto_extension`,
so no external `.so` or runtime extension loading is required. Tests use
deterministic providers or local `httptest` servers; they do not need live
provider credentials.

## Core Checks

```sh
go mod download
make build
make test
make test-race
make vet
```

`make openapi` verifies that the daemon routes and behavior match the documented
OpenAPI contract. `make schema-check` verifies that the documented SQL exactly
matches the embedded schema and initializes a temporary database.

## Recovery And Quality

```sh
make openapi
make schema-check
make restore-drill
make portability-drill
make quality
make performance
make offline-default
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
  pending review, fact temporal evolution, entity-fact/list/proposal APIs,
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
- `python-test` runs the evaluation suites under `evaluation/` (calibration,
  LoCoMo, and LongMemEval) with `pytest`.

### Configuration And Reconciliation

Configuration reload and reconciliation tests live in `internal/daemon` and
`internal/storage`. They use the managed daemon (`StartManaged` with
`config.NewManager`) and the in-package `reloadConfig` helper to write strict TOML
files that toggle provider enablement and policy assignments without live
provider credentials.

- `TestManagedDaemonReloadsCapabilitiesAtomically` and `TestSC014ReloadBecomesEffectiveWithinTenSeconds`
  verify atomic reload, no-change, rejection, and the 10-second SC-014 timing
  bound.
- `TestPolicyCorrectionRecoversBlockedProcessing` and `TestPolicyExpansionDoesNotDuplicateCompletedWork`
  prove policy expansion flips `blocked_policy` jobs to `pending` without
  changing the processing compatibility identity or duplicating completed work.
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

For changes to retrieval or automatic processing, compare the same evaluation
set before and after the change. At minimum track extraction accuracy and
omissions, hallucinations, evidence coverage, scoped recall, ranking quality,
stale-revision return rate, conflict-reference completeness, latency, and
resource use.

`make check` runs the full required suite except the separately invoked
`performance`, `python-test`, and `govulncheck` targets. Performance gates are
also skipped by `test` and `test-race`; a controlled runner or CI job opts in
with `DMEM_PERFORMANCE=1`.
