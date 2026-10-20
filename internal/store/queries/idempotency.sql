-- name: ClaimIdempotencyKey :one
-- Claims a caller's key for a request, or takes over one older than 24
-- hours. Nothing when the key is in use: then GetIdempotencyKey reads it. A
-- concurrent claim of the same key waits here until the first transaction
-- ends (the primary key), so two retries never both run.
INSERT INTO idempotency_keys (caller, operation, key, fingerprint)
VALUES (sqlc.arg(caller), sqlc.arg(operation), sqlc.arg(key), sqlc.arg(fingerprint))
ON CONFLICT (caller, operation, key) DO UPDATE
SET fingerprint = EXCLUDED.fingerprint, agent_id = NULL, status = 0, created_at = now()
WHERE idempotency_keys.created_at < now() - interval '24 hours'
RETURNING *;

-- name: GetIdempotencyKey :one
SELECT * FROM idempotency_keys WHERE caller = sqlc.arg(caller) AND operation = sqlc.arg(operation) AND key = sqlc.arg(key);

-- name: CompleteIdempotencyKey :exec
UPDATE idempotency_keys SET agent_id = sqlc.arg(agent_id), status = sqlc.arg(status)
WHERE caller = sqlc.arg(caller) AND operation = sqlc.arg(operation) AND key = sqlc.arg(key);

-- name: DeleteExpiredIdempotencyKeys :execrows
DELETE FROM idempotency_keys WHERE created_at < now() - interval '24 hours';
