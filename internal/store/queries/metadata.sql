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

-- name: DeleteCorrespondent :one
DELETE FROM correspondents
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner))
RETURNING id, owner_id, name;

-- LockCorrespondent reads a row for a change in the same transaction (a patch).
-- name: LockCorrespondent :one
SELECT * FROM correspondents
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner))
FOR NO KEY UPDATE;

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

-- name: DeleteDocumentType :one
DELETE FROM document_types
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner))
RETURNING id, owner_id, name;

-- LockDocumentType reads a row for a change in the same transaction (a patch).
-- name: LockDocumentType :one
SELECT * FROM document_types
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner))
FOR NO KEY UPDATE;

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

-- name: DeleteTag :one
DELETE FROM tags
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner))
RETURNING id, owner_id, name;

-- LockTag reads a row for a change in the same transaction (a patch).
-- name: LockTag :one
SELECT * FROM tags
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner))
FOR NO KEY UPDATE;

-- TagRootNameConflict finds a child of the tag that would collide with a
-- root tag of the same name once the tag is deleted and the child becomes
-- a root.
-- name: TagRootNameConflict :one
SELECT child.id, child.name FROM tags child
WHERE child.parent_id = sqlc.arg(id)
  AND EXISTS (
      SELECT 1 FROM tags root
      WHERE root.owner_id = child.owner_id AND root.parent_id IS NULL
        AND root.id <> sqlc.arg(id) -- the tag being deleted is gone by then
        AND lower(root.name) = lower(child.name)
  )
ORDER BY lower(child.name), child.id
LIMIT 1;

-- The lists with their document counts: one aggregate per list.

-- name: ListCorrespondentsWithCounts :many
SELECT c.id, c.owner_id, c.name, c.match_text, c.match_mode, c.created_at, c.modified_at,
       count(d.id)::bigint AS document_count
FROM correspondents c
LEFT JOIN documents d ON d.correspondent_id = c.id
WHERE sqlc.narg(owner)::bigint IS NULL OR c.owner_id = sqlc.narg(owner)
GROUP BY c.id
ORDER BY c.owner_id, lower(c.name), c.id;

-- name: ListDocumentTypesWithCounts :many
SELECT t.id, t.owner_id, t.name, t.symbol, t.match_text, t.match_mode, t.created_at, t.modified_at,
       count(d.id)::bigint AS document_count
FROM document_types t
LEFT JOIN documents d ON d.document_type_id = t.id
WHERE sqlc.narg(owner)::bigint IS NULL OR t.owner_id = sqlc.narg(owner)
GROUP BY t.id
ORDER BY t.owner_id, lower(t.name), t.id;

-- ListTagsWithCounts counts per tag its own documents (document_count) and
-- the distinct documents of the tag and its descendants (subtree_count),
-- which is what a tag filter (SearchTagIDs) lists.
-- name: ListTagsWithCounts :many
WITH RECURSIVE scoped AS (
    SELECT tags.id FROM tags
    WHERE sqlc.narg(owner)::bigint IS NULL OR tags.owner_id = sqlc.narg(owner)
), subtree (root, id) AS (
    SELECT scoped.id, scoped.id FROM scoped
    UNION
    SELECT subtree.root, c.id FROM subtree JOIN tags c ON c.parent_id = subtree.id
), own AS (
    SELECT dt.tag_id, count(*) AS n FROM document_tags dt
    WHERE dt.tag_id IN (SELECT scoped.id FROM scoped)
    GROUP BY dt.tag_id
), deep AS (
    SELECT subtree.root, count(DISTINCT dt.document_id) AS n
    FROM subtree JOIN document_tags dt ON dt.tag_id = subtree.id
    GROUP BY subtree.root
)
SELECT t.id, t.owner_id, t.name, t.color_hex, t.symbol, t.parent_id, t.match_text, t.match_mode,
       t.created_at, t.modified_at,
       coalesce(own.n, 0)::bigint AS document_count, coalesce(deep.n, 0)::bigint AS subtree_count
FROM tags t
JOIN scoped ON scoped.id = t.id
LEFT JOIN own ON own.tag_id = t.id
LEFT JOIN deep ON deep.root = t.id
ORDER BY t.owner_id, lower(t.name), t.id;

-- name: DocumentCounts :one
SELECT count(*) FILTER (WHERE is_inbox)::bigint AS inbox,
       count(*) FILTER (WHERE is_favorite)::bigint AS favorites,
       count(*)::bigint AS total
FROM documents
WHERE sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner);
