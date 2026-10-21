-- Saved views. Every read and write is limited to sqlc.narg(owner); NULL
-- means all owners (an admin's scope).

-- name: CreateSavedView :one
INSERT INTO saved_views (owner_id, name, symbol, sort_index, filter)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetSavedView :one
SELECT * FROM saved_views
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner));

-- name: ListSavedViews :many
SELECT * FROM saved_views
WHERE sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner)
ORDER BY owner_id, sort_index, created_at, id;

-- name: UpdateSavedView :one
UPDATE saved_views
SET name = sqlc.arg(name), symbol = sqlc.arg(symbol), sort_index = sqlc.arg(sort_index),
    filter = sqlc.arg(filter), modified_at = now()
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner))
RETURNING *;

-- name: DeleteSavedView :execrows
DELETE FROM saved_views
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner));
