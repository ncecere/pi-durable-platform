-- OAuth Connect: the platform as a small OAuth 2.1 authorization server, so
-- an app (a chat app, a script with a browser) acts as the person who
-- connected it, on the agent routes (internal/oauth; DESIGN.md "OAuth
-- Connect"). Ported from Open MCP Portal without its gateways: the protected
-- resource is the platform itself (APP_URL).
--
--   * oauth_settings: one row; the trusted-client list, a JSON array of
--     {clientId, label?} (https Client ID Metadata Document URLs only), with
--     its own revision. Trusted clients skip the consent screen, never the
--     sign-in. Empty by default.
--   * oauth_clients: clients registered with Dynamic Client Registration
--     (RFC 7591). Clients identified by a metadata document aren't stored:
--     their document is fetched and cached in memory. A registration unused
--     for 30 days is deleted.
--   * oauth_grants: a person's consent for a client, remembered until
--     revoked; at most one active grant per person and client. Revoking it
--     ends its codes and tokens.
--   * oauth_codes: authorization codes (HMAC digests), single use, 60 s.
--   * oauth_tokens: access and refresh tokens as HMAC-SHA256 digests under
--     API_KEY_PEPPER, never in clear.
--   * audit_log: apps become actors (actor_kind oauth: the person, through
--     a connected app).
--
-- New tables and a widened check (expand only): the previous release keeps
-- working, except that its audit log reader doesn't know actor kind oauth.

-- +goose Up
CREATE TABLE oauth_settings (
    id              smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    trusted_clients jsonb       NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(trusted_clients) = 'array'),
    revision        bigint      NOT NULL DEFAULT 1,
    updated_by      uuid        REFERENCES users (id) ON DELETE SET NULL,
    updated_at      timestamptz NOT NULL DEFAULT now()
);
INSERT INTO oauth_settings (id) VALUES (1);

CREATE TABLE oauth_clients (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id     text        NOT NULL CONSTRAINT oauth_clients_client_id_key UNIQUE CHECK (char_length(client_id) BETWEEN 1 AND 100),
    client_name   text        NOT NULL CHECK (char_length(client_name) BETWEEN 1 AND 200),
    redirect_uris text[]      NOT NULL CHECK (cardinality(redirect_uris) BETWEEN 1 AND 10),
    client_uri    text        CHECK (char_length(client_uri) <= 500),
    logo_uri      text        CHECK (char_length(logo_uri) <= 500),
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_used_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX oauth_clients_last_used_idx ON oauth_clients (last_used_at);

CREATE TABLE oauth_grants (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- The client as it identified itself: an https URL (a metadata
    -- document) or a registered client's id.
    client_id      text        NOT NULL CHECK (char_length(client_id) BETWEEN 1 AND 500),
    client_kind    text        NOT NULL CHECK (client_kind IN ('metadata', 'registered')),
    -- The client as consent was given.
    client_name    text        NOT NULL CHECK (char_length(client_name) BETWEEN 1 AND 200),
    client_uri     text        CHECK (char_length(client_uri) <= 500),
    logo_uri       text        CHECK (char_length(logo_uri) <= 500),
    -- What the person allowed: the agents scope, the only one so far.
    scopes         text[]      NOT NULL DEFAULT ARRAY['agents']
        CHECK (cardinality(scopes) >= 1 AND scopes <@ ARRAY['agents']),
    -- The protected resource: APP_URL. Tokens carry it too, so a token is
    -- refused if APP_URL changes.
    resource       text        NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    -- Updated at most once a minute.
    last_used_at   timestamptz,
    revoked_at     timestamptz,
    revoked_reason text        CHECK (revoked_reason IN ('user', 'admin', 'client', 'refresh_reuse'))
);
CREATE UNIQUE INDEX oauth_grants_active_key ON oauth_grants (user_id, client_id) WHERE revoked_at IS NULL;
CREATE INDEX oauth_grants_user_idx ON oauth_grants (user_id, created_at DESC);

CREATE TABLE oauth_codes (
    code_hash      text        PRIMARY KEY CHECK (code_hash ~ '^[0-9a-f]{64}$'),
    grant_id       uuid        NOT NULL REFERENCES oauth_grants (id) ON DELETE CASCADE,
    client_id      text        NOT NULL,
    redirect_uri   text        NOT NULL,
    code_challenge text        NOT NULL,
    resource       text        NOT NULL,
    expires_at     timestamptz NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX oauth_codes_expires_idx ON oauth_codes (expires_at);
CREATE INDEX oauth_codes_grant_idx ON oauth_codes (grant_id);

CREATE TABLE oauth_tokens (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    grant_id   uuid        NOT NULL REFERENCES oauth_grants (id) ON DELETE CASCADE,
    kind       text        NOT NULL CHECK (kind IN ('access', 'refresh')),
    -- HMAC-SHA256 (hex) of "oauth:" and the token, keyed with API_KEY_PEPPER.
    token_hash text        NOT NULL CONSTRAINT oauth_tokens_token_hash_key UNIQUE CHECK (token_hash ~ '^[0-9a-f]{64}$'),
    -- The first 8 bytes of SHA-256 of the pepper that made token_hash.
    pepper_id  bytea       NOT NULL CHECK (octet_length(pepper_id) = 8),
    resource   text        NOT NULL,
    expires_at timestamptz NOT NULL,
    -- A refresh token exchanged for a new pair: presenting it again later
    -- than the grace period is reuse, which revokes the grant.
    rotated_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX oauth_tokens_grant_idx ON oauth_tokens (grant_id);
CREATE INDEX oauth_tokens_expires_idx ON oauth_tokens (expires_at);

ALTER TABLE audit_log DROP CONSTRAINT audit_log_actor_kind_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_actor_kind_check
    CHECK (actor_kind IN ('user', 'api_key', 'service_account', 'oauth', 'system'));

-- +goose Down
-- The audit log is append-only: entries by apps are kept, and only new rows
-- are checked.
ALTER TABLE audit_log DROP CONSTRAINT audit_log_actor_kind_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_actor_kind_check
    CHECK (actor_kind IN ('user', 'api_key', 'service_account', 'system')) NOT VALID;
DROP TABLE oauth_tokens;
DROP TABLE oauth_codes;
DROP TABLE oauth_grants;
DROP TABLE oauth_clients;
DROP TABLE oauth_settings;
