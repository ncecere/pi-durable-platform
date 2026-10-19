-- Threads that exist before they carry MCP servers (#5), and re-checking
-- IdP group membership after sign-in (#6).
--
-- Threads:
--   * thread_mcp_servers belong to a thread record (agent_threads): the
--     servers of a thread nobody has started were the hole (a later private
--     thread with that ID inherited them), so those rows are dropped and the
--     reference is enforced from now on. Deleting a thread (or its agent)
--     deletes its servers. Releases before this one could set servers on an
--     unrecorded thread; their requests fail against the reference during a
--     rolling upgrade (the platform runs one replica).
--   * agent_threads.reserved: the platform created the record (a thread
--     created with POST .../threads, or started by setting its MCP servers)
--     and no message has been accepted yet. Reserved threads are listed only
--     for their creator, are never forgotten when the agent's runtime lists
--     its threads (it does not know them), and are removed after 30 days
--     without a message.

-- Group re-check:
--   * users.group_denied_at / group_denied_reason: the IdP no longer lists the
--     person in an allowed group (OIDC_ALLOWED_GROUPS), or refuses to vouch
--     for them. Separate from users.status, which admins set: the person
--     counts as inactive while either says so, sessions and connected apps
--     are revoked when this is set, and signing in again clears it. Reasons:
--     not_in_group (the IdP's fresh claims lack an allowed group),
--     idp_refused (it rejected the stored grant: person disabled, grant
--     revoked, expired) and idp_unreachable (no answer for the grace period).
--   * user_idp_tokens: the refresh token the IdP issued at the person's last
--     sign-in (sealed with ENCRYPTION_KEY), used to ask the IdP for fresh
--     claims. checked_at schedules the next check; verified_at is when the IdP
--     last confirmed the person (the grace period counts from it).
--   * oauth_grants.revoked_reason gains access_denied (revoked because the
--     person lost access).

-- +goose Up
DELETE FROM thread_mcp_servers s
WHERE NOT EXISTS (SELECT 1 FROM agent_threads t WHERE t.agent_id = s.agent_id AND t.thread_id = s.thread_id);

ALTER TABLE thread_mcp_servers
    ADD CONSTRAINT thread_mcp_servers_thread_fkey
    FOREIGN KEY (agent_id, thread_id) REFERENCES agent_threads (agent_id, thread_id) ON DELETE CASCADE;

ALTER TABLE agent_threads ADD COLUMN reserved boolean NOT NULL DEFAULT false;

ALTER TABLE users
    ADD COLUMN group_denied_at     timestamptz,
    ADD COLUMN group_denied_reason text,
    ADD CONSTRAINT users_group_denied CHECK (
        (group_denied_at IS NULL) = (group_denied_reason IS NULL)
        AND (group_denied_reason IS NULL OR group_denied_reason IN ('not_in_group', 'idp_refused', 'idp_unreachable')));

CREATE TABLE user_idp_tokens (
    user_id           uuid        PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    refresh_token_enc bytea       NOT NULL,
    verified_at       timestamptz NOT NULL DEFAULT now(),
    checked_at        timestamptz NOT NULL DEFAULT now(),
    created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX user_idp_tokens_due ON user_idp_tokens (checked_at);

ALTER TABLE oauth_grants DROP CONSTRAINT oauth_grants_revoked_reason_check;
ALTER TABLE oauth_grants ADD CONSTRAINT oauth_grants_revoked_reason_check
    CHECK (revoked_reason IN ('user', 'admin', 'client', 'refresh_reuse', 'access_denied'));

-- +goose Down
ALTER TABLE oauth_grants DROP CONSTRAINT oauth_grants_revoked_reason_check;
ALTER TABLE oauth_grants ADD CONSTRAINT oauth_grants_revoked_reason_check
    CHECK (revoked_reason IN ('user', 'admin', 'client', 'refresh_reuse'));
DROP TABLE user_idp_tokens;
ALTER TABLE users DROP CONSTRAINT users_group_denied, DROP COLUMN group_denied_reason, DROP COLUMN group_denied_at;
ALTER TABLE agent_threads DROP COLUMN reserved;
ALTER TABLE thread_mcp_servers DROP CONSTRAINT thread_mcp_servers_thread_fkey;
