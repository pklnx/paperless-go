-- +goose Up
-- The trash (#12, part 1): a deleted document keeps its row with
-- deleted_at set and leaves every list, search and count; the sweeper
-- deletes the row once the retention passed or the user asked for it
-- (purge_at). No row is rewritten: documents only gains two nullable
-- columns, a check and indexes.
ALTER TABLE documents
    ADD COLUMN deleted_at TIMESTAMPTZ,
    ADD COLUMN purge_at TIMESTAMPTZ,
    ADD CONSTRAINT documents_purge_needs_trash CHECK (purge_at IS NULL OR deleted_at IS NOT NULL);

-- The trash list (sort deleted) and the sweeper's scan for due rows. The
-- live lists keep their indexes; deleted_at IS NULL is a recheck there.
CREATE INDEX documents_trash_idx ON documents (owner_id, deleted_at DESC, id) WHERE deleted_at IS NOT NULL;

-- The reference check of the file removal (#12, part 2): a blob is in use
-- while any document names it as original, archive or thumbnail.
-- documents_archive_sha256_idx exists since 00002.
CREATE INDEX documents_original_sha256_idx ON documents (original_sha256);
CREATE INDEX documents_thumbnail_sha256_idx ON documents (thumbnail_sha256);

-- The files that may have lost their last reference: a purge queues the
-- three hashes of each deleted row. Part 1 only writes it; the sweeper's
-- queue pass (part 2) removes a file once due_at has passed and no
-- document names it, marking removed_at before the file goes.
CREATE TABLE blob_removals (
    sha256     TEXT        PRIMARY KEY CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    queued_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    due_at     TIMESTAMPTZ NOT NULL,
    removed_at TIMESTAMPTZ
);
CREATE INDEX blob_removals_due_idx ON blob_removals (due_at);

-- Audit targets: saved views (#12, part 4), named by their UUID. The two
-- checks of 00016 this replaces are found on the partitioned table by
-- what they are, as 00016 did: the column check on exactly target_type,
-- and the table check on exactly target_id and target_type that names
-- 'document'. Anything but exactly one match each stops the migration.
-- +goose StatementBegin
DO $$
DECLARE
    kinds text[];
    uuids text[];
BEGIN
    SELECT array_agg(c.conname::text) INTO kinds
    FROM pg_constraint c
    WHERE c.conrelid = 'audit_log'::regclass AND c.contype = 'c'
      AND ARRAY(SELECT a.attname::text FROM pg_attribute a
                WHERE a.attrelid = c.conrelid AND a.attnum = ANY (c.conkey)
                ORDER BY a.attname) = ARRAY['target_type'];
    IF coalesce(cardinality(kinds), 0) <> 1 THEN
        RAISE EXCEPTION 'audit_log: expected one check on target_type, found %',
            coalesce(kinds::text, 'none');
    END IF;

    SELECT array_agg(c.conname::text) INTO uuids
    FROM pg_constraint c
    WHERE c.conrelid = 'audit_log'::regclass AND c.contype = 'c'
      AND ARRAY(SELECT a.attname::text FROM pg_attribute a
                WHERE a.attrelid = c.conrelid AND a.attnum = ANY (c.conkey)
                ORDER BY a.attname) = ARRAY['target_id', 'target_type']
      AND pg_get_constraintdef(c.oid) LIKE '%''document''%';
    IF coalesce(cardinality(uuids), 0) <> 1 THEN
        RAISE EXCEPTION 'audit_log: expected one UUID check on document targets, found %',
            coalesce(uuids::text, 'none');
    END IF;

    EXECUTE format('ALTER TABLE audit_log DROP CONSTRAINT %I', kinds[1]);
    EXECUTE format('ALTER TABLE audit_log DROP CONSTRAINT %I', uuids[1]);
END $$;
-- +goose StatementEnd

ALTER TABLE audit_log ADD CONSTRAINT audit_log_target_type_check
    CHECK (target_type IN ('document', 'user', 'api_key', 'tag', 'correspondent', 'document_type', 'saved_view'));
ALTER TABLE audit_log ADD CONSTRAINT audit_log_target_uuid
    CHECK (target_type NOT IN ('document', 'tag', 'correspondent', 'document_type', 'saved_view')
           OR target_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$');

-- +goose Down
-- The log is append-only, so its saved-view entries cannot be dropped, and
-- the old checks would refuse them: going back needs a backup restore.
-- Trashed documents become live again (their deleted_at goes with the
-- column); the queued file removals are forgotten, so their files stay.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM audit_log WHERE target_type = 'saved_view') THEN
        RAISE EXCEPTION 'the audit log has entries about saved views; they are not dropped by a rollback'
            USING HINT = 'Restore a backup made before the upgrade instead.';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE audit_log DROP CONSTRAINT audit_log_target_uuid;
ALTER TABLE audit_log DROP CONSTRAINT audit_log_target_type_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_target_type_check
    CHECK (target_type IN ('document', 'user', 'api_key', 'tag', 'correspondent', 'document_type'));
ALTER TABLE audit_log ADD CONSTRAINT audit_log_target_uuid
    CHECK (target_type NOT IN ('document', 'tag', 'correspondent', 'document_type')
           OR target_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$');

DROP TABLE blob_removals;
DROP INDEX documents_thumbnail_sha256_idx;
DROP INDEX documents_original_sha256_idx;
DROP INDEX documents_trash_idx;
ALTER TABLE documents
    DROP CONSTRAINT documents_purge_needs_trash,
    DROP COLUMN purge_at,
    DROP COLUMN deleted_at;
