-- The per-owner write lock (store.lockOwners; docs/development/
-- architecture.md, "Write locking"). Every store transaction that writes
-- documents, document_tags, tags, correspondents or document_types takes
-- it first. The key is the one the tags_no_cycle trigger takes
-- (00002_documents.sql): prefix is store.ownerLockPrefix,
-- 'paperless-go:tags:', so inside a store writer the trigger re-acquires a
-- lock its transaction already holds.

-- name: LockOwner :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(prefix)::text || sqlc.arg(owner_id)::bigint::text, 0));
