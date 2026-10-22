-- +goose Up
-- Full-text search (#6, part 1): the index and the query function
-- (docs/development/architecture.md#search). Two tsvector columns, kept up
-- to date by triggers, and one GIN index over their concatenation:
--   search_text   the document's own fields (title, ASN, notes, file name,
--                 extracted text), re-parsed only when one of them changes;
--   search_links  one opaque lexeme per linked correspondent, document type
--                 and tag, rebuilt when a link changes.
-- Names are not stored: document_search_tsquery resolves a term to the IDs
-- of the tags, correspondents and types whose name contains it when the
-- query runs, so renaming or moving a tag rewrites no document.
--
-- unaccent is a trusted contrib extension (PostgreSQL 13+), so the database
-- owner can create it without superuser rights; it ships with the
-- postgres:18-alpine image. It and paperless_simple live in the public
-- schema and are always referenced schema-qualified, like the built-in
-- configurations: a text search configuration is a schema object, and the
-- triggers must not depend on the caller's search_path.
CREATE EXTENSION IF NOT EXISTS unaccent SCHEMA public;

-- simple plus unaccent: exact words, numbers and prefixes, ignoring case
-- and accents (ä → a, ß → ss).
CREATE TEXT SEARCH CONFIGURATION public.paperless_simple (COPY = pg_catalog.simple);
ALTER TEXT SEARCH CONFIGURATION public.paperless_simple
    ALTER MAPPING FOR word, hword, hword_part, asciiword, asciihword, hword_asciipart
    WITH public.unaccent, pg_catalog.simple;

-- A constant default needs no table rewrite.
ALTER TABLE documents
    ADD COLUMN search_text  tsvector NOT NULL DEFAULT '',
    ADD COLUMN search_links tsvector NOT NULL DEFAULT '';

-- documents_search_text_build parses the document's own fields with at
-- most text_cap characters of the extracted text (0: none) and, when
-- field_cap is not NULL, at most field_cap characters of every other field.
-- Weights: title and ASN A, notes and file name C, text D (links are B).
--
-- The order of the parts is on purpose. `a || b` shifts b's positions by
-- a's largest position, and positions stop at 16 383. The short fields go
-- first and the german copy of the text right after them, so phrases rely
-- on that copy: it keeps real positions up to about the 16 000th word of
-- the text, while the simple and english copies behind it end up at
-- 16 383 in a long text. Plain words match in every copy.
-- +goose StatementBegin
CREATE FUNCTION documents_search_text_build(
    title text, asn integer, notes text, file_name text, extracted text,
    text_cap integer, field_cap integer
) RETURNS tsvector
LANGUAGE plpgsql STABLE AS $$
DECLARE
    t text := left(coalesce(title, ''), coalesce(field_cap, 2147483647));
    n text := left(coalesce(notes, ''), coalesce(field_cap, 2147483647));
    f text := translate(left(coalesce(file_name, ''), coalesce(field_cap, 2147483647)), '_.-', '   ');
    x text := left(coalesce(extracted, ''), text_cap);
    v tsvector;
BEGIN
    v := setweight(to_tsvector('public.paperless_simple', t), 'A')
        || setweight(to_tsvector('pg_catalog.german', t), 'A')
        || setweight(to_tsvector('pg_catalog.english', t), 'A')
        || setweight(to_tsvector('pg_catalog.simple',
               CASE WHEN asn IS NULL THEN '' ELSE asn::text || ' asn' || asn::text END), 'A')
        || setweight(to_tsvector('public.paperless_simple', f), 'C')
        || setweight(to_tsvector('pg_catalog.german', n), 'C')
        || setweight(to_tsvector('public.paperless_simple', n), 'C')
        || setweight(to_tsvector('pg_catalog.english', n), 'C')
        || setweight(to_tsvector('pg_catalog.german', x), 'D')
        || setweight(to_tsvector('public.paperless_simple', x), 'D')
        || setweight(to_tsvector('pg_catalog.english', x), 'D');
    -- Leave room below the 1 MB limit for search_links: the index
    -- expression search_text || search_links must never overflow either
    -- (a link lexeme takes about 50 bytes, so 128 KiB hold over 2000).
    IF pg_column_size(v) > 917504 THEN
        RAISE EXCEPTION 'search_text over 896 KiB' USING ERRCODE = 'program_limit_exceeded';
    END IF;
    RETURN v;
END;
$$;
-- +goose StatementEnd

-- documents_search_text_vector never fails because of the index. A tsvector
-- cannot exceed 1 MB (SQLSTATE 54000), and documents_search_text_build
-- refuses more than 896 KiB, so it takes the first 300 000 characters of
-- the text (the worker allows 16 MiB), then 100 000, then none; then
-- 100 000 characters of the text and of each other field, then of the
-- other fields only; then nothing.
-- +goose StatementBegin
CREATE FUNCTION documents_search_text_vector(
    title text, asn integer, notes text, file_name text, extracted text
) RETURNS tsvector
LANGUAGE plpgsql STABLE AS $$
BEGIN
    BEGIN
        RETURN documents_search_text_build(title, asn, notes, file_name, extracted, 300000, NULL);
    EXCEPTION WHEN program_limit_exceeded THEN NULL;
    END;
    BEGIN
        RETURN documents_search_text_build(title, asn, notes, file_name, extracted, 100000, NULL);
    EXCEPTION WHEN program_limit_exceeded THEN NULL;
    END;
    BEGIN
        RETURN documents_search_text_build(title, asn, notes, file_name, extracted, 0, NULL);
    EXCEPTION WHEN program_limit_exceeded THEN NULL;
    END;
    BEGIN
        RETURN documents_search_text_build(title, asn, notes, file_name, extracted, 100000, 100000);
    EXCEPTION WHEN program_limit_exceeded THEN NULL;
    END;
    BEGIN
        RETURN documents_search_text_build(title, asn, notes, file_name, extracted, 0, 100000);
    EXCEPTION WHEN program_limit_exceeded THEN NULL;
    END;
    RETURN ''::tsvector;
END;
$$;
-- +goose StatementEnd

-- documents_search_link_lexeme is the lexeme of one link: a leading space,
-- a kind letter (c correspondent, d document type, t tag) and the UUID. The
-- text parser never produces a lexeme with a space, so a typed word cannot
-- match it.
-- +goose StatementBegin
CREATE FUNCTION documents_search_link_lexeme(kind text, id uuid) RETURNS text
LANGUAGE sql IMMUTABLE STRICT AS $$ SELECT ' ' || kind || id::text $$;
-- +goose StatementEnd

-- documents_search_links_vector builds a document's link lexemes, each at
-- position 1 with weight B (ts_rank_cd needs positions). It reads
-- document_tags, so it is VOLATILE on purpose: each statement in it takes a
-- fresh snapshot. A trigger that waited for the document's row lock thus
-- sees the tags another transaction committed meanwhile; a STABLE function
-- would keep the old snapshot and could drop them. Every writer of
-- document_tags locks the document row first (SetDocumentTags does, via
-- LockDocument), so two rebuilds of one document never interleave.
-- +goose StatementBegin
CREATE FUNCTION documents_search_links_vector(doc uuid, correspondent uuid, document_type uuid)
RETURNS tsvector
LANGUAGE plpgsql VOLATILE AS $$
DECLARE
    lexemes text[];
BEGIN
    SELECT array_agg(l ORDER BY l) INTO lexemes FROM (
        SELECT documents_search_link_lexeme('c', correspondent) AS l WHERE correspondent IS NOT NULL
        UNION ALL
        SELECT documents_search_link_lexeme('d', document_type) WHERE document_type IS NOT NULL
        UNION ALL
        SELECT documents_search_link_lexeme('t', dt.tag_id) FROM document_tags dt WHERE dt.document_id = doc
    ) s;
    IF lexemes IS NULL THEN
        RETURN ''::tsvector;
    END IF;
    -- The tsvector literal: each lexeme quoted, at position 1, weight B. A
    -- lexeme holds only a space, a letter, hex digits and dashes, so it
    -- needs no escaping.
    RETURN (SELECT string_agg('''' || l || ''':1B', ' ') FROM unnest(lexemes) l)::tsvector;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION documents_search_text_trigger() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    -- SetDocumentProcessed assigns extracted_text on every run; an
    -- unchanged row needs no new parse.
    IF TG_OP = 'UPDATE'
       AND NEW.title IS NOT DISTINCT FROM OLD.title
       AND NEW.archive_serial_number IS NOT DISTINCT FROM OLD.archive_serial_number
       AND NEW.notes IS NOT DISTINCT FROM OLD.notes
       AND NEW.original_file_name IS NOT DISTINCT FROM OLD.original_file_name
       AND NEW.extracted_text IS NOT DISTINCT FROM OLD.extracted_text THEN
        RETURN NEW;
    END IF;
    NEW.search_text := documents_search_text_vector(
        NEW.title, NEW.archive_serial_number, NEW.notes, NEW.original_file_name, NEW.extracted_text);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER documents_search_text
    BEFORE INSERT OR UPDATE OF title, archive_serial_number, notes, original_file_name, extracted_text
    ON documents
    FOR EACH ROW EXECUTE FUNCTION documents_search_text_trigger();

-- The ON DELETE SET NULL of a removed correspondent or document type is an
-- UPDATE of that column, so this trigger runs then too.
-- +goose StatementBegin
CREATE FUNCTION documents_search_links_trigger() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE'
       AND NEW.correspondent_id IS NOT DISTINCT FROM OLD.correspondent_id
       AND NEW.document_type_id IS NOT DISTINCT FROM OLD.document_type_id THEN
        RETURN NEW;
    END IF;
    NEW.search_links := documents_search_links_vector(NEW.id, NEW.correspondent_id, NEW.document_type_id);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER documents_search_links
    BEFORE INSERT OR UPDATE OF correspondent_id, document_type_id
    ON documents
    FOR EACH ROW EXECUTE FUNCTION documents_search_links_trigger();

-- Statement-level triggers on document_tags refresh each affected document
-- once per statement. A tag's delete reaches document_tags through its
-- cascade, so it is covered; a document's delete finds no row to refresh.
-- Rows of document_tags are only inserted and deleted, never updated (the
-- store has no such query), so there is no UPDATE trigger; a future UPDATE
-- of document_tags must add one.
-- A tag's delete locks document_tags rows before the documents, while
-- SetDocumentTags locks the document first: run concurrently on the same
-- document, PostgreSQL may detect a deadlock and fail one of them (no hang,
-- nothing half-written). The bulk edit (#7) should retry on 40P01.
-- +goose StatementBegin
CREATE FUNCTION document_tags_search_trigger() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE documents d
    SET search_links = documents_search_links_vector(d.id, d.correspondent_id, d.document_type_id)
    WHERE d.id IN (SELECT document_id FROM changed);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER document_tags_search_insert
    AFTER INSERT ON document_tags
    REFERENCING NEW TABLE AS changed
    FOR EACH STATEMENT EXECUTE FUNCTION document_tags_search_trigger();

CREATE TRIGGER document_tags_search_delete
    AFTER DELETE ON document_tags
    REFERENCING OLD TABLE AS changed
    FOR EACH STATEMENT EXECUTE FUNCTION document_tags_search_trigger();

-- document_search_tsquery builds the full-text condition of a search from
-- the parser's free-text items, passed as bound arrays of equal length:
-- terms[i] is a word or (is_phrase[i]) a phrase, negated[i] excludes it,
-- and items with the same or_group[i] are joined by OR; the groups are
-- joined by AND. Each item matches
--   - by stem or exact word: plainto_tsquery / phraseto_tsquery in the
--     german, english and paperless_simple configurations;
--   - by prefix, for a word of 3 or more characters: each lexeme
--     paperless_simple makes of it, quoted by a fixed escaper, plus :*;
--   - through a link: the lexemes of the owner's tags (with their
--     descendants), correspondents and document types whose name contains
--     the term, ignoring case and accents. A term shorter than 3 characters
--     matches only a whole name. At most 1000 IDs per item, in a fixed order.
-- owner NULL resolves names among all owners (an admin's scope); a document
-- only links IDs of its own owner, so it cannot match another owner's names.
-- An item that yields nothing is dropped; when none is left the result is
-- NULL, meaning no full-text condition. Nothing here is ever concatenated
-- into SQL: the arrays are values, and the only text turned into a tsquery
-- is a quoted lexeme. Callers match it against search_text || search_links,
-- the expression of documents_search_idx.
-- +goose StatementBegin
CREATE FUNCTION document_search_tsquery(
    owner bigint, terms text[], is_phrase boolean[], negated boolean[], or_group integer[]
) RETURNS tsquery
LANGUAGE plpgsql STABLE AS $$
DECLARE
    n integer := coalesce(cardinality(terms), 0);
    item record;
    q tsquery;
    part tsquery;
    groups tsquery[] := '{}';
    group_ids integer[] := '{}';
    pos integer;
    pattern text;
    result tsquery;
BEGIN
    IF coalesce(cardinality(is_phrase), 0) <> n OR coalesce(cardinality(negated), 0) <> n
       OR coalesce(cardinality(or_group), 0) <> n THEN
        RAISE EXCEPTION 'document_search_tsquery: arrays of different lengths' USING ERRCODE = '22023';
    END IF;
    FOR item IN
        SELECT u.term, coalesce(u.phrase, false) AS phrase, coalesce(u.neg, false) AS neg, u.grp, u.ord
        FROM unnest(terms, is_phrase, negated, or_group) WITH ORDINALITY AS u(term, phrase, neg, grp, ord)
        WHERE u.term IS NOT NULL AND btrim(u.term) <> ''
        ORDER BY u.ord
    LOOP
        IF item.phrase THEN
            q := phraseto_tsquery('pg_catalog.german', item.term)
              || phraseto_tsquery('pg_catalog.english', item.term)
              || phraseto_tsquery('public.paperless_simple', item.term);
        ELSE
            q := plainto_tsquery('pg_catalog.german', item.term)
              || plainto_tsquery('pg_catalog.english', item.term)
              || plainto_tsquery('public.paperless_simple', item.term);
            IF char_length(item.term) >= 3 THEN
                SELECT string_agg('''' || replace(replace(v.lexeme, '\', '\\'), '''', '''''') || ''':*', ' & ')::tsquery
                INTO part
                FROM unnest(to_tsvector('public.paperless_simple', item.term)) v;
                IF part IS NOT NULL THEN
                    q := q || part;
                END IF;
            END IF;
        END IF;

        -- Names: substring for 3 or more characters, else the whole name.
        -- LIKE wildcards in the term are escaped (the escape character is
        -- the default backslash).
        IF char_length(item.term) >= 3 THEN
            pattern := '%' || replace(replace(replace(public.unaccent('public.unaccent', item.term),
                       '\', '\\'), '%', '\%'), '_', '\_') || '%';
        ELSE
            pattern := replace(replace(replace(public.unaccent('public.unaccent', item.term),
                       '\', '\\'), '%', '\%'), '_', '\_');
        END IF;
        SELECT string_agg('''' || l.lexeme || '''', ' | ')::tsquery INTO part
        FROM (
            SELECT m.lexeme FROM (
                WITH RECURSIVE matched (id) AS (
                    SELECT t.id FROM tags t
                    WHERE (owner IS NULL OR t.owner_id = owner)
                      AND public.unaccent('public.unaccent', t.name) ILIKE pattern
                    UNION
                    SELECT c.id FROM tags c JOIN matched m ON c.parent_id = m.id
                )
                SELECT documents_search_link_lexeme('t', id) AS lexeme, 3 AS k, id FROM matched
                UNION ALL
                SELECT documents_search_link_lexeme('c', c.id), 1, c.id FROM correspondents c
                WHERE (owner IS NULL OR c.owner_id = owner)
                  AND public.unaccent('public.unaccent', c.name) ILIKE pattern
                UNION ALL
                SELECT documents_search_link_lexeme('d', y.id), 2, y.id FROM document_types y
                WHERE (owner IS NULL OR y.owner_id = owner)
                  AND public.unaccent('public.unaccent', y.name) ILIKE pattern
            ) m
            ORDER BY m.k, m.id
            LIMIT 1000
        ) l;
        IF part IS NOT NULL THEN
            q := q || part;
        END IF;

        IF numnode(q) = 0 THEN
            CONTINUE;
        END IF;
        IF item.neg THEN
            q := !! q;
        END IF;
        pos := array_position(group_ids, item.grp);
        IF item.grp IS NULL OR pos IS NULL THEN
            groups := groups || q;
            group_ids := group_ids || item.grp;
        ELSE
            groups[pos] := groups[pos] || q;
        END IF;
    END LOOP;
    FOREACH q IN ARRAY groups LOOP
        result := CASE WHEN result IS NULL THEN q ELSE result && q END;
    END LOOP;
    RETURN result;
END;
$$;
-- +goose StatementEnd

-- Backfill, then build the indexes: faster than updating them row by row.
-- The UPDATE sets only the search columns, so the triggers do not run.
UPDATE documents d
SET search_text = documents_search_text_vector(
        d.title, d.archive_serial_number, d.notes, d.original_file_name, d.extracted_text),
    search_links = documents_search_links_vector(d.id, d.correspondent_id, d.document_type_id);

CREATE INDEX documents_search_idx ON documents USING GIN ((search_text || search_links));
CREATE INDEX documents_owner_date_idx ON documents (owner_id, document_date DESC, id);

-- +goose Down
DROP INDEX documents_owner_date_idx;
DROP INDEX documents_search_idx;
DROP FUNCTION document_search_tsquery(bigint, text[], boolean[], boolean[], integer[]);
DROP TRIGGER document_tags_search_delete ON document_tags;
DROP TRIGGER document_tags_search_insert ON document_tags;
DROP FUNCTION document_tags_search_trigger();
DROP TRIGGER documents_search_links ON documents;
DROP FUNCTION documents_search_links_trigger();
DROP TRIGGER documents_search_text ON documents;
DROP FUNCTION documents_search_text_trigger();
DROP FUNCTION documents_search_links_vector(uuid, uuid, uuid);
DROP FUNCTION documents_search_link_lexeme(text, uuid);
DROP FUNCTION documents_search_text_vector(text, integer, text, text, text);
DROP FUNCTION documents_search_text_build(text, integer, text, text, text, integer, integer);
ALTER TABLE documents
    DROP COLUMN search_links,
    DROP COLUMN search_text;
DROP TEXT SEARCH CONFIGURATION public.paperless_simple;
-- unaccent goes only when the current user owns it, which is the case when
-- this migration created it, and nothing outside the extension uses it.
-- One an administrator created beforehand (for a user without the CREATE
-- privilege) stays: the user could not drop it, and other software may use
-- it.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_extension x
        WHERE x.extname = 'unaccent'
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
                      AND m2.deptype = 'e'))
    ) THEN
        DROP EXTENSION unaccent;
    END IF;
END;
$$;
-- +goose StatementEnd
