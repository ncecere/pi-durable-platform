# Contributing

Thanks for helping. This file covers how to set up, test and submit changes. Report security problems privately, as described in [`SECURITY.md`](SECURITY.md), not in issues. Everyone taking part follows the [code of conduct](CODE_OF_CONDUCT.md).

## Before you start

- For anything larger than a small fix, open an issue first and describe the problem and the change you have in mind.
- Read [`DESIGN.md`](DESIGN.md) for the part you're changing. A change to a product decision gets a row in its Decisions table.

## Development setup

Requires Go 1.26+, Node 22.19+, pnpm 11 (`corepack enable`), and Docker. `make generate` also needs [sqlc](https://sqlc.dev) 1.31.1 on your `PATH` or in `~/go/bin`.

```sh
make deps-up                     # PostgreSQL 17 on 127.0.0.1:55432 (docker compose project pi-durable-platform)
(cd runtime && pnpm install)     # the agent runtime (the local launcher runs agents as child processes)
make web                         # build the UI into web/dist (embedded by the binary)
make run                         # creates .env from .env.example if needed, serves http://localhost:8098
make web-dev                     # optional: Vite with hot reload on :5176, proxying the API to :8098
```

- Open http://localhost:8098 and choose a development persona (`admin@localhost` is a platform admin). Development sign-in is refused unless `APP_URL` is a loopback address. Never expose it.
- Agents use the scripted `demo` model until you configure a model (Admin › Models, or `AGENTS_MODEL_*`).
- `make deps-down` stops Postgres and keeps its data. `make help` lists every target.
- Settings come from environment variables (optionally layered over a YAML file named by `PDP_CONFIG_FILE`); [`.env.example`](.env.example) describes the common ones, and `internal/config/settings.go` lists them all.
- Never commit `.env` or anything else that holds a secret.

## Tests

| Command | What it runs |
|---|---|
| `make test` | All Go tests; integration tests skip when Postgres is unreachable |
| `make test-integration` | `make deps-up`, then all Go tests with `-race`, failing instead of skipping without Postgres |
| `make lint` | `go vet` and the gofmt check |
| `make actionlint` | Checks the GitHub Actions workflows |
| `make web-test` | UI type-check and Vitest tests |
| `cd runtime && pnpm exec tsc --noEmit && pnpm test` | The agent runtime's type-check and tests (needs the compose Postgres) |

- Go integration tests create one database per test on the server in `PDP_TEST_DATABASE_URL` (default: the compose Postgres, `postgres://pdp:pdp-local-only@127.0.0.1:55432/postgres?sslmode=disable`), never in database `pdp` itself. `PDP_TEST_REQUIRE_DB=1` makes them fail instead of skip, and `PDP_TEST_LOG=1` shows the server's logs.
- The runtime's tests connect to `PDP_TEST_DATABASE_URL` too (default `postgres://pdp:pdp-local-only@127.0.0.1:55432/pdp`) and create and remove their own schemas and roles.
- In `web/`: `npm run typecheck`, `npm test`, `npm run build`, and the policies `npm run check:styling` (Base UI components, CSS Modules and design tokens only) and `npm run check:size` (source files ≤ 400 lines, tests ≤ 700).
- Add or update tests with every behaviour change. Bug fixes come with a test that fails without the fix.

CI (`.github/workflows/ci.yml`) runs the runtime's checks, the web checks above, gofmt, `go vet` and the Go tests against a Postgres service, and `make actionlint`. Run the same before opening a pull request.

## Generated code

`make generate` regenerates:
- sqlc queries: `internal/store/queries/*.sql` → `internal/store/dbgen`
- Go API types: `api/openapi.yaml` → `internal/httpapi/apitypes` (oapi-codegen)
- TypeScript API types: `api/openapi.yaml` → `web/src/api/schema.gen.ts` (openapi-typescript)

Commit generated files with the change that caused them, and never edit them by hand. `make check-generated` fails if they are stale.

## API changes: OpenAPI first

1. Change [`api/openapi.yaml`](api/openapi.yaml) first: paths, schemas, error codes and examples.
2. Run `make generate`.
3. Implement the handler and the UI against the generated types.

- A test fails if the routes the server serves and the spec differ, and the authorization matrix (`internal/httpapi/authz_matrix_*_test.go`) must classify every operation.
- Success responses are `{"data": …}`; errors are `{"error": {"code", "message"}}`.
- The OAuth protocol endpoints, `/.well-known/*`, the model gateway and the runtime's own API are documented in [`DESIGN.md`](DESIGN.md), not in the contract.

## Database

- Queries live in `internal/store/queries/*.sql`; sqlc generates the Go.
- Migrations live in `migrations/`. They are forward-only. Never edit a migration that has been applied anywhere; add the next number, and update the version the doctor test expects (`internal/doctor/doctor_integration_test.go`).
- Changes are audited in the same transaction as the change itself. `audit_log` is append-only.

## Commits and pull requests

- Keep pull requests focused on one change. Separate refactors from behaviour changes.
- Commit subjects are short and say what changed, optionally prefixed by the area (`web: …`, `runtime: …`, `docs: …`).
- The pull request description says what changed and why, how you tested it, and which issue it relates to.
- CI must pass, generated code must be current, and docs (`DESIGN.md`, `README.md`, `.env.example`, `CHANGELOG.md`'s Unreleased section) must match the change.

## Releasing

1. Write `docs/releases/vX.Y.Z.md`, turn the changelog's Unreleased section into `[X.Y.Z] - <date>`, and make `Version` in `internal/buildinfo/buildinfo.go` and `version` in `runtime/package.json` the release version. The release workflow refuses a tag that doesn't match them, or one without notes.
2. Push `main` and wait for CI to pass.
3. Tag `vX.Y.Z` (or `vX.Y.Z-rc.1`, which becomes a pre-release with the base version's notes) and push the tag. `.github/workflows/image.yml` builds both images for linux/amd64 and linux/arm64, checks and scans them, publishes and signs them, and creates the GitHub Release (`scripts/github-release.sh`; `RELEASE_DRY_RUN=1` with `RELEASE_SBOM_FILE` and `RELEASE_RUNTIME_SBOM_FILE` tries it without publishing).
4. Fill in the image digests in the release notes.

Branch pushes publish `sha-<12>` images (linux/amd64) through `.github/workflows/images.yml`; tags never do.

## License

By contributing, you agree that your contributions are licensed under the [MIT License](LICENSE), the project's licence.
