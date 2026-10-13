GO ?= go
GO_TAGS := sqlite_fts5
GOVULNCHECK ?= govulncheck

.PHONY: build install test test-race vet schema schema-init schema-check restore-drill portability-drill quality performance offline-default config-check mcp-check govulncheck check

VERSION ?= 0.1.1
BINDIR ?= $(HOME)/.local/bin
CONFIGDIR ?= $(HOME)/.config/dmem
FORCE ?=

install:
	@set -eu; \
		install -d "$(BINDIR)" "$(CONFIGDIR)"; \
		CGO_ENABLED=1 $(GO) build -tags '$(GO_TAGS)' -trimpath -ldflags '-s -w -X main.version=$(VERSION)' -o "$(BINDIR)/dmemd" ./cmd/dmemd; \
		if [ -f "$(CONFIGDIR)/config.toml" ] && [ -z "$(FORCE)" ]; then \
			printf '%s\n' "keeping existing $(CONFIGDIR)/config.toml (set FORCE=1 to overwrite)"; \
		else \
			install -m 0644 packaging/config.toml "$(CONFIGDIR)/config.toml"; \
			printf '%s\n' "installed $(CONFIGDIR)/config.toml"; \
		fi; \
		printf '%s\n' "installed $(BINDIR)/dmemd"; \
		printf '%s\n' "next steps:"; \
		printf '%s\n' "  export PATH=\"$(BINDIR):\$$PATH\""; \
		printf '%s\n' "  dmemd config validate"; \
		printf '%s\n' "  dmemd init"; \
		printf '%s\n' "  dmemd daemon"

build:
	CGO_ENABLED=1 $(GO) build -tags '$(GO_TAGS)' ./...

test:
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./...

test-race:
	CGO_ENABLED=1 $(GO) test -race -tags '$(GO_TAGS)' ./...

vet:
	CGO_ENABLED=1 $(GO) vet -tags '$(GO_TAGS)' ./...

schema:
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/storage -run '^TestDocumentedSchemaMatchesEmbeddedSchema$$'

schema-init:
	@test -n "$(CONFIG)" || (printf '%s\n' 'CONFIG is required' >&2; exit 2)
	CGO_ENABLED=1 $(GO) run -tags '$(GO_TAGS)' ./cmd/dmemd --config "$(CONFIG)" init

schema-check: schema
	@root="$$(mktemp -d)"; trap 'rm -rf "$$root"' EXIT; \
		config="$$root/config.toml"; \
		printf '%s\n' 'config_version = 1' '' '[instance]' "data_dir = \"$$root/data\"" 'database_filename = "dmem.db"' '' '[server]' 'shutdown_grace_period = "15s"' '' '[jobs]' 'worker_count = 1' 'queue_soft_limit = 10' 'queue_hard_limit = 20' 'max_attempts = 3' 'lease_duration = "30s"' 'per_job_timeout = "2m"' 'retry_initial_backoff = "1s"' 'retry_max_backoff = "5m"' '' '[storage]' 'disk_soft_free_bytes = 1073741824' 'disk_hard_free_bytes = 268435456' '' '[processing]' 'enabled = false' '' '[embedding]' 'enabled = false' > "$$config"; \
		$(MAKE) --no-print-directory schema-init CONFIG="$$config"

restore-drill:
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/maintenance -run '^Test(BackupRestoreDrill|ManifestInventoriesAuthority|RestoreFencesRunningWork|RestoreRebuildsDerivedHealthFromAuthority|RecoveryDrillsFenceInterruptedWork|RecoverySchedulesProjectionAndGraphRebuilds)$$' -count=1

portability-drill:
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/portability -run '^TestExportFreshImportRebuildsEquivalentAuthorityAndRetrieval$$' -count=1 -v

quality:
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/storage -run '^Test(RetrievalQuality|SearchQuality|FeedbackPoisoningSingleCaller|AgentToolRecall)' -count=1 -v

performance:
	DMEM_PERFORMANCE=1 CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/storage -run '^Test(RetrievalPerformance|SearchPerformance|DecisionScaleGates|GraphTraversalFanOutIsBounded)' -count=1 -v
	DMEM_PERFORMANCE=1 CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/portability -run '^TestPortabilityPerformanceAtTargetScale$$' -count=1 -v

offline-default:
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/config -run '^TestProvidersDisabledByDefault$$' -count=1
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/provider -run '^Test(DisabledDoesNotResolveSecretOrUseHTTP|PolicyRejectsBeforeSecretAndNetwork|EmbeddingPolicyRejectsBeforeSecretAndNetwork)$$' -count=1
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/daemon -run '^TestProviderDisabled(OperationPreservesCoreAndDegradesSafely|AgentToolHintRecallClosesLoop)$$' -count=1

config-check:
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/config -run '^TestExampleConfigValidates$$' -count=1

mcp-check:
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./pkg/client ./pkg/toolcontract ./internal/protocol/mcp -count=1
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./internal/daemon -run '^TestMCPToolRouteClosesMemoryLoopWithoutProvider$$' -count=1

govulncheck:
	CGO_ENABLED=1 $(GOVULNCHECK) -tags '$(GO_TAGS)' ./...

check:
	$(MAKE) --no-print-directory test
	$(MAKE) --no-print-directory test-race
	$(MAKE) --no-print-directory vet
	$(MAKE) --no-print-directory schema-check
	$(MAKE) --no-print-directory restore-drill
	$(MAKE) --no-print-directory portability-drill
	$(MAKE) --no-print-directory quality
	$(MAKE) --no-print-directory offline-default
	$(MAKE) --no-print-directory mcp-check
