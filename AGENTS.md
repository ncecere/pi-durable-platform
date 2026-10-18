# Agent notes

Conventions for building and testing are in [CONTRIBUTING.md](CONTRIBUTING.md), and the design and its known limits
in [DESIGN.md](DESIGN.md). This file covers what is specific to working on this repository with a coding agent.

## Names

The product is **Pi Durable Platform** ("the platform"; PDP only after the full name). Agents are built with Pi
(earendil-works/pi) and its durable package; the project is independent of Earendil. Agents belong to **teams** (a
personal workspace is a team of kind `personal`): don't write "personal agents" or an agent's "owner" (a person) in
new code or docs.

## Don't disturb the dev instance

- A dev instance may be running: `make run` (or `bin/pi-durable-platform serve`) on :8098, its model gateway on
  :8099, `make web-dev` on :5176, and agents as local child processes. Don't stop it or start servers on those ports.
  To check a build, prefer `go build -o /tmp/pdp ./cmd/pi-durable-platform`; `make build` overwrites `bin/`.
- The compose Postgres (127.0.0.1:55432, databases `pdp` and `pdp_agents`) holds the dev setup. Never stop or remove
  it or its volume, and never run tests against database `pdp` itself or migrate it unasked.

## Makefile targets

`make help` lists them. The usual ones: `make deps-up`, `make run`, `make build`, `make lint` (`go vet` and gofmt on
`cmd internal migrations web/embed.go`), `make fmt`, `make test`, `make test-integration` / `make test-ci` (`-race`,
Postgres required), `make generate`, `make check-generated`, `make actionlint`, `make web`, `make web-test`,
`make doctor`, `make docker` and `make docker-runtime`.

## Tests

- Go integration tests need the server in `PDP_TEST_DATABASE_URL` and create one database per test:

  ```sh
  PDP_TEST_DATABASE_URL='postgres://pdp:pdp-local-only@127.0.0.1:55432/postgres?sslmode=disable' \
    PDP_TEST_REQUIRE_DB=1 go test -count=1 ./...
  ```

  Without `PDP_TEST_REQUIRE_DB=1` they skip silently when Postgres is unreachable: a green run without it proves
  little. `PDP_TEST_LOG=1` shows the server's logs.
- Runtime (`runtime/`): `pnpm install`, `pnpm exec tsc --noEmit`, `pnpm test`. The tests use the compose Postgres
  (`PDP_TEST_DATABASE_URL`, default database `pdp`) and create and drop their own schemas and roles. Run them after
  any change to `runtime/src`, and keep the storage conformance suite passing before touching the pi-durable pin.
- Web (`web/`): `npm run typecheck`, `npm test`, `npm run build`, `npm run check:styling`, `npm run check:size`.

## Rules that tests enforce

- **OpenAPI first.** Change `api/openapi.yaml`, then `make generate` (sqlc, oapi-codegen and the web's
  `schema.gen.ts`). Never edit generated files (`internal/store/dbgen`, `internal/httpapi/apitypes`,
  `web/src/api/schema.gen.ts`). The contract test fails when served routes and the spec differ.
- **The authorization matrix must classify every operation.** A new operation needs an entry in `matrixPolicies`
  (`internal/httpapi/authz_matrix_policy_test.go`): its scope, who may call it on their own team and on another,
  and a request builder. The spec test fails for an unclassified operation.
- **Migrations.** Forward-only; never edit an applied one, add the next number. Bump the version the doctor test
  expects (`internal/doctor/doctor_integration_test.go`, "current (version N)") in the same change.
- **Audit.** Every change is audited in the same transaction. A new audit action or target type also needs its label
  in `web/src/components/audit/labels.ts` (`actionLabels`, `targetTypeLabels`; `web/src/test/audit-labels.test.ts`),
  or the UI spells out the raw name.

## Security habits

- **Never log secrets**: API keys, service account keys, OAuth tokens and codes, runtime and activation tokens,
  provider keys, MCP headers, database URLs with passwords, or message content. Errors from team-chosen URLs must not
  echo response bodies or network error text to the caller.
- Secrets at rest are sealed with `internal/secrets` (`ENCRYPTION_KEY`); credentials are stored as peppered digests.
- Requests to URLs a team or an app chooses go through `internal/teamnet` / `internal/netguard`, never a plain
  `http.Client`.
- Platform roles never reach conversations, except the platform admin's reasoned, audited read.

## Editing

- **Scripted edits and gofmt.** gofmt aligns struct fields, map literals and trailing comments across neighbouring
  lines. A `sed`/`perl`/Python edit that changes one line can leave the block misaligned (`make lint` fails), and
  after `make fmt` neighbouring lines change too, so an exact-text edit prepared before formatting no longer matches.
  Run `make fmt` (or `gofmt -w` on the files) after scripted edits, then re-read the file before editing it again.
  CI runs `gofmt -l .` on the whole tree.
- Internal doc comments describe current behaviour: when a change makes one wrong, fix it in the same change.
- Before reporting done: `make generate` (if the spec or queries changed), `make lint`, `go build ./...`, the Go
  tests with `PDP_TEST_REQUIRE_DB=1`, the runtime and web checks for what you touched, and `make actionlint` when
  `.github/` changed. Say what was not run or not checked in a browser.
