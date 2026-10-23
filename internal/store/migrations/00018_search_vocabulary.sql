-- +goose Up
-- Search vocabulary (#56, part 1; docs/development/architecture.md, "Search
-- vocabulary"): per owner, every lexeme of 5 to 64 characters in the
-- documents' search_text and the number of the owner's documents that hold
-- it. A trigram index on it lets a search find the lexemes that contain a
-- typed word (part 2: Stromrechnung for rechnung). It is derived data:
-- documents is only read, and Down loses nothing.
--
-- pg_trgm is a trusted contrib extension like unaccent (00011): the database
-- owner creates it without superuser rights, and it ships with postgres:18.
-- It must live in public, as the index names public.gin_trgm_ops.
CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA public;

-- +goose StatementBegin
DO $$
DECLARE
    schema name;
BEGIN
    SELECT n.nspname INTO schema
    FROM pg_extension x JOIN pg_namespace n ON n.oid = x.extnamespace
    WHERE x.extname = 'pg_trgm';
    IF schema IS DISTINCT FROM 'public' THEN
        -- The fix is in the message itself: the command line prints the
        -- message, not PostgreSQL's hint.
        RAISE EXCEPTION 'the extension pg_trgm is installed in schema %, it must be in public: '
            'run ALTER EXTENSION pg_trgm SET SCHEMA public; and migrate again', schema;
    END IF;
END;
$$;
-- +goose StatementEnd

-- search_vocabulary_lexemes is the one rule for what the vocabulary holds,
-- used by the trigger and the backfill: the distinct lexemes of a vector
-- that are 5 to 64 characters long (characters, not bytes), in the order
-- of the primary key. Shorter ones are found by the prefix match already;
-- longer ones are OCR garbage.
-- +goose StatementBegin
CREATE FUNCTION search_vocabulary_lexemes(v tsvector) RETURNS text[]
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT coalesce(array_agg(l ORDER BY l), '{}')
    FROM unnest(tsvector_to_array(v)) AS l
    WHERE char_length(l) BETWEEN 5 AND 64
$$;
-- +goose StatementEnd

-- lexeme keeps the database's default collation on purpose: pg_trgm splits
-- words by the database's ctype, so both indexes depend on the locale, and
-- collation.go rebuilds an index only when its indcollation is not C.
-- The primary key and the trigram index are built after the backfill.
CREATE TABLE search_vocabulary (
    owner_id BIGINT  NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    lexeme   text    NOT NULL,
    n        integer NOT NULL CHECK (n > 0) -- the owner's documents whose search_text holds it
);

-- The upkeep, row by row. A statement trigger with transition tables is
-- not possible: PostgreSQL refuses them on a trigger with a column list,
-- and without one every bulk change of unrelated columns would copy full
-- rows. For a document that is new to an owner its lexemes count up by
-- one; for one that left (deleted, or another owner) they count down: a
-- row that would reach 0 is deleted, the others decrease, so no path can
-- violate the CHECK, even after a count drifted. An update changes only
-- the set difference of the old and the new lexemes.
--
-- Concurrency: every store transaction that writes documents holds the
-- owner's write lock (locking.go) first, and this trigger runs inside it,
-- writing only that owner's rows. So writers of one owner never touch the
-- vocabulary at the same time and writers of different owners touch
-- disjoint rows: the lock alone makes it safe, not the order of the rows
-- (which follows the primary key, as far as one statement goes).
-- +goose StatementBegin
CREATE FUNCTION search_vocabulary_trigger() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    gone  text[] := '{}';
    added text[] := '{}';
BEGIN
    IF TG_OP <> 'INSERT' THEN
        gone := search_vocabulary_lexemes(OLD.search_text);
    END IF;
    IF TG_OP <> 'DELETE' THEN
        added := search_vocabulary_lexemes(NEW.search_text);
    END IF;
    IF TG_OP = 'UPDATE' AND OLD.owner_id = NEW.owner_id THEN
        SELECT ARRAY(SELECT unnest(gone) EXCEPT SELECT unnest(added)),
               ARRAY(SELECT l FROM (SELECT unnest(added) EXCEPT SELECT unnest(gone)) s (l) ORDER BY l)
        INTO gone, added;
    END IF;
    IF cardinality(gone) > 0 THEN
        DELETE FROM search_vocabulary
        WHERE owner_id = OLD.owner_id AND lexeme = ANY (gone) AND n <= 1;
        UPDATE search_vocabulary SET n = n - 1
        WHERE owner_id = OLD.owner_id AND lexeme = ANY (gone) AND n > 1;
    END IF;
    IF cardinality(added) > 0 THEN
        INSERT INTO search_vocabulary AS v (owner_id, lexeme, n)
        SELECT NEW.owner_id, l, 1 FROM unnest(added) AS l
        ON CONFLICT (owner_id, lexeme) DO UPDATE SET n = v.n + 1;
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- Close the backfill race: no writer may commit between the backfill's
-- snapshot and the triggers. SHARE ROW EXCLUSIVE waits for open writers
-- of documents and blocks new ones until this migration commits; readers
-- go on.
LOCK TABLE documents IN SHARE ROW EXCLUSIVE MODE;

INSERT INTO search_vocabulary (owner_id, lexeme, n)
SELECT d.owner_id, l, count(*)
FROM documents d, unnest(search_vocabulary_lexemes(d.search_text)) AS l
GROUP BY d.owner_id, l;

ALTER TABLE search_vocabulary ADD PRIMARY KEY (owner_id, lexeme);
CREATE INDEX search_vocabulary_trgm_idx ON search_vocabulary USING GIN (lexeme public.gin_trgm_ops);

-- The column list holds the source columns too: search_text is set by the
-- BEFORE trigger of 00011, and UPDATE OF looks only at the statement's SET
-- list. The WHEN keeps updates that leave the vector alone (a patch that
-- repeats the title, an OCR run with the same text) nearly free; bulk
-- changes of flags and links do not even fire it.
CREATE TRIGGER search_vocabulary_insert_delete
    AFTER INSERT OR DELETE ON documents
    FOR EACH ROW EXECUTE FUNCTION search_vocabulary_trigger();

CREATE TRIGGER search_vocabulary_update
    AFTER UPDATE OF title, archive_serial_number, notes, original_file_name, extracted_text, search_text, owner_id
    ON documents
    FOR EACH ROW
    WHEN (OLD.search_text IS DISTINCT FROM NEW.search_text OR OLD.owner_id <> NEW.owner_id)
    EXECUTE FUNCTION search_vocabulary_trigger();

-- +goose Down
DROP TRIGGER search_vocabulary_update ON documents;
DROP TRIGGER search_vocabulary_insert_delete ON documents;
DROP FUNCTION search_vocabulary_trigger();
DROP TABLE search_vocabulary;
DROP FUNCTION search_vocabulary_lexemes(tsvector);
-- pg_trgm goes under the conditions of unaccent in 00011's Down: only when
-- the current user owns it (this migration created it) and nothing outside
-- the extension uses it. One an administrator created stays.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_extension x
        WHERE x.extname = 'pg_trgm'
          AND x.extowner = (SELECT oid FROM pg_roles WHERE rolname = current_user)
          AND NOT EXISTS (
              -- an object that is not part of the extension and depends on
              -- one of its members
              SELECT 1 FROM pg_depend member
              JOIN pg_depend d ON d.refclassid = member.classid AND d.refobjid = member.objid
              WHERE member.refclassid = 'pg_extension'::regclass AND member.refobjid = x.oid
                AND member.deptype = 'e' AND d.deptype = 'n'
                AND NOT EXISTS (
                    SELECT 1 FROM pg_depend m2
                    WHERE m2.classid = d.classid AND m2.objid = d.objid
                      AND m2.refclassid = 'pg_extension'::regclass AND m2.refobjid = x.oid
                      AND m2.deptype = 'e')
                -- nor a part of one of them: pg_trgm's upgrade scripts add
                -- support functions to its operator families (pg_amproc
                -- rows that depend on the family automatically)
                AND NOT EXISTS (
                    SELECT 1 FROM pg_depend part
                    JOIN pg_depend m3 ON m3.classid = part.refclassid AND m3.objid = part.refobjid
                    WHERE part.classid = d.classid AND part.objid = d.objid AND part.deptype IN ('a', 'i')
                      AND m3.refclassid = 'pg_extension'::regclass AND m3.refobjid = x.oid
                      AND m3.deptype = 'e'))
    ) THEN
        DROP EXTENSION pg_trgm;
    END IF;
END;
$$;
-- +goose StatementEnd
