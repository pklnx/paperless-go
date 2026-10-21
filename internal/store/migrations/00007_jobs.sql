-- +goose Up
-- The processing queue (#5, part 2): one job per document to extract its
-- text and build its archive PDF. Workers claim jobs with FOR UPDATE SKIP
-- LOCKED and hold a lease (lease_token, locked_until) while they run; a
-- lease that expires puts the job back or fails it. Times are set by the
-- worker's clock, except the defaults of a new job.
CREATE TABLE jobs (
    id           BIGSERIAL   PRIMARY KEY,
    document_id  UUID        NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    kind         TEXT        NOT NULL DEFAULT 'process' CHECK (kind IN ('process')),
    state        TEXT        NOT NULL DEFAULT 'queued'
                             CHECK (state IN ('queued', 'running', 'done', 'failed')),
    -- Started attempts; an attempt whose lease expired counts.
    attempts     INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts INTEGER     NOT NULL DEFAULT 3 CHECK (max_attempts > 0),
    run_after    TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_token  UUID,
    locked_until TIMESTAMPTZ,
    -- A fixed, user-visible message; tool output goes only to the log.
    last_error   TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at  TIMESTAMPTZ,
    -- A running job has a lease, and only a running job has one.
    CONSTRAINT jobs_running_lease CHECK (
        (state = 'running' AND lease_token IS NOT NULL AND locked_until IS NOT NULL)
        OR (state <> 'running' AND lease_token IS NULL AND locked_until IS NULL)),
    CONSTRAINT jobs_finished CHECK ((state IN ('done', 'failed')) = (finished_at IS NOT NULL))
);
-- At most one queued or running job per document and kind: enqueueing is
-- idempotent (INSERT ... ON CONFLICT DO NOTHING).
CREATE UNIQUE INDEX jobs_active_key ON jobs (document_id, kind) WHERE state IN ('queued', 'running');
-- The claim: the oldest due queued job.
CREATE INDEX jobs_due_idx ON jobs (run_after, id) WHERE state = 'queued';
-- The reaper: running jobs whose lease expired.
CREATE INDEX jobs_lease_idx ON jobs (locked_until) WHERE state = 'running';
-- A document's jobs, newest first (its processing status).
CREATE INDEX jobs_document_idx ON jobs (document_id, id DESC);

-- The thumbnail (#5, part 4) and a note on how processing went, for
-- example that a PDF with an owner password could not be OCRed.
ALTER TABLE documents
    ADD COLUMN thumbnail_sha256 TEXT CHECK (thumbnail_sha256 ~ '^[0-9a-f]{64}$'),
    ADD COLUMN processing_note  TEXT NOT NULL DEFAULT '';

-- Every document without an archive PDF gets processed.
INSERT INTO jobs (document_id) SELECT id FROM documents WHERE archive_sha256 IS NULL;

-- +goose Down
ALTER TABLE documents
    DROP COLUMN processing_note,
    DROP COLUMN thumbnail_sha256;
DROP TABLE jobs;
