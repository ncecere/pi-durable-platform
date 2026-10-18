# Pi Durable Platform — design

Status: **first release, v0.1.0** (2026-10-18; [release notes](docs/releases/v0.1.0.md)). Development software.
Decisions here were made with the product owner from 2026-10-17; items marked *proposed* are defaults to revisit and
are not built.

## Goal

An enterprise-style, self-hosted platform where teams deploy, test and manage
[Pi Durable](https://github.com/earendil-works/pi/tree/main/packages/durable) agents, each in its own isolated runtime
on Kubernetes. It is the **console and API**, not the end-user product: people use agents through the teams' own apps.

- A chat application (for example `pi-d-ui`) gives **every person who signs in to it their own assistant**: its
  service account asks the platform for one agent per user (`PUT /v1/teams/{team}/agents/by-key/{userId}`) and relays
  their chats through the platform API.
- A team deploys an agent from a template, tests it in the console, and reaches it from its own integration.

Later: teams build and upload their own agents (self-service). That requires stronger isolation first (below).

## Decisions

| # | Decision | Why |
|---|---|---|
| 1 | Go control plane in the style of Grounded / Open MCP Portal: OIDC + dev personas, fixed platform and team roles, Postgres (pgx, sqlc, goose), OpenAPI, embedded React UI. | Proven patterns, one binary, same RBAC model. |
| 2 | Pi Durable runs in a separate **Node runtime** (`runtime/`), one process per agent. | Pi Durable is TypeScript. |
| 3 | **Agent state in PostgreSQL**, not SQLite. Pi's own `SqliteStorage` runs unchanged on a small SQL facade over one dedicated pg connection (`runtime/src/storage`). | Stateless agent pods: an agent can wake on any node, no node-pinned volumes. |
| 4 | **One schema and one database role per agent**, in a database separate from platform metadata. | Database-enforced isolation between agents; easy delete. |
| 5 | **Single owner per agent** via a session advisory lock on the agent's only connection, never re-established. | A stale pod that loses its connection loses its ability to write at the same moment its lock is released. |
| 6 | **One runtime per active agent; idle agents hibernate** (no pod, no connection). Requests wake them. | Thousands of per-user agents cannot all hold pods. |
| 7 | Clients reach agents **only through the platform API**, which authenticates, authorizes and proxies. Runtimes require a per-agent bearer token. | One enforcement point; runtimes are never exposed. |
| 8 | Agents come from **templates** (instructions, a model and MCP tools; no code) that platform admins or team admins write, running the platform's own runtime. Self-service upload of agent code is later and gated on sandboxed runtimes (gVisor/Kata). | The runtime's code is trusted; uploaded code is not. |
| 9 | Files and artifacts go to **object storage** with per-agent prefixes; working filesystems are ephemeral unless a template needs one. *(proposed, not built)* | Object storage isolates by prefix; NFS is unsuitable for workspaces. |
| 10 | Model credentials are injected into runtimes by the platform (or kept by its model gateway, in proxy mode); browsers never see them. | |
| 11 | **Agents belong to teams** (a personal workspace is a team of kind `personal`). The console is for service owners and platform admins; end users use the teams' apps. | Ownership, data and cost follow the team. |
| 12 | **Service-managed agents** (created by a service account with its own key) hold the app's users' conversations: only the team's service accounts use them; owners and admins see and delete them, never read them. | End users' chats aren't readable by every team member. The only other access is a platform admin's reasoned, audited read (Threads). |
| 13 | **Templates are the governance unit**: platform-curated catalog templates and team templates, versioned, agents pinned to a version, team templates published to the catalog with platform-admin approval. Org-wide agents are per-person instances of a template, not one shared agent. | One agent shares one store: shared agents would mix teams' data. |

## Architecture

```text
Browser / chat app / service ──► Platform (Go)
                                   ├─ auth, teams, roles, service accounts, OAuth Connect, audit
                                   ├─ templates, agents (each belongs to a team; personal workspaces are teams)
                                   ├─ model and MCP catalogs; gateway: model proxy, MCP proxy, usage (AGENTS_GATEWAY_ADDR)
                                   ├─ provisioner: per-agent schema + role in the agents database
                                   ├─ launcher: start / stop agent runtimes (local processes | Kubernetes pods | Agent Sandbox)
                                   ├─ reaper: hibernate idle agents
                                   └─ proxy: /v1/agents/{id}/threads/... → the agent's runtime (SSE streaming)
                                              │ bearer token
                                              ▼
                                   Agent runtime (Node, Pi Durable) ── its schema in PostgreSQL
```

## Personal workspaces

Each person who signs in gets a **personal workspace**, while platform admins have them turned on (Admin › Settings):
a team of kind `personal` whose only member is its owner. It takes no other members, invites or service accounts, and
deploys catalog templates only (for now). Platform admins set defaults for every personal workspace (max agents, max
running at once, a monthly cost budget that is recorded but not enforced yet) and may override them for one
workspace. Models and MCP servers have a `personal` flag: whether personal workspaces may use them (shared teams follow
their visibility). Platform admins and auditors audit personal workspaces: they see the workspace, its agents, usage
and audit log, but not conversations (platform admins may open one with a stated reason, which the workspace's owner
sees: see Threads). Sign-in can be limited to IdP groups (`OIDC_ALLOWED_GROUPS`).

## OAuth Connect

People connect an app (a chat app, a desktop tool) with their own platform identity: the platform is a small OAuth 2.1
authorization server (`internal/oauth`, ported from Open MCP Portal) and the app gets the platform's own opaque
tokens, so it works whatever the identity provider issues (OIDC, Shibboleth's opaque tokens). The protected resource
is the platform itself: `APP_URL`.

**How an app integrates.**

1. Discover: `GET /.well-known/oauth-authorization-server` (RFC 8414; also in `GET /v1/auth/config` under `connect`)
   and `GET /.well-known/oauth-protected-resource` (RFC 9728). A refused app token gets
   `WWW-Authenticate: Bearer resource_metadata="<APP_URL>/.well-known/oauth-protected-resource", error="invalid_token"`.
2. Identify itself, as a public client (no secret), either with a **Client ID Metadata Document**: its `client_id` is
   an https URL with a path, serving JSON with that same `client_id`, `client_name`, `redirect_uris` (https, or http
   on a loopback host), optional `client_uri` and `logo_uri`. The platform fetches it from public addresses only
   (`internal/netguard`: loopback, private, link-local and metadata addresses are refused, the connection is pinned to
   the checked address, no redirects or proxies), at most 16 KiB, cached for its max-age (1 min to 1 h). Or with
   **Dynamic Client Registration**: `POST /oauth/register` (RFC 7591) returns a `pdpcl_…` client ID, deleted after 30
   days unused. Metadata documents are preferred: their host vouches for the name; a registered name is unverified
   and the consent page says so.
3. Send the person to `GET /oauth/authorize` with `response_type=code`, `client_id`, `redirect_uri`, `state`, PKCE
   (`code_challenge`, `code_challenge_method=S256`, required), optional `scope=agents` (the only scope; omitted means
   agents) and optional `resource=<APP_URL>` (anything else is `invalid_target`). Loopback redirect URIs match on any
   port (RFC 8252). The person signs in with the platform's normal sign-in (so `OIDC_ALLOWED_GROUPS` and suspension
   apply; their personal workspace exists from then on), and sees the consent page (`/connect/consent` in the web
   app). Errors before the client and redirect URI are known good (unknown client, unregistered redirect URI, a
   repeated parameter) are shown as a page and never redirected; later ones go back to the app. The redirect carries
   `code`, `state` and `iss` (RFC 9207).
4. Exchange the code at `POST /oauth/token` (form-encoded, `grant_type=authorization_code`, `client_id`, `code`,
   `redirect_uri`, `code_verifier`). Codes live 60 seconds and work once (they are deleted before anything is
   checked). The answer: a `pdpat_…` access token (1 hour), a `pdprt_…` refresh token (30 days) and `scope: agents`.
5. Refresh with `grant_type=refresh_token`: each use returns a new pair and retires the old refresh token. Presenting a
   retired refresh token again within 30 seconds (two refreshes at once) gets a pair; later, it is treated as stolen
   and the whole connection is revoked.
6. Revoke at `POST /oauth/revoke` (RFC 7009): a refresh token ends the connection, an access token only itself.

**What a token can reach.** It acts as the person, **without their platform role** (an app acting for a platform
admin gets no admin powers), on the routes that take bearer credentials: the agent routes (agents, threads, events,
thread MCP servers), template listings, a team's models, usage, limits and MCP servers, plus `GET /v1/me`,
`GET /v1/teams` and `GET /v1/teams/{team}`. Those last three refuse service account keys (403 `person_required`):
they are about a person. Everything else (administration, members, invites, keys, service accounts, connected apps,
consent) takes browser sessions only. Agents with an external key (`PUT …/agents/by-key/…`) stay service-account
only: an app acting as a person creates ordinary agents (for example in the person's personal workspace). Every
request re-checks the token, its connection and the person (suspended: refused) and is rate-limited as the person.
Tokens and codes are stored as HMAC-SHA256 digests (label `oauth:`) under `API_KEY_PEPPER`, and
`API_KEY_PEPPER_PREVIOUS` is accepted during a rotation; they are never logged.

**Consent and trust.** Allowing an app records a connection (a grant), remembered until revoked, so connecting again
skips the consent page. Platform admins can mark apps **trusted** (`PUT /v1/admin/settings/oauth-clients`,
If-Match; the list is empty by default): trusted apps skip the consent page, never the
sign-in. Only metadata-document apps can be trusted, since a registered app's ID changes and its name is anyone's
choice.

**Revoking.** People see and disconnect their apps (`GET`/`DELETE /v1/me/oauth-grants`); platform admins and auditors
see anyone's (`GET /v1/admin/users/{userId}/oauth-grants`) and platform admins disconnect them. Disconnecting ends
every token at once. The audit log records `oauth.consent`, `oauth.token_issue`, `oauth.revoke` (reason `user`,
`admin`, `client` or `refresh_reuse`), `oauth.client_register` and `oauth.trusted_clients_update`; what an app does
is recorded with actor kind `oauth`: the person, through the app.

## Models

Platform admins add **providers** (Admin › Models) and the **models** they offer, and publish each model to every team
or to chosen teams. Provider types: OpenAI (Responses API), Anthropic (Messages API), and servers compatible with
OpenAI chat completions (LiteLLM, vLLM, Ollama, OpenRouter …), the OpenAI Responses API or the Anthropic Messages API.
A provider's key and extra headers are sealed with `ENCRYPTION_KEY` and never returned. "Discover models" lists what a
provider offers.

A template version runs on a model (or the platform's default, `AGENTS_MODEL_*`); a team may choose another published
model for one agent. A disabled or unpublished model takes no new use, and agents on it do not start until moved.

Each provider has an access mode:

- **proxy** (default): agents call the platform's gateway (`AGENTS_GATEWAY_ADDR`, a listener of its own) with their own
  credential (`pdpm.<agent>.<runtime token>`), in the provider's API. The gateway checks the agent may use the model the
  request names and the endpoint (`/chat/completions`, `/responses` or `/v1/messages`), adds the key, streams the
  reply back and records the tokens it reports (`model_usage`). Agents need no network path but the platform's.
- **direct**: agents get the provider's URL and key and call it themselves (for gateways that issue scoped keys; the
  agents' network must allow the provider).

**Usage.** The proxy records every request it relays. For models it does not see (direct-mode providers and the
platform's default model), the runtime counts each finished response (`runtime/src/usage.ts`) and reports batches to
the gateway's `/usage` with its credential; reports for a proxied model are ignored, so nothing counts twice. Reports
(`GET /v1/teams/{team}/usage`, `GET /v1/admin/usage`) sum requests and tokens per day, model, agent or team, with a
cost estimate from the models' current prices. Service-managed agents are named only for the team's owners, admins and
service accounts.

### Team model providers

A team's **owners and admins** add providers and models of the team's own (`/v1/teams/{team}/model-providers`, the
same shapes as Admin › Models), in a browser session only: never with a service account's key, a personal API key or
an app's token. Its editors and members read them (keys are never returned); platform auditors read them, and platform
admins list every owner's providers (`GET /v1/admin/model-providers?owner=all|platform|<team>`, each with its `team`)
and may disable, change or delete a team's through the admin routes. Changes are audited under the same action names
as the platform's, in the team's audit log.

- **The team's only.** A team provider's models are available to its team alone, whatever their visibility (the API
  refuses `visibility: all`, `teams` or `personal: true` for them). They never reach another team: not through
  `GET /v1/teams/{team}/models`, a template, an agent's model or the model proxy.
- **Proxy only.** Direct mode would hand agents a URL their egress rules would have to allow, which a team can't grant.
- **Address guard.** The URL is the team's choice, so it must be https, and every request (discovery, the proxy) goes
  through `internal/teamnet` (on `internal/netguard`; shared with team MCP servers): public addresses; private ones
  only inside the networks platform admins list (`model_settings.private_networks`, the one list for every kind of
  team service: model providers and MCP servers); loopback, link-local and cloud metadata never. The host is checked when the URL
  is saved (422 `address_not_allowed`) and resolved and pinned again at every connection (DNS rebinding); no redirects,
  no proxy from the environment. With the development allowance (`DEV_AUTH` on a loopback `APP_URL`) any address and
  plain http on a loopback host are accepted. Platform providers keep an unguarded client (admins may point them at
  internal gateways).
- **Settings.** Platform admins set `GET/PUT /v1/admin/settings/models` (`teamsMayAddProviders`, default on;
  `teamsMayAddMcpServers`, see below; `privateNetworks`, for team providers and team MCP servers alike). Turned off, teams can't add providers (409 `team_providers_off`) but existing ones keep working
  and can be changed or deleted. Personal workspaces add their own only when the personal workspace settings'
  `ownProviders` is also on (default off; else 409 `personal_workspace`). `GET /v1/teams/{team}/limits` says whether
  the caller may add one now (`canAddProviders`).
- **Templates.** A team template may run on the team's own models; a platform template may not (409 `team_model`),
  and a team template whose current version runs on one can't be submitted to, approved for, or get such a version
  while in, the catalog (409 `team_model`).
- Deleting a team deletes its providers and models along with its agents and templates.

## MCP servers

Platform admins add remote MCP servers (Streamable HTTP; **Admin › MCP servers**) with optional headers (for example
`Authorization`, sealed with `ENCRYPTION_KEY`), and publish them to every team or chosen teams. The platform reads each
server's tool list (`internal/mcp/client.go`) and keeps it. A template version gives its agents servers, all their tools
or some, and can let apps give its agents their own servers for one thread (`clientMcp`).

- **Catalog servers** reach agents as `mcp__<slug>__<tool>` tools, defined from the stored tool list and connected on
  first use with pi-mcp. More than 20 MCP tools are deferred behind `tool_search`. Per server, **proxy** (default:
  the agent calls `/mcp-proxy/server/<id>` on the gateway, which adds the headers, refuses `tools/call` for tools the
  version left out and logs each call in `mcp_calls`) or **direct** (the runtime gets the URL and headers).
- **App servers**: a chat app sends the MCP servers its user enabled with a message (`mcpServers`) or with
  `PUT .../threads/{thread}/mcp-servers`. The platform keeps them per thread with headers sealed until they expire
  (default a day) and never forwards them in the message. Before each message the runtime fetches the thread's servers
  from the gateway (`/thread-mcp`), lists their tools and offers them only in that thread, as `mcpc__<slug>__<tool>`;
  at start it reinstalls every thread's, so resumed runs keep their tools. Hosts must match
  `AGENTS_CLIENT_MCP_HOSTS` (none by default), and `AGENTS_CLIENT_MCP_MODE` says whether agents reach them through the
  gateway (proxy, default) or directly.

### Team MCP servers

Mirrors team model providers. A team's **owners and admins** add MCP servers of the team's own
(`/v1/teams/{team}/mcp-servers/own`, `/own/{serverId}`, `/own/{serverId}/refresh`: the shapes of Admin › MCP servers),
in a browser session only; its editors and members read them (header values are never returned), platform auditors
read them, and platform admins list every owner's (`GET /v1/admin/mcp-servers?owner=all|platform|<team>`, each with
its `team`) and may disable, change, refresh or delete a team's through the admin routes. `GET
/v1/teams/{team}/mcp-servers` (sessions and keys) keeps meaning "the servers the team may use": now the team's own
(first, with `team`) and the platform's published to it. Changes are audited under the same action names, in the
team's audit log.

- **The team's only.** A team server is available to its team alone, whatever its visibility (the API refuses
  `visibility: all`, `teams` or `personal: true` with 400 `team_mcp_visibility`): listing, template versions,
  agents' `MCP_SERVERS` and the gateway's per-request check all use the same rule (`MCPServerAvailable`).
- **Slugs stay unique across every owner.** A slug names the server's tools in an agent's tool namespace
  (`mcp__<slug>__<tool>`), where a team's agents meet the platform's servers and the team's own; per-owner slugs could
  collide there. So a team can't take a slug the platform or another team uses (409 `mcp_server_name_taken`); names are
  unique per owner.
- **Proxy only, through the address guard.** Agents reach a team server only through `/mcp-proxy/server/<id>`; every
  request to it (reading its tools on create, URL change and refresh, the proxy) goes through `internal/teamnet` with
  `model_settings.private_networks`: https (http only on loopback with the development allowance), checked when the
  URL is saved (422 `address_not_allowed` / `address_unresolved`) and at every connection (refresh: 422; proxy: 502
  `address_not_allowed`). Its tools error never repeats the network error's text. Platform servers keep their
  unguarded client.
- **Settings.** `teamsMayAddMcpServers` in the model settings (default on; omitted on update keeps it): off, teams
  can't add servers (409 `team_mcp_servers_off`) but existing ones keep working and can be changed or deleted.
  Personal workspaces add their own only when the personal workspace settings' `ownMcpServers` is also on (default
  off; else 409 `personal_workspace`). `GET /v1/teams/{team}/limits` says `canAddMcpServers`.
- **Templates.** A team template may attach its team's servers (another team's: 409 `mcp_server_unavailable`); a
  platform template may not (409 `team_mcp_server`), and a team template whose current version attaches one can't be
  submitted to, approved for, or get such a version while in, the catalog (409 `team_mcp_server`).
- Calls through the proxy are logged in `mcp_calls` for the calling agent's team, under the server's name.
- Deleting a team deletes its servers along with its templates (`template_version_mcp` also cascades from its
  template, so the attachments go before the servers' `ON DELETE RESTRICT` check).

## Threads

A conversation with an agent is a thread, named by the client (`/v1/agents/{id}/threads/{threadId}`). The runtime
keeps the conversation; the platform keeps a record of each thread (`agent_threads`) so listing doesn't wake the
agent, and announces changes on `GET /v1/agents/events`.

- **Shared or private.** The first message chooses (`visibility`, default `shared`; it never changes: a different
  value later is 409 `visibility_fixed`). A shared thread is seen by everyone who may use the agent. A private one is
  listed and read only by the person or service account that started it: not other members, not the team's owners
  or admins, not another person's test chat in the console. On every thread route (read, send, stop, events, rename,
  delete, the thread's MCP servers), in listings and in event streams, another's private thread reads as not found
  (404 `thread_not_found`, the same as a missing one). A thread the runtime has but the platform has no record of
  (older threads) is shared. Personal workspaces' threads default to shared too: only the owner reaches the agent.
- **Recorded before relaying.** On a thread's first message the platform writes its record (creator, visibility)
  before relaying, so a private thread is never briefly shared; a message the runtime refuses (4xx), or that couldn't
  wake the agent (503), removes the record again unless the runtime has reported the thread since. Other failures
  keep it; the runtime's thread list at its next start reconciles it. That reconciliation ignores records younger than
  five minutes, since a record written while the agent starts may be missing from the list the runtime just took.
- **Authors.** The platform sets each message's author (`{kind: user | service_account, id, name, via?}`; `via` is
  the app's name for OAuth Connect tokens) and drops any author a client sends. The runtime places the message as a
  `pdp.user` entry whose data is the text as sent, its time and its author; the chat view shows them on user messages.
  On shared threads of shared teams' agents the model is told who is speaking: its copy starts with a line
  `[Message from <name>]` (names are kept to one line). Threads list `createdBy` and `participants` (everyone who has
  sent, with when they last did); names are display names (else email) and service account names.
- **Message metadata.** User and assistant messages carry `at` (Unix ms: sent, or the reply's start); assistant
  messages `model` ({provider, id}), `usage` (input, output, cache read and write, total tokens) and `stopReason`; tool
  calls `startedAt` (the reply's end) and `endedAt` (when the result arrived). Older entries show what they have.
- **Platform admins' reasoned reads.** `GET /v1/admin/agents/{id}/threads` lists every thread of any agent, private
  ones too, with visibility, creator and participants; the titles of private threads, of every thread of a
  personal workspace's agent and of every thread of a service-managed agent (an app's agent for one of its users)
  are withheld (titles are content). `POST .../threads/{thread}/read {reason}` (10-500
  characters) returns the conversation once, waking the agent like any read, and records `thread.admin_read` (target
  `agent_thread` `<agentId>/<threadId>`, with the reason, the thread's visibility and whether the workspace is personal)
  in the agent's team's audit log, where its owners see it, before returning it. Platform admins only, with a browser
  session; auditors can't. Otherwise platform roles still never reach conversations.

## Launchers

| `AGENTS_LAUNCHER` | Runtime per active agent | Hibernate | Wake |
| --- | --- | --- | --- |
| `local` | a child process of the platform (development only) | stop the process | start it |
| `kubernetes` | a pod `agent-<id>`, secrets in a Secret of the same name | delete the pod | create it |
| `agent-sandbox` | a [Sandbox](https://agent-sandbox.sigs.k8s.io) `agent-<id>` around the same pod | `operatingMode: Suspended` | `Running` |
| `agent-sandbox` + `AGENTS_SANDBOX_WARM_POOL` | a standby runtime claimed from a `SandboxWarmPool` (claim `agent-<id>`) | release the claim | claim + activate |

Warm mode: the pool's pods run the runtime image without `AGENT_ID`, with `RUNTIME_ACTIVATION_TOKEN`; they are Ready
and wait (`runtime/src/standby.ts`). Waking an agent creates its claim, and the platform `POST`s the agent's
configuration (schema, database URL, runtime token, model settings) to the claimed runtime's `/activate` with the
activation token. From then on the runtime is that agent and only accepts the agent's own token. Nothing about the
agent is stored in Kubernetes, and the pool refills after each claim, so a wake skips scheduling, image pull and Node
start-up. While the previous runtime is still releasing the agent, activation answers `409 owned_elsewhere` and the
platform retries. A runtime that restarts comes back in standby and is activated again on the next request.

The activation token is in a Secret in the agents namespace (`AGENTS_SANDBOX_ACTIVATION_SECRET`; the platform creates
it with a random token if missing, and the pool's pods read it), or given to the platform directly
(`AGENTS_SANDBOX_ACTIVATION_TOKEN`). It reaches only the platform and the pool's pods; agent pods take traffic from the
platform alone, so one agent cannot activate another runtime. To rotate it, delete the Secret and the pool's unclaimed
sandboxes.

## Verified so far

- `runtime/test/conformance.test.ts`: pi-durable 1.3.0's own storage conformance suite (24 cases) passes on PostgreSQL.
- Ownership: a second opener is refused; an owner whose backend is terminated can no longer read or write, and the
  next owner opens (`ownership.test.ts`).
- Isolation: an agent role cannot read another agent's schema (`ownership.test.ts`).
- A real Harness keeps its conversation and deduplicates retried requests across a process restart
  (`harness.test.ts`); a manual `kill -9` mid-reply resumed and completed the reply.

## Known limits and open items

The [v0.1.0 release notes](docs/releases/v0.1.0.md#known-limitations) list the product limits (one replica, no budget
enforcement, text-only messages, no paging, …). The design-level ones:

- Database roles are server-wide: agent roles outlive a dropped agents database (tests remove theirs);
  `prune-agent-storage` only considers schemas in its own database.
- **Advisory locks are database-wide**: any role in the agents database can take another agent's lock key and block
  it from starting. Acceptable for trusted templates; uploaded agents must not get direct database credentials (a
  storage proxy instead).
- Schema-per-agent means ~25 relations per agent. Fine for thousands; for much larger counts, a native storage with an
  `agent_id` column and row-level security may be needed.
- Commit latency is a network round trip; Pi's `settings.progress` intervals can be raised if needed.
- pi-durable is experimental and pinned to 1.3.0; the SQL facade rejects SQLite constructs it does not translate, and
  the conformance suite guards every upgrade.
- Scheduled work while an agent hibernates needs the platform to know the next wake time (not built yet).
- Hostile-code isolation (gVisor/Kata on Talos) is required before self-service uploads.
- Thread IDs are chosen by clients. Anyone who may use an agent can set the MCP servers of a thread that doesn't exist
  yet (`PUT .../mcp-servers`), which a later private thread with that ID would inherit; clients should use random
  thread IDs.
