GO      ?= go
SQLC    ?= $(shell command -v sqlc 2>/dev/null || echo $(HOME)/go/bin/sqlc)
# The version without the tag's "v" (v0.1.0 -> 0.1.0), as the release images report it.
VERSION ?= $(patsubst v%,%,$(shell git describe --tags --always --dirty 2>/dev/null || echo dev))
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
PKG     := github.com/ncecere/pi-durable-platform
LDFLAGS := -s -w -X $(PKG)/internal/buildinfo.Version=$(VERSION) -X $(PKG)/internal/buildinfo.Commit=$(COMMIT)

# Integration tests create one database per test on this server (a role
# allowed to create databases); they skip when it is unreachable. They never
# use database pdp itself.
export PDP_TEST_DATABASE_URL ?= postgres://pdp:pdp-local-only@127.0.0.1:55432/postgres?sslmode=disable

# Go sources checked by gofmt (runtime/ is the TypeScript agent runtime).
GOFMT_PATHS := cmd internal migrations web/embed.go

.PHONY: help deps-up deps-down generate check-generated build run migrate doctor test test-integration test-ci lint actionlint fmt web web-test web-dev docker docker-runtime

help: ## Show targets
	@grep -E '^[a-z0-9-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

deps-up: ## Start local Postgres (127.0.0.1:55432); it restarts with Docker
	docker compose up -d --wait

deps-down: ## Stop local Postgres (keeps data)
	docker compose down

generate: ## Regenerate sqlc queries and OpenAPI types (Go, and TypeScript when web/ exists)
	$(SQLC) generate
	$(GO) generate ./...
	@if [ -f web/package.json ]; then cd web && npm run gen; else echo "web/package.json not found; skipping TypeScript generation"; fi

check-generated: generate ## Fail if generated code is stale
	@git diff --exit-code -- internal/store/dbgen internal/httpapi/apitypes web/src/api/schema.gen.ts || \
		(echo "generated code is stale: run 'make generate' and commit" && exit 1)
	@test -z "$$(git ls-files --others --exclude-standard -- internal/store/dbgen internal/httpapi/apitypes web/src/api)" || \
		(echo "untracked generated files: run 'make generate' and commit" && exit 1)

build: ## Build bin/pi-durable-platform (run `make web` first to include the UI)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/pi-durable-platform ./cmd/pi-durable-platform

run: ## Run `serve` locally on 127.0.0.1:8098 using .env (created from .env.example)
	@test -f .env || cp .env.example .env
	set -a && . ./.env && set +a && $(GO) run ./cmd/pi-durable-platform serve

migrate: ## Apply migrations using .env
	@test -f .env || cp .env.example .env
	set -a && . ./.env && set +a && $(GO) run ./cmd/pi-durable-platform migrate

doctor: ## Check the configuration, Postgres, migrations and the agents database using .env
	@test -f .env || cp .env.example .env
	set -a && . ./.env && set +a && $(GO) run ./cmd/pi-durable-platform doctor

test: ## All Go tests; integration tests skip when Postgres is unreachable
	$(GO) test -count=1 ./...

test-integration: deps-up test-ci ## All Go tests with Postgres required (fails instead of skipping)

test-ci: ## test-integration without deps-up (PDP_TEST_DATABASE_URL)
	PDP_TEST_REQUIRE_DB=1 $(GO) test -race -count=1 ./...

lint: ## go vet and gofmt check
	$(GO) vet ./...
	@test -z "$$(gofmt -l $(GOFMT_PATHS))" || (gofmt -l $(GOFMT_PATHS) && echo "run 'make fmt'" && exit 1)

ACTIONLINT_VERSION ?= v1.7.12

actionlint: ## Check the GitHub Actions workflows
	$(GO) run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

fmt: ## Format Go code
	gofmt -w $(GOFMT_PATHS)

web: ## Build the web UI into web/dist (embedded by `make build`)
	cd web && npm ci --ignore-scripts && npm run build

web-test: ## Type-check and test the web UI
	cd web && npm run typecheck && npm test

docker: ## Build the platform image pi-durable-platform:<version> (local)
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t pi-durable-platform:$(VERSION) .

docker-runtime: ## Build the agent runtime image pi-durable-platform-runtime:<version> (local)
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -f runtime/Dockerfile -t pi-durable-platform-runtime:$(VERSION) runtime

web-dev: ## Run the Vite dev server on :5176 (proxies the API to `make run` on :8098)
	cd web && npm run dev
