-- +goose Up
-- Deadlines and saved views (docs/development/architecture.md#data-model),
-- following the Document Archive app's Deadline and SavedView models.

-- A deadline or expiry date of a document; a document can have several.
-- kind and origin are the app's raw strings, kept as they are, so values of
-- newer app versions survive a round trip. The composite foreign key keeps
-- a deadline with its document's owner; deleting the document deletes its
-- deadlines.
CREATE TABLE deadlines (
    id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id              BIGINT      NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    document_id           UUID        NOT NULL,
    -- expires, due, noticeEnds, other, or a newer app version's value.
    kind                  TEXT        NOT NULL DEFAULT 'other',
    date                  TIMESTAMPTZ NOT NULL,
    label                 TEXT        NOT NULL DEFAULT '',
    -- manual, heuristic, ai, or a newer app version's value.
    origin                TEXT        NOT NULL DEFAULT 'manual',
    is_done               BOOLEAN     NOT NULL DEFAULT FALSE,
    added_to_calendar_at  TIMESTAMPTZ,
    added_to_reminders_at TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    modified_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT deadlines_document_fkey FOREIGN KEY (owner_id, document_id)
        REFERENCES documents (owner_id, id) ON DELETE CASCADE
);
-- A document's deadlines in order; also serves the cascade from documents.
CREATE INDEX deadlines_document_idx ON deadlines (document_id, date, id);
-- An owner's upcoming deadlines (overview, reminders).
CREATE INDEX deadlines_owner_date_idx ON deadlines (owner_id, date) WHERE NOT is_done;

-- A saved view: a named document filter shown in the sidebar. filter is the
-- app's filterJSON as a JSON object; keys the server does not know are kept.
-- Names are not unique: the app allows two views with the same name.
CREATE TABLE saved_views (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id    BIGINT      NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    name        TEXT        NOT NULL DEFAULT '',
    symbol      TEXT        NOT NULL DEFAULT 'line.3.horizontal.decrease.circle',
    sort_index  BIGINT      NOT NULL DEFAULT 0,
    filter      JSONB       NOT NULL DEFAULT '{}'
                            CONSTRAINT saved_views_filter_object CHECK (jsonb_typeof(filter) = 'object'),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    modified_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- The app's order: sortIndex, then createdAt, then id.
CREATE INDEX saved_views_owner_order_idx ON saved_views (owner_id, sort_index, created_at, id);

-- +goose Down
DROP TABLE saved_views;
DROP TABLE deadlines;
