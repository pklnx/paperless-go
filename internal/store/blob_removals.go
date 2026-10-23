package store

import (
	"cmp"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/pklnx/paperless-go/internal/store/db"
)

// Blob removal (#12, part 2; docs/development/architecture.md, "Blob
// removal"). The sweeper's queue pass is the only remover of files; a
// purge or the walk only queues hashes in blob_removals. The guard
// between a writer that names a hash and the sweeper that removes its
// file is a per-hash, transaction-level advisory lock:
//
//   - A writer of a hash (CreateDocument, CompleteJob) calls holdBlobs
//     right after its owner locks: it takes the shared lock of each hash
//     in ascending key, deletes the hashes' queue rows (a row the sweeper
//     marked gives ErrBlobRemoved) and checks that each file is on disk
//     (a missing one gives ErrBlobRemoved). Then it writes the row.
//   - The sweeper's step 1 (MarkBlobRemoval) takes the exclusive lock of
//     one hash and no owner lock, then marks the row (removed_at) only if
//     no document names the hash. Under READ COMMITTED that UPDATE takes
//     its snapshot after the lock was granted, so it sees every writer
//     that held the shared lock. Step 2 removes the file outside any
//     transaction, step 3 (FinishBlobRemoval) deletes the row.
//
// Lock order: owner locks, then hash locks, then row locks in a writer; a
// single hash lock in the sweeper. No cycle can form, and the blob keys
// live in the two-int4 key space, apart from the owner keys.

// blobLockClass is the first int4 of every blob lock's key: "pgob".
const blobLockClass int32 = 0x70676f62

// BlobGrace is how long a queued hash waits before the queue pass may
// remove its file. It is not needed for correctness (the hash lock is):
// it keeps a blob that an upload or the worker has just stored, and that
// no row names yet, from being queued by the walk and removed before its
// writer's insert takes the queue row away, which would only fail that
// writer once.
const BlobGrace = time.Hour

// blobLockKey is the second int4 of hash's lock key: its first four
// bytes. The hash must be valid (isSHA256).
func blobLockKey(hash string) int32 {
	b, _ := hex.DecodeString(hash[:8])
	return int32(binary.BigEndian.Uint32(b)) //nolint:gosec // the bits, as the int4 PostgreSQL stores
}

var (
	// errBlobsBeforeOwner means holdBlobs ran before the owner locks,
	// which would break the lock order.
	errBlobsBeforeOwner = errors.New("store: blob lock before the owner lock")
	// errBlobsHeldTwice means holdBlobs ran twice in one transaction.
	errBlobsHeldTwice = errors.New("store: blob locks taken twice in one transaction")
	// errUnheldBlobWrite refuses a statement that names a hash in
	// documents without holdBlobs before it: a bug in the store.
	errUnheldBlobWrite = errors.New("store: hash written without the blob lock")
)

// holdBlobs is a writer's guard for the hashes it is about to name (see
// above): after the owner locks, the shared blob lock of each hash in
// ascending key, the hashes' queue rows deleted, and each file checked on
// disk. A marked row or a missing file is ErrBlobRemoved; the caller's
// transaction then rolls back and writes nothing. Empty hashes are
// skipped. It is called once per transaction, also with no hash, and the
// guard refuses a hash writer (hashWriterQueries) before it.
func (s *Store) holdBlobs(ctx context.Context, t *writeTx, hashes ...string) error {
	if !t.g.tx.locked {
		return errBlobsBeforeOwner
	}
	if t.g.tx.blobsHeld {
		return errBlobsHeldTwice
	}
	var hs []string
	for _, h := range hashes {
		if h == "" {
			continue
		}
		if !isSHA256(h) {
			return invalid("sha256")
		}
		hs = append(hs, h)
	}
	slices.SortFunc(hs, func(a, b string) int {
		return cmp.Or(cmp.Compare(blobLockKey(a), blobLockKey(b)), cmp.Compare(a, b))
	})
	hs = slices.Compact(hs)
	keys := make([]int32, 0, len(hs))
	for _, h := range hs {
		if k := blobLockKey(h); len(keys) == 0 || keys[len(keys)-1] != k {
			keys = append(keys, k)
		}
	}
	if r, ok := ctx.Value(lockRecorderKey{}).(func(lockEvent)); ok {
		r(lockEvent{Held: true, Blobs: slices.Clone(hs)})
	}
	for _, k := range keys {
		if err := t.q.LockBlobShared(ctx, db.LockBlobSharedParams{Class: blobLockClass, Key: k}); err != nil {
			return err
		}
	}
	if len(hs) > 0 {
		rows, err := t.q.DequeueBlobRemovals(ctx, hs)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.RemovedAt != nil {
				return fmt.Errorf("%w: %s", ErrBlobRemoved, r.Sha256)
			}
		}
		for _, h := range hs {
			// One stat per hash: the named exception to "no I/O under
			// the owner lock" (architecture.md, "Write locking").
			if !s.blobs.Exists(h) {
				return fmt.Errorf("%w: %s is missing", ErrBlobRemoved, h)
			}
		}
	}
	t.g.tx.blobsHeld = true
	if f, ok := ctx.Value(afterBlobHoldKey{}).(func()); ok {
		f()
	}
	return nil
}

// afterBlobHoldKey carries a test hook (export_test.go) that runs in a
// writer's transaction right after holdBlobs.
type afterBlobHoldKey struct{}

// BlobRemovalsDue returns up to limit hashes the queue pass may take at
// now: due rows and rows an earlier run marked without finishing.
func (s *Store) BlobRemovalsDue(ctx context.Context, now time.Time, limit int) ([]string, error) {
	return s.q.BlobRemovalsDue(ctx, db.BlobRemovalsDueParams{Now: now, Lim: int32(min(limit, 1<<30))}) //nolint:gosec // bounded
}

// BlobMark is what MarkBlobRemoval decided for a hash.
type BlobMark int

// The outcomes of MarkBlobRemoval.
const (
	// BlobMarked: the row is marked, no document names the hash; the
	// file may go.
	BlobMarked BlobMark = iota + 1
	// BlobInUse: a document names the hash; its queue row was deleted
	// and the file stays.
	BlobInUse
	// BlobKept: the row is gone (a writer took it) or not due yet;
	// nothing changed.
	BlobKept
)

// MarkBlobRemoval is step 1 of the queue pass for hash at now, in one
// transaction without I/O: the hash's exclusive blob lock, then the mark
// (BlobMarked), or the row deleted when a document names the hash
// (BlobInUse). Only after BlobMarked may the caller remove the file and
// then call FinishBlobRemoval.
func (s *Store) MarkBlobRemoval(ctx context.Context, hash string, now time.Time) (BlobMark, error) {
	if !isSHA256(hash) {
		return 0, invalid("sha256")
	}
	mark := BlobKept
	err := s.inTx(ctx, func(q *db.Queries) error {
		if err := q.LockBlob(ctx, db.LockBlobParams{Class: blobLockClass, Key: blobLockKey(hash)}); err != nil {
			return err
		}
		if f, ok := ctx.Value(afterBlobLockKey{}).(func()); ok {
			f()
		}
		marked, err := q.MarkBlobRemoval(ctx, db.MarkBlobRemovalParams{Now: now, Sha256: hash})
		if err != nil {
			return err
		}
		if len(marked) > 0 {
			mark = BlobMarked
			return nil
		}
		n, err := q.DropReferencedBlobRemoval(ctx, hash)
		if n > 0 {
			mark = BlobInUse
		}
		return err
	})
	return mark, err
}

// afterBlobLockKey carries a test hook (export_test.go) that runs in the
// sweeper's step 1 right after it took the exclusive blob lock.
type afterBlobLockKey struct{}

// FinishBlobRemoval is step 3 of the queue pass: it deletes hash's marked
// row once the file is gone. A row that is not there any more is fine.
func (s *Store) FinishBlobRemoval(ctx context.Context, hash string) error {
	_, err := s.q.FinishBlobRemoval(ctx, hash)
	return err
}

// UnreferencedBlobs returns those of hashes (valid SHA-256s) that no
// document names, sorted.
func (s *Store) UnreferencedBlobs(ctx context.Context, hashes []string) ([]string, error) {
	if len(hashes) == 0 {
		return nil, nil
	}
	return s.q.UnreferencedBlobs(ctx, hashes)
}

// WalkCandidates returns those of hashes (valid SHA-256s) that no
// document names and that are not queued yet, sorted: the walk's
// candidates.
func (s *Store) WalkCandidates(ctx context.Context, hashes []string) ([]string, error) {
	if len(hashes) == 0 {
		return nil, nil
	}
	return s.q.WalkCandidates(ctx, hashes)
}

// QueueUnreferencedBlobs queues those of hashes that no document names at
// the time of the insert, due at due; a queued hash keeps its row. It
// returns the number of rows added.
func (s *Store) QueueUnreferencedBlobs(ctx context.Context, hashes []string, due time.Time) (int64, error) {
	if len(hashes) == 0 {
		return 0, nil
	}
	return s.q.QueueUnreferencedBlobs(ctx, db.QueueUnreferencedBlobsParams{DueAt: due, Hashes: hashes})
}

// HasDocuments reports whether the database holds any document, also in
// the trash.
func (s *Store) HasDocuments(ctx context.Context) (bool, error) {
	return s.q.HasDocuments(ctx)
}
