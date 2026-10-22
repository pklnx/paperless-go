-- +goose Up
-- API keys for scripts (#3 part 5), ported from mail-archive. Only the
-- SHA-256 of a key is stored; the key is shown once at creation. Keys go
-- with their user (ON DELETE CASCADE). Times come from the server clock.
CREATE TABLE api_keys (
    id            BIGSERIAL   PRIMARY KEY,
    user_id       BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    key_hash      BYTEA       NOT NULL UNIQUE CHECK (length(key_hash) = 32),
    hint          TEXT        NOT NULL CHECK (hint ~ '^[a-z2-7]{4}$'),
    name          TEXT        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
    scopes        TEXT[]      NOT NULL CHECK (cardinality(scopes) >= 1 AND scopes <@ ARRAY['read', 'write']::text[]),
    created_at    TIMESTAMPTZ NOT NULL,
    expires_at    TIMESTAMPTZ CHECK (expires_at > created_at),
    last_used_at  TIMESTAMPTZ,
    revoked_at    TIMESTAMPTZ,
    -- Who revoked it: the owner, an admin, or NULL for the CLI and for a
    -- revoking user who was removed later.
    revoked_by    BIGINT      REFERENCES users (id) ON DELETE SET NULL
);
-- Names are unique among a user's keys that are not revoked, so a name can
-- be reused after revoking the old key.
CREATE UNIQUE INDEX api_keys_user_name_idx ON api_keys (user_id, name) WHERE revoked_at IS NULL;
-- Listing a user's keys (also revoked ones) and the cascade on user removal.
CREATE INDEX api_keys_user_id_idx ON api_keys (user_id);
-- The revoker's SET NULL on user removal.
CREATE INDEX api_keys_revoked_by_idx ON api_keys (revoked_by) WHERE revoked_by IS NOT NULL;

-- +goose Down
-- Rolling back removes every API key: scripts need new keys after an
-- upgrade again.
DROP TABLE api_keys;
