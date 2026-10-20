-- The upgrade check (v0.4, item X, #28; docs/custom-agents.md, Upgrades).
--
-- pi-durable migrates a document when it reads it, and code that finds a
-- document a newer version wrote refuses it. So once a runtime with a newer
-- pi-durable has written an agent's storage, a runtime with an older one can't
-- run the agent. agent_storage_versions records, per agent, the newest
-- pi-durable version that has written its storage (reported by the runtime
-- when it starts, before it opens the storage, and at /status). The platform
-- refuses to start an agent on a runtime older than that, and records the
-- latest refusal (refused_version: the runtime's version, '' when it reported
-- none) so the agent's page and the API can say why; a start that is allowed
-- clears it.
--
-- A new table rather than columns on agents: it is the runtime's report, not
-- the agent's settings (no revision, no If-Match), and an agent without a row
-- has never been started by a runtime that reports its version.

-- +goose Up
CREATE TABLE agent_storage_versions (
    agent_id           uuid        PRIMARY KEY REFERENCES agents (id) ON DELETE CASCADE,
    pi_durable_version text        NOT NULL CHECK (char_length(pi_durable_version) BETWEEN 1 AND 64),
    recorded_at        timestamptz NOT NULL DEFAULT now(),
    refused_version    text        CHECK (char_length(refused_version) <= 64),
    refused_at         timestamptz,
    CONSTRAINT agent_storage_versions_refusal CHECK ((refused_version IS NULL) = (refused_at IS NULL))
);

-- +goose Down
DROP TABLE agent_storage_versions;
