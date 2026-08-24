GO ?= go
GO_TAGS := sqlite_fts5
GOVULNCHECK ?= govulncheck

.PHONY: build test test-race vet openapi schema schema-init schema-check restore-drill export-import-drill retrieval-gates offline-default govulncheck phase-2/check phase-3/traceability phase-3/check

build:
	CGO_ENABLED=1 $(GO) build -tags '$(GO_TAGS)' ./...

test:
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./...

test-race:
	CGO_ENABLED=1 $(GO) test -race -tags '$(GO_TAGS)' ./...

vet:
	CGO_ENABLED=1 $(GO) vet -tags '$(GO_TAGS)' ./...

openapi:
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/daemon -run '^TestOpenAPIContract$$'

schema:
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/storage -run '^TestP3CONTRACT002DocumentedSchemaMatchesEmbeddedSchema$$'

schema-init:
	@test -n "$(DATA_DIR)" || (printf '%s\n' 'DATA_DIR is required' >&2; exit 2)
	CGO_ENABLED=1 $(GO) run -tags '$(GO_TAGS)' ./cmd/dmemd init --data-dir "$(DATA_DIR)"

schema-check: schema
	@data_dir="$$(mktemp -d)"; trap 'rm -rf "$$data_dir"' EXIT; \
		$(MAKE) --no-print-directory schema-init DATA_DIR="$$data_dir"

restore-drill:
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/maintenance -run '^Test(BackupRestoreDrill|P3DATA)' -count=1

export-import-drill:
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/portability -run '^TestP3(PORT010|PERF003)' -count=1 -v

retrieval-gates:
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/storage -run '^TestP[23](QUAL|PERF)' -count=1 -v

offline-default:
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/config -run '^TestProvidersDisabledByDefault$$' -count=1
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/provider -run '^TestDisabledDoesNotResolveSecretOrUseHTTP$$' -count=1

govulncheck:
	CGO_ENABLED=1 $(GOVULNCHECK) -tags '$(GO_TAGS)' ./...

phase-2/check:
	$(MAKE) --no-print-directory test
	$(MAKE) --no-print-directory test-race
	$(MAKE) --no-print-directory vet
	$(MAKE) --no-print-directory openapi
	$(MAKE) --no-print-directory schema-check
	$(MAKE) --no-print-directory restore-drill
	$(MAKE) --no-print-directory retrieval-gates
	$(MAKE) --no-print-directory offline-default

phase-3/traceability:
	@CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/storage -run '^TestP3TraceabilityRequiredIDs$$' -count=1

phase-3/check:
	$(MAKE) --no-print-directory phase-3/traceability
	$(MAKE) --no-print-directory phase-2/check
	$(MAKE) --no-print-directory export-import-drill
