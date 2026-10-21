-- The processing queue. Times come from the caller's clock, so tests move
-- a clock instead of waiting; only the defaults of a new job (created_at,
-- run_after) use the database clock.

-- name: EnqueueJobs :execrows
-- Queues a process job for each document unless one is queued or running.
INSERT INTO jobs (document_id) SELECT unnest(@document_ids::uuid[])
ON CONFLICT (document_id, kind) WHERE state IN ('queued', 'running') DO NOTHING;

-- name: EnqueueAllDocuments :execrows
INSERT INTO jobs (document_id) SELECT id FROM documents
ON CONFLICT (document_id, kind) WHERE state IN ('queued', 'running') DO NOTHING;

-- name: EnqueueFailedDocuments :execrows
-- Queues the documents whose latest job failed.
INSERT INTO jobs (document_id)
SELECT l.document_id
FROM (SELECT DISTINCT ON (document_id) document_id, state FROM jobs
      WHERE kind = 'process' ORDER BY document_id, id DESC) l
WHERE l.state = 'failed'
ON CONFLICT (document_id, kind) WHERE state IN ('queued', 'running') DO NOTHING;

-- name: ExistingDocumentIDs :many
SELECT id FROM documents WHERE id = ANY(@ids::uuid[]);

-- name: NotifyJobs :exec
-- Wakes listening workers when the transaction commits.
SELECT pg_notify('paperless_go_jobs', '');

-- name: ClaimJob :one
-- Takes the oldest due queued job; rows other workers hold are skipped. A
-- new job is due at the database time it was created (the column default),
-- so a job counts as due by either clock: a worker whose clock lags the
-- database's still sees new jobs at once.
UPDATE jobs
SET state = 'running', attempts = attempts + 1,
    lease_token = @lease_token, locked_until = @locked_until
WHERE state = 'queued' AND id = (
    SELECT j.id FROM jobs j
    WHERE j.state = 'queued' AND j.run_after <= GREATEST(@now::timestamptz, now())
    ORDER BY j.run_after, j.id
    LIMIT 1
    FOR UPDATE SKIP LOCKED)
RETURNING *;

-- name: ExtendJobLease :execrows
UPDATE jobs SET locked_until = @locked_until
WHERE id = @id AND lease_token = @lease_token AND state = 'running';

-- name: RetryJob :execrows
-- Ends a running attempt; the job runs again after run_after.
UPDATE jobs
SET state = 'queued', lease_token = NULL, locked_until = NULL,
    run_after = @run_after, last_error = @last_error
WHERE id = @id AND lease_token = @lease_token AND state = 'running';

-- name: FailJob :execrows
UPDATE jobs
SET state = 'failed', lease_token = NULL, locked_until = NULL,
    finished_at = @finished_at, last_error = @last_error
WHERE id = @id AND lease_token = @lease_token AND state = 'running';

-- name: ReleaseJob :execrows
-- Gives a job back at shutdown: the attempt does not count.
UPDATE jobs
SET state = 'queued', lease_token = NULL, locked_until = NULL,
    attempts = GREATEST(attempts - 1, 0), run_after = @run_after
WHERE id = @id AND lease_token = @lease_token AND state = 'running';

-- name: ExpiredJobs :many
-- Running jobs whose lease expired, locked for the reaper.
SELECT id, lease_token, attempts, max_attempts FROM jobs
WHERE state = 'running' AND locked_until < @now
ORDER BY id
FOR UPDATE SKIP LOCKED;

-- name: LockJobLease :one
-- Locks a running job if the lease is still the caller's.
SELECT id FROM jobs
WHERE id = @id AND lease_token = @lease_token AND state = 'running'
FOR UPDATE;

-- name: SetDocumentProcessed :exec
-- Stores a processing result without touching modified_at. The text is
-- replaced only when it is empty or came from processing itself
-- (own_sources): text from an import or an edit stays.
UPDATE documents
SET page_count = @page_count,
    archive_sha256 = @archive_sha256, archive_size = @archive_size,
    processing_note = @processing_note,
    extracted_text = CASE WHEN extracted_text = '' OR text_source = ANY(@own_sources::text[])
                          THEN @extracted_text ELSE extracted_text END,
    text_source = CASE WHEN extracted_text = '' OR text_source = ANY(@own_sources::text[])
                       THEN @text_source ELSE text_source END
WHERE id = @id;

-- name: FinishJob :exec
UPDATE jobs
SET state = 'done', lease_token = NULL, locked_until = NULL,
    finished_at = @finished_at, last_error = ''
WHERE id = @id;

-- name: DeleteOlderFinishedJobs :exec
-- After a job is done, the document's earlier finished jobs are history.
DELETE FROM jobs
WHERE document_id = @document_id AND id < @id AND state IN ('done', 'failed');

-- name: DeleteDoneJobsBefore :execrows
DELETE FROM jobs WHERE state = 'done' AND finished_at < @before;

-- name: GetJob :one
SELECT * FROM jobs WHERE id = @id;

-- name: DocumentJobs :many
-- A document's jobs, newest first.
SELECT * FROM jobs WHERE document_id = @document_id ORDER BY id DESC;

-- name: JobCounts :one
SELECT count(*) FILTER (WHERE state = 'queued' AND run_after <= @now) AS due,
       count(*) FILTER (WHERE state = 'queued' AND run_after > @now)  AS waiting,
       count(*) FILTER (WHERE state = 'running')                      AS running,
       count(*) FILTER (WHERE state = 'failed')                       AS failed
FROM jobs;
