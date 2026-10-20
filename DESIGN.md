# Pi Durable Platform — design

Status: **released, v0.1.2** (2026-10-19; first release [v0.1.0](docs/releases/v0.1.0.md), fixes in [v0.1.1](docs/releases/v0.1.1.md) and [v0.1.2](docs/releases/v0.1.2.md)). Development software.
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
| 3 | **Agent state in PostgreSQL**, not SQLite. Pi's own `SqliteStorage` runs unchanged on a small SQL facade over one dedicated pg connection (`runtime/src/lib/storage`). | Stateless agent pods: an agent can wake on any node, no node-pinned volumes. |
| 4 | **One schema and one database role per agent**, in a database separate from platform metadata. | Database-enforced isolation between agents; easy delete. |
| 5 | **Single owner per agent** through an ownership record in the agent's own schema, naming the database session that owns it, on the agent's only connection, never re-established. See [Agent storage and ownership](#agent-storage-and-ownership). | A stale pod that loses its connection loses its ability to write at the same moment its ownership ends. No other agent's role can write, lock or take over the record. |
| 6 | **One runtime per active agent; idle agents hibernate** (no pod, no connection). Requests wake them. | Thousands of per-user agents cannot all hold pods. |
| 7 | Clients reach agents **only through the platform API**, which authenticates, authorizes and proxies. Runtimes require a per-agent bearer token. | One enforcement point; runtimes are never exposed. |
| 8 | Agents come from **templates** (instructions, a model and MCP tools; no code) that platform admins or team admins write, running the platform's own runtime. Self-service upload of agent code is later and gated on sandboxed runtimes (gVisor/Kata). | The runtime's code is trusted; uploaded code is not. |
| 9 | Files and artifacts go to **object storage** with per-agent prefixes; working filesystems are ephemeral unless a template needs one. *(proposed, not built)* | Object storage isolates by prefix; NFS is unsuitable for workspaces. |
| 10 | Model credentials are injected into runtimes by the platform (or kept by its model gateway, in proxy mode); browsers never see them. | |
| 11 | **Agents belong to teams** (a personal workspace is a team of kind `personal`). The console is for service owners and platform admins; end users use the teams' apps. | Ownership, data and cost follow the team. |
| 12 | **Service-managed agents** (created by a service account with its own key, in a shared team) hold the app's users' conversations: only the team's service accounts use them; owners and admins see and delete them, never read them, and they don't move to another team. An agent a person gives a key in their own personal workspace is theirs, not service-managed (v0.3). | End users' chats aren't readable by every team member. The only other access is a platform admin's reasoned, audited read (Threads). |
| 13 | **Templates are the governance unit**: platform-curated catalog templates and team templates, versioned, agents pinned to a version, team templates published to the catalog with platform-admin approval. Org-wide agents are per-person instances of a template, not one shared agent. | One agent shares one store: shared agents would mix teams' data. |
| 14 | **Platform replicas keep nothing another replica could contradict**: shared state is in PostgreSQL, an agent's activity is the runtime's own report (`/status`: work, open streams, last activity), starting an agent is serialised by a database lock, and one replica at a time reaps. See [Several replicas](#several-replicas) and [docs/replicas.md](docs/replicas.md). | Any number of replicas behind one Service, with no leader, sticky sessions or shared disk; a replica that has not seen an agent cannot hibernate it. |
| 15 | **App-level access is a platform admin's decision per app**: a trusted app (metadata document, on the trusted list) gets a secret from a platform admin for one event stream and usage covering everyone who connected it, naming them by an app-stable pseudonymous subject. Not `private_key_jwt`. See [OAuth Connect](#oauth-connect). | It shows an app what every person who connected it does, more than any one person's token; a secret issued where trust is already decided fits the platform's other credentials, with no key fetching or assertion replay cache. |
| 16 | **MCP servers learn who a call is for from a token the gateway signs from its own records** (v0.4): in proxy mode every forwarded request carries a short-lived ES256 JWT (`MCP-Gateway-Identity`, Open MCP Portal's header and claim style) naming the agent, its team, the thread's visibility and, for a personal workspace's agent or a private thread a person started, that person by OIDC issuer and subject. The runtime only names the thread. See [MCP servers](#mcp-servers) and [docs/mcp-identity.md](docs/mcp-identity.md). | Servers that act for a person (mail, memory, the portal's delegated credentials) need to know which person, and the agent's code, which runs model-influenced tools, must not be able to claim one. |
| 17 | **Triggers run agents as no person** (v0.4): schedules and signed webhooks are platform records per agent; each fire's ID is its message's pi-durable `requestId` and its run's unique key; the trigger is the message's author, the team's credentials are the only ones used; owners and admins manage them in a browser session, under per-agent limits. See [Triggers](#triggers) and [docs/triggers.md](docs/triggers.md). | The control plane is always running and agents hibernate; idempotent fires make replicas and retries safe; an unattended run must never borrow a person's identity or tools. |
| 18 | **Background work keeps an agent awake, up to a ceiling; waiting for a person doesn't** (v0.4): the runtime reports live background work from pi-durable's task graph at `/status` (background tasks, background subagents' runs, tool calls still running after their reply), the reaper treats it as busy, and work older than `AGENTS_BACKGROUND_MAX` (2 h) is aborted through the runtime, audited, before the agent hibernates. A task of a kind that waits for a person's answer, and work only waiting on it, counts as idle. See [docs/hibernation.md](docs/hibernation.md). | An agent must not be hibernated in the middle of work nobody is watching, a stuck task must not hold a runtime forever, and an approval can take a day without costing a pod. |

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
sees: see Threads). Sign-in can be limited to IdP groups (`OIDC_ALLOWED_GROUPS`), which the platform keeps
re-checking while people are signed in (Sign-in and group membership).

## Sign-in and group membership

People sign in with OIDC (any provider; PKCE, state and nonce; the ID token's claims fill the profile) or, for
development only, with fixed personas (`DEV_AUTH`, loopback `APP_URL`). `OIDC_ALLOWED_EMAIL_DOMAINS` and
`OIDC_ALLOWED_GROUPS` (against the `OIDC_GROUPS_CLAIM` claim of the ID token) decide who may sign in; the bootstrap
admin (`BOOTSTRAP_ADMIN_SUBJECT`) is exempt from the group rule so the platform can't lock out its first admin. A
session lasts `SESSION_TTL`.

**Re-checking groups (v0.2).** The group rule used to apply at sign-in only: removing someone from the IdP group left
their session (up to `SESSION_TTL`), personal API keys and connected apps working. The platform now asks the IdP
again, with no credentials of its own to the IdP and no SCIM:

- *Mechanism.* At sign-in the platform keeps the **refresh token** the IdP issued (`user_idp_tokens`, sealed with
  `ENCRYPTION_KEY`; only with `OIDC_ALLOWED_GROUPS`, a re-check interval and an IdP that issues one: scope
  `offline_access` in `OIDC_SCOPES`, and the provider must allow it for this client; with authentik 2024.2 and later
  the provider needs the `offline_access` scope mapping). Every `OIDC_GROUP_RECHECK_INTERVAL` (default 15m; 0 turns
  it off) the platform exchanges it at the token endpoint for fresh claims, takes the groups from the new ID token
  (else from the userinfo endpoint with the new access token) and applies the same rule, `OIDC_ALLOWED_GROUPS`
  against `OIDC_GROUPS_CLAIM` (every group the claim lists is matched; only the first 200, sorted, are stored and
  shown). The refresh token the IdP rotates replaces the kept one. A background loop (every
  replica runs it; people are claimed with `FOR UPDATE SKIP LOCKED`) checks people who are *recently active* (a
  sign-in, a live session, or an API key or connected app used in the last 30 days), so a removal takes effect
  within the interval plus up to a minute. Not chosen: userinfo with the sign-in's access token (it expires in
  minutes, so it needs the refresh token anyway); checking on every request or at session first use (puts the IdP
  on the request path and makes its outage everyone's outage; the loop bounds the window instead).
- *Rotated tokens and shutdown.* The IdP may rotate the refresh token with every exchange, so the new one must not
  be lost: a check that has begun is finished and recorded under its own 10 s commit even while the platform stops
  (shutdown waits up to 15 s for it; a check not yet begun is skipped), and the rotated token is kept whatever else
  the answer says, even when the IdP couldn't vouch for the person. Losing it would make the next check
  `invalid_grant` and deny a legitimate person.
- *Outcome by answer.* The IdP lists an allowed group: access continues (the person's stored groups are updated).
  It lists none: **denied**, `not_in_group`. It refuses the refresh token (`invalid_grant`, `access_denied` and the
  like: the person was disabled, the grant revoked or expired): denied, `idp_refused`. It can't be asked
  (unreachable, 5xx, an unexpected answer, neither the ID token nor userinfo has the groups claim, or the stored
  token can't be opened because `ENCRYPTION_KEY` changed without `ENCRYPTION_KEY_PREVIOUS`; the warning names the
  person by ID): nothing changes and the platform logs a warning, until `OIDC_GROUP_RECHECK_GRACE` (default 24h) has passed since the IdP
  last vouched for the person: then denied, `idp_unreachable`. So an IdP outage costs nobody access for a day, and
  a person the IdP no longer answers for loses it after one.
- *Denied is a separate state.* `users.group_denied_at`/`group_denied_reason`, apart from `users.status`, which only
  admins change (`suspended`): an admin's suspension and the group rule never overwrite each other. A person
  counts as inactive while either says so, in every check that a person may use the platform
  (`store.IsUserActive`: sessions, personal API keys, app access tokens, refresh and authorization, the OAuth
  consent page, the last-admin guard). Denying also deletes their sessions, revokes their connected apps (grants
  and tokens; reason `access_denied`) and forgets the kept refresh token; their API keys are not revoked (they stop
  working while denied), and their data, team memberships and agents stay. An event stream already open ends at its
  next access check (every 30 s, see Threads), like one of a suspended person; every new request is refused.
- *Coming back.* Signing in applies the rule afresh; when it passes, the denial is cleared (`user.group_restored`)
  and API keys work again (apps must be connected again). A person who is still outside the group can't sign in
  (403 `group_denied`) and nothing changes. Removing `OIDC_ALLOWED_GROUPS` lifts every denial at the next start.
- *Not covered.* The bootstrap admin and development personas are never re-checked (no refresh token is kept for
  them). People who signed in before the upgrade, or when the IdP issued no refresh token, have nothing to check
  with: their groups apply at their next sign-in. That is fail-open on purpose (denying everyone without a token
  would lock everybody out wherever the IdP isn't set up for refresh tokens), so it is made loud instead: the
  platform warns at start when `OIDC_SCOPES` lacks `offline_access`; each such sign-in logs at Info (the user id,
  never a token or an email) and counts `pdp_group_recheck_total{result="no_token"}`; the gauge
  `pdp_group_recheck_people{state="with_token|without_token"}` counts the recently active people the re-check can
  and can't check; and `pi-durable-platform doctor` warns, with the cause and the fix, while recently active people
  have no stored token (the same *recently active* test as the loop). Refresh tokens that an IdP lets expire for good (an absolute lifetime) end in
  `idp_refused`: the person signs in again.
- *Audit.* `user.group_denied` (system; target the person; the reason in words, its code and how many connected
  apps were revoked, each also `oauth.revoke` with reason `access_denied`) and `user.group_restored` (the person
  signing in, or the system when the rule was removed). Admin › People shows "Group access ended" with the reason
  (`accessDeniedAt` and `accessDeniedReason` on the user).

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
   and the consent page says so. An app's `logo_uri` (https) is not loaded by the browser, whose Content-Security-Policy
   allows images only from the platform: `GET /v1/oauth/client-logo?client_id=…` (browser sessions only; any
   signed-in person may ask for the logo of a client the platform knows: registered, or with a cached metadata document or a connection; a logo is public
   by nature) fetches the
   client's own logo, never an address from the request, through `internal/netguard` (public addresses, https, no
   redirects, 5 s, 256 KiB), serves only PNG, JPEG, GIF and WebP whose bytes check out (never SVG), from a one-hour
   cache, with `nosniff` and a Content-Security-Policy that allows nothing. Without a usable logo the pages show the
   app's initials.
3. Send the person to `GET /oauth/authorize` with `response_type=code`, `client_id`, `redirect_uri`, `state`, PKCE
   (`code_challenge`, `code_challenge_method=S256`, required), optional `scope` (`agents`, the default, or
   `agents:read`: see below), optional `resource=<APP_URL>` (anything else is `invalid_target`), and optional
   `prompt` and `login_hint` (see *Silent reconnects and the expected account*). Loopback redirect URIs match on any
   port (RFC 8252). The person signs in with the platform's normal sign-in (so `OIDC_ALLOWED_GROUPS` and suspension
   apply, and a person the group re-check denied can't connect an app; their personal workspace exists from then on), and sees the consent page (`/connect/consent` in the web
   app). Errors before the client and redirect URI are known good (unknown client, unregistered redirect URI, a
   repeated parameter) are shown as a page and never redirected; later ones go back to the app. The redirect carries
   `code`, `state` and `iss` (RFC 9207).
4. Exchange the code at `POST /oauth/token` (form-encoded, `grant_type=authorization_code`, `client_id`, `code`,
   `redirect_uri`, `code_verifier`). Codes live 60 seconds and work once (they are deleted before anything is
   checked). The answer: a `pdpat_…` access token (1 hour), a `pdprt_…` refresh token (30 days), `scope` (what the
   request asked for, at most what the person allowed) and `subject`: the person's app-stable pseudonymous ID for this
   app (also in `GET /v1/me` as `connectedApp.subject`), the ID the app-level endpoints name them by.
5. Refresh with `grant_type=refresh_token` (optionally a narrower `scope`, never a wider one: `invalid_scope`): each
   use returns a new pair and retires the old refresh token. Presenting a
   retired refresh token again within 30 seconds (two refreshes at once) gets a pair; later, it is treated as stolen
   and the whole connection is revoked.
6. Revoke at `POST /oauth/revoke` (RFC 7009): a refresh token ends the connection, an access token only itself.

**What a token can reach.** It acts as the person, **without their platform role** (an app acting for a platform
admin gets no admin powers), on the routes that take bearer credentials: the agent routes (agents, threads, events,
thread MCP servers), template listings, a team's models, usage, limits and MCP servers, plus `GET /v1/me`,
`GET /v1/teams` and `GET /v1/teams/{team}`. Those last three refuse service account keys (403 `person_required`):
they are about a person. Everything else (administration, members, invites, keys, service accounts, connected apps,
consent) takes browser sessions only. Agents with an external key (`PUT …/agents/by-key/…`) are service-account
only in shared teams; an app acting as a person may ensure them in that person's own personal workspace (one agent
per purpose; see Creating and moving agents), and creates ordinary agents elsewhere, with an `Idempotency-Key` so a
retry doesn't make two. Every
request re-checks the token, its connection and the person (suspended, or denied by the group re-check: refused) and is rate-limited as the person.
Tokens and codes are stored as HMAC-SHA256 digests (label `oauth:`) under `API_KEY_PEPPER`, and
`API_KEY_PEPPER_PREVIOUS` is accepted during a rotation; they are never logged.

**Scopes (v0.3).** `agents` acts as the person as above. `agents:read` is the same routes, reads only: list agents and
threads, read conversations, follow event streams (the person's own and the app-level one), read usage, models and
limits; no messages, no thread creation or renaming, no agent changes. It is enforced at one point, the bearer
middleware: an `agents:read` token gets 403 `insufficient_scope` (with `WWW-Authenticate: Bearer
error="insufficient_scope", scope="agents"`) for every method but GET and HEAD, which is sound because every route
that takes bearer credentials reads with GET and changes with another method (and reading a conversation may wake the
agent, which is a read). `agents` includes `agents:read`. A grant records what the person allowed; codes and tokens
record what they carry (at most the grant's; rows from before v0.3 carry the grant's). An app that asks for `agents`
on an `agents:read` connection goes through the consent page again (with `prompt=none`: `consent_required`), and
allowing widens the grant (audited again as `oauth.consent`, with the scopes); asking for `agents:read` on an
`agents` connection is silent and gets read-only tokens. The consent page says "wants to read your agents" and "never
sends messages or changes anything"; Connected apps marks such a connection Read only.

**Silent reconnects and the expected account (v0.3).** `prompt=none` never shows a page; what would need one goes
back to the redirect URI (with `state` and `iss`): not signed in, `login_required`; signed in but unable to use the
platform (suspended, denied by the group re-check: a page otherwise), `interaction_required`; an app the person never
connected (and not trusted), `consent_required` whatever `login_hint` says (the hint is compared only for apps the
person connected or trusted ones, so no app can probe which email a browser is signed in as); signed in as someone
other than `login_hint`, `login_required` (the hinted person must sign in); no grant that covers the scope and the
app isn't trusted, `consent_required`. With a grant (or a trusted app) the code comes back at once, as
without `prompt`, and no new consent is recorded. `prompt=consent` shows the consent page even then; `login`,
`select_account` and combinations with `none` are `invalid_request` (the platform can't make the IdP re-authenticate
or choose an account). `login_hint` (at most 320 characters on one line, else `invalid_request`) goes to the identity
provider as `login_hint` when the person has to sign in (`/auth/login?login_hint=`; the development sign-in ignores
it), and is checked against a signed-in person: it matches their email (case-insensitively) or their IdP subject.
A mismatch never connects the signed-in account, even for a trusted or remembered app: without `prompt=none` the
browser goes to the consent page, which says whom the app expected and offers **Sign in as <hint>** (it signs out
and starts the same authorization request again, so the platform sends the browser to the IdP with the hint) or
**Cancel** (`access_denied`); Allow is refused (409 `account_mismatch`). Offering the switch rather than an error
keeps the person in one flow; an app that prefers an error uses `prompt=none` first. Apps should still check
`subject` (or `GET /v1/me`) against the account they expected.

**App-level access for trusted apps (v0.3).** A chat app with hundreds of people should not hold one event stream per
person. A platform admin issues a secret (`pdpcs_…`, 256 bits, shown once; Admin › Settings › App secrets,
`POST /v1/admin/settings/oauth-clients/secrets`) to an app on the trusted list; at most two per app, to rotate. It is
stored as a peppered digest like tokens, works only while the app is on the trusted list (read on every use and every
30 s on open streams; taking the app off the list deletes its secrets in the same transaction, audited, so trusting it
again needs new ones), and is used as `Authorization: Bearer pdpcs_…` on two endpoints only, which take nothing
else (not sessions, not people's tokens):

- *Why a secret, not `private_key_jwt`.* Seeing every connected person's events and usage is more than any one
  person's token gives, so it should be a platform admin's decision per app, and that decision already exists (the
  trusted list, metadata-document apps only). A secret issued there needs no fetching of the app's `jwks_uri`, JWT
  and key-type handling, clock-skew rules or a cross-replica `jti` replay cache, and behaves like the platform's other
  credentials (shown once, digest at rest, deleted at once, audited). `private_key_jwt` would let any app that
  publishes keys use app-level access without an admin's say; it can be added later for trusted apps if operators
  want no shared secret. The code flow stays public (PKCE); the secret is not used, or accepted, at `/oauth/token`.
- *`GET /v1/app/events`* is `GET /v1/agents/events` for everyone who connected the app: each thread change carries
  `subjects`, the app-stable IDs of the people who may see it (`AppThreadEvent`). For every change the platform reads
  the app's people again: an active grant of the app with a refresh token that hasn't expired (so a revoked or
  expired connection stops that person's events at the next change, on every replica), an active person (not
  suspended or denied), and a member of the agent's team; then the person stream's rules apply (no platform role, so
  no staff reads; never service-managed agents; another's private thread and reserved threads never; a deleted
  private thread only to its creator). The agent is cached per stream for the 30 s re-check, which also ends the
  stream when its secret was deleted or the app is no longer trusted. A slow reader misses events, as on the person
  stream.
- *Subjects* (`oauth_client_subjects`) are random UUIDs per app and person, made at consent: not the email or the
  platform user ID, the same for one app across disconnecting and connecting again, different for each app. The
  stream's threads (`AppThread`) name people only this way: a thread's creator and participants who have an active
  connection to the app appear as their subjects; anyone else (people who didn't connect the app or whose connection
  ended, service accounts) appears with only their kind, so the app sees that someone else took part, and when, but
  never a platform user ID, email or name.
- *Usage per connected app.* Model and MCP requests are made by agents, not apps, so the platform records the latest
  sender of each thread and the grant behind it (`agent_run_senders`, written before a message is relayed), and the
  gateway attributes an agent's request to a grant when that is unambiguous: the latest senders of its threads that
  are replying, were sent to in the last minute or finished a reply in the last minute are all one grant (a tool call
  on a thread's own MCP server looks at its thread only). Otherwise none (`model_usage.oauth_grant_id`,
  `mcp_calls.oauth_grant_id` stay NULL). This never puts one person's work on another app, but misses work of an
  agent answering two apps (or an app and the console) at once, and on a shared thread a reply counts for whoever
  sent last; the exact answer needs the runtime to tag each model request with its thread (not built). Existing
  reports are unchanged. `GET /v1/app/usage` returns the app's people's usage by subject and UTC day (model requests,
  tokens, a cost estimate at current prices, tool calls; at most 92 days, 1-1000 rows a page, keyset cursor), people
  who disconnected since included. Platform admins filter Admin › Usage by app (`GET /v1/admin/usage?clientId=`) and
  list the apps people connected (`GET /v1/admin/oauth-clients`).

**Consent and trust.** Allowing an app records a connection (a grant), remembered until revoked, so connecting again
skips the consent page. Platform admins can mark apps **trusted** (`PUT /v1/admin/settings/oauth-clients`,
If-Match; the list is empty by default): trusted apps skip the consent page, never the
sign-in (nor the account check of `login_hint`). Only metadata-document apps can be trusted, since a registered app's ID changes and its name is anyone's
choice.

**Revoking.** People see and disconnect their apps (`GET`/`DELETE /v1/me/oauth-grants`); platform admins and auditors
see anyone's (`GET /v1/admin/users/{userId}/oauth-grants`) and platform admins disconnect them. Disconnecting ends
every token at once. The audit log records `oauth.consent`, `oauth.token_issue`, `oauth.revoke` (reason `user`,
`admin`, `client` or `refresh_reuse`), `oauth.client_register` and `oauth.trusted_clients_update`; what an app does
is recorded with actor kind `oauth`: the person, through the app. App secrets are audited as
`oauth.client_secret_create` and `oauth.client_secret_delete` (target `oauth_client_secret`: the app and the secret's
prefix, never the secret); the app-level endpoints only read and record nothing but the secret's last use.

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
platform's default model), the runtime counts each finished response (`runtime/src/lib/usage.ts`) and reports batches to
the gateway's `/usage` with its credential; reports for a proxied model are ignored, so nothing counts twice. Reports
(`GET /v1/teams/{team}/usage`, `GET /v1/admin/usage`) sum requests and tokens per day, model, agent or team, with a
cost estimate from the models' current prices. Service-managed agents are named only for the team's owners, admins and
service accounts. A team's report (a personal workspace's too) also groups by agent and day (`groupBy=agent,day`: one
row per agent per day, by agent name, then ID, then day) and takes a repeatable `agentId` (at most 100), so an app shows
several agents' daily usage in one call. The filter counts the team's own usage only: an ID of another team's agent, of
a deleted or unknown agent, or of a service-managed agent for a caller who sees them summed matches nothing (no error, so
the report reveals nothing about agents the caller may not see). Usage is recorded per team and agent, not per person:
there is no report of one person's usage across their teams.

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
  first use with pi-mcp. More than 20 MCP tools are deferred behind `tool_search`. Tools an MCP App keeps for its own
  interface (`_meta.ui.visibility` without `"model"`, the MCP Apps extension) are never offered to the model; the
  platform shows no app interfaces yet, so an app's tools work as plain tools. Stored tools carry `modelVisible`; a
  list stored before the platform read that field (any server, the platform's or a team's, enabled or not) is read
  again once at start, in the background, through the normal refresh (a team's server through the address guard,
  with the usual time limit); one that can't be read keeps its list, records the error and is tried at the next
  start. Per server, **proxy** (default: the
  agent calls `/mcp-proxy/server/<id>` on the gateway, which adds the headers, refuses `tools/call` for tools the
  version left out and logs each call in `mcp_calls`) or **direct** (the runtime gets the URL and headers).
- **Who a call is for** (v0.4, [docs/mcp-identity.md](docs/mcp-identity.md)). In proxy mode the gateway adds a
  short-lived identity token to every request it forwards to a catalog or team server: an ES256 JWT in
  `MCP-Gateway-Identity` (never in `Authorization`, which keeps the server's stored header), with `iss` the platform's
  `APP_URL`, `aud` the server's URL (or its `identityAudience`), five minutes of life and a `jti`; `sub` is the agent
  (`principal_type: agent`), with its name, its team (`id`, `slug`, `kind`), `thread_visibility` (`private`, `shared`,
  `none`), `person_present`, the tool called, and `person` (OIDC `iss` and `sub`, `email` only with `identityEmail`)
  when the platform's records name one. The runtime only says which thread a tool call belongs to (`PDP-Thread`, on
  its requests to the proxy, never forwarded); the gateway looks it up among the calling agent's threads: a personal
  workspace's agent names the owner, a private thread a person started names its creator (still active and on the
  team), anything else (a shared thread, a thread of another agent or none the platform knows, no thread, a service
  account's private thread) names nobody. Servers verify tokens with `GET /.well-known/mcp-identity-jwks.json`
  (unauthenticated, `max-age=300`). The P-256 signing key is generated by the platform, sealed with `ENCRYPTION_KEY`
  (`mcp_identity_keys`, re-sealed by `rekey`) and rotated with `pi-durable-platform rotate-mcp-identity-key` (published
  a day before it signs, the old key published for an hour after; `--now --drop-previous` for a leaked key); `doctor`
  warns when there is none or it is older than 90 days. Per server, `sendIdentity` (default on), `identityAudience` and
  `identityEmail` (default off), in the API and on the server's Settings. Direct mode and app servers get no token (the
  gateway doesn't see direct calls; an app chose its server and knows its user), and the platform's own tool-list reads
  carry none.
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
- Calls through the proxy are logged in `mcp_calls` for the calling agent's team, under the server's name, and carry
  the identity token like a platform server's (the team sets `sendIdentity`, `identityAudience` and `identityEmail`).
- Deleting a team deletes its servers along with its templates (`template_version_mcp` also cascades from its
  template, so the attachments go before the servers' `ON DELETE RESTRICT` check).

## Threads

A conversation with an agent is a thread (`/v1/agents/{id}/threads/{threadId}`). The runtime keeps the conversation;
the platform keeps a record of each thread (`agent_threads`) so listing doesn't wake the agent, and announces changes
on `GET /v1/agents/events`.

- **Thread IDs.** `POST /v1/agents/{id}/threads` starts a thread under an ID the platform chooses (`t_` and 128
  random bits, base64url) and returns it; anyone who may use the (turned-on) agent may, as for sending a message. The
  body is optional: `visibility` and `title`. Clients may keep choosing IDs (`[A-Za-z0-9_-]{1,64}`), and the
  first message under an unused ID starts the thread as before, but only a platform-chosen ID can't be guessed: whoever
  uses an ID first starts that thread, and anyone allowed on the agent who learns a shared thread's ID may join it.
  This supersedes the v0.1.0 limitation "Thread IDs are chosen by clients" (the
  [release notes](docs/releases/v0.1.0.md) keep describing v0.1.0).
- **A thread has a creator before it has servers.** A thread starts (its record is written, the caller is its
  creator) with its first message, with `POST …/threads`, or when someone sets its MCP servers: `PUT …/mcp-servers`
  on an ID with no record, and a message's `mcpServers`, start the thread under that ID like its first message
  would (`visibility` is part of the PUT body, default `shared`). Servers are never kept for a thread without a
  record: `thread_mcp_servers` references `agent_threads` (deleting a thread deletes its servers), and the migration
  dropped the servers of unrecorded threads (the v0.1.x pre-seeding hole: A sets servers on an ID, B's later private
  thread with that ID inherited them). Now whoever uses the ID later gets the normal rules of the thread they find:
  shared (they may join it, and see that it is someone's and which servers it has: names and URLs, never headers) or
  private (404 `thread_not_found` on every route, 409 `visibility_fixed` for a conflicting `visibility`). Reading
  and changing a thread's servers takes the same access as reading the thread: the creator for a private one, anyone
  who may use the agent for a shared one. An empty list on an unknown ID starts nothing; bad servers (host, template
  without `clientMcp`) start nothing.
  *Reserved threads and other people's servers.* A thread whose servers were set but nobody has sent to yet is
  reserved, hidden from everyone but its creator, so the servers on it are the creator's choice and nobody else knows
  they are there. Two rules keep them from reaching someone else's turn. The first message from anyone other than the
  creator (a client-chosen ID the creator guessed or learned) is accepted, but drops the creator's servers and ends
  the reservation, in one transaction that holds the thread's row lock, so a concurrent write of servers is deleted
  or lands on a thread that is no longer hidden; the sender's own `mcpServers` in that message apply. (Not chosen: a
  409 for that message, which would break the documented use of a shared reserved thread by its ID.) And nobody but
  the creator may set or clear servers on a reserved thread (404 `thread_not_found`): otherwise they would be active
  for the creator's first message without the creator knowing. Once a thread has its first message it is an ordinary
  shared thread, whose servers anyone who may use the agent can see and set.
  *Why claim rather than refuse.* Refusing servers for unrecorded threads (404) would break apps that set servers
  before the first message (the documented flow) and would need a second, identical step; claiming keeps that flow
  working and still binds the servers to a known creator. Platform-chosen IDs make the attack impossible rather
  than merely handled, and apps that want private threads with servers set up front can use them.
- **Reserved threads.** A thread started without a message (by `POST …/threads` or by its servers being set) is
  *reserved*: listed and announced to its creator only (anyone allowed on the agent may still use a shared one by its
  ID), titled "New chat" until its first message names it (a title given at creation stays), kept when the
  runtime lists the threads it has (it knows none of them), and deleted after 30 days without a message (with its
  servers; `agents.Cleanup`, which also prunes expired thread servers). The first accepted message ends the
  reservation (a first message from someone other than the creator ends it at once and drops the servers the creator
  set).
- **Shared or private.** The first message (or `POST …/threads`, or the servers `PUT`) chooses (`visibility`, default
  `shared`; it never changes: a different value later is 409 `visibility_fixed`). A shared thread is seen by everyone who may use the agent. A private one is
  listed and read only by the person or service account that started it: not other members, not the team's owners
  or admins, not another person's test chat in the console. On every thread route (read, send, stop, events, rename,
  delete, the thread's MCP servers), in listings and in event streams, another's private thread reads as not found
  (404 `thread_not_found`, the same as a missing one). A thread the runtime has but the platform has no record of
  (older threads) is shared. Personal workspaces' threads default to shared too: only the owner reaches the agent.
- **Open streams are re-checked.** Access to an event stream (`GET /v1/agents/events`, with or without `agentId`,
  and a thread's `…/events`, which relays the runtime's conversation) is decided when it opens and checked again
  every 30 s (`agents.DefaultStreamRecheck`; one indexed read of the person per stream, plus the agent's team role
  for a stream of one agent): the stream ends when the person is suspended, denied by the group re-check or deleted,
  or may no longer use the agent (removed from its team, a lower role), or the thread is another's private one. A
  stream of all their agents stays open but stops carrying an agent's threads once they can't use it. The reconnect is
  refused like any request. Without this, titles and participant names would keep flowing for as long as the stream
  stayed open. Not re-checked: a revoked personal API key, connected app grant or service account key (and its
  team) behind a stream that is already open; those end with the stream. (The app-level stream of OAuth Connect
  reads its people's grants for every change, so a revoked connection stops at once there.)
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
- **Paging and search.** Both are opt-in, so clients that pass nothing get the whole list and the whole transcript as
  before. `GET /v1/agents/{id}/threads?limit=&cursor=&q=` pages the platform's record, newest activity first
  (`updated_at DESC, thread_id`, served by `agent_threads_updated`): with `limit` (1-200) the reply is
  `{"data": [...], "nextCursor": "..."}`, the array as before and the cursor beside it, absent on the last page. The
  cursor is opaque (base64url of the last row's `updated_at` in microseconds and its ID; a malformed one is 400
  `invalid_cursor`). `q` is a case-insensitive substring of the title (at most 200 characters, LIKE wildcards
  escaped), with or without `limit`; an agent's threads are few enough to filter without a trigram index, so no
  migration. Visibility applies to every page as to the whole list. A conversation pages backwards in the runtime,
  which serves the view: `GET …/threads/{thread}?limit=&before=` returns the `limit` (default 50) messages before the
  one `before` names, or the latest, and `earlierCursor` when earlier ones exist (opaque: the base64url of the first
  message's ID). A page never starts with a tool result (it then also holds the reply that made the call), and a
  cursor whose message has left the view (compacted or reset) gives an empty page. The event streams take `limit`
  too: whole views and snapshots keep the latest messages, with `earlierCursor`.
- **Incremental event streams.** `GET …/threads/{thread}/events?format=delta&v=1` sends a `snapshot` event (a ChatView)
  and then only changes, each an SSE event whose name is its `type` and whose `id` is its `seq` (counting up by one):
  `message.added`, `message.updated` (a transcript message replaced by ID: a tool call's end time, for instance),
  `partial.set`, `partial.appended` (text and thinking appended to the reply in progress), `partial.cleared`,
  `tool.started`, `tool.updated` and `tool.finished` (status, `outputAppended` or `output`, null for none),
  `tool.removed`, `tools.set` and `run` (`busy`). Tool events carry `startedAt` (the call's time in the transcript,
  else when the stream first saw it) and `elapsedMs`, live metadata outside the view. Without `format` (or with
  `format=view`) the stream is unchanged: whole views. The runtime's stream is coalesced like whole views (at most 10
  flushes a second); each flush compares the view the client holds with the current one (`runtime/src/lib/delta.ts`), so
  applying the events in order to the snapshot gives exactly the view a new snapshot would (a property test checks
  it on random evolutions, and an end-to-end test on a recorded run with a tool round and a steer). The reply grows
  by appends, so the bytes follow the new tokens, not the transcript's length (tested). The transcript is rebuilt
  only when pi-durable's watch frame brings new entries (unchanged parts are shared between frames), so a token costs
  no transcript work either. Whatever can't be a change (the conversation was compacted or reset, or the window's
  first message left it) is sent as a new snapshot. Every connection starts with a snapshot; with `Last-Event-ID`
  (EventSource sends it when reconnecting; the platform relays that header and no other) the numbering goes on after
  it, so a client keeps one increasing sequence, and a gap in `seq` means it should reconnect. *Not chosen:*
  pi-durable's `watchEvents` (experimental). Its events describe pi's structure (entries, content blocks, task
  records), not the platform's chat view (`pdp.user` entries shown as sent, tool calls timed from their results,
  system and compaction entries hidden, the transcript cut at a compaction's head), so translating them would
  duplicate `buildView` in a second form that must stay in step with it, and merging coalesced batches is its own
  problem. Comparing chat views is correct by construction and its cost is the same `buildView` work the whole-view
  stream already did. The console follows threads with the delta format.
- **Steering.** `POST …/messages` takes `whenBusy`: `followUp` (the default, as before) waits for the reply in
  progress to end and starts the next; `steer` is passed to pi-durable's submit and joins the running reply: it is
  placed after the reply's current tool round (or at its answer), so the model sees it before going on, and the run
  continues. The transcript shows a steering message where it was placed: a user message after the tool results of
  the reply it changed and before that reply's next answer (in the delta format, a `message.added`). On an idle
  thread both start a reply. The console's test chat offers Steer while a reply runs.
- **Platform admins' reasoned reads.** `GET /v1/admin/agents/{id}/threads` lists every thread of any agent, private
  ones too, with visibility, creator and participants; the titles of private threads, of every thread of a
  personal workspace's agent and of every thread of a service-managed agent (an app's agent for one of its users)
  are withheld (titles are content). `POST .../threads/{thread}/read {reason}` (10-500
  characters) returns the conversation once, waking the agent like any read, and records `thread.admin_read` (target
  `agent_thread` `<agentId>/<threadId>`, with the reason, the thread's visibility and whether the workspace is personal)
  in the agent's team's audit log, where its owners see it, before returning it. Platform admins only, with a browser
  session; auditors can't. Otherwise platform roles still never reach conversations.
- **Staff see every team's agents.** Platform admins and auditors read any team's agents (`GET
  /v1/teams/{team}/agents`, `GET /v1/agents/{id}`: settings and status), shared teams' as well as personal
  workspaces', so Admin › Teams › a team lists them and links to each agent's page. Read-only: they don't talk to,
  change or hibernate an agent of a team they aren't on (platform admins may delete one).

## Triggers

Schedules (cron in an IANA time zone) and webhooks (`POST /v1/hooks/{hookId}`, signed `X-PDP-Signature: t=…,v1=…`
HMAC-SHA256 over `"<t>." + body`, 5 minutes' tolerance) start runs of an agent without a person (v0.4, #15); the
design, threat model and limits are in [docs/triggers.md](docs/triggers.md).

- **Records**: `agent_triggers` (a webhook's secret and a callback's secret sealed with `ENCRYPTION_KEY`, shown once),
  `agent_trigger_settings` (the agent's limits), `trigger_runs` (one per fire). API: `/v1/agents/{agentId}/triggers`,
  `…/trigger-settings`, `…/runs`, `/v1/teams/{team}/runs`; the console's agent page has Triggers and Runs tabs.
- **Fires are idempotent**: a schedule's fire ID is its trigger and scheduled time, a webhook's derives from its
  signature (a replay finds the first run), and it is the message's `requestId`, so neither a second replica nor a
  retried delivery runs it twice. Every replica runs the trigger loop (`TRIGGERS_INTERVAL`); one pass at a time
  (`pdp.trigger-loop`), due schedules locked `SKIP LOCKED`. Missed fires: `skip` or `run_once` per schedule.
- **As no person**: the author is `{kind: "trigger", id, name}`; the thread is shared and started by nobody; a fixed
  thread that is someone's (private, reserved) or has an app's MCP servers is refused; the run's sender is recorded
  without a grant (MCP identity tokens, decision 16: a shared team's run names nobody, a personal workspace's names its
  owner with `person_present: false`). Owners and admins manage triggers in a browser session (personal workspaces: their owner); members,
  editors and service accounts read them; platform staff read them; service-managed agents take none; a move deletes
  them.
- **Limits**: runs at once, maximum run time (the thread is aborted), daily budgets per trigger and per agent (UTC day;
  a run's cost is the agent's usage while it ran), pause switches per trigger and per agent, back-off after a failure
  and an automatic pause after `TRIGGERS_PAUSE_AFTER_FAILURES` in a row.
- **Results**: the thread is the record; an optional completion callback (signed with the callback secret, through
  `internal/teamnet`, retried) carries the run's status, cost, a 500-character excerpt of the last reply and a link.
- **Audit**: `trigger.create|update|delete|secret_rotate`, `trigger.run` (system) and `trigger.fire` (Run now),
  `trigger.run_stop`, `trigger.auto_pause`, `trigger_settings.update`.

## Creating and moving agents

The reasoning is in [docs/agent-keys-and-moves.md](docs/agent-keys-and-moves.md) (v0.3, #11 and #13).

- **Idempotency-Key.** `POST /v1/teams/{team}/agents` takes an `Idempotency-Key` (1-255 printable ASCII, bare or
  quoted), remembered for 24 hours per caller: a person (sessions and personal API keys share it), each app a person
  connected (per grant), each service account. The same key and request returns the first answer (201,
  `Idempotent-Replayed: true`; 404 if the agent was deleted since); the same key with another request (body or team)
  is 422 `idempotency_key_reused`. The key is claimed under a primary key in the creating transaction, so concurrent
  retries wait for the first and replay it; failures aren't remembered. The cleanup loop deletes keys older than a day.
- **Keys in a personal workspace.** `PUT …/agents/by-key/{key}` is editor service accounts' in shared teams (the
  agent is service-managed, `agents.service_managed`), and the owner's in their personal workspace by any of their
  credentials, apps included (the agent is the workspace's, not service-managed). Anyone else: 403 in a shared team,
  404 in someone else's workspace. The database keeps the rule itself (a trigger, migration 00020): a keyed agent in a
  team that isn't a personal workspace is service-managed whoever writes the row, so one a v0.2 replica creates during
  a rolling upgrade (its insert doesn't name the column) is too.
- **Moving.** `POST /v1/agents/{id}/move {team, modelId?}` (If-Match) and its dry run `GET …/move?team=` (the same
  authorization; counts of shared and private threads and who gains and loses access, the key dropped, servers that
  stop applying, and blockers with their error codes). People who are owners or admins of both teams; a personal
  workspace's owner only, on either side; platform admins between shared teams, but into a team they are on only
  when they may move it out of its own. The agent keeps its storage, version and threads; shared threads become the
  new team's, private ones stay their creator's. The template and model must be available to the target (409
  `move_template_unavailable`, `move_model_unavailable`; `modelId` switches the model), the name free (409
  `name_taken`), the limits not reached; service-managed agents stay (409 `agent_service_managed`). The old team's
  service accounts, the agent's external key, the MCP servers apps set on its threads and the old team's own servers
  stop applying; usage stays with the team it happened in. Audited as `agent.move` in both teams' logs. The console
  offers it in an agent's Settings › Danger zone.

## Agent storage and ownership

Each agent has a schema and a login role of the same name (`agent_<uuid without dashes>`) in the agents database. The
provisioner (`internal/agenthost/provision`, run by a CREATEROLE role that is not a superuser) creates the role
(`LOGIN NOINHERIT NOCREATEDB NOCREATEROLE CONNECTION LIMIT 2`) and the schema it owns; PUBLIC has no access to schema
`public` or `TEMP`. The role owns every table in its schema and nothing else, so another agent's role gets "permission
denied" on all of it. The role's own tables are created by the runtime at start (pi-durable's by `SqliteStorage.open`,
the ownership record below), which is why the agents database has no migrations of its own.

**What ownership has to guarantee.** One process at a time writes for an agent; a replacement takes over as soon as the
owner is gone (a crash, a lost connection, a termination), so an agent never waits on a timer; and a stale owner can't
write after its replacement started. The runtime exits when its connection is lost, so the supervisor starts a new one.

**Design: an ownership record in the agent's own schema.** The runtime keeps a one-row table, `pdp_ownership`, in the
agent's schema. It holds the owner's database session (backend pid and start time), when it took over and a generation
counter. To take ownership the runtime, in one transaction: locks the row (`SELECT ... FOR UPDATE`), then looks at
`pg_stat_activity`. If the recorded session still exists, it fails with `AgentOwnedElsewhereError` (the platform retries,
as before). If not (the process exited, the connection dropped, the backend was terminated), it records itself and
continues. A session is identified by pid and start time, so a reused pid doesn't count. `pg_stat_activity` is read only
after the row lock is held (its snapshot lasts the transaction; one taken earlier could miss a session that started while
waiting). A clean stop clears the record before disconnecting, so a hibernated agent's next start never waits.

This keeps what the session advisory lock gave: ownership is bound to the one connection that also writes, so it ends
exactly when the ability to write ends, with no lease, heartbeat or expiry to wait out or to get wrong (a stalled but
connected owner stays the owner, as before; `Evict` ends its sessions). It also removes what the lock couldn't: the
decision rests on a table only the agent's role can write and on the agent's own role's sessions, so no other role can
influence it. The runtime refuses to start when its own session isn't visible in `pg_stat_activity` (`track_activities`
off), since a live owner could then look dead.

**What another agent's role can do** (`runtime/test/ownership.test.ts`, `internal/agenthost/provision`):

| Attack | Session advisory lock (v0.1.x) | Ownership record |
|---|---|---|
| Take a free agent's key while it is hibernated, and hold it | Blocks that agent's start for as long as it is held | Can't take ownership (permission denied on the schema and table); the key it holds is ignored by the runtime (`cannot block A's start by holding A's legacy lock key`, `cannot take ownership of an agent's storage`) |
| Observe `pg_locks` for the key, grab it when the owner stops | Same | Same; B sees that some session holds an advisory lock, nothing it can act on |
| Write, lock, drop or read the record; plant objects in the schema | n/a (no record) | Permission denied (`cannot write, lock, drop or read A's ownership record`) |
| End the owner's session (`pg_terminate_backend`, `pg_cancel_backend`) to force a restart | Refused | Refused |
| Use up the agent's connections | Per-role limit of 2 | Same (`TestAnotherAgentCannotInterfereWithOwnership`) |

What is left: any role can see which sessions of other roles exist (`pg_stat_activity` shows the user name and pid),
and agents share the server's connection and CPU capacity, so a hostile agent can still be a noisy neighbour. Per-role
quotas are the database's, not this design's. An agent's own role can of course tamper with its own record and data.

**Where an agent's role can connect: the trust boundary.** An agent's role is a login role of the PostgreSQL *server*, and
the runtime that holds its password runs code the platform does not trust. Schema privileges keep it inside its own
schema of the agents database, but PostgreSQL grants PUBLIC the `CONNECT` privilege on every database it creates, so unless
that is revoked the role can also open the platform's own database and any other database on the server. Three things
there are not protected by table privileges:

- *Advisory locks* are per database, and the platform's live in the platform database: the wake lock for each agent
  (`pdp.agent-wake:<agent id>`), the reaper's (`pdp.agent-reaper`) and the migration lock. Any role may take any advisory
  lock key, so a role that can connect there could hold them: no agent would wake, no replica would reap, a starting pod
  would wait on the migration lock. A role that cannot connect to the platform database cannot take any of them.
- *LISTEN/NOTIFY* is per database too and `NOTIFY` needs no privilege, so such a role could send notices on `pdp_threads`.
  The platform re-reads the thread from its table for every notice (the payload only names an agent and a thread), so a
  forged notice causes a refresh, or a "deleted" event for a thread that does not exist, and wasted reads; nothing is taken
  from the payload as fact. Closing `CONNECT` closes this too.
- Anything else in those databases that PUBLIC may read or use.

So the platform keeps its locks in its own database, which is sound **once agent roles cannot connect to it**; this
is an operator step, not a migration, because other roles may depend on PUBLIC's `CONNECT`:

```sql
REVOKE CONNECT ON DATABASE <platform database> FROM PUBLIC;
GRANT CONNECT ON DATABASE <platform database> TO <the platform's role>;  -- and any other role that uses it
```

Other databases on the server need the same `REVOKE`, or `pg_hba.conf` rules that let agent roles (named
`agent_<uuid without dashes>`; PostgreSQL 16 reads `/^agent_` as a regular expression) reach the agents database only.
`pi-durable-platform doctor` warns, never fails, while PUBLIC may connect to the platform database (`postgres public-connect`,
read from `datacl` with the statements above filled in) and lists the other databases of the agents database's server
that PUBLIC may connect to (`agents public-connect`). It only compares the two servers by their start time, so an agents
database on another server is reported as out of reach. The CloudNativePG example under `deploy/examples/cloudnative-pg`
does both steps.

The agents database itself cannot be closed to agent roles, so nothing the platform needs in it depends on a lock an agent
could hold: the one advisory lock taken there (`pdp.prepare-agents-database`, which serialises replicas' `Prepare`) is only an
optimisation, and `Prepare` goes on without it after 20 seconds (`TestAnAgentHoldingThePrepareLockDoesNotBlockPrepare`).

**Upgrading agents made by v0.1.x, and rolling upgrades.** There is nothing to migrate in the platform database or by the
provisioner. The runtime creates the table with the agent's own role (`CREATE TABLE IF NOT EXISTS`, retried if two
processes of one agent race) the first time a new runtime starts that agent: agents upgrade as they next wake, with no
downtime beyond that restart. Rolling back to a v0.1.x runtime works; it ignores the table.

Runtimes before and after the change may run side by side while pods are replaced (an old pod still running, or an agent
woken with the old image). The new runtime therefore also takes the v0.1.x session advisory lock
(`pg_try_advisory_lock(hashtextextended('pdp-agent:<schema>', 0))`) on its connection. If a session of this agent's own
role holds it, that is a v0.1.x runtime of the same agent and the new one refuses (`AgentOwnedElsewhereError`); while the
new runtime runs, it holds the lock, so a v0.1.x runtime refuses. A holder running as any other role is ignored: that is
the attack above. The one gap is a v0.1.x runtime started while another role holds the key and releases it again, which
needs untrusted code in the agents database during the upgrade; self-service agent code is not available before this
change. The runtime option `legacyLock: false` turns the compatibility lock off; once no v0.1.x runtime is left (every
agent has been restarted on the new image) it can be removed.

**Orphaned roles.** Roles are server-wide, so a dropped or recreated agents database leaves its agents' roles behind. The
provisioner marks each role it creates with a comment naming its agents database. `pi-durable-platform doctor` lists the
roles named like agents' (`agent_...`) that belong to no agent in the platform database and to no schema in the agents
database; `pi-durable-platform prune-roles [--dry-run]` drops them. It only drops a role that is what the provisioner
makes (a role without privileged attributes, member of nothing, one the provisioning role manages, not marked for
another agents database; it may be NOLOGIN, which a half-finished deletion leaves) and has no open sessions. It drops
what the role still owns in the agents database (`DROP OWNED`: a large object or a table the role created is left over,
since the role has no schema here), and nothing in any other database: if the server finds the role still owns objects
or has privileges there (another installation's live agent on a shared server owns its schema there), the role is kept
and reported with the reason. Anything else is listed as left alone. Roles made before the comment existed carry no
mark and are still considered; only those that own nothing in other databases can go.

**Deleting an agent** must work however hostile the agent's code was, so its role is dealt with in an order that a retry
can resume: `ALTER ROLE … NOLOGIN` (the runtime still knows the password), end its sessions, drop its schema, `DROP OWNED
BY` the role in the agents database (large objects and anything else it created: `lo_create` makes an object the schema
drop doesn't take, and `DROP ROLE` refuses a role that owns one), then `DROP ROLE`. Each step is idempotent and the agent
row is deleted only after all of them, so a failure (for example a role that owns something in another database of the
server, or privileges granted by someone else) leaves the agent in place with an error, and deleting again finishes. A
role left `NOLOGIN` by a failed deletion gets its login back when the agent's storage is next provisioned.

## The agent runtime and custom agents

`runtime/` is a library, `@pdp/runtime` (`runtime/src/lib`, exported from `runtime/package.json`), and the platform's
own agent is its default composition (`src/main.ts` runs `assistantAgent`: the template's instructions and `get_time`).
A custom agent defines its own extensions (tools, prompt sections, hooks, tasks), documents, `conversationCreated` and
settings with the same API; the library owns each thread's extension selection, so an author's guard runs on every turn.
[docs/custom-agents.md](docs/custom-agents.md) is the design note (trust model, API, what the library guarantees and
what authors must not do, versioning, the test kit). What any runtime must do (the HTTP API, `/status`, standby and
activation, reports, ownership, exit codes, environment) is [docs/runtime-contract.md](docs/runtime-contract.md), with a
conformance suite in the test kit. Only runtimes the operator builds into an image run; teams can't upload code yet.

Custom state reaches apps through the thread, with the thread's access: a tool's `details` on its call in the view and
events (bounded JSON), and conversation documents an author exposes, read with
`GET /v1/agents/{id}/threads/{thread}/documents/{kind}` and announced in the thread's streams (`document.updated`);
platform admins read them only with a reason, audited as `thread.admin_read`. Session documents are not exposed (every
thread shares them). The platform records per agent the newest pi-durable version that has written its storage
(`agent_storage_versions`) and refuses to start the agent on an older runtime, which the runtime asks before it opens
the storage (`/runtime/start` on the gateway) and the platform checks again at `/status`: documents migrate when read,
and older code rejects newer data, so rolling a runtime back after it has written is not supported. A starter project
is in `examples/custom-agent/`.

## Launchers

| `AGENTS_LAUNCHER` | Runtime per active agent | Hibernate | Wake |
| --- | --- | --- | --- |
| `local` | a child process of the platform (development only; one platform process) | stop the process | start it |
| `kubernetes` | a pod `agent-<id>`, secrets in a Secret of the same name | delete the pod | create it |
| `agent-sandbox` | a [Sandbox](https://agent-sandbox.sigs.k8s.io) `agent-<id>` around the same pod | `operatingMode: Suspended` | `Running` |
| `agent-sandbox` + `AGENTS_SANDBOX_WARM_POOL` | a standby runtime claimed from a `SandboxWarmPool` (claim `agent-<id>`) | release the claim | claim + activate |

Warm mode: the pool's pods run the runtime image without `AGENT_ID`, with `RUNTIME_ACTIVATION_TOKEN`; they are Ready
and wait (`runtime/src/lib/standby.ts`). Waking an agent creates its claim, and the platform `POST`s the agent's
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

## Secrets and key rotation

Two keys protect what the database holds:

- **`ENCRYPTION_KEY`** seals secrets the platform must use again, with AES-256-GCM (`internal/secrets`): model provider
  keys and headers, MCP server headers, thread MCP server headers, each agent's database password and runtime
  token, people's IdP refresh tokens and the MCP identity token's signing keys. A sealed value is `version | key id | nonce | ciphertext+tag`, where the key id is the first 8 bytes of the
  key's SHA-256 and the associated data (the row it belongs to) stops a value being moved to another row. Because a value
  names its key, "which values are still on the old key" is answered from the stored bytes, in SQL, with no marker
  column and no migration.
- **`API_KEY_PEPPER`** keys the HMAC digests of personal API keys, service account keys and OAuth Connect tokens and
  codes, which are never stored. Digests are not reversible, so they move to a new pepper when they are used
  (`API_KEY_PEPPER_PREVIOUS` is accepted meanwhile; `doctor` counts what is left).

**Rotating `ENCRYPTION_KEY`**

1. Generate a key (`openssl rand -base64 32`). Set it as `ENCRYPTION_KEY` and the old one as `ENCRYPTION_KEY_PREVIOUS`;
   restart. New and changed secrets are sealed under the new key, old ones still open.
2. `pi-durable-platform rekey --dry-run` counts, per sealed column, the values under each key and checks they open.
3. `pi-durable-platform rekey` re-seals the values under the previous key. It works column by column, in batches (a
   transaction each, `--batch`, default 200), and replaces a value only if the row still holds the ciphertext it read,
   so a secret someone saves meanwhile is never overwritten with its old value: it is safe while the platform serves.
   It does not change a row's revision or `updated_at`, is idempotent, and resumes after an interrupt by running again.
   Before writing anything it opens every value to be re-sealed: if one opens with neither key (a missing
   `ENCRYPTION_KEY_PREVIOUS`, a third key, damage) it stops, names the columns and rows (never values) and changes
   nothing. It records one audit entry (`platform.secrets_rekey`: counts per column).
4. `pi-durable-platform doctor` (check `keys encryption`) warns while any value is still under the previous key, and
   fails for values under no configured key. When it reports none, remove `ENCRYPTION_KEY_PREVIOUS`.

## Observability

Metrics are Prometheus metrics (`internal/metrics`, the Prometheus Go client); traces are OpenTelemetry, optional
(`internal/tracing`). Both are instrumented at the choke points, not scattered: the HTTP middleware of each listener
(`internal/httpapi/observe.go`), the agent manager (wake, hibernate, reaper), the model and MCP proxies, OAuth's token
endpoint, sign-in, the rate limiter (`ratelimit.Allow`) and the background loops. A package given no metrics does
nothing (every method accepts nil), so commands that don't serve and most unit tests need no registry.

**Ports.** `METRICS_ADDR` (default off; the chart sets `:9090`) starts a third listener that serves `GET /metrics` and
nothing else (any other path is a 404). It has no authentication, because it is a port of its own: it is not behind the
Ingress, neither the web listener (`LISTEN_ADDR`) nor the agents' gateway serves `/metrics`, and the chart's
NetworkPolicy lets only the peers in `networkPolicy.metrics.from` reach it (nobody by default). Never publish it. It is
not part of `api/openapi.yaml`, as the gateway isn't.

**What is exported** (every name starts with `pdp_`; histograms add `_bucket`, `_sum` and `_count`):

| Metric | Labels | Meaning |
|---|---|---|
| `http_requests_total`, `http_request_duration_seconds`, `http_requests_in_flight` | `listener` (api, gateway), `method`, `route`, `code` (2xx…; no `code` on the histogram and the gauge) | Every request of both listeners. `route` is the mux's pattern (`/v1/agents/{agentId}`), never the path; requests nothing matched (the web app's files, 404s) are `unmatched` |
| `agent_wakes_total`, `agent_wake_duration_seconds` | `launcher` (local, kubernetes, agent-sandbox), `start`, `result` | Every call that makes sure an agent's runtime is up. `start`: `running` (nothing to start: the fast path), `cold` (started or resumed), `warm` (a standby runtime of the warm pool was adopted), `pool_miss` (the pool was empty: created on demand), `unknown` (failed before saying). `result`: ok, error, timeout, canceled |
| `warm_pool_claims_total` | `result` (hit, miss) | Claims on the Agent Sandbox warm pool. A claim is a hit when the sandbox the claim got is not named after the claim (the controller names a sandbox it creates itself after the claim) |
| `agents_running` | none | Agents with a live runtime, as the reaper last saw them (every 30 s) |
| `agents` | `enabled` | Agents that exist (a query every 30 s; the same on every replica: use `max`) |
| `agent_hibernations_total` | `reason` (idle, request), `result` | `idle`: the reaper; `request`: an explicit hibernate, or a change that restarts the agent |
| `agents_awake` | `reason` (reply, stream, background, recent) | Running agents the reaper last found kept awake by each reason (an agent can count for several): a reply, an open event stream, background work under `AGENTS_BACKGROUND_MAX`, use within the idle timeout ([docs/hibernation.md](docs/hibernation.md)) |
| `agent_background_aborts_total` | `result` | Background work past `AGENTS_BACKGROUND_MAX` the reaper had a runtime abort before hibernating it |
| `model_proxy_requests_total`, `model_proxy_request_duration_seconds` | `provider_kind`, `model`, `code` | The model proxy. Duration is the whole reply, streams included |
| `model_proxy_tokens_total` | `provider_kind`, `model`, `direction` (input, output, cache_read, cache_write) | Tokens the replies report |
| `mcp_proxy_requests_total`, `mcp_proxy_request_duration_seconds` | `server`, `code` | The MCP proxy |
| `mcp_tool_calls_total` | `server`, `result` (ok, error, denied) | Tool calls through the proxy (tool names are not labels) |
| `oauth_tokens_issued_total` | `grant_type` | Token pairs issued |
| `oauth_token_errors_total` | `error` (the OAuth error code) | Refused token requests |
| `oauth_refresh_reuse_revocations_total` | none | Grants revoked for refresh token reuse |
| `signins_total` | `method` (oidc, dev), `result`, `reason` (none, denied, invalid, unavailable, error) | Sign-ins |
| `group_recheck_total` | `result` (allowed, denied, unknown, error, no_token) | Group re-check outcomes. `unknown`: the IdP couldn't say, access kept for now; `error`: the stored token can't be opened or the answer couldn't be recorded; `no_token` isn't a check but a sign-in under `OIDC_ALLOWED_GROUPS` that brought no refresh token (that person is never re-checked) |
| `group_recheck_people` | `state` (with_token, without_token) | Recently active people the group re-check applies to, by whether a refresh token is kept for them (set by the re-check loop; the same on every replica: use `max`) |
| `rate_limit_rejections_total` | `limiter` (login, user, apikey, service-account, oauth, …) | Requests refused with 429 |
| `thread_event_subscribers` | none | Open thread event streams on this replica |
| `background_runs_total`, `background_run_duration_seconds`, `background_last_success_timestamp_seconds` | `loop` (reaper, cleanup, rate_cleanup, group_recheck, thread_events, agent_count, triggers), `result` | Background loops; `thread_events` counts only listeners that failed |
| `db_pool_*` | `pool` (platform, agents) | `acquired_connections`, `idle_connections`, `total_connections`, `max_connections` and the counters `acquires_total`, `acquires_waited_total`, `acquire_wait_seconds_total`, `canceled_acquires_total`, from pgx's pool statistics, read at scrape time |
| `build_info` | `version`, `commit`, `go_version` | Always 1 |

The Go runtime and process collectors (`go_*`, `process_*`) are there too.

**Label policy.** A label holds only a small set of values the platform defines: route patterns, status classes,
methods, launcher modes, provider kinds, loop names. Nothing that identifies a person, a team, an agent, a thread or a
URL is ever a label, and no label comes from a request (a test throws hundreds of different IDs and paths at the API
and asserts that the series stay few; another asserts that every label name is on an allowlist). Two labels are
chosen by administrators and so go through a bounded guard (at most 64 values, plain characters, else `other`): the
`model` label is the upstream name of a *platform* model, and the `server` label of the MCP metrics is the slug of a
*platform* MCP server. A team's own models are `team`, a team's own MCP servers `team`, and servers an app gave a
thread `app`: their names belong to the teams and are unbounded. Per-team usage is in the usage reports (Models), not
in metrics; team slugs are not labelled.

**Several replicas.** Each replica exports its own metrics, so sum counters and rates. Gauges that describe the cluster
rather than the process (`agents`, and `agents_running` with a cluster launcher, which lists the cluster's pods) are
the same on every replica: use `max`.

**Tracing** is off unless `OTEL_EXPORTER_OTLP_ENDPOINT` is set (the collector's base URL; OTLP over HTTP, port 4318, not
gRPC). `OTEL_SERVICE_NAME` (default `pi-durable-platform`) and `OTEL_TRACES_SAMPLER_ARG` (the share of new traces kept,
default 1) are settings; the exporter itself honours the standard variables for headers, timeout, compression and TLS
(`OTEL_EXPORTER_OTLP_HEADERS` and its siblings). A caller does not decide sampling: every request to the API or the gateway
starts a trace of its own, which `OTEL_TRACES_SAMPLER_ARG` decides, whatever `traceparent` (or sampled flag) it
carries, so nobody can force traces to be recorded. An inbound `traceparent` is kept as a span link only; the
spans below the request (wake, runtime request, upstream calls) follow the request's decision and send their own
`traceparent`. `baggage` is neither read nor sent. Spans:

- a server span for every request of the API and the gateway, named `METHOD route-pattern` (the same pattern the
  metrics use, never the path), with `http.route`, `http.response.status_code` and `pdp.request_id`; the access log
  carries `trace_id` when there is a trace;
- `agent.wake` (launcher mode, how it started, the result; the agent's ID) and `agent.runtime_request` (the relay to
  the runtime) below the request that woke the agent. The runtime request carries `traceparent`, as does the warm
  pool's activation call;
- `model.upstream` (provider kind, the platform model's name, status, tokens; a team's model is not named) and
  `mcp.upstream` (the server's label, the number and names of the tools called) below the gateway request, with the
  trace context sent to the provider or MCP server.

A span never holds message content, request or response bodies, headers, URLs, keys, tokens or a person's identity:
call sites pick their attributes by that rule, errors are reduced to a fixed reason, and a test checks all the spans of
a conversation for any of the secrets and prompts it used. Not done: the runtime does not read `traceparent` yet (no
runtime-side spans), and the model and MCP requests an agent makes to the gateway start traces of their own, because
the runtime does not forward the trace context it was given. Following a conversation from the API into the runtime
needs the runtime to propagate it.

**Dashboards and alerts.** `deploy/grafana/pi-durable-platform.json` is a Grafana dashboard (wake latency and warm vs
cold, running agents, request rate and errors, model proxy, MCP proxy, sign-in and OAuth, the reaper, database pools,
process); `deploy/grafana/alerts.yaml` is an example PrometheusRule. A test (`internal/metrics/grafana_test.go`)
checks every expression in both against the metrics and labels the platform exports. The chart can ship the dashboard
as a ConfigMap for Grafana's sidecar (`metrics.dashboard.enabled`) and a ServiceMonitor
(`metrics.serviceMonitor.enabled`); see [deploy/README.md](deploy/README.md).

## Deployment

`deploy/` ships a Helm chart and a Kustomize base with overlays ([deploy/README.md](deploy/README.md)); the chart is the
source and the Kustomize resources are generated from it, with a parity check in CI. What they deploy follows from the
launchers above:

- **RBAC in the agents namespace only.** The platform's ServiceAccount gets a Role there, with the verbs its launcher
  uses and no more (`kubernetes`: pods and secrets; `agent-sandbox` with a warm pool: sandbox claims, and the
  activation Secret; without: sandboxes, pods get and secrets). Nothing is cluster-wide.
- **Agent isolation is NetworkPolicy.** The agents namespace denies everything; an agent pod takes traffic from the
  platform pods (port 4100) and reaches only PostgreSQL, the platform's gateway (8099) and DNS. The platform pods take
  web traffic from the ingress peers and the gateway port from agent pods, and reach DNS, PostgreSQL, the agents, the
  Kubernetes API (an address list for standard policies, the `kube-apiserver` entity for Cilium) and public HTTPS. The
  policies need a CNI that enforces them; `deploy/scripts/smoke-kind.sh` (run in CI) checks the rules on kind.
- **Pod Security `restricted`** for the agents namespace; the platform pod meets it too.
- **Several platform replicas** are supported (`replicaCount: 2` is the tested setup; see below), with the `kubernetes` or
  `agent-sandbox` launcher.

## Several replicas

The platform runs as any number of replicas behind one Service ([docs/replicas.md](docs/replicas.md) is the design note,
with the inventory of what each replica keeps). In short:

- Sessions, sign-in state, OAuth Connect state, rate limits, the thread record and its change notifications
  (`LISTEN/NOTIFY`, heard by every replica) are in PostgreSQL. The clean-up loops delete idempotently and the group
  re-check claims people with `SKIP LOCKED`, so every replica runs them.
- **The runtime is the authority on whether an agent is idle.** `/status` reports `busyThreads`, `openStreams`,
  `lastActivityAt` and (contract version 2) `backgroundWork`; the reaper hibernates an agent only when they all say idle
  for `AGENTS_IDLE_TIMEOUT`, background work past `AGENTS_BACKGROUND_MAX` aside, which it has the runtime abort first
  ([docs/hibernation.md](docs/hibernation.md)). Any replica's
  requests and streams count, because they all go through the runtime. One replica at a time reaps (a PostgreSQL
  advisory lock per pass), to avoid duplicate status calls; correctness does not depend on it. An agent whose runtime
  predates `openStreams` is judged by its own replica's streams until it restarts.
- **Waking is serialised by a lock per agent.** A running agent costs one `Launcher.Lookup`; starting one takes an
  advisory lock (`pdp.agent-wake:<id>`) and calls `Ensure`, which re-checks under the lock, so two replicas waking an
  agent start one runtime. The runtime starts with the agent's spec read from the database under the lock, not the
  one the request built when it was authorized, and a stop on request (a move, a change, a hibernate) takes the same
  lock: a request in flight never restarts an agent with the configuration a change stopped (a moved agent with the
  old team's model key and MCP servers).
- Launchers: `kubernetes` and `agent-sandbox` share their state through the Kubernetes API. `local` is one process.
- Not shared, and fine: caches with a TTL (OIDC discovery, OAuth client metadata documents and logos, team connections).
  A consent page's logo is a second request that may reach a replica that has not cached the app's document, and then
  shows the app's initials.

## Verified so far

- `runtime/test/conformance.test.ts`: pi-durable 1.3.0's own storage conformance suite (24 cases) passes on PostgreSQL.
- Ownership: a second opener is refused; an owner whose backend is terminated can no longer read or write, and the
  next owner opens within a bounded time; concurrent openers never produce two owners; an agent without the ownership
  table is upgraded in place (`ownership.test.ts`).
- Isolation: an agent role cannot read another agent's schema, and cannot take, block, steal, lock or observe-and-grab
  another agent's ownership (`ownership.test.ts`, `provision_integration_test.go`).
- Orphaned agent roles are listed by `doctor` and dropped by `prune-roles` without touching live ones
  (`provision_integration_test.go`, `doctor_integration_test.go`, `prune_roles_integration_test.go`).
- A real Harness keeps its conversation and deduplicates retried requests across a process restart
  (`harness.test.ts`); a manual `kill -9` mid-reply resumed and completed the reply.
- The runtime contract: the platform's runtime process passes the HTTP conformance suite (and the standby suite), exits
  1 when its database connection is lost, 3 while another process owns the agent and 0 on `SIGTERM`
  (`contract.test.ts`). A crash in the middle of a model request, a tool call or a subagent neither repeats nor loses
  work after the restart (`restart.test.ts`, on the test kit's in-memory storage), and a guard in an agent's definition runs on every turn, across model and
  MCP changes and for a run resumed at start (`guards.test.ts`).

## Known limits and open items

The [v0.1.0 release notes](docs/releases/v0.1.0.md#known-limitations) list the product limits as of v0.1.0 (no budget
enforcement, text-only messages, no paging, …; several replicas and metrics are supported since v0.2, paging, delta
event streams and steering since v0.3, see above). The design-level ones:

- Agent roles still share the server's connection and CPU capacity (no per-role quotas), and any role can see that
  other roles' sessions exist. Cross-agent ownership interference is closed (see Agent storage and ownership), but
  uploaded agent code (#29; the checks are listed in [docs/custom-agents.md](docs/custom-agents.md#what-step-2-must-check)) still needs hostile-code isolation of the pod and a decision on whether it gets direct
  database credentials at all (a storage proxy is the alternative).
- The v0.1.x compatibility advisory lock stays until no v0.1.x runtime is left; removing it is a later release.
- Schema-per-agent means ~25 relations per agent. Fine for thousands; for much larger counts, a native storage with an
  `agent_id` column and row-level security may be needed.
- Commit latency is a network round trip; Pi's `settings.progress` intervals can be raised if needed.
- pi-durable is experimental and pinned to 1.3.0; the SQL facade rejects SQLite constructs it does not translate, and
  the conformance suite guards every upgrade.
- Scheduled work: the platform's triggers wake agents (see Triggers); an agent can't yet set its own schedule (the
  `schedule` tool waits for the runtime library, #28; its gateway API is designed in docs/triggers.md). A triggered
  run's cost counts all the agent's usage while it ran (usage isn't tagged by thread).
- An agent can't yet ask to be woken at a time it chooses: pi-durable's coming wake-up API is the way (see
  [docs/hibernation.md](docs/hibernation.md#when-pi-durable-ships-a-wake-up-api)). Background work that runs keeps an
  agent awake, up to `AGENTS_BACKGROUND_MAX`.
- Hostile-code isolation (gVisor/Kata on Talos) is required before self-service uploads.
- Usage per connected app (OAuth Connect) is attributed by which app's person an agent is answering at the moment
  of each model or tool request, and only when that is unambiguous: an agent answering two apps (or an app and the
  console) at once records neither, and a shared thread's reply counts for its latest sender. Exact attribution
  needs the runtime to tag each request with its thread.
- The MCP identity token trusts the runtime to name the thread a call belongs to among its own agent's threads: a
  hostile runtime in a shared team could name a private thread of the same agent while working on another and get a
  token naming that thread's creator (the agent already acts in that person's private conversation). Binding a call
  to the run in progress is the planned fix before team code runs in runtimes (#29); see
  [docs/mcp-identity.md](docs/mcp-identity.md).
- Client-chosen thread IDs are guessable if the client picks guessable ones: whoever uses an ID first starts that
  thread (a shared one anyone allowed on the agent may join; a private one is 404 for everyone else). Use
  `POST …/threads` or random IDs. (The v0.1.0 pre-seeding of thread MCP servers is closed: see Threads.) For the
  same reason a 404 `thread_not_found` on a client-chosen ID tells the caller that the ID is someone's private
  thread (every route does, since a free ID would have worked): that can't be hidden without refusing free IDs too,
  and platform-chosen IDs (128 random bits) can't be probed that way.
