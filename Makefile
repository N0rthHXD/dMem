GO ?= go
GO_TAGS := sqlite_fts5
GOVULNCHECK ?= govulncheck

.PHONY: build test test-race vet openapi schema schema-init schema-check restore-drill portability-drill quality performance offline-default config-check govulncheck check

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
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/storage -run '^TestDocumentedSchemaMatchesEmbeddedSchema$$'

schema-init:
	@test -n "$(CONFIG)" || (printf '%s\n' 'CONFIG is required' >&2; exit 2)
	CGO_ENABLED=1 $(GO) run -tags '$(GO_TAGS)' ./cmd/dmemd --config "$(CONFIG)" init

schema-check: schema
	@root="$$(mktemp -d)"; trap 'rm -rf "$$root"' EXIT; \
		config="$$root/config.toml"; \
		printf '%s\n' 'config_version = 1' '' '[instance]' "data_dir = \"$$root/data\"" "runtime_dir = \"$$root/runtime\"" 'database_filename = "dmem.db"' 'socket_filename = "dmem.sock"' '' '[server]' 'request_timeout = "30s"' 'shutdown_grace_period = "15s"' 'max_connections = 4' 'max_request_bytes = 4194304' '' '[jobs]' 'worker_count = 1' 'queue_soft_limit = 10' 'queue_hard_limit = 20' 'max_attempts = 3' 'lease_duration = "30s"' 'per_job_timeout = "2m"' 'retry_initial_backoff = "1s"' 'retry_max_backoff = "5m"' '' '[storage]' 'disk_soft_free_bytes = 1073741824' 'disk_hard_free_bytes = 268435456' '' '[processing]' 'version = "builtin-noop-v1"' '' '[processing.provider]' 'enabled = false' '' '[embedding.provider]' 'enabled = false' > "$$config"; \
		$(MAKE) --no-print-directory schema-init CONFIG="$$config"

restore-drill:
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/maintenance -run '^Test(BackupRestoreDrill|ManifestInventoriesAuthority|RestoreFencesRunningWork|RestoreRebuildsDerivedHealthFromAuthority)$$' -count=1

portability-drill:
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/portability -run '^TestExportFreshImportRebuildsEquivalentAuthorityAndRetrieval$$' -count=1 -v

quality:
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/storage -run '^Test(RetrievalQuality|SearchQuality|FeedbackPoisoningSingleCaller)' -count=1 -v

performance:
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/storage -run '^Test(RetrievalPerformance|SearchPerformance)' -count=1 -v
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/portability -run '^TestPortabilityPerformanceAtTargetScale$$' -count=1 -v

offline-default:
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/config -run '^TestProvidersDisabledByDefault$$' -count=1
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/provider -run '^TestDisabledDoesNotResolveSecretOrUseHTTP$$' -count=1

config-check:
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/config -run '^TestExampleConfigValidates$$' -count=1

govulncheck:
	CGO_ENABLED=1 $(GOVULNCHECK) -tags '$(GO_TAGS)' ./...

check:
	$(MAKE) --no-print-directory test
	$(MAKE) --no-print-directory test-race
	$(MAKE) --no-print-directory vet
	$(MAKE) --no-print-directory openapi
	$(MAKE) --no-print-directory schema-check
	$(MAKE) --no-print-directory restore-drill
	$(MAKE) --no-print-directory portability-drill
	$(MAKE) --no-print-directory quality
	$(MAKE) --no-print-directory offline-default
