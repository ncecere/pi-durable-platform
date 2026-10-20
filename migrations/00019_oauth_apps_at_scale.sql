-- OAuth Connect for apps with many people (#12; DESIGN.md "OAuth Connect").
--
--   * oauth_grants.scopes gains agents:read (list agents and threads, read
--     conversations, follow events; no messages, no changes). agents
--     includes it. oauth_codes.scopes and oauth_tokens.scopes are what one
--     code or token carries (at most its grant's; NULL: the grant's, for
--     rows issued before this release).
--   * oauth_client_subjects: an app-stable pseudonymous ID per person and
--     client (like OpenID Connect's pairwise sub). The app-level event stream
--     and usage name people by it, never by email or platform user ID. It
--     survives disconnecting and connecting again. Existing grants get one.
--   * oauth_client_secrets: secrets platform admins issue to trusted apps
--     (metadata-document clients on the trusted list) for the app-level
--     endpoints (one event stream for everyone who connected the app, and
--     their usage). Stored as HMAC-SHA256 digests under API_KEY_PEPPER,
--     like tokens; a secret works only while its client is trusted.
--   * agent_run_senders: who sent the latest message to each thread, and
--     through which connected app (grant; NULL: not through an app), so the
--     model gateway can attribute an agent's requests to an app.
--   * model_usage.oauth_grant_id and mcp_calls.oauth_grant_id: the
--     connected app (grant) a model request or tool call is attributed to
--     (NULL: none, or not unambiguous). Existing reports ignore them.
--
-- Expand only: the previous release keeps working (it writes NULLs and never
-- reads the new tables), except that it can't read grants with agents:read.

-- +goose Up
ALTER TABLE oauth_grants DROP CONSTRAINT oauth_grants_scopes_check;
ALTER TABLE oauth_grants ADD CONSTRAINT oauth_grants_scopes_check
    CHECK (cardinality(scopes) >= 1 AND scopes <@ ARRAY['agents', 'agents:read']);
CREATE INDEX oauth_grants_client_idx ON oauth_grants (client_id, user_id) WHERE revoked_at IS NULL;

ALTER TABLE oauth_codes ADD COLUMN scopes text[] CHECK (scopes IS NULL OR (cardinality(scopes) >= 1 AND scopes <@ ARRAY['agents', 'agents:read']));
ALTER TABLE oauth_tokens ADD COLUMN scopes text[] CHECK (scopes IS NULL OR (cardinality(scopes) >= 1 AND scopes <@ ARRAY['agents', 'agents:read']));

CREATE TABLE oauth_client_subjects (
    client_id  text        NOT NULL CHECK (char_length(client_id) BETWEEN 1 AND 500),
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    subject    uuid        NOT NULL DEFAULT gen_random_uuid() CONSTRAINT oauth_client_subjects_subject_key UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (client_id, user_id)
);
INSERT INTO oauth_client_subjects (client_id, user_id)
SELECT DISTINCT client_id, user_id FROM oauth_grants
ON CONFLICT DO NOTHING;

CREATE TABLE oauth_client_secrets (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id    text        NOT NULL CHECK (char_length(client_id) BETWEEN 1 AND 500),
    -- HMAC-SHA256 (hex) of "oauth:" and the secret, keyed with API_KEY_PEPPER.
    secret_hash  text        NOT NULL CONSTRAINT oauth_client_secrets_hash_key UNIQUE CHECK (secret_hash ~ '^[0-9a-f]{64}$'),
    pepper_id    bytea       NOT NULL CHECK (octet_length(pepper_id) = 8),
    -- The secret's first characters, to tell secrets apart (never enough to use).
    prefix       text        NOT NULL CHECK (char_length(prefix) BETWEEN 1 AND 20),
    created_by   uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz
);
CREATE INDEX oauth_client_secrets_client_idx ON oauth_client_secrets (client_id);

CREATE TABLE agent_run_senders (
    agent_id  uuid        NOT NULL,
    thread_id text        NOT NULL,
    grant_id  uuid        REFERENCES oauth_grants (id) ON DELETE SET NULL,
    sent_at   timestamptz NOT NULL,
    PRIMARY KEY (agent_id, thread_id),
    FOREIGN KEY (agent_id, thread_id) REFERENCES agent_threads (agent_id, thread_id) ON DELETE CASCADE
);

ALTER TABLE model_usage ADD COLUMN oauth_grant_id uuid REFERENCES oauth_grants (id) ON DELETE SET NULL;
CREATE INDEX model_usage_grant_time ON model_usage (oauth_grant_id, created_at) WHERE oauth_grant_id IS NOT NULL;
ALTER TABLE mcp_calls ADD COLUMN oauth_grant_id uuid REFERENCES oauth_grants (id) ON DELETE SET NULL;
CREATE INDEX mcp_calls_grant_time ON mcp_calls (oauth_grant_id, created_at) WHERE oauth_grant_id IS NOT NULL;

-- +goose Down
DROP INDEX mcp_calls_grant_time;
ALTER TABLE mcp_calls DROP COLUMN oauth_grant_id;
DROP INDEX model_usage_grant_time;
ALTER TABLE model_usage DROP COLUMN oauth_grant_id;
DROP TABLE agent_run_senders;
DROP TABLE oauth_client_secrets;
DROP TABLE oauth_client_subjects;
ALTER TABLE oauth_tokens DROP COLUMN scopes;
ALTER TABLE oauth_codes DROP COLUMN scopes;
DROP INDEX oauth_grants_client_idx;
-- Grants with agents:read are narrowed to nothing they didn't have: they go.
UPDATE oauth_grants SET revoked_at = coalesce(revoked_at, now()), revoked_reason = coalesce(revoked_reason, 'admin')
WHERE NOT scopes <@ ARRAY['agents'];
UPDATE oauth_grants SET scopes = ARRAY['agents'] WHERE NOT scopes <@ ARRAY['agents'];
ALTER TABLE oauth_grants DROP CONSTRAINT oauth_grants_scopes_check;
ALTER TABLE oauth_grants ADD CONSTRAINT oauth_grants_scopes_check
    CHECK (cardinality(scopes) >= 1 AND scopes <@ ARRAY['agents']);
