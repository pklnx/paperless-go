-- The queue of files that may have lost their last reference (00020_trash.sql)
-- and the sweeper's file removal (#12, part 2; store/blob_removals.go). A
-- file is in use while a document names it as original, archive or
-- thumbnail (the three hash indexes).

-- name: QueueBlobRemovals :exec
-- Queues each hash once; a hash already queued keeps its row and due_at.
INSERT INTO blob_removals (sha256, due_at)
SELECT DISTINCT h, sqlc.arg(due_at)::timestamptz FROM unnest(sqlc.arg(hashes)::text[]) AS h
WHERE h IS NOT NULL
ON CONFLICT (sha256) DO NOTHING;

-- name: DequeueBlobRemovals :many
-- A writer of these hashes, under their shared blob locks, takes them out
-- of the queue; a row with removed_at set means the sweeper is removing the
-- file (ErrBlobRemoved).
DELETE FROM blob_removals WHERE sha256 = ANY(sqlc.arg(hashes)::text[])
RETURNING sha256, removed_at;

-- name: BlobRemovalsDue :many
-- The queue pass's candidates: due unmarked rows, and rows an earlier run
-- marked before stale_before and did not finish (it crashed).
SELECT sha256 FROM blob_removals
WHERE (removed_at IS NULL AND due_at <= sqlc.arg(now)::timestamptz)
   OR removed_at <= sqlc.arg(stale_before)::timestamptz
ORDER BY due_at, sha256
LIMIT sqlc.arg(lim);

-- name: MarkBlobRemoval :many
-- Step 1 of the queue pass, under the hash's exclusive blob lock: marks a
-- due unmarked row, or takes over a row marked before stale_before (a
-- crashed run), whose file no document names. A fresh mark of another
-- pass is never taken over. Zero or one row.
UPDATE blob_removals SET removed_at = sqlc.arg(now)::timestamptz
WHERE sha256 = sqlc.arg(sha256)::text
  AND ((removed_at IS NULL AND due_at <= sqlc.arg(now)::timestamptz)
       OR removed_at <= sqlc.arg(stale_before)::timestamptz)
  AND NOT EXISTS (
      SELECT 1 FROM documents d
      WHERE d.original_sha256 = sqlc.arg(sha256)::text OR d.archive_sha256 = sqlc.arg(sha256)::text
         OR d.thumbnail_sha256 = sqlc.arg(sha256)::text)
RETURNING sha256;

-- name: DropReferencedBlobRemoval :execrows
-- Step 1, when the file is in use: the row goes (a later purge queues the
-- hash again).
DELETE FROM blob_removals
WHERE sha256 = sqlc.arg(sha256)::text
  AND EXISTS (
      SELECT 1 FROM documents d
      WHERE d.original_sha256 = sqlc.arg(sha256)::text OR d.archive_sha256 = sqlc.arg(sha256)::text
         OR d.thumbnail_sha256 = sqlc.arg(sha256)::text);

-- name: FinishBlobRemoval :execrows
-- Step 3, after the file was removed: the row this pass marked goes.
DELETE FROM blob_removals WHERE sha256 = sqlc.arg(sha256)::text AND removed_at = sqlc.arg(marked_at)::timestamptz;

-- name: RequeueBlobRemoval :execrows
-- After a failed removal: the row this pass marked is unmarked and due
-- again at due_at, so writers of the hash are not refused meanwhile.
UPDATE blob_removals SET removed_at = NULL, due_at = sqlc.arg(due_at)::timestamptz
WHERE sha256 = sqlc.arg(sha256)::text AND removed_at = sqlc.arg(marked_at)::timestamptz;

-- name: UnreferencedBlobs :many
-- The hashes of a batch that no document names (the walk; a dry run of the
-- queue pass).
SELECT h::text AS sha256 FROM unnest(sqlc.arg(hashes)::text[]) AS h
WHERE NOT EXISTS (
    SELECT 1 FROM documents d
    WHERE d.original_sha256 = h OR d.archive_sha256 = h OR d.thumbnail_sha256 = h)
ORDER BY 1;

-- name: QueueUnreferencedBlobs :execrows
-- The walk queues the hashes no document names at the time of the insert;
-- a hash already queued keeps its row.
INSERT INTO blob_removals (sha256, due_at)
SELECT DISTINCT h, sqlc.arg(due_at)::timestamptz FROM unnest(sqlc.arg(hashes)::text[]) AS h
WHERE NOT EXISTS (
    SELECT 1 FROM documents d
    WHERE d.original_sha256 = h OR d.archive_sha256 = h OR d.thumbnail_sha256 = h)
ON CONFLICT (sha256) DO NOTHING;

-- name: HasDocuments :one
-- Any document at all, also in the trash: the walk's sanity stop.
SELECT EXISTS (SELECT 1 FROM documents);

-- name: WalkCandidates :many
-- The walk's candidates of a batch: hashes no document names and that are
-- not queued yet (a queued hash goes anyway, and counting it would make a
-- large purge look like a wrong database to the 10 % stop).
SELECT h::text AS sha256 FROM unnest(sqlc.arg(hashes)::text[]) AS h
WHERE NOT EXISTS (
    SELECT 1 FROM documents d
    WHERE d.original_sha256 = h OR d.archive_sha256 = h OR d.thumbnail_sha256 = h)
  AND NOT EXISTS (SELECT 1 FROM blob_removals r WHERE r.sha256 = h)
ORDER BY 1;

-- name: BlobMarkedAt :one
-- Whether hash's row still carries the mark set at marked_at: no other
-- pass took it over.
SELECT EXISTS (SELECT 1 FROM blob_removals WHERE sha256 = sqlc.arg(sha256)::text AND removed_at = sqlc.arg(marked_at)::timestamptz);
