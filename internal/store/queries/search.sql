-- Document search (#6, part 2; docs/development/architecture.md#search).
-- store.SearchDocuments runs one query per sort order: a CASE in ORDER BY
-- would keep the planner from walking an index in that order. The filter
-- block and the snippet are copied between the markers; a test keeps the
-- copies equal. Every value is a bound parameter.
--
-- query is the tsquery of the free text, computed beforehand by
-- SearchTsquery: a constant here, so the GIN index serves it and nothing
-- builds it again per row. NULL means no full-text condition. The store
-- runs these queries with unnamed statements, so each is planned with its
-- values and the NULL tests of unused filters fall away.
--
-- The inner query finds the page's ids (lim rows, one more than the page
-- to tell whether another follows); the outer one reads their columns,
-- tags and snippets, so these are computed only for the page. No query
-- returns extracted_text.

-- name: SearchTsquery :one
-- The tsquery of the free-text items as text, or '' when none is left
-- (document_search_tsquery in 00011_search.sql). Its text form reads back
-- as the same tsquery.
SELECT coalesce(document_search_tsquery(sqlc.narg(owner)::bigint, sqlc.arg(terms)::text[], sqlc.arg(is_phrase)::boolean[],
                                        sqlc.arg(negated)::boolean[], sqlc.arg(or_group)::int[])::text, '')::text AS query;

-- name: SearchTagIDs :many
-- The tags each name of names stands for (exact name ignoring case, in
-- the scope) and each tag of ids (in the scope), with their descendants.
-- ord is the name's 1-based position, 0 for ids.
WITH RECURSIVE matched (ord, id) AS (
    SELECT w.ord, t.id
    FROM unnest(sqlc.arg(names)::text[]) WITH ORDINALITY AS w (name, ord)
    JOIN tags t ON lower(t.name) = lower(w.name)
    WHERE sqlc.narg(owner)::bigint IS NULL OR t.owner_id = sqlc.narg(owner)
    UNION
    SELECT 0, t.id FROM tags t
    WHERE t.id = ANY (sqlc.arg(ids)::uuid[]) AND (sqlc.narg(owner)::bigint IS NULL OR t.owner_id = sqlc.narg(owner))
    UNION
    SELECT m.ord, c.id FROM tags c JOIN matched m ON c.parent_id = m.id
)
SELECT ord::int AS ord, id FROM matched;

-- name: SearchLinkIDs :many
-- The correspondents (kind c) and document types (kind d) each name
-- stands for: exact name ignoring case, in the scope. ord is the name's
-- 1-based position.
SELECT w.ord::int AS ord, c.id
FROM unnest(sqlc.arg(names)::text[]) WITH ORDINALITY AS w (name, ord)
JOIN correspondents c ON (sqlc.arg(kinds)::text[])[w.ord] = 'c' AND lower(c.name) = lower(w.name)
WHERE sqlc.narg(owner)::bigint IS NULL OR c.owner_id = sqlc.narg(owner)
UNION ALL
SELECT w.ord::int, y.id
FROM unnest(sqlc.arg(names)::text[]) WITH ORDINALITY AS w (name, ord)
JOIN document_types y ON (sqlc.arg(kinds)::text[])[w.ord] = 'd' AND lower(y.name) = lower(w.name)
WHERE sqlc.narg(owner)::bigint IS NULL OR y.owner_id = sqlc.narg(owner);

-- name: SearchDocumentsRelevance :many
-- Best match first (ts_rank_cd, weights D 0.1, C 0.2, B 0.4, A 1.0,
-- divided by 1 + the log of the vector's length), then by id. Needs
-- query. The rank is computed once per match: OFFSET 0 keeps the planner
-- from repeating it in the cursor condition.
SELECT x.id, x.owner_id, x.title, x.document_date, x.created_at, x.modified_at, x.notes, x.is_favorite,
       x.is_inbox, x.archive_serial_number, x.page_count, ''::text AS extracted_text, x.text_source, x.source,
       x.original_file_name, x.original_sha256, x.original_size, x.original_mime, x.archive_sha256,
       x.archive_size, x.correspondent_id, x.document_type_id, x.thumbnail_sha256, x.processing_note,
       x.deleted_at, x.purge_at,
       COALESCE((SELECT array_agg(dt.tag_id ORDER BY dt.tag_id) FROM document_tags dt
                 WHERE dt.document_id = x.id), '{}')::uuid[] AS tag_ids,
       p.rank::real AS rank, si.h::text AS snippet
FROM (
    SELECT r.id, r.rank
    FROM (
        SELECT d.id, ts_rank_cd('{0.1,0.2,0.4,1.0}', d.search_text || d.search_links, sqlc.narg(query)::tsquery, 1) AS rank
        FROM documents d
        WHERE
      -- filters:begin (keep all copies equal; TestSearchFilterCopies checks)
      (sqlc.narg(owner)::bigint IS NULL OR d.owner_id = sqlc.narg(owner))
      -- The live documents, or with trashed the trash: trashed documents
      -- not yet due for purge (store.trashDue). Two branches, so the
      -- constant trashed folds one of them away and each list keeps its
      -- index (documents_trash_idx for the trash).
      AND ((NOT sqlc.arg(trashed)::boolean AND d.deleted_at IS NULL)
           OR (sqlc.arg(trashed)::boolean AND d.deleted_at IS NOT NULL
               AND coalesce(d.purge_at, d.deleted_at + sqlc.arg(retention_us)::bigint * interval '1 microsecond')
                   > sqlc.arg(now)::timestamptz))
      AND (sqlc.narg(query)::tsquery IS NULL OR (d.search_text || d.search_links) @@ sqlc.narg(query)::tsquery)
      -- Tags: a tag of each of the tag_group_count groups (tag_groups[i]
      -- is the group of tag_ids[i]), and none of not_tag_ids.
      AND (sqlc.arg(tag_group_count)::int = 0 OR sqlc.arg(tag_group_count)::int = (
           SELECT count(DISTINCT (sqlc.arg(tag_groups)::int[])[g.n]) FROM document_tags dt
           JOIN unnest(sqlc.arg(tag_ids)::uuid[]) WITH ORDINALITY AS g (id, n) ON g.id = dt.tag_id
           WHERE dt.document_id = d.id))
      AND (coalesce(cardinality(sqlc.arg(not_tag_ids)::uuid[]), 0) = 0 OR NOT EXISTS (
           SELECT 1 FROM document_tags dt
           WHERE dt.document_id = d.id AND dt.tag_id = ANY (sqlc.arg(not_tag_ids)::uuid[])))
      AND (sqlc.narg(correspondents)::uuid[] IS NULL OR d.correspondent_id = ANY (sqlc.narg(correspondents)::uuid[]))
      AND (coalesce(cardinality(sqlc.arg(not_correspondents)::uuid[]), 0) = 0 OR d.correspondent_id IS NULL
           OR NOT d.correspondent_id = ANY (sqlc.arg(not_correspondents)::uuid[]))
      AND (sqlc.narg(types)::uuid[] IS NULL OR d.document_type_id = ANY (sqlc.narg(types)::uuid[]))
      AND (coalesce(cardinality(sqlc.arg(not_types)::uuid[]), 0) = 0 OR d.document_type_id IS NULL
           OR NOT d.document_type_id = ANY (sqlc.arg(not_types)::uuid[]))
      AND (sqlc.narg(asns)::int[] IS NULL OR d.archive_serial_number = ANY (sqlc.narg(asns)::int[]))
      AND (coalesce(cardinality(sqlc.arg(not_asns)::int[]), 0) = 0 OR d.archive_serial_number IS NULL
           OR NOT d.archive_serial_number = ANY (sqlc.arg(not_asns)::int[]))
      -- Dates: within [from, to), and outside each excluded range
      -- [not_*_from[i], not_*_to[i]).
      AND (sqlc.narg(date_from)::timestamptz IS NULL OR d.document_date >= sqlc.narg(date_from)::timestamptz)
      AND (sqlc.narg(date_to)::timestamptz IS NULL OR d.document_date < sqlc.narg(date_to)::timestamptz)
      AND (coalesce(cardinality(sqlc.arg(not_date_from)::timestamptz[]), 0) = 0 OR NOT EXISTS (
           SELECT 1 FROM unnest(sqlc.arg(not_date_from)::timestamptz[]) WITH ORDINALITY AS r (lo, n)
           WHERE d.document_date >= r.lo AND d.document_date < (sqlc.arg(not_date_to)::timestamptz[])[r.n]))
      AND (sqlc.narg(added_from)::timestamptz IS NULL OR d.created_at >= sqlc.narg(added_from)::timestamptz)
      AND (sqlc.narg(added_to)::timestamptz IS NULL OR d.created_at < sqlc.narg(added_to)::timestamptz)
      AND (coalesce(cardinality(sqlc.arg(not_added_from)::timestamptz[]), 0) = 0 OR NOT EXISTS (
           SELECT 1 FROM unnest(sqlc.arg(not_added_from)::timestamptz[]) WITH ORDINALITY AS r (lo, n)
           WHERE d.created_at >= r.lo AND d.created_at < (sqlc.arg(not_added_to)::timestamptz[])[r.n]))
      AND (sqlc.narg(inbox)::boolean IS NULL OR d.is_inbox = sqlc.narg(inbox)::boolean)
      AND (sqlc.narg(favorite)::boolean IS NULL OR d.is_favorite = sqlc.narg(favorite)::boolean)
      -- filters:end
        OFFSET 0
    ) r
    WHERE sqlc.narg(cursor_rank)::real IS NULL
       OR r.rank < sqlc.narg(cursor_rank)::real
       OR r.rank = sqlc.narg(cursor_rank)::real AND r.id > sqlc.narg(cursor_id)::uuid
    ORDER BY r.rank DESC, r.id
    LIMIT sqlc.arg(lim)
) p
JOIN documents x ON x.id = p.id
-- snippet:begin (keep all copies equal; TestSearchFilterCopies checks)
-- A snippet in the first configuration that marks a match: german, then
-- english (other stems), then paperless_simple (accents). Only for the
-- page's rows, and only with a full-text condition.
CROSS JOIN LATERAL (
    SELECT CASE WHEN sqlc.narg(query)::tsquery IS NULL THEN ''
           ELSE ts_headline('pg_catalog.german', left(x.extracted_text, 100000), sqlc.narg(query)::tsquery,
                'MaxFragments=2, MaxWords=25, MinWords=10, StartSel=' || chr(57344) || ', StopSel=' || chr(57345))
           END AS h
    OFFSET 0) de
CROSS JOIN LATERAL (
    SELECT CASE WHEN sqlc.narg(query)::tsquery IS NULL OR strpos(de.h, chr(57344)) > 0 THEN de.h
           ELSE ts_headline('pg_catalog.english', left(x.extracted_text, 100000), sqlc.narg(query)::tsquery,
                'MaxFragments=2, MaxWords=25, MinWords=10, StartSel=' || chr(57344) || ', StopSel=' || chr(57345))
           END AS h
    OFFSET 0) en
CROSS JOIN LATERAL (
    SELECT CASE WHEN sqlc.narg(query)::tsquery IS NULL OR strpos(en.h, chr(57344)) > 0 THEN en.h
           ELSE ts_headline('public.paperless_simple', left(x.extracted_text, 100000), sqlc.narg(query)::tsquery,
                'MaxFragments=2, MaxWords=25, MinWords=10, StartSel=' || chr(57344) || ', StopSel=' || chr(57345))
           END AS h
    OFFSET 0) si
-- snippet:end
ORDER BY p.rank DESC, x.id;

-- name: SearchDocumentsAddedDesc :many
-- Newest added first, then by id.
SELECT x.id, x.owner_id, x.title, x.document_date, x.created_at, x.modified_at, x.notes, x.is_favorite,
       x.is_inbox, x.archive_serial_number, x.page_count, ''::text AS extracted_text, x.text_source, x.source,
       x.original_file_name, x.original_sha256, x.original_size, x.original_mime, x.archive_sha256,
       x.archive_size, x.correspondent_id, x.document_type_id, x.thumbnail_sha256, x.processing_note,
       x.deleted_at, x.purge_at,
       COALESCE((SELECT array_agg(dt.tag_id ORDER BY dt.tag_id) FROM document_tags dt
                 WHERE dt.document_id = x.id), '{}')::uuid[] AS tag_ids,
       0::real AS rank, si.h::text AS snippet
FROM (
    SELECT d.id
    FROM documents d
    WHERE
      -- filters:begin (keep all copies equal; TestSearchFilterCopies checks)
      (sqlc.narg(owner)::bigint IS NULL OR d.owner_id = sqlc.narg(owner))
      -- The live documents, or with trashed the trash: trashed documents
      -- not yet due for purge (store.trashDue). Two branches, so the
      -- constant trashed folds one of them away and each list keeps its
      -- index (documents_trash_idx for the trash).
      AND ((NOT sqlc.arg(trashed)::boolean AND d.deleted_at IS NULL)
           OR (sqlc.arg(trashed)::boolean AND d.deleted_at IS NOT NULL
               AND coalesce(d.purge_at, d.deleted_at + sqlc.arg(retention_us)::bigint * interval '1 microsecond')
                   > sqlc.arg(now)::timestamptz))
      AND (sqlc.narg(query)::tsquery IS NULL OR (d.search_text || d.search_links) @@ sqlc.narg(query)::tsquery)
      -- Tags: a tag of each of the tag_group_count groups (tag_groups[i]
      -- is the group of tag_ids[i]), and none of not_tag_ids.
      AND (sqlc.arg(tag_group_count)::int = 0 OR sqlc.arg(tag_group_count)::int = (
           SELECT count(DISTINCT (sqlc.arg(tag_groups)::int[])[g.n]) FROM document_tags dt
           JOIN unnest(sqlc.arg(tag_ids)::uuid[]) WITH ORDINALITY AS g (id, n) ON g.id = dt.tag_id
           WHERE dt.document_id = d.id))
      AND (coalesce(cardinality(sqlc.arg(not_tag_ids)::uuid[]), 0) = 0 OR NOT EXISTS (
           SELECT 1 FROM document_tags dt
           WHERE dt.document_id = d.id AND dt.tag_id = ANY (sqlc.arg(not_tag_ids)::uuid[])))
      AND (sqlc.narg(correspondents)::uuid[] IS NULL OR d.correspondent_id = ANY (sqlc.narg(correspondents)::uuid[]))
      AND (coalesce(cardinality(sqlc.arg(not_correspondents)::uuid[]), 0) = 0 OR d.correspondent_id IS NULL
           OR NOT d.correspondent_id = ANY (sqlc.arg(not_correspondents)::uuid[]))
      AND (sqlc.narg(types)::uuid[] IS NULL OR d.document_type_id = ANY (sqlc.narg(types)::uuid[]))
      AND (coalesce(cardinality(sqlc.arg(not_types)::uuid[]), 0) = 0 OR d.document_type_id IS NULL
           OR NOT d.document_type_id = ANY (sqlc.arg(not_types)::uuid[]))
      AND (sqlc.narg(asns)::int[] IS NULL OR d.archive_serial_number = ANY (sqlc.narg(asns)::int[]))
      AND (coalesce(cardinality(sqlc.arg(not_asns)::int[]), 0) = 0 OR d.archive_serial_number IS NULL
           OR NOT d.archive_serial_number = ANY (sqlc.arg(not_asns)::int[]))
      -- Dates: within [from, to), and outside each excluded range
      -- [not_*_from[i], not_*_to[i]).
      AND (sqlc.narg(date_from)::timestamptz IS NULL OR d.document_date >= sqlc.narg(date_from)::timestamptz)
      AND (sqlc.narg(date_to)::timestamptz IS NULL OR d.document_date < sqlc.narg(date_to)::timestamptz)
      AND (coalesce(cardinality(sqlc.arg(not_date_from)::timestamptz[]), 0) = 0 OR NOT EXISTS (
           SELECT 1 FROM unnest(sqlc.arg(not_date_from)::timestamptz[]) WITH ORDINALITY AS r (lo, n)
           WHERE d.document_date >= r.lo AND d.document_date < (sqlc.arg(not_date_to)::timestamptz[])[r.n]))
      AND (sqlc.narg(added_from)::timestamptz IS NULL OR d.created_at >= sqlc.narg(added_from)::timestamptz)
      AND (sqlc.narg(added_to)::timestamptz IS NULL OR d.created_at < sqlc.narg(added_to)::timestamptz)
      AND (coalesce(cardinality(sqlc.arg(not_added_from)::timestamptz[]), 0) = 0 OR NOT EXISTS (
           SELECT 1 FROM unnest(sqlc.arg(not_added_from)::timestamptz[]) WITH ORDINALITY AS r (lo, n)
           WHERE d.created_at >= r.lo AND d.created_at < (sqlc.arg(not_added_to)::timestamptz[])[r.n]))
      AND (sqlc.narg(inbox)::boolean IS NULL OR d.is_inbox = sqlc.narg(inbox)::boolean)
      AND (sqlc.narg(favorite)::boolean IS NULL OR d.is_favorite = sqlc.narg(favorite)::boolean)
      -- filters:end
      AND (sqlc.narg(cursor_at)::timestamptz IS NULL
           OR d.created_at <= sqlc.narg(cursor_at)::timestamptz
              AND (d.created_at < sqlc.narg(cursor_at)::timestamptz
                   OR d.id > sqlc.narg(cursor_id)::uuid))
    ORDER BY d.created_at DESC, d.id
    LIMIT sqlc.arg(lim)
) p
JOIN documents x ON x.id = p.id
-- snippet:begin (keep all copies equal; TestSearchFilterCopies checks)
-- A snippet in the first configuration that marks a match: german, then
-- english (other stems), then paperless_simple (accents). Only for the
-- page's rows, and only with a full-text condition.
CROSS JOIN LATERAL (
    SELECT CASE WHEN sqlc.narg(query)::tsquery IS NULL THEN ''
           ELSE ts_headline('pg_catalog.german', left(x.extracted_text, 100000), sqlc.narg(query)::tsquery,
                'MaxFragments=2, MaxWords=25, MinWords=10, StartSel=' || chr(57344) || ', StopSel=' || chr(57345))
           END AS h
    OFFSET 0) de
CROSS JOIN LATERAL (
    SELECT CASE WHEN sqlc.narg(query)::tsquery IS NULL OR strpos(de.h, chr(57344)) > 0 THEN de.h
           ELSE ts_headline('pg_catalog.english', left(x.extracted_text, 100000), sqlc.narg(query)::tsquery,
                'MaxFragments=2, MaxWords=25, MinWords=10, StartSel=' || chr(57344) || ', StopSel=' || chr(57345))
           END AS h
    OFFSET 0) en
CROSS JOIN LATERAL (
    SELECT CASE WHEN sqlc.narg(query)::tsquery IS NULL OR strpos(en.h, chr(57344)) > 0 THEN en.h
           ELSE ts_headline('public.paperless_simple', left(x.extracted_text, 100000), sqlc.narg(query)::tsquery,
                'MaxFragments=2, MaxWords=25, MinWords=10, StartSel=' || chr(57344) || ', StopSel=' || chr(57345))
           END AS h
    OFFSET 0) si
-- snippet:end
ORDER BY x.created_at DESC, x.id;

-- name: SearchDocumentsDeletedDesc :many
-- The trash only (store.SearchDocuments refuses it otherwise): the most
-- recently deleted first, then by id, on documents_trash_idx.
SELECT x.id, x.owner_id, x.title, x.document_date, x.created_at, x.modified_at, x.notes, x.is_favorite,
       x.is_inbox, x.archive_serial_number, x.page_count, ''::text AS extracted_text, x.text_source, x.source,
       x.original_file_name, x.original_sha256, x.original_size, x.original_mime, x.archive_sha256,
       x.archive_size, x.correspondent_id, x.document_type_id, x.thumbnail_sha256, x.processing_note,
       x.deleted_at, x.purge_at,
       COALESCE((SELECT array_agg(dt.tag_id ORDER BY dt.tag_id) FROM document_tags dt
                 WHERE dt.document_id = x.id), '{}')::uuid[] AS tag_ids,
       0::real AS rank, si.h::text AS snippet
FROM (
    SELECT d.id
    FROM documents d
    WHERE
      -- filters:begin (keep all copies equal; TestSearchFilterCopies checks)
      (sqlc.narg(owner)::bigint IS NULL OR d.owner_id = sqlc.narg(owner))
      -- The live documents, or with trashed the trash: trashed documents
      -- not yet due for purge (store.trashDue). Two branches, so the
      -- constant trashed folds one of them away and each list keeps its
      -- index (documents_trash_idx for the trash).
      AND ((NOT sqlc.arg(trashed)::boolean AND d.deleted_at IS NULL)
           OR (sqlc.arg(trashed)::boolean AND d.deleted_at IS NOT NULL
               AND coalesce(d.purge_at, d.deleted_at + sqlc.arg(retention_us)::bigint * interval '1 microsecond')
                   > sqlc.arg(now)::timestamptz))
      AND (sqlc.narg(query)::tsquery IS NULL OR (d.search_text || d.search_links) @@ sqlc.narg(query)::tsquery)
      -- Tags: a tag of each of the tag_group_count groups (tag_groups[i]
      -- is the group of tag_ids[i]), and none of not_tag_ids.
      AND (sqlc.arg(tag_group_count)::int = 0 OR sqlc.arg(tag_group_count)::int = (
           SELECT count(DISTINCT (sqlc.arg(tag_groups)::int[])[g.n]) FROM document_tags dt
           JOIN unnest(sqlc.arg(tag_ids)::uuid[]) WITH ORDINALITY AS g (id, n) ON g.id = dt.tag_id
           WHERE dt.document_id = d.id))
      AND (coalesce(cardinality(sqlc.arg(not_tag_ids)::uuid[]), 0) = 0 OR NOT EXISTS (
           SELECT 1 FROM document_tags dt
           WHERE dt.document_id = d.id AND dt.tag_id = ANY (sqlc.arg(not_tag_ids)::uuid[])))
      AND (sqlc.narg(correspondents)::uuid[] IS NULL OR d.correspondent_id = ANY (sqlc.narg(correspondents)::uuid[]))
      AND (coalesce(cardinality(sqlc.arg(not_correspondents)::uuid[]), 0) = 0 OR d.correspondent_id IS NULL
           OR NOT d.correspondent_id = ANY (sqlc.arg(not_correspondents)::uuid[]))
      AND (sqlc.narg(types)::uuid[] IS NULL OR d.document_type_id = ANY (sqlc.narg(types)::uuid[]))
      AND (coalesce(cardinality(sqlc.arg(not_types)::uuid[]), 0) = 0 OR d.document_type_id IS NULL
           OR NOT d.document_type_id = ANY (sqlc.arg(not_types)::uuid[]))
      AND (sqlc.narg(asns)::int[] IS NULL OR d.archive_serial_number = ANY (sqlc.narg(asns)::int[]))
      AND (coalesce(cardinality(sqlc.arg(not_asns)::int[]), 0) = 0 OR d.archive_serial_number IS NULL
           OR NOT d.archive_serial_number = ANY (sqlc.arg(not_asns)::int[]))
      -- Dates: within [from, to), and outside each excluded range
      -- [not_*_from[i], not_*_to[i]).
      AND (sqlc.narg(date_from)::timestamptz IS NULL OR d.document_date >= sqlc.narg(date_from)::timestamptz)
      AND (sqlc.narg(date_to)::timestamptz IS NULL OR d.document_date < sqlc.narg(date_to)::timestamptz)
      AND (coalesce(cardinality(sqlc.arg(not_date_from)::timestamptz[]), 0) = 0 OR NOT EXISTS (
           SELECT 1 FROM unnest(sqlc.arg(not_date_from)::timestamptz[]) WITH ORDINALITY AS r (lo, n)
           WHERE d.document_date >= r.lo AND d.document_date < (sqlc.arg(not_date_to)::timestamptz[])[r.n]))
      AND (sqlc.narg(added_from)::timestamptz IS NULL OR d.created_at >= sqlc.narg(added_from)::timestamptz)
      AND (sqlc.narg(added_to)::timestamptz IS NULL OR d.created_at < sqlc.narg(added_to)::timestamptz)
      AND (coalesce(cardinality(sqlc.arg(not_added_from)::timestamptz[]), 0) = 0 OR NOT EXISTS (
           SELECT 1 FROM unnest(sqlc.arg(not_added_from)::timestamptz[]) WITH ORDINALITY AS r (lo, n)
           WHERE d.created_at >= r.lo AND d.created_at < (sqlc.arg(not_added_to)::timestamptz[])[r.n]))
      AND (sqlc.narg(inbox)::boolean IS NULL OR d.is_inbox = sqlc.narg(inbox)::boolean)
      AND (sqlc.narg(favorite)::boolean IS NULL OR d.is_favorite = sqlc.narg(favorite)::boolean)
      -- filters:end
      AND (sqlc.narg(cursor_at)::timestamptz IS NULL
           OR d.deleted_at <= sqlc.narg(cursor_at)::timestamptz
              AND (d.deleted_at < sqlc.narg(cursor_at)::timestamptz
                   OR d.id > sqlc.narg(cursor_id)::uuid))
    ORDER BY d.deleted_at DESC, d.id
    LIMIT sqlc.arg(lim)
) p
JOIN documents x ON x.id = p.id
-- snippet:begin (keep all copies equal; TestSearchFilterCopies checks)
-- A snippet in the first configuration that marks a match: german, then
-- english (other stems), then paperless_simple (accents). Only for the
-- page's rows, and only with a full-text condition.
CROSS JOIN LATERAL (
    SELECT CASE WHEN sqlc.narg(query)::tsquery IS NULL THEN ''
           ELSE ts_headline('pg_catalog.german', left(x.extracted_text, 100000), sqlc.narg(query)::tsquery,
                'MaxFragments=2, MaxWords=25, MinWords=10, StartSel=' || chr(57344) || ', StopSel=' || chr(57345))
           END AS h
    OFFSET 0) de
CROSS JOIN LATERAL (
    SELECT CASE WHEN sqlc.narg(query)::tsquery IS NULL OR strpos(de.h, chr(57344)) > 0 THEN de.h
           ELSE ts_headline('pg_catalog.english', left(x.extracted_text, 100000), sqlc.narg(query)::tsquery,
                'MaxFragments=2, MaxWords=25, MinWords=10, StartSel=' || chr(57344) || ', StopSel=' || chr(57345))
           END AS h
    OFFSET 0) en
CROSS JOIN LATERAL (
    SELECT CASE WHEN sqlc.narg(query)::tsquery IS NULL OR strpos(en.h, chr(57344)) > 0 THEN en.h
           ELSE ts_headline('public.paperless_simple', left(x.extracted_text, 100000), sqlc.narg(query)::tsquery,
                'MaxFragments=2, MaxWords=25, MinWords=10, StartSel=' || chr(57344) || ', StopSel=' || chr(57345))
           END AS h
    OFFSET 0) si
-- snippet:end
ORDER BY x.deleted_at DESC, x.id;

-- name: SearchDocumentsAddedAsc :many
-- Oldest added first; the reverse of SearchDocumentsAddedDesc.
SELECT x.id, x.owner_id, x.title, x.document_date, x.created_at, x.modified_at, x.notes, x.is_favorite,
       x.is_inbox, x.archive_serial_number, x.page_count, ''::text AS extracted_text, x.text_source, x.source,
       x.original_file_name, x.original_sha256, x.original_size, x.original_mime, x.archive_sha256,
       x.archive_size, x.correspondent_id, x.document_type_id, x.thumbnail_sha256, x.processing_note,
       x.deleted_at, x.purge_at,
       COALESCE((SELECT array_agg(dt.tag_id ORDER BY dt.tag_id) FROM document_tags dt
                 WHERE dt.document_id = x.id), '{}')::uuid[] AS tag_ids,
       0::real AS rank, si.h::text AS snippet
FROM (
    SELECT d.id
    FROM documents d
    WHERE
      -- filters:begin (keep all copies equal; TestSearchFilterCopies checks)
      (sqlc.narg(owner)::bigint IS NULL OR d.owner_id = sqlc.narg(owner))
      -- The live documents, or with trashed the trash: trashed documents
      -- not yet due for purge (store.trashDue). Two branches, so the
      -- constant trashed folds one of them away and each list keeps its
      -- index (documents_trash_idx for the trash).
      AND ((NOT sqlc.arg(trashed)::boolean AND d.deleted_at IS NULL)
           OR (sqlc.arg(trashed)::boolean AND d.deleted_at IS NOT NULL
               AND coalesce(d.purge_at, d.deleted_at + sqlc.arg(retention_us)::bigint * interval '1 microsecond')
                   > sqlc.arg(now)::timestamptz))
      AND (sqlc.narg(query)::tsquery IS NULL OR (d.search_text || d.search_links) @@ sqlc.narg(query)::tsquery)
      -- Tags: a tag of each of the tag_group_count groups (tag_groups[i]
      -- is the group of tag_ids[i]), and none of not_tag_ids.
      AND (sqlc.arg(tag_group_count)::int = 0 OR sqlc.arg(tag_group_count)::int = (
           SELECT count(DISTINCT (sqlc.arg(tag_groups)::int[])[g.n]) FROM document_tags dt
           JOIN unnest(sqlc.arg(tag_ids)::uuid[]) WITH ORDINALITY AS g (id, n) ON g.id = dt.tag_id
           WHERE dt.document_id = d.id))
      AND (coalesce(cardinality(sqlc.arg(not_tag_ids)::uuid[]), 0) = 0 OR NOT EXISTS (
           SELECT 1 FROM document_tags dt
           WHERE dt.document_id = d.id AND dt.tag_id = ANY (sqlc.arg(not_tag_ids)::uuid[])))
      AND (sqlc.narg(correspondents)::uuid[] IS NULL OR d.correspondent_id = ANY (sqlc.narg(correspondents)::uuid[]))
      AND (coalesce(cardinality(sqlc.arg(not_correspondents)::uuid[]), 0) = 0 OR d.correspondent_id IS NULL
           OR NOT d.correspondent_id = ANY (sqlc.arg(not_correspondents)::uuid[]))
      AND (sqlc.narg(types)::uuid[] IS NULL OR d.document_type_id = ANY (sqlc.narg(types)::uuid[]))
      AND (coalesce(cardinality(sqlc.arg(not_types)::uuid[]), 0) = 0 OR d.document_type_id IS NULL
           OR NOT d.document_type_id = ANY (sqlc.arg(not_types)::uuid[]))
      AND (sqlc.narg(asns)::int[] IS NULL OR d.archive_serial_number = ANY (sqlc.narg(asns)::int[]))
      AND (coalesce(cardinality(sqlc.arg(not_asns)::int[]), 0) = 0 OR d.archive_serial_number IS NULL
           OR NOT d.archive_serial_number = ANY (sqlc.arg(not_asns)::int[]))
      -- Dates: within [from, to), and outside each excluded range
      -- [not_*_from[i], not_*_to[i]).
      AND (sqlc.narg(date_from)::timestamptz IS NULL OR d.document_date >= sqlc.narg(date_from)::timestamptz)
      AND (sqlc.narg(date_to)::timestamptz IS NULL OR d.document_date < sqlc.narg(date_to)::timestamptz)
      AND (coalesce(cardinality(sqlc.arg(not_date_from)::timestamptz[]), 0) = 0 OR NOT EXISTS (
           SELECT 1 FROM unnest(sqlc.arg(not_date_from)::timestamptz[]) WITH ORDINALITY AS r (lo, n)
           WHERE d.document_date >= r.lo AND d.document_date < (sqlc.arg(not_date_to)::timestamptz[])[r.n]))
      AND (sqlc.narg(added_from)::timestamptz IS NULL OR d.created_at >= sqlc.narg(added_from)::timestamptz)
      AND (sqlc.narg(added_to)::timestamptz IS NULL OR d.created_at < sqlc.narg(added_to)::timestamptz)
      AND (coalesce(cardinality(sqlc.arg(not_added_from)::timestamptz[]), 0) = 0 OR NOT EXISTS (
           SELECT 1 FROM unnest(sqlc.arg(not_added_from)::timestamptz[]) WITH ORDINALITY AS r (lo, n)
           WHERE d.created_at >= r.lo AND d.created_at < (sqlc.arg(not_added_to)::timestamptz[])[r.n]))
      AND (sqlc.narg(inbox)::boolean IS NULL OR d.is_inbox = sqlc.narg(inbox)::boolean)
      AND (sqlc.narg(favorite)::boolean IS NULL OR d.is_favorite = sqlc.narg(favorite)::boolean)
      -- filters:end
      AND (sqlc.narg(cursor_at)::timestamptz IS NULL
           OR d.created_at >= sqlc.narg(cursor_at)::timestamptz
              AND (d.created_at > sqlc.narg(cursor_at)::timestamptz
                   OR d.id < sqlc.narg(cursor_id)::uuid))
    ORDER BY d.created_at, d.id DESC
    LIMIT sqlc.arg(lim)
) p
JOIN documents x ON x.id = p.id
-- snippet:begin (keep all copies equal; TestSearchFilterCopies checks)
-- A snippet in the first configuration that marks a match: german, then
-- english (other stems), then paperless_simple (accents). Only for the
-- page's rows, and only with a full-text condition.
CROSS JOIN LATERAL (
    SELECT CASE WHEN sqlc.narg(query)::tsquery IS NULL THEN ''
           ELSE ts_headline('pg_catalog.german', left(x.extracted_text, 100000), sqlc.narg(query)::tsquery,
                'MaxFragments=2, MaxWords=25, MinWords=10, StartSel=' || chr(57344) || ', StopSel=' || chr(57345))
           END AS h
    OFFSET 0) de
CROSS JOIN LATERAL (
    SELECT CASE WHEN sqlc.narg(query)::tsquery IS NULL OR strpos(de.h, chr(57344)) > 0 THEN de.h
           ELSE ts_headline('pg_catalog.english', left(x.extracted_text, 100000), sqlc.narg(query)::tsquery,
                'MaxFragments=2, MaxWords=25, MinWords=10, StartSel=' || chr(57344) || ', StopSel=' || chr(57345))
           END AS h
    OFFSET 0) en
CROSS JOIN LATERAL (
    SELECT CASE WHEN sqlc.narg(query)::tsquery IS NULL OR strpos(en.h, chr(57344)) > 0 THEN en.h
           ELSE ts_headline('public.paperless_simple', left(x.extracted_text, 100000), sqlc.narg(query)::tsquery,
                'MaxFragments=2, MaxWords=25, MinWords=10, StartSel=' || chr(57344) || ', StopSel=' || chr(57345))
           END AS h
    OFFSET 0) si
-- snippet:end
ORDER BY x.created_at, x.id DESC;

-- name: SearchDocumentsDateDesc :many
-- Newest document date first, then by id.
SELECT x.id, x.owner_id, x.title, x.document_date, x.created_at, x.modified_at, x.notes, x.is_favorite,
       x.is_inbox, x.archive_serial_number, x.page_count, ''::text AS extracted_text, x.text_source, x.source,
       x.original_file_name, x.original_sha256, x.original_size, x.original_mime, x.archive_sha256,
       x.archive_size, x.correspondent_id, x.document_type_id, x.thumbnail_sha256, x.processing_note,
       x.deleted_at, x.purge_at,
       COALESCE((SELECT array_agg(dt.tag_id ORDER BY dt.tag_id) FROM document_tags dt
                 WHERE dt.document_id = x.id), '{}')::uuid[] AS tag_ids,
       0::real AS rank, si.h::text AS snippet
FROM (
    SELECT d.id
    FROM documents d
    WHERE
      -- filters:begin (keep all copies equal; TestSearchFilterCopies checks)
      (sqlc.narg(owner)::bigint IS NULL OR d.owner_id = sqlc.narg(owner))
      -- The live documents, or with trashed the trash: trashed documents
      -- not yet due for purge (store.trashDue). Two branches, so the
      -- constant trashed folds one of them away and each list keeps its
      -- index (documents_trash_idx for the trash).
      AND ((NOT sqlc.arg(trashed)::boolean AND d.deleted_at IS NULL)
           OR (sqlc.arg(trashed)::boolean AND d.deleted_at IS NOT NULL
               AND coalesce(d.purge_at, d.deleted_at + sqlc.arg(retention_us)::bigint * interval '1 microsecond')
                   > sqlc.arg(now)::timestamptz))
      AND (sqlc.narg(query)::tsquery IS NULL OR (d.search_text || d.search_links) @@ sqlc.narg(query)::tsquery)
      -- Tags: a tag of each of the tag_group_count groups (tag_groups[i]
      -- is the group of tag_ids[i]), and none of not_tag_ids.
      AND (sqlc.arg(tag_group_count)::int = 0 OR sqlc.arg(tag_group_count)::int = (
           SELECT count(DISTINCT (sqlc.arg(tag_groups)::int[])[g.n]) FROM document_tags dt
           JOIN unnest(sqlc.arg(tag_ids)::uuid[]) WITH ORDINALITY AS g (id, n) ON g.id = dt.tag_id
           WHERE dt.document_id = d.id))
      AND (coalesce(cardinality(sqlc.arg(not_tag_ids)::uuid[]), 0) = 0 OR NOT EXISTS (
           SELECT 1 FROM document_tags dt
           WHERE dt.document_id = d.id AND dt.tag_id = ANY (sqlc.arg(not_tag_ids)::uuid[])))
      AND (sqlc.narg(correspondents)::uuid[] IS NULL OR d.correspondent_id = ANY (sqlc.narg(correspondents)::uuid[]))
      AND (coalesce(cardinality(sqlc.arg(not_correspondents)::uuid[]), 0) = 0 OR d.correspondent_id IS NULL
           OR NOT d.correspondent_id = ANY (sqlc.arg(not_correspondents)::uuid[]))
      AND (sqlc.narg(types)::uuid[] IS NULL OR d.document_type_id = ANY (sqlc.narg(types)::uuid[]))
      AND (coalesce(cardinality(sqlc.arg(not_types)::uuid[]), 0) = 0 OR d.document_type_id IS NULL
           OR NOT d.document_type_id = ANY (sqlc.arg(not_types)::uuid[]))
      AND (sqlc.narg(asns)::int[] IS NULL OR d.archive_serial_number = ANY (sqlc.narg(asns)::int[]))
      AND (coalesce(cardinality(sqlc.arg(not_asns)::int[]), 0) = 0 OR d.archive_serial_number IS NULL
           OR NOT d.archive_serial_number = ANY (sqlc.arg(not_asns)::int[]))
      -- Dates: within [from, to), and outside each excluded range
      -- [not_*_from[i], not_*_to[i]).
      AND (sqlc.narg(date_from)::timestamptz IS NULL OR d.document_date >= sqlc.narg(date_from)::timestamptz)
      AND (sqlc.narg(date_to)::timestamptz IS NULL OR d.document_date < sqlc.narg(date_to)::timestamptz)
      AND (coalesce(cardinality(sqlc.arg(not_date_from)::timestamptz[]), 0) = 0 OR NOT EXISTS (
           SELECT 1 FROM unnest(sqlc.arg(not_date_from)::timestamptz[]) WITH ORDINALITY AS r (lo, n)
           WHERE d.document_date >= r.lo AND d.document_date < (sqlc.arg(not_date_to)::timestamptz[])[r.n]))
      AND (sqlc.narg(added_from)::timestamptz IS NULL OR d.created_at >= sqlc.narg(added_from)::timestamptz)
      AND (sqlc.narg(added_to)::timestamptz IS NULL OR d.created_at < sqlc.narg(added_to)::timestamptz)
      AND (coalesce(cardinality(sqlc.arg(not_added_from)::timestamptz[]), 0) = 0 OR NOT EXISTS (
           SELECT 1 FROM unnest(sqlc.arg(not_added_from)::timestamptz[]) WITH ORDINALITY AS r (lo, n)
           WHERE d.created_at >= r.lo AND d.created_at < (sqlc.arg(not_added_to)::timestamptz[])[r.n]))
      AND (sqlc.narg(inbox)::boolean IS NULL OR d.is_inbox = sqlc.narg(inbox)::boolean)
      AND (sqlc.narg(favorite)::boolean IS NULL OR d.is_favorite = sqlc.narg(favorite)::boolean)
      -- filters:end
      AND (sqlc.narg(cursor_at)::timestamptz IS NULL
           OR d.document_date <= sqlc.narg(cursor_at)::timestamptz
              AND (d.document_date < sqlc.narg(cursor_at)::timestamptz
                   OR d.id > sqlc.narg(cursor_id)::uuid))
    ORDER BY d.document_date DESC, d.id
    LIMIT sqlc.arg(lim)
) p
JOIN documents x ON x.id = p.id
-- snippet:begin (keep all copies equal; TestSearchFilterCopies checks)
-- A snippet in the first configuration that marks a match: german, then
-- english (other stems), then paperless_simple (accents). Only for the
-- page's rows, and only with a full-text condition.
CROSS JOIN LATERAL (
    SELECT CASE WHEN sqlc.narg(query)::tsquery IS NULL THEN ''
           ELSE ts_headline('pg_catalog.german', left(x.extracted_text, 100000), sqlc.narg(query)::tsquery,
                'MaxFragments=2, MaxWords=25, MinWords=10, StartSel=' || chr(57344) || ', StopSel=' || chr(57345))
           END AS h
    OFFSET 0) de
CROSS JOIN LATERAL (
    SELECT CASE WHEN sqlc.narg(query)::tsquery IS NULL OR strpos(de.h, chr(57344)) > 0 THEN de.h
           ELSE ts_headline('pg_catalog.english', left(x.extracted_text, 100000), sqlc.narg(query)::tsquery,
                'MaxFragments=2, MaxWords=25, MinWords=10, StartSel=' || chr(57344) || ', StopSel=' || chr(57345))
           END AS h
    OFFSET 0) en
CROSS JOIN LATERAL (
    SELECT CASE WHEN sqlc.narg(query)::tsquery IS NULL OR strpos(en.h, chr(57344)) > 0 THEN en.h
           ELSE ts_headline('public.paperless_simple', left(x.extracted_text, 100000), sqlc.narg(query)::tsquery,
                'MaxFragments=2, MaxWords=25, MinWords=10, StartSel=' || chr(57344) || ', StopSel=' || chr(57345))
           END AS h
    OFFSET 0) si
-- snippet:end
ORDER BY x.document_date DESC, x.id;

-- name: SearchDocumentsDateAsc :many
-- Oldest document date first; the reverse of SearchDocumentsDateDesc.
SELECT x.id, x.owner_id, x.title, x.document_date, x.created_at, x.modified_at, x.notes, x.is_favorite,
       x.is_inbox, x.archive_serial_number, x.page_count, ''::text AS extracted_text, x.text_source, x.source,
       x.original_file_name, x.original_sha256, x.original_size, x.original_mime, x.archive_sha256,
       x.archive_size, x.correspondent_id, x.document_type_id, x.thumbnail_sha256, x.processing_note,
       x.deleted_at, x.purge_at,
       COALESCE((SELECT array_agg(dt.tag_id ORDER BY dt.tag_id) FROM document_tags dt
                 WHERE dt.document_id = x.id), '{}')::uuid[] AS tag_ids,
       0::real AS rank, si.h::text AS snippet
FROM (
    SELECT d.id
    FROM documents d
    WHERE
      -- filters:begin (keep all copies equal; TestSearchFilterCopies checks)
      (sqlc.narg(owner)::bigint IS NULL OR d.owner_id = sqlc.narg(owner))
      -- The live documents, or with trashed the trash: trashed documents
      -- not yet due for purge (store.trashDue). Two branches, so the
      -- constant trashed folds one of them away and each list keeps its
      -- index (documents_trash_idx for the trash).
      AND ((NOT sqlc.arg(trashed)::boolean AND d.deleted_at IS NULL)
           OR (sqlc.arg(trashed)::boolean AND d.deleted_at IS NOT NULL
               AND coalesce(d.purge_at, d.deleted_at + sqlc.arg(retention_us)::bigint * interval '1 microsecond')
                   > sqlc.arg(now)::timestamptz))
      AND (sqlc.narg(query)::tsquery IS NULL OR (d.search_text || d.search_links) @@ sqlc.narg(query)::tsquery)
      -- Tags: a tag of each of the tag_group_count groups (tag_groups[i]
      -- is the group of tag_ids[i]), and none of not_tag_ids.
      AND (sqlc.arg(tag_group_count)::int = 0 OR sqlc.arg(tag_group_count)::int = (
           SELECT count(DISTINCT (sqlc.arg(tag_groups)::int[])[g.n]) FROM document_tags dt
           JOIN unnest(sqlc.arg(tag_ids)::uuid[]) WITH ORDINALITY AS g (id, n) ON g.id = dt.tag_id
           WHERE dt.document_id = d.id))
      AND (coalesce(cardinality(sqlc.arg(not_tag_ids)::uuid[]), 0) = 0 OR NOT EXISTS (
           SELECT 1 FROM document_tags dt
           WHERE dt.document_id = d.id AND dt.tag_id = ANY (sqlc.arg(not_tag_ids)::uuid[])))
      AND (sqlc.narg(correspondents)::uuid[] IS NULL OR d.correspondent_id = ANY (sqlc.narg(correspondents)::uuid[]))
      AND (coalesce(cardinality(sqlc.arg(not_correspondents)::uuid[]), 0) = 0 OR d.correspondent_id IS NULL
           OR NOT d.correspondent_id = ANY (sqlc.arg(not_correspondents)::uuid[]))
      AND (sqlc.narg(types)::uuid[] IS NULL OR d.document_type_id = ANY (sqlc.narg(types)::uuid[]))
      AND (coalesce(cardinality(sqlc.arg(not_types)::uuid[]), 0) = 0 OR d.document_type_id IS NULL
           OR NOT d.document_type_id = ANY (sqlc.arg(not_types)::uuid[]))
      AND (sqlc.narg(asns)::int[] IS NULL OR d.archive_serial_number = ANY (sqlc.narg(asns)::int[]))
      AND (coalesce(cardinality(sqlc.arg(not_asns)::int[]), 0) = 0 OR d.archive_serial_number IS NULL
           OR NOT d.archive_serial_number = ANY (sqlc.arg(not_asns)::int[]))
      -- Dates: within [from, to), and outside each excluded range
      -- [not_*_from[i], not_*_to[i]).
      AND (sqlc.narg(date_from)::timestamptz IS NULL OR d.document_date >= sqlc.narg(date_from)::timestamptz)
      AND (sqlc.narg(date_to)::timestamptz IS NULL OR d.document_date < sqlc.narg(date_to)::timestamptz)
      AND (coalesce(cardinality(sqlc.arg(not_date_from)::timestamptz[]), 0) = 0 OR NOT EXISTS (
           SELECT 1 FROM unnest(sqlc.arg(not_date_from)::timestamptz[]) WITH ORDINALITY AS r (lo, n)
           WHERE d.document_date >= r.lo AND d.document_date < (sqlc.arg(not_date_to)::timestamptz[])[r.n]))
      AND (sqlc.narg(added_from)::timestamptz IS NULL OR d.created_at >= sqlc.narg(added_from)::timestamptz)
      AND (sqlc.narg(added_to)::timestamptz IS NULL OR d.created_at < sqlc.narg(added_to)::timestamptz)
      AND (coalesce(cardinality(sqlc.arg(not_added_from)::timestamptz[]), 0) = 0 OR NOT EXISTS (
           SELECT 1 FROM unnest(sqlc.arg(not_added_from)::timestamptz[]) WITH ORDINALITY AS r (lo, n)
           WHERE d.created_at >= r.lo AND d.created_at < (sqlc.arg(not_added_to)::timestamptz[])[r.n]))
      AND (sqlc.narg(inbox)::boolean IS NULL OR d.is_inbox = sqlc.narg(inbox)::boolean)
      AND (sqlc.narg(favorite)::boolean IS NULL OR d.is_favorite = sqlc.narg(favorite)::boolean)
      -- filters:end
      AND (sqlc.narg(cursor_at)::timestamptz IS NULL
           OR d.document_date >= sqlc.narg(cursor_at)::timestamptz
              AND (d.document_date > sqlc.narg(cursor_at)::timestamptz
                   OR d.id < sqlc.narg(cursor_id)::uuid))
    ORDER BY d.document_date, d.id DESC
    LIMIT sqlc.arg(lim)
) p
JOIN documents x ON x.id = p.id
-- snippet:begin (keep all copies equal; TestSearchFilterCopies checks)
-- A snippet in the first configuration that marks a match: german, then
-- english (other stems), then paperless_simple (accents). Only for the
-- page's rows, and only with a full-text condition.
CROSS JOIN LATERAL (
    SELECT CASE WHEN sqlc.narg(query)::tsquery IS NULL THEN ''
           ELSE ts_headline('pg_catalog.german', left(x.extracted_text, 100000), sqlc.narg(query)::tsquery,
                'MaxFragments=2, MaxWords=25, MinWords=10, StartSel=' || chr(57344) || ', StopSel=' || chr(57345))
           END AS h
    OFFSET 0) de
CROSS JOIN LATERAL (
    SELECT CASE WHEN sqlc.narg(query)::tsquery IS NULL OR strpos(de.h, chr(57344)) > 0 THEN de.h
           ELSE ts_headline('pg_catalog.english', left(x.extracted_text, 100000), sqlc.narg(query)::tsquery,
                'MaxFragments=2, MaxWords=25, MinWords=10, StartSel=' || chr(57344) || ', StopSel=' || chr(57345))
           END AS h
    OFFSET 0) en
CROSS JOIN LATERAL (
    SELECT CASE WHEN sqlc.narg(query)::tsquery IS NULL OR strpos(en.h, chr(57344)) > 0 THEN en.h
           ELSE ts_headline('public.paperless_simple', left(x.extracted_text, 100000), sqlc.narg(query)::tsquery,
                'MaxFragments=2, MaxWords=25, MinWords=10, StartSel=' || chr(57344) || ', StopSel=' || chr(57345))
           END AS h
    OFFSET 0) si
-- snippet:end
ORDER BY x.document_date, x.id DESC;

-- name: MatchDocumentIDs :many
-- The ids of the documents a bulk change by search covers (store.
-- BulkUpdateDocuments): every match of the filters, in id order, at most
-- lim, each with the number of all matches and their owners; with only_ids
-- only those among them.
SELECT d.id, d.owner_id, count(*) OVER ()::bigint AS total
FROM documents d
WHERE
      -- filters:begin (keep all copies equal; TestSearchFilterCopies checks)
      (sqlc.narg(owner)::bigint IS NULL OR d.owner_id = sqlc.narg(owner))
      -- The live documents, or with trashed the trash: trashed documents
      -- not yet due for purge (store.trashDue). Two branches, so the
      -- constant trashed folds one of them away and each list keeps its
      -- index (documents_trash_idx for the trash).
      AND ((NOT sqlc.arg(trashed)::boolean AND d.deleted_at IS NULL)
           OR (sqlc.arg(trashed)::boolean AND d.deleted_at IS NOT NULL
               AND coalesce(d.purge_at, d.deleted_at + sqlc.arg(retention_us)::bigint * interval '1 microsecond')
                   > sqlc.arg(now)::timestamptz))
      AND (sqlc.narg(query)::tsquery IS NULL OR (d.search_text || d.search_links) @@ sqlc.narg(query)::tsquery)
      -- Tags: a tag of each of the tag_group_count groups (tag_groups[i]
      -- is the group of tag_ids[i]), and none of not_tag_ids.
      AND (sqlc.arg(tag_group_count)::int = 0 OR sqlc.arg(tag_group_count)::int = (
           SELECT count(DISTINCT (sqlc.arg(tag_groups)::int[])[g.n]) FROM document_tags dt
           JOIN unnest(sqlc.arg(tag_ids)::uuid[]) WITH ORDINALITY AS g (id, n) ON g.id = dt.tag_id
           WHERE dt.document_id = d.id))
      AND (coalesce(cardinality(sqlc.arg(not_tag_ids)::uuid[]), 0) = 0 OR NOT EXISTS (
           SELECT 1 FROM document_tags dt
           WHERE dt.document_id = d.id AND dt.tag_id = ANY (sqlc.arg(not_tag_ids)::uuid[])))
      AND (sqlc.narg(correspondents)::uuid[] IS NULL OR d.correspondent_id = ANY (sqlc.narg(correspondents)::uuid[]))
      AND (coalesce(cardinality(sqlc.arg(not_correspondents)::uuid[]), 0) = 0 OR d.correspondent_id IS NULL
           OR NOT d.correspondent_id = ANY (sqlc.arg(not_correspondents)::uuid[]))
      AND (sqlc.narg(types)::uuid[] IS NULL OR d.document_type_id = ANY (sqlc.narg(types)::uuid[]))
      AND (coalesce(cardinality(sqlc.arg(not_types)::uuid[]), 0) = 0 OR d.document_type_id IS NULL
           OR NOT d.document_type_id = ANY (sqlc.arg(not_types)::uuid[]))
      AND (sqlc.narg(asns)::int[] IS NULL OR d.archive_serial_number = ANY (sqlc.narg(asns)::int[]))
      AND (coalesce(cardinality(sqlc.arg(not_asns)::int[]), 0) = 0 OR d.archive_serial_number IS NULL
           OR NOT d.archive_serial_number = ANY (sqlc.arg(not_asns)::int[]))
      -- Dates: within [from, to), and outside each excluded range
      -- [not_*_from[i], not_*_to[i]).
      AND (sqlc.narg(date_from)::timestamptz IS NULL OR d.document_date >= sqlc.narg(date_from)::timestamptz)
      AND (sqlc.narg(date_to)::timestamptz IS NULL OR d.document_date < sqlc.narg(date_to)::timestamptz)
      AND (coalesce(cardinality(sqlc.arg(not_date_from)::timestamptz[]), 0) = 0 OR NOT EXISTS (
           SELECT 1 FROM unnest(sqlc.arg(not_date_from)::timestamptz[]) WITH ORDINALITY AS r (lo, n)
           WHERE d.document_date >= r.lo AND d.document_date < (sqlc.arg(not_date_to)::timestamptz[])[r.n]))
      AND (sqlc.narg(added_from)::timestamptz IS NULL OR d.created_at >= sqlc.narg(added_from)::timestamptz)
      AND (sqlc.narg(added_to)::timestamptz IS NULL OR d.created_at < sqlc.narg(added_to)::timestamptz)
      AND (coalesce(cardinality(sqlc.arg(not_added_from)::timestamptz[]), 0) = 0 OR NOT EXISTS (
           SELECT 1 FROM unnest(sqlc.arg(not_added_from)::timestamptz[]) WITH ORDINALITY AS r (lo, n)
           WHERE d.created_at >= r.lo AND d.created_at < (sqlc.arg(not_added_to)::timestamptz[])[r.n]))
      AND (sqlc.narg(inbox)::boolean IS NULL OR d.is_inbox = sqlc.narg(inbox)::boolean)
      AND (sqlc.narg(favorite)::boolean IS NULL OR d.is_favorite = sqlc.narg(favorite)::boolean)
      -- filters:end
      -- Outside the filter block on purpose (TestSearchFilterCopies): the
      -- recheck after the lock passes the locked ids, so it returns at
      -- most those and the limit cannot cut any off; the match and the
      -- dry run pass NULL.
      AND (sqlc.narg(only_ids)::uuid[] IS NULL OR d.id = ANY (sqlc.narg(only_ids)::uuid[]))
ORDER BY d.id
LIMIT sqlc.arg(lim);
