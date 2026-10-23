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

	"github.com/jackc/pgx/v5"

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

// MarkStale is how old a mark must be before another queue pass takes it
// over: only a crashed pass leaves a mark behind (a failed removal is
// requeued), and the sweep lock keeps passes apart, so a younger mark is
// never touched. After a crash, writers of the hash are refused (and
// retry) for that long.
const MarkStale = 5 * time.Minute

// sweepLockClass is the class of the sweep lock (ExclusiveSweep): "pgos",
// apart from the blob locks' class, so no hash prefix can take it.
const sweepLockClass int32 = 0x70676f73

// ErrSweepRunning means another queue pass or walk holds the sweep lock,
// in this or another process; nothing was done.
var ErrSweepRunning = errors.New("store: another sweep is running")

// ExclusiveSweep runs fn while it holds the sweep lock, a session-level
// advisory lock on a connection of its own: at most one queue pass or
// walk runs at a time across processes (serve's sweeper and ./pgo sweep),
// so no pass acts on a mark another pass already finished. When another
// holds it, fn does not run and the error is ErrSweepRunning. The lock
// goes with the connection if this process dies; if it cannot be released
// the connection is closed.
func (s *Store) ExclusiveSweep(ctx context.Context, fn func() error) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	q := db.New(guardedDB{d: conn})
	got, err := q.TrySweepLock(ctx, sweepLockClass)
	if err != nil {
		conn.Release()
		return err
	}
	if !got {
		conn.Release()
		return ErrSweepRunning
	}
	defer func() {
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if ok, err := q.SweepUnlock(uctx, sweepLockClass); err != nil || !ok {
			// A connection that may still hold the lock never goes back
			// to the pool.
			_ = conn.Conn().Close(uctx)
		}
		conn.Release()
	}()
	return fn()
}

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
				now, _ := s.trashClock()
				return &BlobRemovedError{Hash: r.Sha256, RetryAfter: markRetryAfter(*r.RemovedAt, now)}
			}
		}
		for _, h := range hs {
			// One stat per hash: the named exception to "no I/O under
			// the owner lock" (architecture.md, "Write locking").
			ok, err := s.blobs.Present(h)
			if err != nil {
				return fmt.Errorf("store: check the file %s: %w", h, err)
			}
			if !ok {
				return &BlobRemovedError{Hash: h, RetryAfter: time.Second}
			}
		}
	}
	t.g.tx.blobsHeld = true
	if f, ok := ctx.Value(afterBlobHoldKey{}).(func()); ok {
		f()
	}
	return nil
}

// BlobRemovedError is ErrBlobRemoved for one hash, with how long a caller
// should wait before it tries again: a second when the file is missing
// (the retry stores it again), or, when the sweeper's mark is on the row,
// until a pass that crashed after its mark would be taken over
// (MarkStale) plus one sweep round. A live pass finishes much sooner, but
// a writer cannot tell the two apart. errors.Is(err, ErrBlobRemoved) holds.
type BlobRemovedError struct {
	Hash       string
	RetryAfter time.Duration
}

func (e *BlobRemovedError) Error() string {
	return fmt.Sprintf("%v: %s", ErrBlobRemoved, e.Hash)
}

// Is makes errors.Is(err, ErrBlobRemoved) hold.
func (e *BlobRemovedError) Is(target error) bool { return target == ErrBlobRemoved }

// markRound is the longest wait between a stale mark and the pass that
// takes it over: serve sweeps every minute (sweep.DefaultInterval).
const markRound = time.Minute

// markRetryAfter is how long a writer that met a mark set at markedAt
// waits at now: until the mark is stale plus one round, at least a second.
func markRetryAfter(markedAt, now time.Time) time.Duration {
	return max(markedAt.Add(MarkStale+markRound).Sub(now), time.Second)
}

// afterBlobHoldKey carries a test hook (export_test.go) that runs in a
// writer's transaction right after holdBlobs.
type afterBlobHoldKey struct{}

// BlobRemovalsDue returns up to limit hashes the queue pass may take at
// now: due unmarked rows and rows a crashed run marked more than
// MarkStale before.
func (s *Store) BlobRemovalsDue(ctx context.Context, now time.Time, limit int) ([]string, error) {
	return s.q.BlobRemovalsDue(ctx, db.BlobRemovalsDueParams{
		Now: now, StaleBefore: now.Add(-MarkStale), Lim: int32(min(limit, 1<<30)), //nolint:gosec // bounded
	})
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
// (BlobMarked; the row's removed_at is now), or the row deleted when a
// document names the hash (BlobInUse). A row another pass marked less
// than MarkStale before is left alone (BlobKept). Only after BlobMarked may
// the caller remove the file and then call FinishBlobRemoval, or
// RequeueBlobRemoval when the removal failed, with the same now.
//
// The transaction runs at READ COMMITTED whatever the database's default:
// the UPDATE must take its snapshot after the lock was granted, to see
// every writer that held the shared lock (under REPEATABLE READ the
// snapshot would be the lock statement's, taken before the wait).
func (s *Store) MarkBlobRemoval(ctx context.Context, hash string, now time.Time) (BlobMark, error) {
	if !isSHA256(hash) {
		return 0, invalid("sha256")
	}
	now = now.Truncate(time.Microsecond)
	mark := BlobKept
	err := s.readCommittedTx(ctx, func(q *db.Queries) error {
		if err := q.LockBlob(ctx, db.LockBlobParams{Class: blobLockClass, Key: blobLockKey(hash)}); err != nil {
			return err
		}
		if f, ok := ctx.Value(afterBlobLockKey{}).(func()); ok {
			f()
		}
		marked, err := q.MarkBlobRemoval(ctx, db.MarkBlobRemovalParams{Now: now, StaleBefore: now.Add(-MarkStale), Sha256: hash})
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

// readCommittedTx runs fn in a transaction at READ COMMITTED that writes
// none of the owner tables.
func (s *Store) readCommittedTx(ctx context.Context, fn func(*db.Queries) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(db.New(guardedDB{d: tx, tx: &txLocks{}})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FinishBlobRemoval is step 3 of the queue pass: it deletes hash's row
// marked at markedAt (MarkBlobRemoval's now) once the file is gone. A row
// that is not there any more, or another pass's mark, is left alone.
func (s *Store) FinishBlobRemoval(ctx context.Context, hash string, markedAt time.Time) error {
	_, err := s.q.FinishBlobRemoval(ctx, db.FinishBlobRemovalParams{Sha256: hash, MarkedAt: markedAt.Truncate(time.Microsecond)})
	return err
}

// RequeueBlobRemoval is the queue pass's answer to a failed removal: hash's
// row marked at markedAt is unmarked and due again at due. Writers of the
// hash are not refused meanwhile (a removal that failed after the unlink
// makes them store the file again).
func (s *Store) RequeueBlobRemoval(ctx context.Context, hash string, markedAt, due time.Time) error {
	_, err := s.q.RequeueBlobRemoval(ctx, db.RequeueBlobRemovalParams{
		Sha256: hash, MarkedAt: markedAt.Truncate(time.Microsecond), DueAt: due,
	})
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
