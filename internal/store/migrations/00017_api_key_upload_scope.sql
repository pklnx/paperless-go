-- +goose Up
-- The API key scope upload (#87, part 1): POST /api/documents only, for a
-- phone or a scanner that must not read the archive. write includes it, so
-- write keys are stored as [write] and keep working unchanged.

-- The check of 00009 that this replaces is found on the table by what it
-- is, not by a typed name: the column check on exactly scopes (generated
-- name api_keys_scopes_check). Anything but exactly one match stops the
-- migration.
-- +goose StatementBegin
DO $$
DECLARE
    checks text[];
BEGIN
    SELECT array_agg(c.conname::text) INTO checks
    FROM pg_constraint c
    WHERE c.conrelid = 'api_keys'::regclass AND c.contype = 'c'
      AND ARRAY(SELECT a.attname::text FROM pg_attribute a
                WHERE a.attrelid = c.conrelid AND a.attnum = ANY (c.conkey)
                ORDER BY a.attname) = ARRAY['scopes'];
    IF coalesce(cardinality(checks), 0) <> 1 THEN
        RAISE EXCEPTION 'api_keys: expected one check on scopes, found %',
            coalesce(checks::text, 'none');
    END IF;
    EXECUTE format('ALTER TABLE api_keys DROP CONSTRAINT %I', checks[1]);
END $$;
-- +goose StatementEnd

ALTER TABLE api_keys ADD CONSTRAINT api_keys_scopes_check
    CHECK (cardinality(scopes) >= 1 AND scopes <@ ARRAY['read', 'upload', 'write']::text[]);

-- +goose Down
-- The old version does not know upload: it is removed from the keys that
-- have another scope too (a [read, upload] key keeps read, a [write,
-- upload] key keeps write, which includes the upload), and the keys left
-- with no scope, those that had only upload, are deleted. Nothing refers
-- to api_keys by foreign key; the audit log names keys only as text.
UPDATE api_keys SET scopes = array_remove(scopes, 'upload')
WHERE 'upload' = ANY (scopes) AND cardinality(array_remove(scopes, 'upload')) >= 1;
DELETE FROM api_keys WHERE 'upload' = ANY (scopes);
ALTER TABLE api_keys DROP CONSTRAINT api_keys_scopes_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_scopes_check
    CHECK (cardinality(scopes) >= 1 AND scopes <@ ARRAY['read', 'write']::text[]);
