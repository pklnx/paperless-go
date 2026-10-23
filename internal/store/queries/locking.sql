-- The per-owner write lock (store.lockOwners; docs/development/
-- architecture.md, "Write locking"). Every store transaction that writes
-- documents, document_tags, tags, correspondents or document_types takes
-- it first. The key is the one the tags_no_cycle trigger takes
-- (00002_documents.sql): prefix is store.ownerLockPrefix,
-- 'paperless-go:tags:', so inside a store writer the trigger re-acquires a
-- lock its transaction already holds.

-- name: LockOwner :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(prefix)::text || sqlc.arg(owner_id)::bigint::text, 0));

-- The per-hash blob lock (store.holdBlobs and the sweeper's queue pass;
-- docs/development/architecture.md, "Blob removal"). It uses the two-int4
-- form, whose key space is separate from the single-bigint keys of the
-- owner lock and the other advisory locks of the code base, so a blob key
-- can never equal an owner key. class is store.blobLockClass, key the
-- first four bytes of the SHA-256 (store.blobLockKey): two hashes with the
-- same key only serialise. Writers take it shared, after their owner
-- locks, in ascending key; the sweeper takes one key exclusively and no
-- owner lock.

-- name: LockBlobShared :exec
SELECT pg_advisory_xact_lock_shared(sqlc.arg(class)::int, sqlc.arg(key)::int);

-- name: LockBlob :exec
SELECT pg_advisory_xact_lock(sqlc.arg(class)::int, sqlc.arg(key)::int);

-- The sweep lock (store.ExclusiveSweep): a session-level advisory lock in
-- its own class (store.sweepLockClass), held on one connection for a whole
-- queue pass or walk, so at most one of them runs at a time across
-- processes. A crash releases it with the connection.

-- name: TrySweepLock :one
SELECT pg_try_advisory_lock(sqlc.arg(class)::int, 0);

-- name: SweepUnlock :one
SELECT pg_advisory_unlock(sqlc.arg(class)::int, 0);
