-- Triggers: schedules and webhooks that start runs of an agent (#15;
-- docs/triggers.md).
--
--   * agent_triggers: one agent's schedule (cron in a time zone, its next
--     fire time and missed-fire policy) or webhook (an unguessable hook ID and
--     a secret sealed with ENCRYPTION_KEY, an optional filter), the thread and
--     message template a fire sends, an optional completion callback (its
--     secret sealed too), a daily budget, a pause switch, and the failures in
--     a row that back it off and, at the limit, pause it.
--   * agent_trigger_settings: an agent's limits for its triggers' runs (a
--     missing row, or a NULL, is the platform's default).
--   * trigger_runs: one record per fire. fire_id is the message's pi-durable
--     requestId and is unique: a second replica, a retried delivery or a
--     replayed webhook can't make a second run. The rendered message is kept
--     sealed only until it has been delivered. team_id is the agent's team when
--     it fired (runs stay with the team they happened in).
--
-- Expand only: the previous release ignores the new tables.

-- +goose Up
CREATE TABLE agent_triggers (
    id                     uuid             PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id               uuid             NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    kind                   text             NOT NULL CHECK (kind IN ('schedule', 'webhook')),
    name                   text             NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    -- Who made it: a person (the console) or, later, the agent itself (#28).
    source                 text             NOT NULL DEFAULT 'person' CHECK (source IN ('person', 'agent')),
    paused                 boolean          NOT NULL DEFAULT false,
    paused_reason          text             CHECK (paused_reason IN ('user', 'failures')),
    -- NULL: a new thread at every fire.
    thread_id              text             CHECK (thread_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    message_template       text             NOT NULL CHECK (char_length(message_template) BETWEEN 1 AND 8000),
    cron                   text             CHECK (char_length(cron) BETWEEN 1 AND 200),
    timezone               text             CHECK (char_length(timezone) BETWEEN 1 AND 64),
    missed_policy          text             CHECK (missed_policy IN ('skip', 'run_once')),
    next_fire_at           timestamptz,
    hook_id                text             CONSTRAINT agent_triggers_hook_key UNIQUE CHECK (hook_id ~ '^h_[A-Za-z0-9_-]{22}$'),
    webhook_secret_enc     bytea,
    webhook_secret_prefix  text             CHECK (char_length(webhook_secret_prefix) <= 20),
    filter_path            text             CHECK (char_length(filter_path) BETWEEN 1 AND 200),
    filter_equals          text             CHECK (char_length(filter_equals) <= 1000),
    callback_url           text             CHECK (char_length(callback_url) BETWEEN 1 AND 2000),
    callback_secret_enc    bytea,
    callback_secret_prefix text             CHECK (char_length(callback_secret_prefix) <= 20),
    daily_budget_usd       double precision CHECK (daily_budget_usd >= 0),
    consecutive_failures   integer          NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    backoff_until          timestamptz,
    last_fired_at          timestamptz,
    created_by             uuid             REFERENCES users (id) ON DELETE SET NULL,
    created_at             timestamptz      NOT NULL DEFAULT now(),
    updated_at             timestamptz      NOT NULL DEFAULT now(),
    revision               bigint           NOT NULL DEFAULT 1,
    CONSTRAINT agent_triggers_name_key UNIQUE (agent_id, name),
    CHECK ((kind = 'schedule') = (cron IS NOT NULL AND timezone IS NOT NULL AND missed_policy IS NOT NULL)),
    CHECK ((kind = 'webhook') = (hook_id IS NOT NULL AND webhook_secret_enc IS NOT NULL)),
    CHECK ((filter_path IS NULL) = (filter_equals IS NULL)),
    CHECK ((callback_url IS NULL) = (callback_secret_enc IS NULL))
);
CREATE INDEX agent_triggers_agent ON agent_triggers (agent_id, created_at);
CREATE INDEX agent_triggers_due ON agent_triggers (next_fire_at) WHERE kind = 'schedule' AND NOT paused;

CREATE TABLE agent_trigger_settings (
    agent_id            uuid             PRIMARY KEY REFERENCES agents (id) ON DELETE CASCADE,
    paused              boolean          NOT NULL DEFAULT false,
    max_concurrent_runs integer          CHECK (max_concurrent_runs BETWEEN 1 AND 100),
    max_run_seconds     integer          CHECK (max_run_seconds BETWEEN 60 AND 86400),
    daily_budget_usd    double precision CHECK (daily_budget_usd >= 0),
    revision            bigint           NOT NULL DEFAULT 1,
    updated_at          timestamptz      NOT NULL DEFAULT now()
);

CREATE TABLE trigger_runs (
    id                uuid             PRIMARY KEY DEFAULT gen_random_uuid(),
    fire_id           text             NOT NULL CONSTRAINT trigger_runs_fire_key UNIQUE CHECK (fire_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    trigger_id        uuid             REFERENCES agent_triggers (id) ON DELETE SET NULL,
    agent_id          uuid             NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    team_id           uuid             NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    trigger_name      text             NOT NULL CHECK (char_length(trigger_name) BETWEEN 1 AND 100),
    trigger_kind      text             NOT NULL CHECK (trigger_kind IN ('schedule', 'webhook')),
    fired_by          text             NOT NULL CHECK (fired_by IN ('schedule', 'webhook', 'manual')),
    thread_id         text             CHECK (thread_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    -- The rendered message, sealed until it has been delivered.
    message_enc       bytea,
    status            text             NOT NULL CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'stopped', 'skipped')),
    reason            text             CHECK (char_length(reason) <= 200),
    scheduled_for     timestamptz,
    attempts          integer          NOT NULL DEFAULT 0,
    next_attempt_at   timestamptz,
    created_at        timestamptz      NOT NULL,
    started_at        timestamptz,
    finished_at       timestamptz,
    cost_usd          double precision NOT NULL DEFAULT 0,
    requests          bigint           NOT NULL DEFAULT 0,
    callback_status   text             CHECK (callback_status IN ('pending', 'delivered', 'failed')),
    callback_attempts integer          NOT NULL DEFAULT 0,
    callback_next_at  timestamptz
);
CREATE INDEX trigger_runs_agent ON trigger_runs (agent_id, created_at DESC, id);
CREATE INDEX trigger_runs_team ON trigger_runs (team_id, created_at DESC, id);
CREATE INDEX trigger_runs_trigger ON trigger_runs (trigger_id, created_at);
CREATE INDEX trigger_runs_active ON trigger_runs (agent_id) WHERE status IN ('pending', 'running');
CREATE INDEX trigger_runs_callbacks ON trigger_runs (callback_next_at) WHERE callback_status = 'pending';

-- +goose Down
DROP TABLE trigger_runs;
DROP TABLE agent_trigger_settings;
DROP TABLE agent_triggers;
