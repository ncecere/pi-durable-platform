-- Agent-made schedules (#15, docs/triggers.md "Agent-made schedules"): the
-- runtime library's schedule tool lets an agent set its own reminders and
-- recurring work, as ordinary triggers marked as made by the agent.
--
--   * agent_template_versions.agent_schedules: whether the version's agents
--     may schedule (off by default).
--   * agent_triggers.once: a one-time reminder, removed when it fires (its
--     cron names the same minute, so the record reads as any schedule; the
--     time itself is next_fire_at).
--   * agent_triggers.origin_thread_id: the thread the agent set it in (an
--     agent-made trigger fires there, thread_id, when that thread may take a
--     trigger's message; otherwise into a new thread).
--   * trigger_runs.trigger_source: whether the run's trigger was made by a
--     person or by the agent (kept when a one-time reminder is gone: its
--     delivery may go to its workspace owner's private thread).
--   * audit_log: agents become actors (actor_kind agent; the agent is named
--     in the entry's metadata, agentActorId and agentActorName).
--
-- Expand only: the previous release ignores the new columns (it would read a
-- one-time reminder as a yearly schedule) and writes no agent actor.

-- +goose Up
ALTER TABLE agent_template_versions ADD COLUMN agent_schedules boolean NOT NULL DEFAULT false;

ALTER TABLE agent_triggers
    ADD COLUMN once             boolean NOT NULL DEFAULT false,
    ADD COLUMN origin_thread_id text    CHECK (origin_thread_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    ADD CONSTRAINT agent_triggers_agent_made CHECK (source = 'agent' OR (NOT once AND origin_thread_id IS NULL)),
    ADD CONSTRAINT agent_triggers_once_schedule CHECK (NOT once OR kind = 'schedule');
CREATE INDEX agent_triggers_agent_made ON agent_triggers (agent_id) WHERE source = 'agent';

ALTER TABLE trigger_runs
    ADD COLUMN trigger_source text NOT NULL DEFAULT 'person' CHECK (trigger_source IN ('person', 'agent'));

ALTER TABLE audit_log DROP CONSTRAINT audit_log_actor_kind_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_actor_kind_check
    CHECK (actor_kind IN ('user', 'api_key', 'service_account', 'oauth', 'system', 'agent'));

-- +goose Down
-- The audit log is append-only: entries by agents are kept, and only new rows
-- are checked.
ALTER TABLE audit_log DROP CONSTRAINT audit_log_actor_kind_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_actor_kind_check
    CHECK (actor_kind IN ('user', 'api_key', 'service_account', 'oauth', 'system')) NOT VALID;
ALTER TABLE trigger_runs DROP COLUMN trigger_source;
DROP INDEX agent_triggers_agent_made;
ALTER TABLE agent_triggers DROP CONSTRAINT agent_triggers_once_schedule, DROP CONSTRAINT agent_triggers_agent_made,
    DROP COLUMN origin_thread_id, DROP COLUMN once;
ALTER TABLE agent_template_versions DROP COLUMN agent_schedules;
