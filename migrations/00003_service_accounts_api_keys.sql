-- Personal API keys, team service accounts and their keys.
--
--   * api_keys: a person's keys. Only an HMAC-SHA256 digest (keyed with
--     API_KEY_PEPPER) is stored, never the key.
--   * service_accounts: team-owned identities for services and automation,
--     with a team role of member or editor (never owner or admin), names
--     unique per team.
--   * service_account_keys: their keys (HMAC digests, always expire).
--   * pepper_id: the first 8 bytes of SHA-256 of the pepper that made the
--     digest, so keys still on API_KEY_PEPPER_PREVIOUS can be found.

-- +goose Up
CREATE TABLE api_keys (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    -- The key's first characters, to recognise it: pdp_ and 8 more.
    prefix       text        NOT NULL CHECK (prefix ~ '^pdp_[A-Za-z0-9_-]{8}$'),
    -- HMAC-SHA256 (hex) of the whole key, keyed with API_KEY_PEPPER.
    digest       text        NOT NULL CONSTRAINT api_keys_digest_key UNIQUE CHECK (digest ~ '^[0-9a-f]{64}$'),
    pepper_id    bytea       NOT NULL CHECK (octet_length(pepper_id) = 8),
    created_at   timestamptz NOT NULL DEFAULT now(),
    -- Keys always expire.
    expires_at   timestamptz NOT NULL,
    revoked_at   timestamptz,
    -- Updated at most once a minute.
    last_used_at timestamptz,
    CONSTRAINT api_keys_expiry CHECK (expires_at > created_at)
);
CREATE INDEX api_keys_user_idx ON api_keys (user_id, created_at DESC);

CREATE TABLE service_accounts (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id      uuid        NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    name         text        NOT NULL CHECK (name ~ '^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$'),
    description  text        NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
    -- The account's team role; never owner or admin.
    role         text        NOT NULL DEFAULT 'member' CHECK (role IN ('member', 'editor')),
    enabled      boolean     NOT NULL DEFAULT true,
    revision     bigint      NOT NULL DEFAULT 1,
    created_by   uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_by   uuid        REFERENCES users (id) ON DELETE SET NULL,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    -- Updated at most once a minute.
    last_used_at timestamptz,
    CONSTRAINT service_accounts_team_name_key UNIQUE (team_id, name)
);

CREATE TABLE service_account_keys (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    service_account_id uuid        NOT NULL REFERENCES service_accounts (id) ON DELETE CASCADE,
    name               text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    -- The key's first characters: pdp_sa_ and 8 more.
    prefix             text        NOT NULL CHECK (prefix ~ '^pdp_sa_[A-Za-z0-9_-]{8}$'),
    -- HMAC-SHA256 (hex) of the whole key, keyed with API_KEY_PEPPER.
    digest             text        NOT NULL CONSTRAINT service_account_keys_digest_key UNIQUE CHECK (digest ~ '^[0-9a-f]{64}$'),
    pepper_id          bytea       NOT NULL CHECK (octet_length(pepper_id) = 8),
    created_by         uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    expires_at         timestamptz NOT NULL,
    revoked_at         timestamptz,
    last_used_at       timestamptz,
    CONSTRAINT service_account_keys_expiry CHECK (expires_at > created_at)
);
CREATE INDEX service_account_keys_account_idx ON service_account_keys (service_account_id, created_at DESC);

-- +goose Down
DROP TABLE service_account_keys;
DROP TABLE service_accounts;
DROP TABLE api_keys;
