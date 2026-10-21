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
SELECT d.*,
       COALESCE((SELECT array_agg(dt.tag_id ORDER BY dt.tag_id) FROM document_tags dt
                 WHERE dt.document_id = d.id), '{}')::uuid[] AS tag_ids
FROM documents d
WHERE d.id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR d.owner_id = sqlc.narg(owner));

-- name: ListDocuments :many
SELECT d.*,
       COALESCE((SELECT array_agg(dt.tag_id ORDER BY dt.tag_id) FROM document_tags dt
                 WHERE dt.document_id = d.id), '{}')::uuid[] AS tag_ids
FROM documents d
WHERE sqlc.narg(owner)::bigint IS NULL OR d.owner_id = sqlc.narg(owner)
ORDER BY d.created_at DESC, d.id;

-- name: UpdateDocument :execrows
UPDATE documents
SET title = sqlc.arg(title),
    document_date = COALESCE(sqlc.narg(document_date)::timestamptz, document_date), notes = sqlc.arg(notes),
    is_favorite = sqlc.arg(is_favorite), is_inbox = sqlc.arg(is_inbox),
    archive_serial_number = sqlc.narg(archive_serial_number),
    correspondent_id = sqlc.narg(correspondent_id), document_type_id = sqlc.narg(document_type_id),
    modified_at = now()
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner));

-- LockDocument locks the document row for a change of its tags and returns
-- its owner.
-- name: LockDocument :one
SELECT owner_id FROM documents
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner))
FOR UPDATE;

-- name: ClearDocumentTags :exec
DELETE FROM document_tags WHERE document_id = $1;

-- name: AddDocumentTags :exec
INSERT INTO document_tags (owner_id, document_id, tag_id)
SELECT sqlc.arg(owner_id)::bigint, sqlc.arg(document_id)::uuid, t
FROM unnest(sqlc.arg(tag_ids)::uuid[]) AS t
ON CONFLICT DO NOTHING;

-- name: TouchDocument :exec
UPDATE documents SET modified_at = now() WHERE id = $1;

-- name: DeleteDocument :execrows
DELETE FROM documents
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner));
