# Pi Durable Platform

An enterprise platform where teams and services deploy and manage Pi Durable agents (one isolated runtime per agent,
for example one assistant per user of a chat app).

This repository holds:

- **The platform** (Go 1.26 + PostgreSQL 17, with a React/Vite web UI embedded in one binary): sign-in (OIDC, plus
  local development personas), teams and their members, platform and team roles, personal API keys, team service
  accounts, an append-only audit log, and a `doctor` command. The agent domain is not built yet: **Agents** in the UI
  is a placeholder.
- **The agent runtime** in [`runtime/`](runtime) (TypeScript), developed separately.

**Development software, unreleased.**

## Quick start (development)

Requires Go 1.26+, Node 22+, Docker, and [sqlc](https://sqlc.dev) 1.31 for `make generate`.

```sh
make deps-up      # PostgreSQL 17 on 127.0.0.1:55432 (docker compose project pi-durable-platform)
make web          # build the web UI into web/dist (embedded by the Go binary)
make run          # creates .env from .env.example if needed, serves http://localhost:8098
```

Open <http://localhost:8098> and choose a development persona: **Dev Platform Admin** (`admin@localhost`) is a
platform admin, **Dev Platform Auditor** (`auditor@localhost`) a platform auditor, and **Dev User**, **Alex Dev**,
**Blair Dev** and **Casey Dev** have no platform role. Development sign-in (`DEV_AUTH=true`) and the example keys in
`.env.example` are refused unless `APP_URL` is a loopback address. Never expose them.

Other targets (`make help` lists them all):

| Command | What it does |
|---|---|
| `make build` | Builds `bin/pi-durable-platform` (run `make web` first to include the UI) |
| `make test` | All Go tests; integration tests skip when Postgres is unreachable |
| `make test-integration` | `make deps-up`, then all Go tests with `-race`, failing instead of skipping without Postgres |
| `make lint` | `go vet` and the gofmt check |
| `make generate` | Regenerates sqlc queries, Go API types and the web's TypeScript API types |
| `make web-test` | Web type-check and Vitest tests |
| `make web-dev` | Vite with hot reload on :5176, proxying the API to :8098 |
| `make doctor` | Checks the configuration, Postgres and migrations |
| `make deps-down` | Stops Postgres (keeps its data) |

In `web/`: `npm run typecheck`, `npm test`, `npm run build`, `npm run check:styling` (Base UI components, CSS
Modules and design tokens only) and `npm run check:size` (source files ≤ 400 lines, tests ≤ 700).

Integration tests create one database per test on the server in `PDP_TEST_DATABASE_URL` (default: the compose
Postgres, `postgres://pdp:pdp-local-only@127.0.0.1:55432/postgres`), never in database `pdp` itself.
`PDP_TEST_REQUIRE_DB=1` makes them fail instead of skip, and `PDP_TEST_LOG=1` shows the server's logs.

## Commands

```
pi-durable-platform serve     HTTP API, embedded UI and background loops
pi-durable-platform migrate   apply database migrations and exit
pi-durable-platform doctor    check the configuration, Postgres and migrations
pi-durable-platform version   print the version
```

Configuration comes from environment variables (optionally layered over a YAML file named by `PDP_CONFIG_FILE`);
[`.env.example`](.env.example) describes each one.

## Layout and conventions

| Path | What |
|---|---|
| `api/openapi.yaml` | The management API contract. Change it first, then `make generate`. A test fails if served routes and the spec differ, and the authorization matrix covers every operation. |
| `migrations/` | Forward-only goose migrations (embedded). Never edit an applied migration; add the next number. |
| `internal/store/queries/` | SQL for sqlc → `internal/store/dbgen` |
| `internal/authz` | Pure authorization rules (platform and team roles) |
| `internal/httpapi` | Route table (`server.go`), handlers, middleware, the contract and authorization-matrix tests |
| `internal/{auth,teams,platform,apikeys,serviceaccounts,audit}` | The services; each authorizes and audits in the same transaction as the change |
| `web/src/router.tsx`, `web/src/components/layout/nav.tsx` | The web routes and navigation |

Success responses are `{"data": …}`; errors are `{"error": {"code", "message"}}`. Browser writes need the session's
`X-CSRF-Token` and a same-origin `Origin`. Changes are audited in the same transaction as the change itself;
`audit_log` is append-only.

Forked from the reusable foundation of Open MCP Portal.
