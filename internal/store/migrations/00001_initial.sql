-- +goose Up
-- One row that identifies this installation, for example in exported
-- archive folders ("created by"). The document tables follow with the
-- document model (see docs/development/roadmap.md).
CREATE TABLE installation (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    singleton  BOOLEAN     NOT NULL DEFAULT TRUE UNIQUE CHECK (singleton),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO installation DEFAULT VALUES;

-- +goose Down
DROP TABLE installation;
