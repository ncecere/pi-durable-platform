-- name: InsertAudit :exec
INSERT INTO audit_log (
    actor_kind, actor_user_id, team_id, action, target_type, target_id,
    before_state, after_state, metadata, request_id, client_ip
) VALUES (
    @actor_kind, @actor_user_id, @team_id, @action, @target_type, @target_id,
    @before_state, @after_state, @metadata, @request_id, @client_ip
);

-- name: ListAudit :many
-- One page of the audit log, newest first, with the actor and the target's
-- name resolved at read time. live_label is the target's current name (''
-- when it no longer exists); recorded_label is the last name the log
-- recorded for the same target, used for deleted targets ('' when none).
-- Names are never empty, so '' means unknown.
-- Every target type the code audits is covered here (a test enumerates them).
-- action_prefix and exclude_prefixes are LIKE-escaped by the caller.
-- actor_kind: 'system' for the system's entries.
SELECT a.id, a.occurred_at, a.actor_kind, a.actor_user_id, a.team_id, a.action,
       a.target_type, a.target_id, a.before_state, a.after_state, a.metadata,
       a.request_id, a.client_ip,
       COALESCE(u.email::text, '')::text AS actor_email,
       COALESCE(tm.name, '')::text AS team_name, COALESCE(tm.slug, '')::text AS team_slug,
       u.display_name AS actor_display_name,
       COALESCE(live.label, '')::text AS live_label,
       COALESCE(CASE WHEN live.label IS NULL THEN (
           SELECT COALESCE(h.metadata->>'name', h.after_state->>'name', h.after_state->>'displayName',
                           h.after_state->>'email', h.before_state->>'name',
                           h.before_state->>'displayName', h.before_state->>'email')
           FROM audit_log h
           WHERE h.target_type = a.target_type AND h.target_id = a.target_id AND a.target_id <> ''
             AND COALESCE(h.metadata->>'name', h.after_state->>'name', h.after_state->>'displayName',
                          h.after_state->>'email', h.before_state->>'name',
                          h.before_state->>'displayName', h.before_state->>'email') IS NOT NULL
           ORDER BY h.id DESC
           LIMIT 1
       ) END, '')::text AS recorded_label
FROM audit_log a
CROSS JOIN LATERAL (
    SELECT CASE WHEN a.target_id ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
                THEN a.target_id::uuid END AS target_uuid
) ids
LEFT JOIN users u ON u.id = a.actor_user_id
LEFT JOIN teams tm ON tm.id = a.team_id
CROSS JOIN LATERAL (
    SELECT (CASE a.target_type
        WHEN 'team' THEN (SELECT t.name FROM teams t WHERE t.id = ids.target_uuid)
        WHEN 'user' THEN (SELECT COALESCE(NULLIF(x.display_name, ''), x.email::text) FROM users x WHERE x.id = ids.target_uuid)
        WHEN 'team_invite' THEN (SELECT i.email::text FROM team_invites i WHERE i.id = ids.target_uuid)
        WHEN 'api_key' THEN (SELECT k.name FROM api_keys k WHERE k.id = ids.target_uuid)
        WHEN 'service_account' THEN (SELECT sa.name FROM service_accounts sa WHERE sa.id = ids.target_uuid)
        WHEN 'agent' THEN (SELECT ag.name FROM agents ag WHERE ag.id = ids.target_uuid)
        WHEN 'service_account_key' THEN (SELECT sa.name || ' · ' || k.name FROM service_account_keys k
                                         JOIN service_accounts sa ON sa.id = k.service_account_id WHERE k.id = ids.target_uuid)
    END)::text AS label
) live
WHERE (sqlc.narg(team_id)::uuid IS NULL OR a.team_id = sqlc.narg(team_id)::uuid)
  AND (sqlc.narg(before_id)::bigint IS NULL OR a.id < sqlc.narg(before_id)::bigint)
  AND (sqlc.narg(action)::text IS NULL OR a.action = sqlc.narg(action)::text)
  AND (sqlc.narg(action_prefix)::text IS NULL OR a.action LIKE sqlc.narg(action_prefix)::text || '%' ESCAPE '\')
  AND NOT EXISTS (SELECT 1 FROM unnest(sqlc.arg(exclude_prefixes)::text[]) AS x(prefix) WHERE a.action LIKE x.prefix || '%' ESCAPE '\')
  AND (sqlc.narg(actor_user_id)::uuid IS NULL OR a.actor_user_id = sqlc.narg(actor_user_id)::uuid)
  AND (sqlc.narg(actor_kind)::text IS NULL OR a.actor_kind = sqlc.narg(actor_kind)::text)
  AND (sqlc.narg(target_type)::text IS NULL OR a.target_type = sqlc.narg(target_type)::text)
  AND (sqlc.narg(occurred_from)::timestamptz IS NULL OR a.occurred_at >= sqlc.narg(occurred_from)::timestamptz)
  AND (sqlc.narg(occurred_to)::timestamptz IS NULL OR a.occurred_at < sqlc.narg(occurred_to)::timestamptz)
ORDER BY a.id DESC
LIMIT @page_size;
