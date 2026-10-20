-- Triggers, their agents' limits, and their runs (internal/triggers;
-- docs/triggers.md). Times come from the caller's clock (sqlc.arg(now)::timestamptz), so
-- the trigger loop can be tested with a clock of its own.

-- name: InsertTrigger :one
-- source: person (the console) or agent (its schedule tool: once and
-- origin_thread_id are the agent's).
INSERT INTO agent_triggers (agent_id, kind, name, source, once, origin_thread_id, thread_id, message_template, cron,
                            timezone, missed_policy, next_fire_at, hook_id, webhook_secret_enc, webhook_secret_prefix,
                            filter_path, filter_equals, callback_url, callback_secret_enc, callback_secret_prefix,
                            daily_budget_usd, created_by, created_at, updated_at)
VALUES (sqlc.arg(agent_id), sqlc.arg(kind), sqlc.arg(name), sqlc.arg(source), sqlc.arg(once), sqlc.narg(origin_thread_id),
        sqlc.narg(thread_id), sqlc.arg(message_template),
        sqlc.narg(cron), sqlc.narg(timezone), sqlc.narg(missed_policy), sqlc.narg(next_fire_at), sqlc.narg(hook_id),
        sqlc.narg(webhook_secret_enc), sqlc.narg(webhook_secret_prefix), sqlc.narg(filter_path), sqlc.narg(filter_equals),
        sqlc.narg(callback_url), sqlc.narg(callback_secret_enc), sqlc.narg(callback_secret_prefix),
        sqlc.narg(daily_budget_usd), sqlc.narg(created_by), sqlc.arg(now)::timestamptz, sqlc.arg(now)::timestamptz)
RETURNING *;

-- name: TriggerSetSecrets :exec
-- A new trigger's sealed secrets (sealed after insert: the associated data is
-- its ID).
UPDATE agent_triggers
SET webhook_secret_enc = coalesce(sqlc.narg(webhook_secret_enc), webhook_secret_enc),
    callback_secret_enc = coalesce(sqlc.narg(callback_secret_enc), callback_secret_enc)
WHERE id = sqlc.arg(id);

-- name: GetTrigger :one
SELECT * FROM agent_triggers WHERE id = sqlc.arg(id) AND agent_id = sqlc.arg(agent_id);

-- name: GetTriggerByID :one
SELECT * FROM agent_triggers WHERE id = $1;

-- name: LockTrigger :one
SELECT * FROM agent_triggers WHERE id = sqlc.arg(id) AND agent_id = sqlc.arg(agent_id) FOR UPDATE;

-- name: GetTriggerByHook :one
SELECT * FROM agent_triggers WHERE hook_id = $1 AND kind = 'webhook';

-- name: ListAgentTriggers :many
SELECT * FROM agent_triggers WHERE agent_id = $1 ORDER BY created_at, id;

-- name: CountAgentTriggers :one
SELECT count(*) FROM agent_triggers WHERE agent_id = $1;

-- name: ListAgentMadeTriggers :many
-- The triggers the agent made itself (its schedule tool).
SELECT * FROM agent_triggers WHERE agent_id = $1 AND source = 'agent' ORDER BY created_at, id;

-- name: CountAgentMadeTriggers :one
SELECT count(*) FROM agent_triggers WHERE agent_id = $1 AND source = 'agent';

-- name: TriggerThreadInfo :one
-- What decides whether one of the agent's threads may take an agent-made
-- trigger's message: the agent's team (a personal workspace's owner), and the
-- thread as the platform records it (no record: known is false), with
-- whether an app gave it MCP servers.
SELECT a.team_id, t.kind AS team_kind, t.personal_owner, (th.thread_id IS NOT NULL)::boolean AS known,
       th.visibility, coalesce(th.reserved, false)::boolean AS reserved, th.created_by_user,
       EXISTS (SELECT 1 FROM thread_mcp_servers s WHERE s.agent_id = a.id AND s.thread_id = sqlc.arg(thread_id)::text)::boolean AS has_servers
FROM agents a
JOIN teams t ON t.id = a.team_id
LEFT JOIN agent_threads th ON th.agent_id = a.id AND th.thread_id = sqlc.arg(thread_id)::text
WHERE a.id = sqlc.arg(agent_id);

-- name: DeleteAgentTriggers :execrows
-- A moved agent's triggers (docs/triggers.md: they belonged to the old team).
DELETE FROM agent_triggers WHERE agent_id = $1;

-- name: UpdateTrigger :one
-- A person's change: every changeable field as it is to be.
UPDATE agent_triggers
SET name = sqlc.arg(name), paused = sqlc.arg(paused), paused_reason = sqlc.narg(paused_reason),
    thread_id = sqlc.narg(thread_id), message_template = sqlc.arg(message_template), cron = sqlc.narg(cron),
    timezone = sqlc.narg(timezone), missed_policy = sqlc.narg(missed_policy), next_fire_at = sqlc.narg(next_fire_at),
    webhook_secret_enc = sqlc.narg(webhook_secret_enc), webhook_secret_prefix = sqlc.narg(webhook_secret_prefix),
    filter_path = sqlc.narg(filter_path), filter_equals = sqlc.narg(filter_equals), callback_url = sqlc.narg(callback_url),
    callback_secret_enc = sqlc.narg(callback_secret_enc), callback_secret_prefix = sqlc.narg(callback_secret_prefix),
    daily_budget_usd = sqlc.narg(daily_budget_usd), consecutive_failures = sqlc.arg(consecutive_failures),
    backoff_until = sqlc.narg(backoff_until), revision = revision + 1, updated_at = sqlc.arg(now)::timestamptz
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteTrigger :execrows
DELETE FROM agent_triggers WHERE id = sqlc.arg(id) AND agent_id = sqlc.arg(agent_id);

-- name: DueSchedules :many
-- Schedules due at now, with their agent and team, locked (a replica that
-- fires them advances them in the same transaction; another skips them).
SELECT sqlc.embed(t), a.team_id, a.enabled AS agent_enabled, tm.status AS team_status
FROM agent_triggers t
JOIN agents a ON a.id = t.agent_id
JOIN teams tm ON tm.id = a.team_id
WHERE t.kind = 'schedule' AND NOT t.paused AND t.next_fire_at <= sqlc.arg(now)::timestamptz
ORDER BY t.next_fire_at
LIMIT 100
FOR UPDATE OF t SKIP LOCKED;

-- name: AdvanceSchedule :exec
UPDATE agent_triggers SET next_fire_at = sqlc.narg(next_fire_at), last_fired_at = coalesce(sqlc.narg(fired_at), last_fired_at)
WHERE id = sqlc.arg(id);

-- name: TriggerFired :exec
UPDATE agent_triggers SET last_fired_at = sqlc.arg(at)::timestamptz WHERE id = sqlc.arg(id);

-- name: TriggerOutcome :one
-- A run of the trigger ended: failures in a row, back-off, and an automatic
-- pause (paused_reason failures) at the limit. Not a person's change: the
-- revision stays.
UPDATE agent_triggers
SET consecutive_failures = sqlc.arg(consecutive_failures), backoff_until = sqlc.narg(backoff_until),
    paused = paused OR sqlc.arg(pause)::boolean,
    paused_reason = CASE WHEN sqlc.arg(pause)::boolean AND NOT paused THEN 'failures' ELSE paused_reason END
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: GetTriggerSettings :one
SELECT * FROM agent_trigger_settings WHERE agent_id = $1;

-- name: UpsertTriggerSettings :one
-- expected_revision: the revision the caller loaded (the row's, when it has one).
-- A new row starts at revision 2: the defaults it replaces were revision 1.
INSERT INTO agent_trigger_settings AS s (agent_id, paused, max_concurrent_runs, max_run_seconds, daily_budget_usd, revision, updated_at)
VALUES (sqlc.arg(agent_id), sqlc.arg(paused), sqlc.narg(max_concurrent_runs), sqlc.narg(max_run_seconds),
        sqlc.narg(daily_budget_usd), 2, sqlc.arg(now)::timestamptz)
ON CONFLICT (agent_id) DO UPDATE SET paused = EXCLUDED.paused, max_concurrent_runs = EXCLUDED.max_concurrent_runs,
    max_run_seconds = EXCLUDED.max_run_seconds, daily_budget_usd = EXCLUDED.daily_budget_usd,
    revision = s.revision + 1, updated_at = EXCLUDED.updated_at
WHERE s.revision = sqlc.arg(expected_revision)
RETURNING *;

-- name: LockAgentRuns :exec
-- Serialises the decisions about one agent's runs (runs at once, budgets)
-- until the transaction ends, on every replica.
SELECT pg_advisory_xact_lock(hashtextextended('pdp.trigger-runs:' || sqlc.arg(agent_id)::uuid::text, 0));

-- name: ActiveRunCount :one
SELECT count(*) FROM trigger_runs WHERE agent_id = $1 AND status IN ('pending', 'running');

-- name: TriggerCostSince :one
SELECT coalesce(sum(cost_usd), 0)::float8 FROM trigger_runs WHERE trigger_id = sqlc.arg(trigger_id) AND created_at >= sqlc.arg(since);

-- name: AgentRunCostSince :one
SELECT coalesce(sum(cost_usd), 0)::float8 FROM trigger_runs WHERE agent_id = sqlc.arg(agent_id) AND created_at >= sqlc.arg(since);

-- name: InsertRun :one
-- A fire's run, unless one with its fire ID exists (nothing returned then).
INSERT INTO trigger_runs (fire_id, trigger_id, agent_id, team_id, trigger_name, trigger_kind, trigger_source, fired_by,
                          thread_id, status, reason, scheduled_for, created_at, finished_at)
VALUES (sqlc.arg(fire_id), sqlc.narg(trigger_id), sqlc.arg(agent_id), sqlc.arg(team_id), sqlc.arg(trigger_name),
        sqlc.arg(trigger_kind), sqlc.arg(trigger_source), sqlc.arg(fired_by), sqlc.narg(thread_id), sqlc.arg(status), sqlc.narg(reason),
        sqlc.narg(scheduled_for), sqlc.arg(now)::timestamptz, sqlc.narg(finished_at))
ON CONFLICT (fire_id) DO NOTHING
RETURNING *;

-- name: SetRunMessage :exec
UPDATE trigger_runs SET message_enc = sqlc.arg(message_enc) WHERE id = sqlc.arg(id);

-- name: GetRunByFireID :one
SELECT * FROM trigger_runs WHERE fire_id = $1;

-- name: GetRun :one
SELECT * FROM trigger_runs WHERE id = sqlc.arg(id) AND agent_id = sqlc.arg(agent_id);

-- name: GetRunByID :one
SELECT * FROM trigger_runs WHERE id = $1;

-- name: ListAgentRuns :many
-- An agent's runs, newest first, optionally one trigger's or one status's,
-- after a cursor (created_at, id), at most row_limit.
SELECT * FROM trigger_runs
WHERE agent_id = sqlc.arg(agent_id)
  AND (sqlc.narg(trigger_id)::uuid IS NULL OR trigger_id = sqlc.narg(trigger_id)::uuid)
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
  AND (sqlc.narg(after_at)::timestamptz IS NULL
       OR created_at < sqlc.narg(after_at)::timestamptz
       OR (created_at = sqlc.narg(after_at)::timestamptz AND id < sqlc.narg(after_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListTeamRuns :many
-- A team's runs, newest first, of the agents in agent_ids (those the caller
-- may use).
SELECT * FROM trigger_runs
WHERE team_id = sqlc.arg(team_id) AND agent_id = ANY(sqlc.arg(agent_ids)::uuid[])
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
  AND (sqlc.narg(after_at)::timestamptz IS NULL
       OR created_at < sqlc.narg(after_at)::timestamptz
       OR (created_at = sqlc.narg(after_at)::timestamptz AND id < sqlc.narg(after_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: PendingRunsDue :many
SELECT id FROM trigger_runs
WHERE status = 'pending' AND (next_attempt_at IS NULL OR next_attempt_at <= sqlc.arg(now)::timestamptz)
ORDER BY created_at
LIMIT 100;

-- name: ClaimRunDelivery :one
-- A lease on a pending run's delivery until lease: one replica delivers it
-- at a time; after the lease any replica may try again.
UPDATE trigger_runs SET attempts = attempts + 1, next_attempt_at = sqlc.arg(lease)::timestamptz
WHERE id = sqlc.arg(id) AND status = 'pending' AND (next_attempt_at IS NULL OR next_attempt_at <= sqlc.arg(now)::timestamptz)
RETURNING *;

-- name: RunStarted :one
-- The runtime accepted the run's message: the sealed copy goes.
UPDATE trigger_runs SET status = 'running', started_at = sqlc.arg(at)::timestamptz, message_enc = NULL, next_attempt_at = NULL
WHERE id = sqlc.arg(id) AND status = 'pending'
RETURNING *;

-- name: RunRetryAt :exec
UPDATE trigger_runs SET next_attempt_at = sqlc.arg(at)::timestamptz WHERE id = sqlc.arg(id) AND status = 'pending';

-- name: FinishRun :one
-- A run ended (or never started). Only a pending or running run finishes, so
-- a replica that decided late changes nothing. With a callback it is due now.
UPDATE trigger_runs
SET status = sqlc.arg(status), reason = sqlc.narg(reason), finished_at = sqlc.arg(at)::timestamptz, cost_usd = sqlc.arg(cost_usd),
    requests = sqlc.arg(requests), message_enc = NULL, next_attempt_at = NULL,
    callback_status = CASE WHEN sqlc.arg(callback)::boolean THEN 'pending' END,
    callback_next_at = CASE WHEN sqlc.arg(callback)::boolean THEN sqlc.arg(at)::timestamptz END
WHERE id = sqlc.arg(id) AND status IN ('pending', 'running')
RETURNING *;

-- name: RunningRuns :many
-- Runs in progress, with their thread's status as the runtime last reported
-- it, and their agent's limits.
SELECT sqlc.embed(r), th.busy AS thread_busy, th.status_at AS thread_status_at, th.last_error AS thread_last_error,
       s.max_run_seconds, s.daily_budget_usd AS agent_daily_budget_usd, t.daily_budget_usd AS trigger_daily_budget_usd
FROM trigger_runs r
LEFT JOIN agent_threads th ON th.agent_id = r.agent_id AND th.thread_id = r.thread_id
LEFT JOIN agent_trigger_settings s ON s.agent_id = r.agent_id
LEFT JOIN agent_triggers t ON t.id = r.trigger_id
WHERE r.status = 'running'
ORDER BY r.started_at
LIMIT 500;

-- name: UpdateRunCost :exec
UPDATE trigger_runs SET cost_usd = sqlc.arg(cost_usd), requests = sqlc.arg(requests) WHERE id = sqlc.arg(id) AND status = 'running';

-- name: AgentUsageBetween :one
-- An agent's model usage in [since, until): its cost at the models' current
-- prices (USD per million tokens, as the usage reports) and requests.
SELECT coalesce(sum(coalesce(u.input_tokens * m.input_price, 0) + coalesce(u.output_tokens * m.output_price, 0)) / 1e6, 0)::float8 AS cost_usd,
       coalesce(sum(u.requests), 0)::bigint AS requests
FROM model_usage u
LEFT JOIN models m ON m.id = u.model_id
WHERE u.agent_id = sqlc.arg(agent_id)::uuid AND u.created_at >= sqlc.arg(since) AND u.created_at < sqlc.arg(until);

-- name: CallbacksDue :many
SELECT id FROM trigger_runs
WHERE callback_status = 'pending' AND callback_next_at <= sqlc.arg(now)::timestamptz
ORDER BY callback_next_at
LIMIT 50;

-- name: ClaimCallback :one
-- A lease on a run's callback until lease: one replica sends it at a time.
UPDATE trigger_runs SET callback_next_at = sqlc.arg(lease)::timestamptz
WHERE id = sqlc.arg(id) AND callback_status = 'pending' AND callback_next_at <= sqlc.arg(now)::timestamptz
RETURNING *;

-- name: CallbackResult :exec
UPDATE trigger_runs SET callback_status = sqlc.arg(callback_status)::text, callback_attempts = sqlc.arg(callback_attempts),
    callback_next_at = sqlc.narg(callback_next_at)
WHERE id = sqlc.arg(id);

-- name: AgentTeamSlug :one
SELECT t.slug FROM agents a JOIN teams t ON t.id = a.team_id WHERE a.id = $1;
