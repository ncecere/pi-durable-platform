# Running several platform replicas

Design note for roadmap item B ([#2](https://github.com/ncecere/pi-durable-platform/issues/2)). The platform runs as one
Deployment of any number of replicas behind one Service; `replicas: 2` is the supported, tested minimum for availability
during node drains and rollouts.

## Principle

A replica keeps nothing that another replica could contradict. Everything that decides something is in PostgreSQL (or in
the Kubernetes API, or in the runtime itself); what a replica keeps in memory is a cache or a hint that saves calls.

## Inventory of per-process state

| State | Where | With N replicas |
|---|---|---|
| Sessions, CSRF tokens, sign-in state (OIDC state, nonce, PKCE verifier) | PostgreSQL (`sessions`, `login_states`) | Safe: a sign-in started on one replica ends on another |
| OAuth Connect: authorization request, consent, codes, PKCE challenge, tokens, grants | The request travels with the browser; codes and tokens in PostgreSQL | Safe |
| Rate limits (per person, per address, sign-in attempts) | PostgreSQL (`rate_limit_counters`) | Safe: counted once, shared |
| Thread record and status (`agent_threads`), runtime reports (`/threads/report`, `/threads/sync`) | PostgreSQL; the gateway Service picks any replica | Safe |
| Thread events | `NOTIFY pdp_threads` after the change, every replica `LISTEN`s and fans out to its own subscribers | Safe: a subscriber on one replica hears changes made through another |
| Gateway and MCP proxy (model proxy, MCP gateway) | Stateless per request; `Mcp-Session-Id` is the upstream's | Safe |
| Background loops: session, OAuth and thread clean-up; rate counter clean-up | Idempotent deletes | Safe to run on every replica |
| Group re-check | People are claimed with `FOR UPDATE SKIP LOCKED` and marked checked | Safe: each person is asked about once per interval |
| Agent activity for the idle reaper: open streams, last request | **Was** this replica's memory | Made safe, below |
| Waking an agent | **Was** one process's mutex (single flight) | Made safe, below |
| Idle reaper | **Was** every replica reaping on its own view | Made safe, below |
| OIDC discovery; OAuth client metadata documents and logos; team model and MCP connections | In-memory caches with a TTL | Unchanged: each replica fetches and caches on its own. A consent page's logo is loaded by a second request that may land on a replica that has not cached the document yet: the page then shows the app's initials. Closing connections after a settings change reaches other replicas within 30 seconds |
| Soft limit of running agents per personal workspace (`too_many_running`) | Check, then wake | Unchanged: concurrent wakes can exceed it by one, as they could in one process |
| `local` launcher | Child processes of one platform process | Single-process by nature: use `kubernetes` or `agent-sandbox` with several replicas |

## The reaper

The reaper hibernates an agent idle for `AGENTS_IDLE_TIMEOUT`: no thread busy, no event stream open, no request.

*Considered:* recording activity in PostgreSQL (a row per agent with the last request and, for each open stream, a
replica ID and a heartbeat). It adds a write to every request (or a coalescing layer), a heartbeat that must outlive a
replica that dies, and a clock-skew question; and the runtime already knows the truth, because every request and every
stream goes through it.

*Chosen:* **the runtime is the source of truth.** `/status` returns `busyThreads`, `lastActivityAt` and now
`openStreams` (the count of open event streams; closing the last one counts as activity, so the idle clock starts when
the last watcher leaves). The reaper hibernates an agent only when the runtime says it has no work, no open stream and
no activity for the idle timeout. Requests and streams that went through any replica are therefore seen, and a replica
that has seen nothing of an agent cannot hibernate it by mistake. A stream whose replica died is closed by the runtime
when its connection fails (it writes a keep-alive every 15 seconds).

- Only one replica reaps at a time: each pass takes a PostgreSQL advisory lock (`pdp.agent-reaper`, `pg_try_advisory_lock`
  on a connection of its own) and skips the pass when another replica holds it. This saves duplicate `/status` calls; it
  is not needed for correctness, since a second `Stop` is harmless. The lock ends with its holder.
- What the replica has seen locally (streams, last request) still skips agents that are plainly active without asking
  the runtime, and a replica remembers when an agent can first be idle (its `lastActivityAt` plus the timeout) and does
  not ask before. A single replica asks no more often than before.
- A runtime that predates `openStreams` (an agent started before the platform was upgraded) reports none: the reaper
  then goes by its own replica's streams, as v0.1 did, until that agent next restarts.
- As with one replica, a request that reaches the runtime in the moment between the reaper's `/status` and its stop
  finds a runtime shutting down; the client retries and wakes it again. Work in flight stays in storage.

## Waking

A request for an agent first asks the launcher whether the runtime is running (`Lookup`: one read of the pod, sandbox or
claim, what `Ensure` did first anyway). Only when it is not, the replica queues behind other requests of its own for the
same agent, takes a PostgreSQL advisory lock for the agent (`pdp.agent-wake:<id>`, on a connection of its own, waiting
until the request ends, at most three minutes) and calls `Ensure`, which is idempotent and so re-checks under the lock:
two replicas waking one agent start one runtime, the second finds it. The lock is held while the runtime starts and ends
with its holder. A running agent costs no lock.

The launchers' own safeguards stay: the agent's storage ownership record refuses a second runtime for one agent, whatever
starts it.

These locks (the wake locks, the reaper's, the migration lock) are in the platform database, and advisory locks are per
database, so they are out of an agent's reach exactly when its role cannot connect to that database. PostgreSQL lets PUBLIC
connect by default: revoke it (`REVOKE CONNECT ON DATABASE <platform database> FROM PUBLIC`, then grant it to the platform's
role); `doctor` warns until that is done. See "Where an agent's role can connect" in [DESIGN.md](../DESIGN.md).

## Deployment

`replicaCount` may be greater than 1 (the schema no longer refuses it; the default stays 1). With more than one replica:
a rolling update (`maxUnavailable: 0`, so there is always a ready replica), pod anti-affinity (preferred, by node) and a
topology spread across zones (`ScheduleAnyway`) by default, and `podDisruptionBudget` is documented with `minAvailable: 1`.
Event streams end with their replica (shutdown closes them) and browsers reconnect to another one.

## Not covered

- `AGENTS_LAUNCHER=local` is one process. Two platform processes on one host would each start their own child for an
  agent and the second would be refused by the storage ownership record (`ErrOwnedElsewhere`).
- A network partition that leaves a replica holding the wake lock is bounded by the three-minute wait of the others (they
  give up with an error), not by PostgreSQL noticing the dead connection.
- Thread events are `LISTEN/NOTIFY`: a replica whose listener connection drops misses the changes announced until it
  reconnects (two seconds), as with one replica.
