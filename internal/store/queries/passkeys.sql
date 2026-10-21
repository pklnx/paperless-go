-- name: SetWebAuthnHandle :exec
UPDATE users SET webauthn_handle = $2 WHERE id = $1 AND webauthn_handle IS NULL;

-- name: ListPasskeys :many
SELECT * FROM passkeys WHERE user_id = $1 ORDER BY created_at, id;

-- name: CountPasskeys :one
SELECT count(*) FROM passkeys WHERE user_id = $1;

-- name: CountPasskeysByUser :many
SELECT user_id, count(*) AS n FROM passkeys GROUP BY user_id;

-- name: InsertPasskey :exec
INSERT INTO passkeys (user_id, credential_id, name, credential, created_at)
VALUES (@user_id, @credential_id, @name, @credential, @now);

-- name: PasskeyOwner :one
SELECT user_id FROM passkeys WHERE credential_id = $1;

-- name: LockPasskey :one
-- The row stays locked until the transaction ends, so concurrent logins
-- with the same passkey see each other's sign count.
SELECT * FROM passkeys WHERE credential_id = $1 FOR UPDATE;

-- name: UpdatePasskeyUse :exec
UPDATE passkeys SET credential = @credential, last_used_at = @now::timestamptz WHERE id = @id;

-- name: DeletePasskey :one
DELETE FROM passkeys WHERE id = $1 AND user_id = $2 RETURNING name, credential_id;

-- name: DeleteUserPasskeys :execrows
DELETE FROM passkeys WHERE user_id = $1;

-- name: CreateWebAuthnCeremony :exec
INSERT INTO webauthn_ceremonies (id, kind, user_id, name, data, created_at, expires_at)
VALUES (@id, @kind, @user_id, @name, @data, @now, @expires_at);

-- name: TakeWebAuthnCeremony :one
-- Uses up a ceremony that has not expired at now.
DELETE FROM webauthn_ceremonies WHERE id = @id AND kind = @kind AND expires_at > @now
RETURNING *;

-- name: CountOpenLoginCeremonies :one
SELECT count(*) FROM webauthn_ceremonies WHERE kind = 'login' AND expires_at > @now;

-- name: DeleteExpiredWebAuthnCeremonies :execrows
DELETE FROM webauthn_ceremonies WHERE expires_at <= @now;

-- name: DeleteUserWebAuthnCeremonies :exec
DELETE FROM webauthn_ceremonies WHERE user_id = $1;
