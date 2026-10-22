-- +goose Up
-- The database locale that the collation-dependent indexes were last built
-- with (store.Migrate, collation.go). A new libc (the switch from the
-- Alpine to the Debian PostgreSQL image), a new ICU or another PostgreSQL
-- major version can sort text differently and leave those indexes out of
-- order; migrate then rebuilds them and stores the new fingerprint. No row
-- means unknown: migrate fills it in.
CREATE TABLE index_collation (
    singleton   BOOLEAN     PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    fingerprint TEXT        NOT NULL CHECK (fingerprint <> ''),
    built_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE index_collation;
