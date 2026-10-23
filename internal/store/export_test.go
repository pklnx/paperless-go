package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pklnx/paperless-go/internal/search"
	"github.com/pklnx/paperless-go/internal/store/db"
)

// SearchFilterIDs resolves q's filters as SearchDocuments does and returns
// the IDs they stand for, so a test can check name resolution on its own:
// the search query's owner predicate would hide a resolution that leaks.
func SearchFilterIDs(ctx context.Context, s *Store, sc Scope, q search.Query) (tags, notTags, correspondents, types []string, none bool, err error) {
	var args searchArgs
	if none, err = s.searchFilters(ctx, sc, q, &args); err != nil || none {
		return nil, nil, nil, nil, none, err
	}
	str := func(ids []pgtype.UUID) []string {
		out := []string{}
		for _, id := range ids {
			out = append(out, idString(id))
		}
		return out
	}
	return str(args.TagIds), str(args.NotTagIds), str(args.Correspondents), str(args.Types), false, nil
}

// RecordSearchSQL makes s hand the SQL and arguments of each search query
// to rec before running it, so a test can EXPLAIN exactly what the store
// runs.
func RecordSearchSQL(s *Store, rec func(sql string, args []any)) {
	s.searchDB = func(d guardedDB) db.DBTX { return recorder{unnamedStatements{d}, rec} }
	s.sq = db.New(s.searchDB(s.g))
}

type recorder struct {
	db.DBTX
	rec func(string, []any)
}

func (r recorder) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	r.rec(sql, args)
	return r.DBTX.Exec(ctx, sql, args...)
}

func (r recorder) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	r.rec(sql, args)
	return r.DBTX.Query(ctx, sql, args...)
}

func (r recorder) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	r.rec(sql, args)
	return r.DBTX.QueryRow(ctx, sql, args...)
}

// SetReindexLockTimeout shortens the lock timeout of the index rebuild for
// the test.
func SetReindexLockTimeout(t *testing.T, d time.Duration) {
	old := reindexLockTimeout
	reindexLockTimeout = d
	t.Cleanup(func() { reindexLockTimeout = old })
}

// WithAfterLock makes the next patch of a tag, correspondent, document
// type or document, a bulk change of documents and a delete of a tag,
// correspondent or document type call f once it holds the owner's write
// lock and its row locks, before it writes: a test holds the change there
// to make a concurrent one wait for the lock. A patch of a saved view
// calls it after its row lock, an appending create of one after the
// owner's saved-view order lock.
func WithAfterLock(ctx context.Context, f func()) context.Context {
	return context.WithValue(ctx, afterLockKey{}, f)
}

// WithBeforeLock makes a bulk change of documents call f right before it
// locks the documents: by search after the match (in a scope of one
// owner already under that owner's write lock, in an admin's scope over
// all owners before the owner locks), by IDs before the owner locks. A
// test holds it there to change a match concurrently.
func WithBeforeLock(ctx context.Context, f func()) context.Context {
	return context.WithValue(ctx, beforeLockKey{}, f)
}

// LockEvent is what WithLockRecorder reports: an owner transaction past
// its gate (Gated set, ascending), a lockOwners call (Owners set,
// ascending) or a guarded statement (Query its sqlc name, "" for
// other SQL; Writes whether it needs the owner lock; Locked whether the
// transaction held it).
type LockEvent = lockEvent

// WithLockRecorder makes every transaction run with ctx report its owner
// locks and guarded statements to rec, in order.
func WithLockRecorder(ctx context.Context, rec func(LockEvent)) context.Context {
	return context.WithValue(ctx, lockRecorderKey{}, rec)
}

// DisableOwnerLock makes lockOwners skip the lock until the test ends, and
// with it the guard (the writes run as if the lock were held): it shows
// that a concurrency test fails without the owner lock. The owner gate
// stays on. It flips a package-global switch, so a test that uses it must
// not run in parallel (t.Parallel) with other store tests.
func DisableOwnerLock(t *testing.T) {
	ownerLockDisabled.Store(true)
	t.Cleanup(func() { ownerLockDisabled.Store(false) })
}

// DisableOwnerGate makes ownerTx skip the owner gate until the test ends
// (the owner lock stays): it shows that a test fails without the gate.
// Like DisableOwnerLock it flips a package-global switch: no t.Parallel.
func DisableOwnerGate(t *testing.T) {
	ownerGateDisabled.Store(true)
	t.Cleanup(func() { ownerGateDisabled.Store(false) })
}

// WithGateWait makes a writer with ctx call f when it starts to wait at
// the owner gate, because another writer of the owner holds it.
func WithGateWait(ctx context.Context, f func()) context.Context {
	return context.WithValue(ctx, gateWaitKey{}, f)
}

// WithAfterOwnerLock makes a writer with ctx call f in its transaction
// right after it took the owner locks; query runs a statement there and
// returns its single text value.
func WithAfterOwnerLock(ctx context.Context, f func(query func(sql string) (string, error))) context.Context {
	return context.WithValue(ctx, afterOwnerLockKey{}, func(t *writeTx) {
		f(func(sql string) (string, error) {
			var v string
			err := t.g.QueryRow(ctx, sql).Scan(&v)
			return v, err
		})
	})
}

// PoolSetting returns the run-time parameter name as a statement on the
// pool sees it.
func PoolSetting(ctx context.Context, s *Store, name string) (string, error) {
	var v string
	err := s.g.QueryRow(ctx, `SELECT current_setting($1)`, name).Scan(&v)
	return v, err
}

// AcquiredConns is the number of the store's pool connections in use now.
func AcquiredConns(s *Store) int32 { return s.pool.Stat().AcquiredConns() }

// GateEntries is the number of owners the store's gate has an entry for.
func GateEntries(s *Store) int { return s.gate.size() }

// LockWithoutGate calls lockOwners in a transaction that did not take the
// gate, as a writer bypassing ownerTx would.
func LockWithoutGate(ctx context.Context, s *Store, owner int64) error {
	return s.inTxWith(ctx, func(t *writeTx) error { return t.lockOwners(ctx, owner) })
}

// NestedCreateTag creates a tag of owner inside an owner transaction of
// owner, with the transaction's context, as a writer calling another
// writer would.
func NestedCreateTag(ctx context.Context, s *Store, owner int64) error {
	return s.ownerTx(ctx, []int64{owner}, func(ctx context.Context, _ *writeTx) error {
		_, err := s.CreateTag(ctx, Scope{UserID: owner}, TagInput{Name: "nested"})
		return err
	})
}

// OwnerLockPrefix is the key text of the owner lock.
const OwnerLockPrefix = ownerLockPrefix

// Errors of the write lock, for the guard tests.
var (
	ErrUnlockedWrite  = errUnlockedWrite
	ErrOwnerNotLocked = errOwnerNotLocked
	ErrLockedTwice    = errLockedTwice
	ErrNoOwners       = errNoOwners
	ErrNotGated       = errNotGated
	ErrNestedOwnerTx  = errNestedOwnerTx
)

// UnlockedPatch runs the raw PatchDocument query on document id (a new
// title) without the owner lock: in a transaction (inTx) or on the pool.
func UnlockedPatch(ctx context.Context, s *Store, id string, inTx bool) error {
	u, ok := parseID(id)
	if !ok {
		return ErrNotFound
	}
	title := "unlocked"
	p := db.PatchDocumentParams{ID: u, Title: &title}
	if !inTx {
		_, err := s.q.PatchDocument(ctx, p)
		return err
	}
	return s.inTx(ctx, func(q *db.Queries) error {
		_, err := q.PatchDocument(ctx, p)
		return err
	})
}

// GuardedExec runs sql through the guard: on the pool (owners nil), in a
// transaction without the lock (an empty outer list), or in an owner
// transaction of the first list of owners that then calls lockOwners
// again with each further list.
func GuardedExec(ctx context.Context, s *Store, sql string, owners [][]int64) error {
	if owners == nil {
		_, err := s.g.Exec(ctx, sql)
		return err
	}
	if len(owners) == 0 {
		return s.inTxWith(ctx, func(t *writeTx) error {
			_, err := t.g.Exec(ctx, sql)
			return err
		})
	}
	return s.ownerTx(ctx, owners[0], func(ctx context.Context, t *writeTx) error {
		for _, o := range owners[1:] {
			if err := t.lockOwners(ctx, o...); err != nil {
				return err
			}
		}
		_, err := t.g.Exec(ctx, sql)
		return err
	})
}

// WithAfterBlobHold makes a writer with ctx call f in its transaction
// right after holdBlobs: it then holds its owner and blob locks.
func WithAfterBlobHold(ctx context.Context, f func()) context.Context {
	return context.WithValue(ctx, afterBlobHoldKey{}, f)
}

// WithAfterBlobLock makes MarkBlobRemoval with ctx call f in its
// transaction right after it took the hash's exclusive blob lock.
func WithAfterBlobLock(ctx context.Context, f func()) context.Context {
	return context.WithValue(ctx, afterBlobLockKey{}, f)
}

// BlobLockClass and BlobLockKey are the two int4 of a hash's blob lock.
const BlobLockClass = blobLockClass

// BlobLockKey is the second int4 of hash's blob lock key.
func BlobLockKey(hash string) int32 { return blobLockKey(hash) }

// IsHashWriterQuery reports whether the sqlc query name writes a hash
// column of documents (hashWriterQueries).
func IsHashWriterQuery(name string) bool { return hashWriterQueries[name] }

// Errors of the blob guard.
var (
	ErrUnheldBlobWrite  = errUnheldBlobWrite
	ErrBlobsBeforeOwner = errBlobsBeforeOwner
	ErrBlobsHeldTwice   = errBlobsHeldTwice
	ErrNoBlobs          = errNoBlobs
)

// HoldBlobsTwice runs holdBlobs twice in an owner transaction of owner;
// HoldBlobsBeforeOwner runs it in a transaction without the owner lock.
func HoldBlobsTwice(ctx context.Context, s *Store, owner int64) error {
	return s.ownerTx(ctx, []int64{owner}, func(ctx context.Context, t *writeTx) error {
		if err := s.holdBlobs(ctx, t); err != nil {
			return err
		}
		return s.holdBlobs(ctx, t)
	})
}

// HoldBlobsBeforeOwner runs holdBlobs in a transaction that holds no
// owner lock.
func HoldBlobsBeforeOwner(ctx context.Context, s *Store) error {
	return s.inTxWith(ctx, func(t *writeTx) error { return s.holdBlobs(ctx, t) })
}
