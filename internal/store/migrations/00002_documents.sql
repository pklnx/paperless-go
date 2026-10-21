-- +goose Up
-- The document model (docs/development/architecture.md#data-model). Every
-- row belongs to one user. Composite foreign keys (owner_id, x_id) →
-- x (owner_id, id) keep owners apart in the database itself: a document can
-- only point at tags, correspondents and document types of its own owner.

-- Minimal on purpose: users and login (#3) add the login columns and
-- sessions with ALTER TABLE. Nothing creates users before that.
CREATE TABLE users (
    id         BIGSERIAL   PRIMARY KEY,
    name       TEXT        NOT NULL UNIQUE CHECK (btrim(name) <> ''),
    is_admin   BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE correspondents (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id    BIGINT      NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    name        TEXT        NOT NULL CHECK (btrim(name) <> ''),
    match_text  TEXT        NOT NULL DEFAULT '',
    match_mode  TEXT        NOT NULL DEFAULT 'any'
                            CHECK (match_mode IN ('none', 'any', 'all', 'exact', 'regex')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    modified_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (owner_id, id)
);
CREATE UNIQUE INDEX correspondents_owner_name_key ON correspondents (owner_id, lower(name));

CREATE TABLE document_types (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id    BIGINT      NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    name        TEXT        NOT NULL CHECK (btrim(name) <> ''),
    symbol      TEXT        NOT NULL DEFAULT 'doc.text',
    match_text  TEXT        NOT NULL DEFAULT '',
    match_mode  TEXT        NOT NULL DEFAULT 'any'
                            CHECK (match_mode IN ('none', 'any', 'all', 'exact', 'regex')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    modified_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (owner_id, id)
);
CREATE UNIQUE INDEX document_types_owner_name_key ON document_types (owner_id, lower(name));

CREATE TABLE tags (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id    BIGINT      NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    name        TEXT        NOT NULL CHECK (btrim(name) <> ''),
    color_hex   TEXT        NOT NULL DEFAULT '#3B82F6' CHECK (color_hex ~ '^#[0-9A-Fa-f]{6}$'),
    symbol      TEXT        NOT NULL DEFAULT 'tag.fill',
    parent_id   UUID,
    match_text  TEXT        NOT NULL DEFAULT '',
    match_mode  TEXT        NOT NULL DEFAULT 'any'
                            CHECK (match_mode IN ('none', 'any', 'all', 'exact', 'regex')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    modified_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (owner_id, id),
    CONSTRAINT tags_parent_not_self CHECK (parent_id <> id),
    -- Deleting a parent makes its children roots, as in the app.
    CONSTRAINT tags_parent_fkey FOREIGN KEY (owner_id, parent_id)
        REFERENCES tags (owner_id, id) ON DELETE SET NULL (parent_id)
);
-- Sibling names are unique, ignoring case; NULLS NOT DISTINCT makes this
-- hold for root tags too.
CREATE UNIQUE INDEX tags_owner_parent_name_key ON tags (owner_id, parent_id, lower(name)) NULLS NOT DISTINCT;
CREATE INDEX tags_parent_idx ON tags (parent_id);

-- tags_no_cycle refuses a parent that would make a tag its own ancestor.
-- A per-owner advisory lock serialises parent changes of one owner, so two
-- concurrent changes (A→B and B→A) cannot both pass; under READ COMMITTED
-- the walk after the lock sees the other transaction's committed change.
-- The dedicated SQLSTATE PGT01 lets the store map it to ErrTagCycle.
-- +goose StatementBegin
CREATE FUNCTION tags_no_cycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.parent_id IS NULL THEN
        RETURN NEW;
    END IF;
    IF NEW.parent_id = NEW.id THEN
        RAISE EXCEPTION 'tag % cannot be its own parent', NEW.id USING ERRCODE = 'PGT01';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended('paperless-go:tags:' || NEW.owner_id::text, 0));
    IF EXISTS (
        WITH RECURSIVE ancestors (id, parent_id) AS (
            SELECT t.id, t.parent_id FROM tags t
            WHERE t.id = NEW.parent_id AND t.owner_id = NEW.owner_id
            UNION
            SELECT t.id, t.parent_id FROM tags t
            JOIN ancestors a ON t.id = a.parent_id
            WHERE t.owner_id = NEW.owner_id
        )
        SELECT 1 FROM ancestors WHERE id = NEW.id
    ) THEN
        RAISE EXCEPTION 'tag % cannot be a descendant of itself', NEW.id USING ERRCODE = 'PGT01';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER tags_no_cycle
    BEFORE INSERT OR UPDATE OF parent_id ON tags
    FOR EACH ROW EXECUTE FUNCTION tags_no_cycle();

CREATE TABLE documents (
    id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id              BIGINT      NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    title                 TEXT        NOT NULL DEFAULT '',
    -- Issue date of the document (for example the invoice date).
    document_date         TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    modified_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    notes                 TEXT        NOT NULL DEFAULT '',
    is_favorite           BOOLEAN     NOT NULL DEFAULT FALSE,
    is_inbox              BOOLEAN     NOT NULL DEFAULT TRUE,
    archive_serial_number INTEGER     CHECK (archive_serial_number > 0),
    page_count            INTEGER     NOT NULL DEFAULT 0 CHECK (page_count >= 0),
    extracted_text        TEXT        NOT NULL DEFAULT '',
    -- Raw string from the app ("Mistral OCR", "PDF-Text", ...), kept as is.
    text_source           TEXT        NOT NULL DEFAULT '',
    source                TEXT        NOT NULL CHECK (source IN ('file', 'scan', 'image')),
    -- The uploaded file (the app's contentHash); used for duplicates.
    original_file_name    TEXT        NOT NULL DEFAULT '',
    original_sha256       TEXT        NOT NULL CHECK (original_sha256 ~ '^[0-9a-f]{64}$'),
    original_size         BIGINT      NOT NULL CHECK (original_size >= 0),
    original_mime         TEXT        NOT NULL,
    -- The PDF with a text layer, NULL until OCR (#5) has run.
    archive_sha256        TEXT        CHECK (archive_sha256 ~ '^[0-9a-f]{64}$'),
    archive_size          BIGINT      CHECK (archive_size >= 0),
    correspondent_id      UUID,
    document_type_id      UUID,
    CONSTRAINT documents_archive_file_check CHECK ((archive_sha256 IS NULL) = (archive_size IS NULL)),
    UNIQUE (owner_id, id),
    CONSTRAINT documents_owner_original_sha256_key UNIQUE (owner_id, original_sha256),
    CONSTRAINT documents_correspondent_fkey FOREIGN KEY (owner_id, correspondent_id)
        REFERENCES correspondents (owner_id, id) ON DELETE SET NULL (correspondent_id),
    CONSTRAINT documents_document_type_fkey FOREIGN KEY (owner_id, document_type_id)
        REFERENCES document_types (owner_id, id) ON DELETE SET NULL (document_type_id)
);
CREATE UNIQUE INDEX documents_owner_asn_key ON documents (owner_id, archive_serial_number);
CREATE INDEX documents_owner_created_idx ON documents (owner_id, created_at DESC, id);
CREATE INDEX documents_correspondent_idx ON documents (correspondent_id);
CREATE INDEX documents_document_type_idx ON documents (document_type_id);
CREATE INDEX documents_archive_sha256_idx ON documents (archive_sha256);

CREATE TABLE document_tags (
    owner_id    BIGINT NOT NULL,
    document_id UUID   NOT NULL,
    tag_id      UUID   NOT NULL,
    PRIMARY KEY (document_id, tag_id),
    CONSTRAINT document_tags_document_fkey FOREIGN KEY (owner_id, document_id)
        REFERENCES documents (owner_id, id) ON DELETE CASCADE,
    CONSTRAINT document_tags_tag_fkey FOREIGN KEY (owner_id, tag_id)
        REFERENCES tags (owner_id, id) ON DELETE CASCADE
);
CREATE INDEX document_tags_tag_idx ON document_tags (tag_id);

-- +goose Down
DROP TABLE document_tags;
DROP TABLE documents;
DROP TRIGGER tags_no_cycle ON tags;
DROP FUNCTION tags_no_cycle();
DROP TABLE tags;
DROP TABLE document_types;
DROP TABLE correspondents;
DROP TABLE users;
