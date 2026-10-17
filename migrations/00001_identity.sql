-- Identity, browser sessions, OIDC sign-in state, the append-only audit log
-- and the shared rate limit counters.
--
-- Migration rules: once a migration has been applied anywhere, never edit it;
-- add the next number. Every migration must be compatible with the previous
-- release's code (expand/contract). Never rename or drop in the same release
-- that stops using a column; ship the destructive step one release later.

-- +goose Up
CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE users (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    oidc_issuer   text        NOT NULL,
    oidc_subject  text        NOT NULL,
    email         citext      NOT NULL,
    display_name  text        NOT NULL DEFAULT '',
    platform_role text        NOT NULL DEFAULT 'none'
        CHECK (platform_role IN ('none', 'platform_admin', 'platform_auditor')),
    status        text        NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'suspended')),
    -- All OIDC claims from the latest login, kept so access can later be
    -- granted by affiliation or group without a redesign.
    claims        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    -- The IdP groups of the latest sign-in (the OIDC_GROUPS_CLAIM claim,
    -- bounded).
    groups        text[]      NOT NULL DEFAULT '{}'
        CONSTRAINT users_groups_bounded CHECK (cardinality(groups) <= 200),
    -- Optimistic-concurrency revision for admin edits (If-Match).
    revision      bigint      NOT NULL DEFAULT 1,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    last_login_at timestamptz,
    CONSTRAINT users_identity_key UNIQUE (oidc_issuer, oidc_subject)
);
CREATE INDEX users_email_idx ON users (email);

-- Records that the configured bootstrap identity was promoted once, so a later
-- intentional demotion is not undone by logging in again.
CREATE TABLE platform_bootstrap (
    oidc_issuer  text        NOT NULL,
    oidc_subject text        NOT NULL,
    applied_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (oidc_issuer, oidc_subject)
);

CREATE TABLE sessions (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- SHA-256 of the cookie secret; the secret itself is never stored.
    token_digest text        NOT NULL UNIQUE,
    csrf_token   text        NOT NULL,
    user_agent   text        NOT NULL DEFAULT '',
    client_ip    text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL
);
CREATE INDEX sessions_user_idx ON sessions (user_id);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

-- Pending OIDC sign-ins (state, PKCE verifier, nonce), keyed by the SHA-256
-- of the state value. Each row is used once: the callback deletes it.
CREATE TABLE login_states (
    state_digest text        PRIMARY KEY,
    verifier     text        NOT NULL,
    nonce        text        NOT NULL,
    next_path    text        NOT NULL DEFAULT '/',
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL
);
CREATE INDEX login_states_expires_idx ON login_states (expires_at);

CREATE TABLE audit_log (
    id            bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at   timestamptz NOT NULL DEFAULT now(),
    actor_kind    text        NOT NULL CHECK (actor_kind IN ('user', 'api_key', 'system')),
    actor_user_id uuid        REFERENCES users (id),
    team_id       uuid,
    action        text        NOT NULL,
    target_type   text        NOT NULL,
    target_id     text        NOT NULL DEFAULT '',
    before_state  jsonb,
    after_state   jsonb,
    metadata      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    request_id    text        NOT NULL DEFAULT '',
    client_ip     text        NOT NULL DEFAULT ''
);
CREATE INDEX audit_log_occurred_idx ON audit_log (occurred_at DESC);
CREATE INDEX audit_log_team_idx ON audit_log (team_id, occurred_at DESC) WHERE team_id IS NOT NULL;
CREATE INDEX audit_log_actor_idx ON audit_log (actor_user_id, occurred_at DESC);
-- Reading the log names each entry's target; for a target that no longer
-- exists it uses the last name the log recorded for the same target.
CREATE INDEX audit_log_target_idx ON audit_log (target_type, target_id, id DESC);

-- The audit log is append-only. Updates are always rejected. Deletes are only
-- allowed for a retention job, which sets pdp.audit_purge = 'on' for its
-- transaction.
-- +goose StatementBegin
CREATE FUNCTION audit_log_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' AND current_setting('pdp.audit_purge', true) = 'on' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'audit_log is append-only';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER audit_log_append_only
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_append_only();

-- Rate limits (sign-in attempts per address, /v1 requests per person) in
-- fixed one-minute windows shared by every replica (internal/ratelimit).
-- UNLOGGED: counters may be lost on a crash or failover, by design.
CREATE UNLOGGED TABLE rate_limit_counters (
    bucket       text        NOT NULL CHECK (char_length(bucket) <= 200),
    window_start timestamptz NOT NULL,
    count        integer     NOT NULL DEFAULT 1,
    PRIMARY KEY (bucket, window_start)
);
CREATE INDEX rate_limit_counters_window_idx ON rate_limit_counters (window_start);

-- +goose Down
DROP TABLE rate_limit_counters;
DROP TABLE audit_log;
DROP FUNCTION audit_log_append_only();
DROP TABLE login_states;
DROP TABLE sessions;
DROP TABLE platform_bootstrap;
DROP TABLE users;
