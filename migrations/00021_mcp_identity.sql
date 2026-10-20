-- The person behind an MCP tool call (docs/mcp-identity.md): the platform's
-- MCP gateway signs a short-lived identity token for every request it
-- forwards to an MCP server in proxy mode.
--
--   * mcp_identity_keys: the platform's ES256 signing keys. The private key
--     (PKCS #8 DER) is sealed with ENCRYPTION_KEY (associated data
--     "mcp_identity_key:<id>", re-sealed by rekey); public_jwk is published
--     in the JWKS. kid is the key's RFC 7638 thumbprint. The key that signs
--     is the one with the latest signs_from that has passed: a rotated key
--     is published before it signs, and the previous one stays published
--     for a while after it stopped (internal/mcpidentity).
--   * mcp_servers.send_identity: send the token to this server (default on;
--     direct mode never does). identity_audience: the token's aud ('' is the
--     server's URL). identity_email: include the person's email.
--
-- Rolling upgrade: a previous release's replica writes servers without the
-- new columns (their defaults apply) and never reads them.

-- +goose Up
CREATE TABLE mcp_identity_keys (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    kid             text        NOT NULL UNIQUE CHECK (char_length(kid) BETWEEN 1 AND 100),
    private_key_enc bytea       NOT NULL,
    public_jwk      jsonb       NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    signs_from      timestamptz NOT NULL
);
CREATE INDEX mcp_identity_keys_signs_from ON mcp_identity_keys (signs_from);

ALTER TABLE mcp_servers ADD COLUMN send_identity boolean NOT NULL DEFAULT true;
ALTER TABLE mcp_servers ADD COLUMN identity_audience text NOT NULL DEFAULT ''
    CHECK (char_length(identity_audience) <= 2048);
ALTER TABLE mcp_servers ADD COLUMN identity_email boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE mcp_servers DROP COLUMN identity_email;
ALTER TABLE mcp_servers DROP COLUMN identity_audience;
ALTER TABLE mcp_servers DROP COLUMN send_identity;
DROP TABLE mcp_identity_keys;
