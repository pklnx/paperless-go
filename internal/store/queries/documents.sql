-- Documents. Every read and write is limited to sqlc.narg(owner); NULL
-- means all owners (an admin's scope).

-- name: CreateDocument :one
INSERT INTO documents (
    owner_id, title, document_date, notes, is_favorite, is_inbox,
    archive_serial_number, page_count, extracted_text, text_source, source,
    original_file_name, original_sha256, original_size, original_mime,
    archive_sha256, archive_size, correspondent_id, document_type_id
) VALUES (
    sqlc.arg(owner_id), sqlc.arg(title), sqlc.arg(document_date), sqlc.arg(notes),
    sqlc.arg(is_favorite), sqlc.arg(is_inbox), sqlc.narg(archive_serial_number),
    sqlc.arg(page_count), sqlc.arg(extracted_text), sqlc.arg(text_source), sqlc.arg(source),
    sqlc.arg(original_file_name), sqlc.arg(original_sha256), sqlc.arg(original_size),
    sqlc.arg(original_mime), sqlc.narg(archive_sha256), sqlc.narg(archive_size),
    sqlc.narg(correspondent_id), sqlc.narg(document_type_id)
)
RETURNING id;

-- name: DocumentByOriginal :one
SELECT id, title FROM documents WHERE owner_id = $1 AND original_sha256 = $2;

-- name: GetDocument :one
-- The columns are listed: the search vectors (search_text, search_links)
-- can be large and are never read.
SELECT d.id, d.owner_id, d.title, d.document_date, d.created_at, d.modified_at, d.notes, d.is_favorite,
       d.is_inbox, d.archive_serial_number, d.page_count, d.extracted_text, d.text_source, d.source,
       d.original_file_name, d.original_sha256, d.original_size, d.original_mime, d.archive_sha256,
       d.archive_size, d.correspondent_id, d.document_type_id, d.thumbnail_sha256, d.processing_note,
       COALESCE((SELECT array_agg(dt.tag_id ORDER BY dt.tag_id) FROM document_tags dt
                 WHERE dt.document_id = d.id), '{}')::uuid[] AS tag_ids
FROM documents d
WHERE d.id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR d.owner_id = sqlc.narg(owner));

-- name: ListDocuments :many
SELECT d.id, d.owner_id, d.title, d.document_date, d.created_at, d.modified_at, d.notes, d.is_favorite,
       d.is_inbox, d.archive_serial_number, d.page_count, d.extracted_text, d.text_source, d.source,
       d.original_file_name, d.original_sha256, d.original_size, d.original_mime, d.archive_sha256,
       d.archive_size, d.correspondent_id, d.document_type_id, d.thumbnail_sha256, d.processing_note,
       COALESCE((SELECT array_agg(dt.tag_id ORDER BY dt.tag_id) FROM document_tags dt
                 WHERE dt.document_id = d.id), '{}')::uuid[] AS tag_ids
FROM documents d
WHERE sqlc.narg(owner)::bigint IS NULL OR d.owner_id = sqlc.narg(owner)
ORDER BY d.created_at DESC, d.id;

-- DocumentOwner and DocumentOwners read the owner of documents in the
-- scope, without a lock, so a writer can take the owner's write lock
-- (store.lockOwners) before it locks or writes the rows.
-- name: DocumentOwner :one
SELECT owner_id FROM documents
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner));

-- name: DocumentOwners :many
SELECT DISTINCT owner_id FROM documents
WHERE id = ANY (sqlc.arg(ids)::uuid[]) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner))
ORDER BY owner_id;

-- LockDocument locks the document row for a change and returns its owner
-- and modified_at (PatchDocument's precondition). FOR NO KEY UPDATE: no
-- key column changes, so the foreign-key checks of rows referencing the
-- document (document_tags, jobs; FOR KEY SHARE) need not wait for the
-- change.
-- name: LockDocument :one
SELECT owner_id, modified_at FROM documents
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner))
FOR NO KEY UPDATE;

-- name: ClearDocumentTags :exec
DELETE FROM document_tags WHERE document_id = $1;

-- name: AddDocumentTags :exec
INSERT INTO document_tags (owner_id, document_id, tag_id)
SELECT sqlc.arg(owner_id)::bigint, sqlc.arg(document_id)::uuid, t
FROM unnest(sqlc.arg(tag_ids)::uuid[]) AS t
ON CONFLICT DO NOTHING;

-- name: DeleteDocument :one
DELETE FROM documents
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner))
RETURNING owner_id;

-- name: PatchDocument :execrows
-- Sets only the given columns: a NULL narg, or a false set_* flag, keeps
-- the stored value. The caller has locked the row (LockDocument).
-- Every write of a PATCH-able field or of document_tags advances
-- modified_at strictly (GREATEST: now() is the transaction's start, so two
-- writes in one transaction, or in the same microsecond, would otherwise
-- get the same value); equal modified_at means unchanged since read.
UPDATE documents
SET title = COALESCE(sqlc.narg(title)::text, title),
    document_date = COALESCE(sqlc.narg(document_date)::timestamptz, document_date),
    notes = COALESCE(sqlc.narg(notes)::text, notes),
    is_favorite = COALESCE(sqlc.narg(is_favorite)::boolean, is_favorite),
    is_inbox = COALESCE(sqlc.narg(is_inbox)::boolean, is_inbox),
    archive_serial_number = CASE WHEN sqlc.arg(set_asn)::boolean THEN sqlc.narg(archive_serial_number)::int
                                 ELSE archive_serial_number END,
    correspondent_id = CASE WHEN sqlc.arg(set_correspondent)::boolean THEN sqlc.narg(correspondent_id)::uuid
                            ELSE correspondent_id END,
    document_type_id = CASE WHEN sqlc.arg(set_document_type)::boolean THEN sqlc.narg(document_type_id)::uuid
                            ELSE document_type_id END,
    modified_at = GREATEST(now(), modified_at + interval '1 microsecond')
WHERE id = sqlc.arg(id);

-- name: DocumentByASN :one
SELECT id, title FROM documents WHERE owner_id = $1 AND archive_serial_number = $2;

-- LockDocuments locks the documents of ids in the scope, in id order, and
-- returns the locked ids with their owners. FOR NO KEY UPDATE as in
-- LockDocument.
-- name: LockDocuments :many
SELECT id, owner_id FROM documents
WHERE id = ANY (sqlc.arg(ids)::uuid[]) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner))
ORDER BY id
FOR NO KEY UPDATE;

-- name: CountOwnedTags :one
SELECT count(*) FROM tags WHERE owner_id = sqlc.arg(owner_id) AND id = ANY (sqlc.arg(ids)::uuid[]);

-- name: BulkRemoveDocumentTags :exec
DELETE FROM document_tags
WHERE document_id = ANY (sqlc.arg(ids)::uuid[]) AND tag_id = ANY (sqlc.arg(tag_ids)::uuid[]);

-- name: BulkAddDocumentTags :exec
INSERT INTO document_tags (owner_id, document_id, tag_id)
SELECT d.owner_id, d.id, t
FROM documents d CROSS JOIN unnest(sqlc.arg(tag_ids)::uuid[]) AS t
WHERE d.id = ANY (sqlc.arg(ids)::uuid[])
ON CONFLICT DO NOTHING;

-- name: BulkUpdateDocuments :exec
-- Sets the given columns of the locked documents as PatchDocument does,
-- and advances modified_at of every one of them.
UPDATE documents
SET is_favorite = COALESCE(sqlc.narg(is_favorite)::boolean, is_favorite),
    is_inbox = COALESCE(sqlc.narg(is_inbox)::boolean, is_inbox),
    correspondent_id = CASE WHEN sqlc.arg(set_correspondent)::boolean THEN sqlc.narg(correspondent_id)::uuid
                            ELSE correspondent_id END,
    document_type_id = CASE WHEN sqlc.arg(set_document_type)::boolean THEN sqlc.narg(document_type_id)::uuid
                            ELSE document_type_id END,
    modified_at = GREATEST(now(), modified_at + interval '1 microsecond')
WHERE id = ANY (sqlc.arg(ids)::uuid[]);
