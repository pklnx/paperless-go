-- +goose Up
-- Audit targets for metadata (#7, part 1): tags, correspondents and
-- document types, named by their UUID like documents.
--
-- The checks are on the partitioned table: adding one checks every
-- partition, drops and adds reach the partitions' inherited copies, and
-- later partitions (audit_log_ensure_partitions) inherit them. Validation
-- does not fire the append-only row triggers.

-- The two checks of 00005 that this replaces are found on the parent table
-- by what they are, not by a typed name: the column check on exactly
-- target_type (audit_log_target_type_check), and the table check on
-- exactly target_id and target_type that names 'document' (the UUID check,
-- generated name audit_log_check1). The other check on those two columns,
-- (target_type IS NULL) = (target_id IS NULL), does not name 'document'.
-- Anything but exactly one match each stops the migration.
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
    CHECK (target_type IN ('document', 'user', 'api_key', 'tag', 'correspondent', 'document_type'));
-- Documents and metadata are named by their UUID (lowercase, as PostgreSQL
-- prints it).
ALTER TABLE audit_log ADD CONSTRAINT audit_log_target_uuid
    CHECK (target_type NOT IN ('document', 'tag', 'correspondent', 'document_type')
           OR target_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$');

-- +goose Down
-- The log is append-only, so its metadata entries cannot be dropped, and
-- the old checks would refuse them: going back needs a backup restore.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM audit_log WHERE target_type IN ('tag', 'correspondent', 'document_type')) THEN
        RAISE EXCEPTION 'the audit log has entries about tags, correspondents or document types; they are not dropped by a rollback'
            USING HINT = 'Restore a backup made before the upgrade instead.';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE audit_log DROP CONSTRAINT audit_log_target_uuid;
ALTER TABLE audit_log DROP CONSTRAINT audit_log_target_type_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_target_type_check
    CHECK (target_type IN ('document', 'user', 'api_key'));
ALTER TABLE audit_log ADD
    CHECK (target_type <> 'document' OR target_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$');
