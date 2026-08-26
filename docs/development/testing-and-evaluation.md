# Testing And Evaluation

Linux tests use a C compiler, `CGO_ENABLED=1`, and the `sqlite_fts5` build tag.
Tests use deterministic providers or local `httptest` servers; they do not need
live provider credentials.

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
- `performance` checks broad shared Linux CI bounds for latency, allocation,
  rebuild cost, portability throughput, and storage growth. These are regression
  gates, not production capacity promises.
- `offline-default` verifies that disabled providers do not read secrets or make
  HTTP requests.

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
`performance` and `govulncheck` targets.
