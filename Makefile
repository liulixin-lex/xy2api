.PHONY: build build-backend build-frontend test test-backend test-frontend lint generate

# Build the frontend, then a single backend binary (backend/bin/server) that embeds it.
build: build-frontend
	@$(MAKE) -C backend build TAGS=embed

build-backend:
	@$(MAKE) -C backend build

build-frontend:
	@pnpm --dir frontend run build

# Run backend unit tests and the full frontend test suite.
test: test-backend test-frontend

test-backend:
	@$(MAKE) -C backend test-unit

test-frontend:
	@pnpm --dir frontend run lint:check
	@pnpm --dir frontend run typecheck
	@pnpm --dir frontend exec vitest run

lint:
	@cd backend && golangci-lint run ./...
	@pnpm --dir frontend run lint:check

# Regenerate Ent and Wire code after editing backend/ent/schema or providers.
generate:
	@$(MAKE) -C backend generate
