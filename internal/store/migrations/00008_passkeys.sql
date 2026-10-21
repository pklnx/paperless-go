-- +goose Up
-- Passkeys (WebAuthn, #3 part 4), ported from mail-archive. A passkey login
-- replaces password and second factor. Auth tables go with their user
-- (ON DELETE CASCADE). Times come from the server clock.

-- The WebAuthn user ID: random, created with the first passkey. It is
-- neither the database ID nor the name.
ALTER TABLE users ADD COLUMN webauthn_handle BYTEA UNIQUE CHECK (length(webauthn_handle) = 32);

CREATE TABLE passkeys (
    id            BIGSERIAL   PRIMARY KEY,
    user_id       BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    credential_id BYTEA       NOT NULL UNIQUE,
    name          TEXT        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
    -- The library's credential record: public key, sign count, flags.
    credential    JSONB       NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL,
    last_used_at  TIMESTAMPTZ,
    UNIQUE (user_id, name)
);

-- A registration or login in progress. Single use; the id is the SHA-256
-- of the token the client got.
CREATE TABLE webauthn_ceremonies (
    id         BYTEA       PRIMARY KEY CHECK (length(id) = 32),
    kind       TEXT        NOT NULL CHECK (kind IN ('register', 'login')),
    user_id    BIGINT      REFERENCES users (id) ON DELETE CASCADE,
    name       TEXT,
    -- The second factor that confirmed a registration (totp,
    -- recovery_code), for its audit entry; NULL without 2FA.
    factor     TEXT CHECK (factor IN ('totp', 'recovery_code')),
    data       JSONB       NOT NULL,
    -- The client of a login (its address, IPv6 grouped by /64), for the
    -- per-client cap on open logins.
    client_key TEXT,
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    CHECK ((kind = 'register') = (user_id IS NOT NULL AND name IS NOT NULL))
);
CREATE INDEX webauthn_ceremonies_user_idx ON webauthn_ceremonies (user_id);
CREATE INDEX webauthn_ceremonies_expires_idx ON webauthn_ceremonies (expires_at);
CREATE INDEX webauthn_ceremonies_client_idx ON webauthn_ceremonies (client_key) WHERE kind = 'login';

-- +goose Down
-- Rolling back removes every passkey: users log in with their password.
DROP TABLE webauthn_ceremonies;
DROP TABLE passkeys;
ALTER TABLE users DROP COLUMN webauthn_handle;
