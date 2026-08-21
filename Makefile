GO ?= go
GO_TAGS := sqlite_fts5

.PHONY: build test test-race vet

build:
	CGO_ENABLED=1 $(GO) build -tags '$(GO_TAGS)' ./...

test:
	CGO_ENABLED=1 $(GO) test -tags '$(GO_TAGS)' ./...

test-race:
	CGO_ENABLED=1 $(GO) test -race -tags '$(GO_TAGS)' ./...

vet:
	CGO_ENABLED=1 $(GO) vet -tags '$(GO_TAGS)' ./...
