-- +goose Up
-- Two-factor login with TOTP (#3, part 3). The secrets are sealed with
-- PAPERLESS_GO_SECRET_KEY (AES-256-GCM, bound to the user ID), recovery
-- codes are stored as keyed hashes. Existing users start without 2FA;
-- admins set it up at their next login.
ALTER TABLE users
    ADD COLUMN two_factor_secret         BYTEA,
    ADD COLUMN two_factor_enabled        BOOLEAN NOT NULL DEFAULT FALSE,
    -- A secret shown to the user but not confirmed with a code yet.
    ADD COLUMN two_factor_pending_secret BYTEA,
    -- The last accepted TOTP time step: each step counts once (replay).
    ADD COLUMN two_factor_last_counter   BIGINT,
    -- Incremented by every change of the user's 2FA state; a login
    -- challenge issued before a change no longer works.
    ADD COLUMN two_factor_version        BIGINT  NOT NULL DEFAULT 0;

-- One-time recovery codes, as HMAC-SHA-256 with a key derived from the
-- secret key. Auth tables go with their user (ON DELETE CASCADE).
CREATE TABLE two_factor_recovery_codes (
    id        BIGSERIAL   PRIMARY KEY,
    user_id   BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    code_hash BYTEA       NOT NULL UNIQUE CHECK (length(code_hash) = 32),
    used_at   TIMESTAMPTZ
);
CREATE INDEX two_factor_recovery_codes_user_idx ON two_factor_recovery_codes (user_id);

-- The second step of a login: the id is the SHA-256 of the token the
-- client got after the correct password. Times come from the server clock.
CREATE TABLE two_factor_challenges (
    id                 BYTEA       PRIMARY KEY CHECK (length(id) = 32),
    user_id            BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    two_factor_version BIGINT      NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL,
    expires_at         TIMESTAMPTZ NOT NULL
);
CREATE INDEX two_factor_challenges_user_idx ON two_factor_challenges (user_id);

-- +goose Down
-- Rolling back turns 2FA off for everybody: admins set it up again at
-- their next login after the upgrade.
DROP TABLE two_factor_challenges;
DROP TABLE two_factor_recovery_codes;
ALTER TABLE users
    DROP COLUMN two_factor_version,
    DROP COLUMN two_factor_last_counter,
    DROP COLUMN two_factor_pending_secret,
    DROP COLUMN two_factor_enabled,
    DROP COLUMN two_factor_secret;
