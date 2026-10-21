-- name: LockUserRow :one
-- The whole row, locked until the transaction ends.
SELECT * FROM users WHERE id = $1 FOR UPDATE;

-- name: BeginTwoFactorSetup :exec
UPDATE users
SET two_factor_pending_secret = $2, two_factor_version = two_factor_version + 1
WHERE id = $1;

-- name: EnableTwoFactor :exec
UPDATE users
SET two_factor_secret = $2, two_factor_pending_secret = NULL,
    two_factor_enabled = TRUE, two_factor_last_counter = $3,
    two_factor_version = two_factor_version + 1
WHERE id = $1;

-- name: DisableTwoFactor :exec
UPDATE users
SET two_factor_secret = NULL, two_factor_pending_secret = NULL,
    two_factor_enabled = FALSE, two_factor_last_counter = NULL,
    two_factor_version = two_factor_version + 1
WHERE id = $1;

-- name: AcceptTwoFactorCounter :execrows
-- Each time step is accepted once, and never one older than the last.
UPDATE users
SET two_factor_last_counter = $2
WHERE id = $1 AND two_factor_enabled = TRUE
  AND (two_factor_last_counter IS NULL OR two_factor_last_counter < $2);

-- name: BumpTwoFactorVersion :exec
UPDATE users SET two_factor_version = two_factor_version + 1 WHERE id = $1;

-- name: InsertRecoveryCode :exec
INSERT INTO two_factor_recovery_codes (user_id, code_hash) VALUES ($1, $2);

-- name: DeleteRecoveryCodes :exec
DELETE FROM two_factor_recovery_codes WHERE user_id = $1;

-- name: ConsumeRecoveryCode :execrows
UPDATE two_factor_recovery_codes
SET used_at = @now::timestamptz
WHERE user_id = @user_id AND code_hash = @code_hash AND used_at IS NULL;

-- name: CountUnusedRecoveryCodes :one
SELECT count(*) FROM two_factor_recovery_codes WHERE user_id = $1 AND used_at IS NULL;

-- name: CreateTwoFactorChallenge :exec
INSERT INTO two_factor_challenges (id, user_id, two_factor_version, created_at, expires_at)
VALUES (@id, @user_id, @two_factor_version, @now, @expires_at);

-- name: DeleteTwoFactorChallenge :exec
DELETE FROM two_factor_challenges WHERE id = $1;

-- name: DeleteUserTwoFactorChallenges :exec
DELETE FROM two_factor_challenges WHERE user_id = $1;

-- name: LockTwoFactorChallenge :one
-- Only a challenge that has not expired at now.
SELECT * FROM two_factor_challenges WHERE id = @id AND expires_at > @now FOR UPDATE;

-- name: GetTwoFactorChallengeUser :one
SELECT user_id FROM two_factor_challenges WHERE id = @id AND expires_at > @now;

-- name: DeleteExpiredTwoFactorChallenges :execrows
DELETE FROM two_factor_challenges WHERE expires_at <= @now;
