-- name: ListMCPIdentityKeys :many
-- The platform's MCP identity signing keys, oldest first.
SELECT * FROM mcp_identity_keys ORDER BY signs_from, created_at, id;

-- name: InsertMCPIdentityKey :one
INSERT INTO mcp_identity_keys (id, kid, private_key_enc, public_jwk, signs_from)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: CountMCPIdentityKeys :one
SELECT count(*)::int FROM mcp_identity_keys;

-- name: DeleteMCPIdentityKeysExcept :execrows
-- Every key but one (a rotation that drops the previous keys at once).
DELETE FROM mcp_identity_keys WHERE id <> $1;

-- name: DeleteRetiredMCPIdentityKeys :execrows
-- Keys that stopped signing before the cutoff: a newer key began signing
-- before it.
DELETE FROM mcp_identity_keys k
WHERE EXISTS (SELECT 1 FROM mcp_identity_keys n
              WHERE n.signs_from > k.signs_from AND n.signs_from <= sqlc.arg(cutoff)::timestamptz);

-- name: MCPIdentityCaller :one
-- What the identity token says about a call: the calling agent, its team,
-- and the thread the call names, looked up among that agent's threads only
-- (no thread: the thread columns are NULL). triggered: the thread is working
-- on a triggered run (a schedule's or a webhook's), which nobody is present
-- for.
SELECT a.name AS agent_name, t.id AS team_id, t.slug AS team_slug, t.kind AS team_kind, t.personal_owner,
       th.visibility AS thread_visibility, th.created_by_user AS thread_creator,
       EXISTS (SELECT 1 FROM trigger_runs r WHERE r.agent_id = a.id AND r.thread_id = sqlc.arg(thread_id)::text
                                            AND r.status IN ('pending', 'running'))::boolean AS triggered
FROM agents a
JOIN teams t ON t.id = a.team_id
LEFT JOIN agent_threads th ON th.agent_id = a.id AND th.thread_id = sqlc.arg(thread_id)::text
WHERE a.id = sqlc.arg(agent_id);

-- name: MCPIdentityPerson :one
-- A person the token may name: their OIDC identity, whether they may use the
-- platform, and whether they are a member of the team.
SELECT u.oidc_issuer, u.oidc_subject, u.email, u.status, u.group_denied_at,
       EXISTS (SELECT 1 FROM team_members m WHERE m.team_id = sqlc.arg(team_id) AND m.user_id = u.id)::boolean AS member
FROM users u
WHERE u.id = sqlc.arg(user_id);
