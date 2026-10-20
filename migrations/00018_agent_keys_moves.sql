-- Safe agent creation and moving agents between teams (v0.3, items K and M).
--
--   * agents.service_managed: the agent holds an app's users' conversations
--     (made by a service account with an external key, in a shared team):
--     only the team's service accounts use it. Until now that was "has an
--     external key"; people may now give agents keys in their own personal
--     workspace (PUT .../agents/by-key/{key}), and those agents are the
--     person's like any other in the workspace, so the two are told apart.
--   * idempotency_keys: the Idempotency-Key of agent creation, remembered
--     per caller (a person, an app the person connected, a service account)
--     for 24 hours with a digest of the request. The primary key makes two
--     concurrent requests with one key wait for each other instead of both
--     creating an agent. agent_id is not a foreign key: a key whose agent was
--     deleted replays as not found.

-- +goose Up
ALTER TABLE agents ADD COLUMN service_managed boolean NOT NULL DEFAULT false;
UPDATE agents SET service_managed = true WHERE external_key IS NOT NULL;
ALTER TABLE agents ADD CONSTRAINT agents_service_managed_key CHECK (NOT service_managed OR external_key IS NOT NULL);

CREATE TABLE idempotency_keys (
    -- 'user:<id>' (a session or personal API key), 'oauth:<grant id>' (an
    -- app acting for a person) or 'service_account:<id>'.
    caller      text        NOT NULL,
    operation   text        NOT NULL,
    key         text        NOT NULL CHECK (char_length(key) BETWEEN 1 AND 255),
    -- SHA-256 of the request as the platform understood it.
    fingerprint bytea       NOT NULL,
    agent_id    uuid,
    status      integer     NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (caller, operation, key)
);
CREATE INDEX idempotency_keys_created_at ON idempotency_keys (created_at);

-- +goose Down
DROP TABLE idempotency_keys;
ALTER TABLE agents DROP CONSTRAINT agents_service_managed_key;
ALTER TABLE agents DROP COLUMN service_managed;
