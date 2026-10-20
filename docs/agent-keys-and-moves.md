# Safe agent creation and moving agents

Design note for v0.3 items K ([#11](https://github.com/ncecere/pi-durable-platform/issues/11)) and M
([#13](https://github.com/ncecere/pi-durable-platform/issues/13)). [DESIGN.md](../DESIGN.md) has the summary; this note
has the reasoning.

## Idempotency-Key on agent creation

`POST /v1/teams/{team}/agents` takes an optional `Idempotency-Key` header
([draft-ietf-httpapi-idempotency-key-header](https://datatracker.ietf.org/doc/draft-ietf-httpapi-idempotency-key-header/)):
1-255 printable ASCII characters, bare or as a structured-field string (`"…"`). Anything else is 400
`invalid_idempotency_key`.

**Scope: per caller, 24 hours.** A key is remembered in `idempotency_keys` under the caller and the operation
(`agent.create`):

| Caller | Scope | Why |
|---|---|---|
| A person with a browser session or a personal API key | `user:<id>` | Both are the person acting directly; a script retrying with a key after a session timed out is the same person. |
| An app the person connected (OAuth Connect) | `oauth:<grant id>` | Two apps that choose the same key must not see each other's results, nor collide with the person's own keys. A reconnected app gets a new grant and a new scope, which is harmless: a retry spans seconds, not reconnects. |
| A service account | `service_account:<id>` | Its own, across its keys (rotating a key mid-retry still works). |

Keys are not per team: the team is part of the request, so reusing a key on another team is a different request.

**What is compared.** A SHA-256 of the request as the platform understood it: the resolved team ID (a slug and an ID
are the same team) and the decoded body (name, description, template, templateId, modelId), so whitespace and field
order don't matter.

**Outcomes.**

- New key: the request runs. If it creates the agent, the key is completed with the agent's ID in the same
  transaction; if it fails (any error), the transaction rolls back and the key is not remembered, so the client can
  retry with the same key. Only successes are remembered.
- Same key, same request, within 24 hours: the first answer, `201` with the agent (as it is now) and
  `Idempotent-Replayed: true`. The caller must still be allowed to see the agent (re-checked); a deleted agent is 404
  `agent_not_found` rather than a new agent.
- Same key, different request: 422 `idempotency_key_reused`. The draft uses 422 for a key reused with a different
  payload, and 409 for a request still in progress; the platform never answers 409 for that (below).
- After 24 hours the key is treated as unused even before the cleanup loop deletes it (the claim takes over a row older
  than 24 hours); the cleanup loop deletes them.

**Concurrency: a unique constraint, not check-then-insert.** The key is claimed first in the creating transaction,
with `INSERT … ON CONFLICT (caller, operation, key) DO UPDATE … WHERE created_at < now() - 24h RETURNING`. A concurrent
retry's insert waits on the primary key until the first transaction ends: if the first committed, the retry finds the
completed row and replays it; if the first rolled back, the retry runs itself. So there is no "in progress" state to
report, two retries never both create an agent, and a crash between the insert and the commit leaves nothing behind.

## Agents with a key in one's own personal workspace

`PUT /v1/teams/{team}/agents/by-key/{key}` was service-account-only because an agent created there is
**service-managed**: it holds an app's users' conversations, so only the team's service accounts use it, and the team's
people (owners and admins included) see that it exists but never read it (Decision 12). A person creating one in a
shared team would make an agent they couldn't use, and the team's service could later find, by a key a person chose,
an agent that person set up.

None of that applies in a personal workspace: its owner is its only member, it has no service accounts, and every
conversation in it is the owner's. So:

- In a **personal workspace**, its owner may call it with any of their credentials: a browser session, a personal API
  key, or an app they connected. The agent belongs to the workspace and is **not** service-managed (a new column,
  `agents.service_managed`, says which agents are; before, "has an external key" meant it). The owner uses it, lists
  it and reads it like any other agent of the workspace. An app acting for another person, another person, or a
  service account gets 404 `team_not_found` (they aren't on the workspace).
- In a **shared team**, it stays editor-service-accounts-only (403 for people, by any credential). No sound rule was
  found for people there: a person-keyed agent in a shared team would either be service-managed (unusable by its
  creator) or an ordinary team agent findable by a key, which the ordinary `POST` with an `Idempotency-Key` already
  covers.

The workspace's agent limit counts keyed agents like any other. Names of keyed agents aren't unique (a key defaults the
name), as before.

## Moving an agent

`POST /v1/agents/{id}/move {"team": "<slug or ID>", "modelId"?}` with `If-Match`, and its dry run
`GET /v1/agents/{id}/move?team=…[&modelId=…]`, authorized identically.

**Who.** People only (a session, a personal API key, an app they connected); service accounts get 403
`person_required`. On each side an owner or admin of the team, as for deleting an agent there and for bringing
conversations into it. A personal workspace on either side: its owner only. Platform admins (never through an app,
which carries no platform role) stand in for an owner or admin of a shared team: of the target always, of the source
only when they are not on the target team. Otherwise a platform admin could move any team's agent into a team they
belong to and read its conversations, which only the reasoned, audited read allows. A target team the caller isn't on
reads as not found (404 `team_not_found`); an agent they can't see, 404 `agent_not_found`.

**What doesn't change.** The agent's ID, its schema and role in the agents database (its conversations), its template
version, and its threads' records (creators, visibility, participants). No storage moves: only `agents.team_id` (and
the fields below) change, in one transaction that locks the agent and both teams.

**What the target team must offer** (each missing piece is a *blocker*: the dry run lists it with its error code and
`allowed: false`; the move refuses with 409 and that code):

- `move_template_unavailable`: the template is another team's and not in the catalog. A personal workspace takes
  catalog templates only, which this rule gives (a workspace has no templates of its own). Archived templates are
  fine: a move is not a new deployment.
- `move_model_unavailable`: the agent's model (its own, or its template version's) isn't one the target team may use
  (a team's own model, a model published to the old team only, or not to personal workspaces). `modelId` switches the
  model as part of the move.
- `name_taken`: the target team has an agent with the name.
- `too_many_agents` / `limit_reached`: the target's limits.
- `agent_service_managed`: service-managed agents don't move. They hold an app's users' conversations, which no person
  of either team may read; moving one into a personal workspace would hand them to that person.

Refusals that aren't about the target's resources are errors, not blockers: `move_same_team` (409), `team_archived`
(409, either side), and the authorization errors above.

**What changes for conversations.** Shared threads become visible to everyone who may use the agent in the target team
(the dry run counts them, and the target's people and service accounts). Private threads stay their creator's; those
whose creator can't use the agent in the target team (a service account of the old team, a person who isn't a member)
are out of reach until the creator joins (counted). Platform admins can still read them with a reason.

**What stops applying** (decided per thing; all shown by the dry run):

| Thing | After the move | Why |
|---|---|---|
| The old team's service accounts and their keys | No longer reach the agent | Agents are used by their team's members and service accounts. |
| People of the old team who aren't in the new one | Lose the agent (404); open event streams end at their next access check (30 s) | Same rule. |
| The agent's external key (by-key binding) | Dropped | A key names an agent within one team (`(team_id, external_key)`); kept in a shared team it would make the agent service-managed and unusable by the people who just moved it. The dry run names the key. |
| MCP servers apps set on its threads (`thread_mcp_servers`) | Deleted | Their headers are credentials callers of the old team gave; the new team's people must not send messages that use them. Apps set them again. |
| The old team's own MCP servers, and platform servers published to the old team only | Stop applying (the runtime takes only servers the agent's team may use) | Same rule as for an unpublished server; the dry run names them. |
| The old team's own models | Blocker (above), or switch with `modelId` | An agent must have a model it may use. |
| Usage already recorded | Stays with the old team (`model_usage.team_id`) | It was the old team's cost; new usage is the new team's. |

The runtime is hibernated after the move, so it starts with the new team's models and servers on the next request.
The stop takes the agent's start lock after the commit, and every start reads the agent's spec under that lock, so a
request authorized before the move can't restart it with the old team's configuration. A write of a thread's MCP servers
locks the agent's row and re-checks its team in its transaction: it finishes before the move (which then removes its
servers) or waits for it and is refused (404).

**Audit.** `agent.move` (target `agent`) in both teams' logs, so the owners of each see it, with the agent before and
after (including the team), the teams' slugs, the thread counts, the dropped key and the number of thread MCP servers
removed.

**Console.** Settings › Danger zone › Move to another team, for the team's owners and admins: pick one of the teams
where they are an owner or admin (or their personal workspace), read the dry run's summary and blockers, confirm.
