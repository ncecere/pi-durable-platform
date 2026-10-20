# A custom agent: the task board

A starter project for an agent built with the runtime library, `@pdp/runtime`
([docs/custom-agents.md](../../docs/custom-agents.md)). It is the platform's assistant (the template's instructions and
`get_time`) plus:

- **One tool**, `board`: add a task, mark one done, or clear the board. It is replay-safe (a call that a crash
  interrupted runs again and finds its own write) and returns `details` for apps (`{ action, open, done }`), which
  appear on the tool call in the thread view and its event streams, never to the model.
- **One document**, `example.board`: the conversation's tasks, kept next to the transcript. It is **exposed**, so an
  app reads it with `GET /v1/agents/{agent}/threads/{thread}/documents/example.board` (the same access as reading the
  thread), and the thread's events say when it changes (`document.updated`).
- **One guard**, a `beforeTool` hook: the model can't clear the board (or call an MCP tool that deletes) on its own.
  Asking a person and going on with their answer comes with the platform's approvals
  ([#16](https://github.com/ncecere/pi-durable-platform/issues/16)); until then a guard blocks, with a reason the model
  sees.

| Path | What it is |
|---|---|
| `src/agent.ts` | The agent: its document, tool, guard and definition (`defineAgent`). |
| `src/main.ts` | Its process (`runAgentProcess`): what the local launcher and the image run. |
| `test/agent.test.ts` | A conversation against the fake model, the guard, and restarts in the middle of the tool call and of the model's answer (`expectReplayed`). |
| `test/conformance.test.ts` | The runtime contract's conformance suite against this agent. |
| `Dockerfile` | The agent's image, on the platform's runtime image. |

## Set up and test

The library is this repository's `runtime/` (linked, not published yet), so install it first:

```sh
cd runtime && pnpm install && cd ..
cd examples/custom-agent && pnpm install
pnpm typecheck
pnpm test
```

The tests need no database and no model key: the test kit (`@pdp/runtime/testing`) runs the agent on in-memory storage
with a scripted model, behind its real HTTP API.

## Run it against the console

The local launcher runs every agent as a child process from one directory (`AGENTS_RUNTIME_DIR`, its
`node_modules/.bin/tsx src/main.ts`). Point it at this one:

1. Start the compose PostgreSQL: `make deps-up` (from the repository's root).
2. In `.env` (copied from `.env.example` by `make run`), set `AGENTS_RUNTIME_DIR=examples/custom-agent`. Every agent of
   this instance now runs the task board; set it back to `runtime` for the platform's own assistant.
3. `make run`, open the console at <http://127.0.0.1:8098>, sign in, deploy an agent in a team, and chat with it in
   its **Test** tab. Open a tool call to see its details.
4. Read the board as an app would: with a service account key of the team,
   `curl -H "Authorization: Bearer $KEY" http://127.0.0.1:8098/v1/agents/$AGENT/threads/$THREAD/documents/example.board`.

The platform's default model is a scripted demo (`AGENTS_MODEL_PROVIDER=demo`): it only ever calls `get_time` (ask it
the time). To see the model use the board, add a model in **Admin > Models** (any OpenAI-compatible endpoint, a local
one too) and choose it in the agent's settings. After a change to `src/`, hibernate the agent (its page, or
`POST /v1/agents/{agent}/hibernate`): the next message starts it with the new code.

## Build the image

```sh
make docker-runtime VERSION=dev                       # the runtime image this agent builds on
docker build -t custom-agent:dev examples/custom-agent
```

Give the image to the platform as `AGENTS_IMAGE` (Kubernetes and Agent Sandbox launchers). Only the operator chooses
the image: every agent of the instance runs it (custom agents per template come later,
[#29](https://github.com/ncecere/pi-durable-platform/issues/29)).

## Upgrades

The platform records, per agent, the newest pi-durable version that has written its storage, and refuses to start the
agent on a runtime with an older one (`409 runtime_too_old`, and the agent's page says why): documents migrate when
read, and data a newer version wrote can't be read by older code. **Rolling a runtime back after it has written is not
supported.** Keep `example.board`'s kind; change its shape by raising `version` with a `migrate`.
