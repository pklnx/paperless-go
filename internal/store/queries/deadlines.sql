-- Deadlines. Every read and write is limited to sqlc.narg(owner); NULL
-- means all owners (an admin's scope).

-- AddDeadline adds a deadline to a document in the scope; the deadline gets
-- the document's owner. No row means the document is not in the scope.
-- name: AddDeadline :one
INSERT INTO deadlines (
    owner_id, document_id, kind, date, label, origin, is_done,
    added_to_calendar_at, added_to_reminders_at
)
SELECT d.owner_id, d.id, sqlc.arg(kind), sqlc.arg(date), sqlc.arg(label), sqlc.arg(origin),
       sqlc.arg(is_done), sqlc.narg(added_to_calendar_at), sqlc.narg(added_to_reminders_at)
FROM documents d
WHERE d.id = sqlc.arg(document_id) AND (sqlc.narg(owner)::bigint IS NULL OR d.owner_id = sqlc.narg(owner))
RETURNING *;

-- name: ListDeadlines :many
SELECT * FROM deadlines
WHERE document_id = sqlc.arg(document_id)
  AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner))
ORDER BY date, created_at, id;

-- name: UpdateDeadline :one
UPDATE deadlines
SET kind = sqlc.arg(kind), date = sqlc.arg(date), label = sqlc.arg(label), origin = sqlc.arg(origin),
    is_done = sqlc.arg(is_done), added_to_calendar_at = sqlc.narg(added_to_calendar_at),
    added_to_reminders_at = sqlc.narg(added_to_reminders_at), modified_at = now()
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner))
RETURNING *;

-- name: DeleteDeadline :execrows
DELETE FROM deadlines
WHERE id = sqlc.arg(id) AND (sqlc.narg(owner)::bigint IS NULL OR owner_id = sqlc.narg(owner));
