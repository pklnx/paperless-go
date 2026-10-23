-- The queue of files that may have lost their last reference (00020_trash.sql).
-- Part 1 of #12 only queues; the sweeper's queue pass (part 2) removes.

-- name: QueueBlobRemovals :exec
-- Queues each hash once; a hash already queued keeps its row and due_at.
INSERT INTO blob_removals (sha256, due_at)
SELECT DISTINCT h, sqlc.arg(due_at)::timestamptz FROM unnest(sqlc.arg(hashes)::text[]) AS h
WHERE h IS NOT NULL
ON CONFLICT (sha256) DO NOTHING;
