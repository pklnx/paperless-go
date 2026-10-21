-- name: CreateUser :one
INSERT INTO users (name, password_hash, is_admin, must_change_password)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetUserByName :one
SELECT * FROM users WHERE name = $1;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: ListUsers :many
SELECT * FROM users ORDER BY name;

-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: LockActiveAdmins :many
-- Locks the rows of all unlocked admins, so that two changes cannot remove
-- the last two admins at the same time.
SELECT id FROM users WHERE is_admin AND locked_at IS NULL FOR UPDATE;

-- name: LockUser :one
-- Locks a user's row for a change that must not race with a removal.
SELECT id FROM users WHERE id = $1 FOR UPDATE;

-- name: SetUserPassword :execrows
UPDATE users SET password_hash = $2, must_change_password = $3, password_changed_at = now() WHERE id = $1;

-- name: RehashUserPassword :execrows
-- Replaces the hash with one of the same password (new parameters); unlike
-- SetUserPassword it keeps the sessions.
UPDATE users SET password_hash = $2 WHERE id = $1;

-- name: SetUserAdmin :execrows
UPDATE users SET is_admin = $2 WHERE id = $1;

-- name: SetUserLocked :execrows
UPDATE users SET locked_at = CASE WHEN @locked::boolean THEN coalesce(locked_at, now()) END WHERE id = $1;

-- name: RecordLogin :exec
UPDATE users SET last_login_at = @at::timestamptz WHERE id = @id;

-- name: DeleteUser :execrows
DELETE FROM users WHERE id = $1;

-- name: CountOwnedRows :many
-- The rows each user owns, per table. Any of them keeps the user from
-- being removed (ON DELETE RESTRICT). Deadlines hang off documents, so the
-- documents count covers them.
SELECT u.id,
       (SELECT count(*) FROM documents d WHERE d.owner_id = u.id)::bigint AS documents,
       (SELECT count(*) FROM tags t WHERE t.owner_id = u.id)::bigint AS tags,
       (SELECT count(*) FROM correspondents c WHERE c.owner_id = u.id)::bigint AS correspondents,
       (SELECT count(*) FROM document_types dt WHERE dt.owner_id = u.id)::bigint AS document_types,
       (SELECT count(*) FROM saved_views sv WHERE sv.owner_id = u.id)::bigint AS saved_views
FROM users u;

-- name: CreateSession :exec
INSERT INTO sessions (id, user_id, created_at, last_seen_at, expires_at, user_agent)
VALUES (@id, @user_id, @now, @now, @expires_at, @user_agent);

-- name: GetSession :one
-- A session is valid while it has not expired, was used after the idle
-- cutoff and its user is not locked.
SELECT s.id, s.last_seen_at, u.id AS user_id, u.name, u.is_admin, u.must_change_password
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.id = @id
  AND s.expires_at > @now
  AND s.last_seen_at > @idle_cutoff
  AND u.locked_at IS NULL;

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = @now WHERE id = @id;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = $1;

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = $1;

-- name: DeleteOtherSessions :exec
-- All sessions of a user except one.
DELETE FROM sessions WHERE user_id = $1 AND id <> $2;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at <= @now OR last_seen_at <= @idle_cutoff;
