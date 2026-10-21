-- Correspondents, document types and tags. Every read and write is limited
-- to sqlc.narg(owner); NULL means all owners (an admin's scope).

-- name: CreateCorrespondent :one
INSERT INTO correspondents (owner_id, name, match_text, match_mode)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetCorrespondent :one
SELECT * FROM correspondents
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner));

-- name: ListCorrespondents :many
SELECT * FROM correspondents
WHERE sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner)
ORDER BY owner_id, lower(name), id;

-- name: UpdateCorrespondent :one
UPDATE correspondents
SET name = sqlc.arg(name), match_text = sqlc.arg(match_text), match_mode = sqlc.arg(match_mode),
    modified_at = now()
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner))
RETURNING *;

-- name: DeleteCorrespondent :execrows
DELETE FROM correspondents
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner));

-- name: CreateDocumentType :one
INSERT INTO document_types (owner_id, name, symbol, match_text, match_mode)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetDocumentType :one
SELECT * FROM document_types
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner));

-- name: ListDocumentTypes :many
SELECT * FROM document_types
WHERE sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner)
ORDER BY owner_id, lower(name), id;

-- name: UpdateDocumentType :one
UPDATE document_types
SET name = sqlc.arg(name), symbol = sqlc.arg(symbol), match_text = sqlc.arg(match_text),
    match_mode = sqlc.arg(match_mode), modified_at = now()
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner))
RETURNING *;

-- name: DeleteDocumentType :execrows
DELETE FROM document_types
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner));

-- name: CreateTag :one
INSERT INTO tags (owner_id, name, color_hex, symbol, parent_id, match_text, match_mode)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetTag :one
SELECT * FROM tags
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner));

-- name: ListTags :many
SELECT * FROM tags
WHERE sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner)
ORDER BY owner_id, lower(name), id;

-- name: UpdateTag :one
UPDATE tags
SET name = sqlc.arg(name), color_hex = sqlc.arg(color_hex), symbol = sqlc.arg(symbol),
    parent_id = sqlc.narg(parent_id), match_text = sqlc.arg(match_text),
    match_mode = sqlc.arg(match_mode), modified_at = now()
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner))
RETURNING *;

-- name: DeleteTag :execrows
DELETE FROM tags
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner));

-- TagRootNameConflict finds a child of the tag that would collide with a
-- root tag of the same name once the tag is deleted and the child becomes
-- a root.
-- name: TagRootNameConflict :one
SELECT child.id, child.name FROM tags child
WHERE child.parent_id = sqlc.arg(id)
  AND EXISTS (
      SELECT 1 FROM tags root
      WHERE root.owner_id = child.owner_id AND root.parent_id IS NULL
        AND lower(root.name) = lower(child.name)
  )
ORDER BY lower(child.name), child.id
LIMIT 1;
