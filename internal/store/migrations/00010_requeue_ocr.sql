-- +goose Up
-- OCR and image conversion (#5, part 3). The version before could not run
-- them: it failed PDFs with pages without text and every image for good,
-- and left PDFs with an empty user password unOCRed with a note about an
-- owner password. Those documents are queued again, so nobody has to run
-- reprocess --failed. The messages are the ones that version wrote
-- (internal/process, #5 part 2); documents queued or running already are
-- left alone.
INSERT INTO jobs (document_id)
SELECT l.document_id
FROM (SELECT DISTINCT ON (document_id) document_id, state, last_error FROM jobs
      WHERE kind = 'process' ORDER BY document_id, id DESC) l
WHERE l.state = 'failed'
  AND l.last_error IN (
      'Images cannot be processed by this version yet; run reprocess --failed after the update.',
      'This PDF has pages without text, and this version cannot run OCR yet; run reprocess --failed after the update.')
ON CONFLICT (document_id, kind) WHERE state IN ('queued', 'running') DO NOTHING;

INSERT INTO jobs (document_id)
SELECT id FROM documents
WHERE processing_note = 'The PDF is protected by an owner password, so pages without text were not OCRed.'
ON CONFLICT (document_id, kind) WHERE state IN ('queued', 'running') DO NOTHING;

-- +goose Down
-- Nothing to undo: the queued jobs are ordinary jobs.
