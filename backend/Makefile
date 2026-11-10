.PHONY: build generate test test-unit test-integration test-e2e

VERSION ?= $(shell ./scripts/resolve-version.sh)
LDFLAGS ?= -s -w -X main.Version=$(VERSION)
# Set TAGS=embed to embed the built frontend (frontend/ must be built first).
TAGS ?=

build:
	CGO_ENABLED=0 go build -tags="$(TAGS)" -ldflags="$(LDFLAGS)" -trimpath -o bin/server ./cmd/server

generate:
	go generate ./ent
	go generate ./cmd/server
	go mod tidy

test:
	go test ./...
	golangci-lint run ./...

test-unit:
	go test -tags=unit ./...

test-integration:
	go test -tags=integration ./...

test-e2e:
	go test -tags=e2e -v -timeout=300s ./internal/integration/...
