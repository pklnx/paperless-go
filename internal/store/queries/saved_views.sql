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

-- name: DeleteSavedView :one
DELETE FROM saved_views
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner))
RETURNING *;

-- LockSavedView reads a view for a patch and holds its row until the
-- commit, so two patches of one view merge one after the other.
-- name: LockSavedView :one
SELECT * FROM saved_views
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner))
FOR UPDATE;

-- NextSavedViewSortIndex is the sort index after an owner's last view: 0
-- for the first. It stays within ±(2^53 − 1), the API's range (a value
-- JavaScript holds exactly): a larger stored index (written before the API
-- checked it, or by SQL) gives the largest one, never an overflow.
-- name: NextSavedViewSortIndex :one
-- (LEAST ignores NULL, so the empty case is spelled out.)
SELECT (CASE WHEN max(sort_index) IS NULL THEN 0
             ELSE LEAST(max(sort_index), 9007199254740990) + 1 END)::bigint
FROM saved_views WHERE owner_id = $1;

-- LockSavedViewOrder serialises the creates of one owner that append a
-- view (SortLast), so each reads the maximum the one before it wrote and
-- two parallel ones never get the same sort index. It is a
-- transaction-level advisory lock with its own key prefix, released at
-- the commit; the transaction holding it writes only saved_views and the
-- audit log and waits for no other lock.
-- name: LockSavedViewOrder :exec
SELECT pg_advisory_xact_lock(hashtextextended('paperless-go:saved-views:' || sqlc.arg(owner_id)::bigint::text, 0));
