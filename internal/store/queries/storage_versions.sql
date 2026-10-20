-- The upgrade check: the newest pi-durable version that has written each
-- agent's storage, and the latest refusal (migration 00024).

-- name: LockAgentRuntime :one
-- Serialises the upgrade checks of one agent (FOR NO KEY UPDATE: settings
-- changes and moves, FOR UPDATE, wait for it, thread writes, FOR KEY SHARE,
-- don't).
SELECT team_id FROM agents WHERE id = $1 FOR NO KEY UPDATE;

-- name: GetAgentStorageVersion :one
SELECT * FROM agent_storage_versions WHERE agent_id = $1;

-- name: RecordAgentStorageVersion :exec
-- The version a runtime that may run the agent reported (the caller has
-- checked it is not older than the recorded one); clears any refusal.
INSERT INTO agent_storage_versions (agent_id, pi_durable_version, recorded_at)
VALUES ($1, $2, now())
ON CONFLICT (agent_id) DO UPDATE
SET pi_durable_version = EXCLUDED.pi_durable_version,
    recorded_at = CASE WHEN agent_storage_versions.pi_durable_version = EXCLUDED.pi_durable_version
                       THEN agent_storage_versions.recorded_at ELSE now() END,
    refused_version = NULL, refused_at = NULL;

-- name: RefuseAgentRuntime :exec
UPDATE agent_storage_versions SET refused_version = $2, refused_at = now() WHERE agent_id = $1;

-- name: DeleteAgentStorageVersion :exec
-- The operator's reset (reset-storage-version): the next runtime start
-- records its own version.
DELETE FROM agent_storage_versions WHERE agent_id = $1;
