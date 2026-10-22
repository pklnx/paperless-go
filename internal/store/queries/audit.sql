-- name: InsertAuditEntry :exec
-- at comes from the database clock (the column default).
INSERT INTO audit_log (actor, user_id, user_name, api_key_id, api_key_name, action,
                       target_type, target_id, target_name, client_ip, user_agent_id, details)
VALUES (@actor, sqlc.narg(user_id), sqlc.narg(user_name), sqlc.narg(api_key_id), sqlc.narg(api_key_name), @action,
        sqlc.narg(target_type), sqlc.narg(target_id), sqlc.narg(target_name), sqlc.narg(client_ip), sqlc.narg(user_agent_id), @details);

-- name: AuditUserAgentID :one
-- The id of a user agent, inserted if new. ON CONFLICT DO UPDATE would be
-- refused by the append-only trigger. Under READ COMMITTED a concurrent
-- insert of the same new string leaves both branches empty for the loser:
-- no row means run it once more.
WITH ins AS (
    INSERT INTO audit_user_agents (user_agent) VALUES (@user_agent)
    ON CONFLICT (user_agent) DO NOTHING
    RETURNING id)
SELECT id FROM ins
UNION ALL
SELECT id FROM audit_user_agents WHERE user_agent = @user_agent
LIMIT 1;

-- name: EnsureAuditPartitions :one
-- Creates the partitions of this month and months_ahead months; returns how
-- many it created.
SELECT audit_log_ensure_partitions(@months_ahead::integer)::integer;

-- name: ExportAuditEntries :many
-- One batch of the entries in [since, until) after (after_at, after_id), oldest
-- first, optionally of one user (by id, or by name for a deleted user).
SELECT l.id, l.at, l.actor, l.user_id, l.user_name, l.api_key_id, l.api_key_name, l.action,
       l.target_type, l.target_id, l.target_name, l.client_ip, ua.user_agent, l.details
FROM audit_log l
LEFT JOIN audit_user_agents ua ON ua.id = l.user_agent_id
WHERE l.at >= @since AND l.at < @until
  AND (sqlc.narg(user_id)::bigint IS NULL OR l.user_id = sqlc.narg(user_id))
  AND (sqlc.narg(user_name)::text IS NULL OR l.user_name = sqlc.narg(user_name))
  AND l.action LIKE ANY (@patterns::text[])
  AND (l.at, l.id) > (@after_at::timestamptz, @after_id::bigint)
ORDER BY l.at, l.id
LIMIT @lim;

-- name: ListAuditEntries :many
-- A page of every user's entries in [since, until), newest first, for
-- admins; optionally one user's (by id, or by name for a deleted user) or
-- one actor's.
SELECT l.id, l.at, l.actor, l.user_id, l.user_name, l.api_key_name, l.action,
       l.target_type, l.target_id, l.target_name, l.client_ip, ua.user_agent, l.details
FROM audit_log l
LEFT JOIN audit_user_agents ua ON ua.id = l.user_agent_id
WHERE l.at >= @since AND l.at < @until
  AND (sqlc.narg(user_id)::bigint IS NULL OR l.user_id = sqlc.narg(user_id))
  AND (sqlc.narg(user_name)::text IS NULL OR l.user_name = sqlc.narg(user_name))
  AND (sqlc.narg(actor)::text IS NULL OR l.actor = sqlc.narg(actor))
  AND l.action LIKE ANY (@patterns::text[])
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (l.at, l.id) < (sqlc.narg(cursor_at), @cursor_id::bigint))
ORDER BY l.at DESC, l.id DESC
LIMIT @lim;

-- name: ListAuditEntriesForUser :many
-- A page of one user's log, newest first: the entries by the user and the
-- entries about them (target user, by an admin or the command line). A
-- UNION ALL of two branches rather than an OR, so that each branch uses its
-- index (audit_log_user_idx, audit_log_target_user_idx) also in a cached
-- generic plan; the literal target_type = 'user' matches the partial
-- index. An entry both by and about the user comes from the first branch
-- only. Each branch is limited on its own, then the union.
SELECT l.id, l.at, l.actor, l.user_id, l.user_name, l.api_key_name, l.action,
       l.target_type, l.target_id, l.target_name, l.client_ip, ua.user_agent, l.details
FROM (
    (SELECT a.id, a.at, a.actor, a.user_id, a.user_name, a.api_key_name, a.action,
            a.target_type, a.target_id, a.target_name, a.client_ip, a.user_agent_id, a.details
     FROM audit_log a
     WHERE a.user_id = @user_id::bigint
       AND a.at >= @since AND a.at < @until
       AND a.action LIKE ANY (@patterns::text[])
       AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (a.at, a.id) < (sqlc.narg(cursor_at), @cursor_id::bigint))
     ORDER BY a.at DESC, a.id DESC
     LIMIT @lim)
    UNION ALL
    (SELECT b.id, b.at, b.actor, b.user_id, b.user_name, b.api_key_name, b.action,
            b.target_type, b.target_id, b.target_name, b.client_ip, b.user_agent_id, b.details
     FROM audit_log b
     WHERE b.target_type = 'user' AND b.target_id = (@user_id::bigint)::text
       AND b.user_id IS DISTINCT FROM @user_id::bigint
       AND b.at >= @since AND b.at < @until
       AND b.action LIKE ANY (@patterns::text[])
       AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (b.at, b.id) < (sqlc.narg(cursor_at), @cursor_id::bigint))
     ORDER BY b.at DESC, b.id DESC
     LIMIT @lim)
) l
LEFT JOIN audit_user_agents ua ON ua.id = l.user_agent_id
ORDER BY l.at DESC, l.id DESC
LIMIT @lim;

-- name: InsertAuditEntries :exec
-- Several entries by the same actor and client, one per element of the
-- arrays (equal lengths): the per-document entries of a bulk change.
INSERT INTO audit_log (actor, user_id, user_name, api_key_id, api_key_name, action,
                       target_type, target_id, target_name, client_ip, user_agent_id, details)
SELECT @actor, sqlc.narg(user_id), sqlc.narg(user_name), sqlc.narg(api_key_id), sqlc.narg(api_key_name), e.action,
       (sqlc.arg(target_types)::text[])[e.n], (sqlc.arg(target_ids)::text[])[e.n],
       NULLIF((sqlc.arg(target_names)::text[])[e.n], ''), sqlc.narg(client_ip), sqlc.narg(user_agent_id),
       (sqlc.arg(details)::jsonb[])[e.n]
FROM unnest(sqlc.arg(actions)::text[]) WITH ORDINALITY AS e (action, n);
