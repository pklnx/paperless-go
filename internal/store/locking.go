package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/paperless-go/internal/store/db"
)

// Write locking (docs/development/architecture.md, "Write locking"): every
// store transaction that writes documents, document_tags, tags,
// correspondents or document_types first takes the per-owner
// transaction-level advisory lock of each owner whose rows it writes, in
// ascending owner id. Every row of these tables belongs to exactly one
// owner, owner_id is never updated, and every link between them stays
// within one owner (composite foreign keys), so the writes of one owner
// serialise and no wait between store writers can form a cycle.
//
// Writers reach the lock only through ownerTx (ownergate.go), which takes
// the process's owner gate first, so a writer waiting for an owner holds
// no pool connection while another writer of that owner in this process
// is in its transaction.
//
// The lock is taken inside the transaction and released at its commit or
// rollback, also on an error or a cancelled context. A transaction that
// holds it does no slow work: no I/O, no network, no blob store (uploads
// store their file and OCR runs before the transaction starts).

// ownerLockPrefix is the text of the owner lock's key:
// hashtextextended(ownerLockPrefix || owner_id::text, 0). It is the key
// the tags_no_cycle trigger takes (migrations/00002_documents.sql), so the
// trigger's lock is this one: inside a store writer the trigger
// re-acquires a lock its transaction already holds, and SQL outside the
// store keeps the trigger's own protection. It reads "tags" because the
// trigger came first; TestOwnerLockMatchesTrigger breaks if either side
// changes.
const ownerLockPrefix = "paperless-go:tags:"

var (
	// errUnlockedWrite refuses a statement that writes or row-locks one
	// of the owner tables without the owner lock: outside a transaction,
	// or in one before lockOwners. It is a bug in the store (500), never
	// a silent write.
	errUnlockedWrite = errors.New("store: write without the owner lock")

	// errOwnerNotLocked means a writer met a row of an owner whose lock it
	// does not hold. Owners never change, so this is a bug in the store.
	errOwnerNotLocked = errors.New("store: row of an owner whose lock is not held")

	// errLockedTwice means lockOwners ran twice in one transaction, which
	// could break the ascending order.
	errLockedTwice = errors.New("store: owner lock taken twice in one transaction")

	// errNoOwners means lockOwners got no owner: the writer must answer
	// an empty selection before it locks.
	errNoOwners = errors.New("store: owner lock without an owner")
)

// ownerTables are the tables only a holder of the owner lock may write.
// search_vocabulary (00018) is written only by its trigger on documents,
// which runs inside the writer's locked transaction; it is listed so that
// a statement of the store that ever wrote it directly would need the lock
// too.
const ownerTables = `(?:documents|document_tags|tags|correspondents|document_types|search_vocabulary)`

// ownerTable matches one of them as a table name: optionally in schema
// public, each part optionally quoted ("documents", public."tags").
const ownerTable = `(?:(?:public|"public")\.)?(?:` + ownerTables + `\b|"` + ownerTables + `")`

var (
	// writeRE matches a statement that writes one of the owner tables.
	writeRE = regexp.MustCompile(`(?is)\b(?:INSERT\s+INTO|UPDATE|DELETE\s+FROM|MERGE\s+INTO|TRUNCATE(?:\s+TABLE)?)\s+(?:ONLY\s+)?` +
		ownerTable)
	// rowLockRE and fromRE together match a statement that row-locks one
	// of them: a locking clause and the table in its FROM or a JOIN.
	rowLockRE = regexp.MustCompile(`(?is)\bFOR\s+(?:NO\s+KEY\s+UPDATE|UPDATE|KEY\s+SHARE|SHARE)\b`)
	fromRE    = regexp.MustCompile(`(?is)\b(?:FROM|JOIN)\s+(?:ONLY\s+)?` + ownerTable)
)

// writesOwnerTables reports whether the SQL text sql writes or row-locks
// one of the owner tables, ignoring line comments. It decides for
// statements without a sqlc name at run time and is the definition
// TestWriterQueriesComplete holds writerQueries to.
func writesOwnerTables(sql string) bool {
	s := stripSQLComments(sql)
	return writeRE.MatchString(s) || rowLockRE.MatchString(s) && fromRE.MatchString(s)
}

// stripSQLComments removes "--" line comments. A "--" inside a string
// literal would cut the line short; the queries have none, and cutting
// only makes the check stricter for the text left.
func stripSQLComments(s string) string {
	if !strings.Contains(s, "--") {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if j := strings.Index(l, "--"); j >= 0 {
			lines[i] = l[:j]
		}
	}
	return strings.Join(lines, "\n")
}

// writerQueries are the sqlc queries that write or row-lock an owner
// table. TestWriterQueriesComplete checks it against queries/*.sql.
var writerQueries = map[string]bool{
	// documents.sql
	"CreateDocument": true, "LockDocument": true, "ClearDocumentTags": true, "AddDocumentTags": true,
	"DeleteDocument": true, "PatchDocument": true, "LockDocuments": true, "BulkRemoveDocumentTags": true,
	"BulkAddDocumentTags": true, "BulkUpdateDocuments": true,
	// metadata.sql
	"CreateCorrespondent": true, "UpdateCorrespondent": true, "DeleteCorrespondent": true, "LockCorrespondent": true,
	"CreateDocumentType": true, "UpdateDocumentType": true, "DeleteDocumentType": true, "LockDocumentType": true,
	"CreateTag": true, "UpdateTag": true, "DeleteTag": true, "LockTag": true,
	// jobs.sql
	"SetDocumentProcessed": true,
}

// queryName returns the sqlc name of a generated query ("-- name: X
// :kind" on its first line), or "" for other SQL.
func queryName(sql string) string {
	const prefix = "-- name: "
	if !strings.HasPrefix(sql, prefix) {
		return ""
	}
	rest := sql[len(prefix):]
	if i := strings.IndexAny(rest, " \n"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// isWriter reports whether a statement needs the owner lock: a named
// query by writerQueries, any other SQL by writesOwnerTables (fail
// closed: unnamed SQL is checked, never let through).
func isWriter(sql string) bool {
	if name := queryName(sql); name != "" {
		return writerQueries[name]
	}
	return writesOwnerTables(sql)
}

// txLocks is what a transaction has locked.
type txLocks struct {
	locked bool
	owners []int64 // ascending
}

// guardedDB is the only way the store reaches the database (Open, inTxWith,
// readTx): before each statement it refuses a writer of the owner tables
// (isWriter) unless it runs in a transaction that holds the owner lock.
// tx is nil outside a transaction.
type guardedDB struct {
	d  db.DBTX
	tx *txLocks
}

func (g guardedDB) check(ctx context.Context, sql string) error {
	w := isWriter(sql)
	locked := g.tx != nil && g.tx.locked
	if r, ok := ctx.Value(lockRecorderKey{}).(func(lockEvent)); ok {
		r(lockEvent{Query: queryName(sql), Writes: w, Locked: locked})
	}
	if w && !locked {
		return fmt.Errorf("%w: %s", errUnlockedWrite, statementLabel(sql))
	}
	return nil
}

// statementLabel names a statement in an error: its sqlc name, or the
// start of its text.
func statementLabel(sql string) string {
	if n := queryName(sql); n != "" {
		return n
	}
	s := strings.Join(strings.Fields(stripSQLComments(sql)), " ")
	if len(s) > 40 {
		s = s[:40] + "…"
	}
	return s
}

func (g guardedDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if err := g.check(ctx, sql); err != nil {
		return pgconn.CommandTag{}, err
	}
	return g.d.Exec(ctx, sql, args...)
}

func (g guardedDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if err := g.check(ctx, sql); err != nil {
		return nil, err
	}
	return g.d.Query(ctx, sql, args...)
}

func (g guardedDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if err := g.check(ctx, sql); err != nil {
		return errRow{err}
	}
	return g.d.QueryRow(ctx, sql, args...)
}

// errRow is a pgx.Row whose Scan returns err.
type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

// writeTx is a store transaction: its queries and the guarded connection
// they run on (for the search queries' unnamed statements).
type writeTx struct {
	q *db.Queries
	g guardedDB
	// gated are the owners whose gate ownerTx holds (ascending), and
	// deadline the end of the wait bound (zero: none, see WithOwnerWait).
	gated    []int64
	deadline time.Time
}

// ownerLockDisabled makes lockOwners record the owners without taking the
// lock, so the guard lets the writes through unlocked: a test-only switch
// (export_test.go) that shows a concurrency test fails without the lock.
var ownerLockDisabled atomic.Bool

// lockOwners takes the owner lock of each owner, deduplicated, in
// ascending id, one statement each (SQL does not promise the order in
// which a function runs over rows). Only ownerTx calls it, once, as the
// first statement of its transaction, with the owners it gated; other
// owners (errNotGated), a second call (errLockedTwice) and an empty list
// (errNoOwners) are bugs.
//
// With a wait bound (t.deadline) each LockOwner runs under a
// lock_timeout of the time left then (at least 1 ms; none left: ErrBusy
// without the statement), and lock_timeout is reset to its default right
// after the last one, so row locks and triggers never run under it. A
// lock_timeout at LockOwner (55P03) is ErrBusy.
func (t *writeTx) lockOwners(ctx context.Context, owners ...int64) error {
	if t.g.tx.locked {
		return errLockedTwice
	}
	if len(owners) == 0 {
		return errNoOwners
	}
	sorted := slices.Clone(owners)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	if !slices.Equal(sorted, t.gated) {
		return fmt.Errorf("%w: locking %v, gated %v", errNotGated, sorted, t.gated)
	}
	if r, ok := ctx.Value(lockRecorderKey{}).(func(lockEvent)); ok {
		r(lockEvent{Owners: slices.Clone(sorted)})
	}
	if !ownerLockDisabled.Load() {
		bounded := !t.deadline.IsZero()
		for _, o := range sorted {
			if bounded {
				left := time.Until(t.deadline)
				if left <= 0 {
					return ErrBusy
				}
				if _, err := t.g.Exec(ctx, fmt.Sprintf("SET LOCAL lock_timeout = %d", max(left.Milliseconds(), 1))); err != nil {
					return err
				}
			}
			if err := t.q.LockOwner(ctx, db.LockOwnerParams{Prefix: ownerLockPrefix, OwnerID: o}); err != nil {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == "55P03" { // lock_not_available
					return ErrBusy
				}
				return err
			}
		}
		if bounded {
			if _, err := t.g.Exec(ctx, "SET LOCAL lock_timeout TO DEFAULT"); err != nil {
				return err
			}
		}
	}
	t.g.tx.locked, t.g.tx.owners = true, sorted
	return nil
}

// holds returns errOwnerNotLocked unless the transaction holds the lock
// of each owner: the check after a row lock or write that the row's
// owner, read before the lock, is still the one locked.
func (t *writeTx) holds(owners ...int64) error {
	for _, o := range owners {
		if !slices.Contains(t.g.tx.owners, o) {
			return fmt.Errorf("%w: owner %d, locked %v", errOwnerNotLocked, o, t.g.tx.owners)
		}
	}
	return nil
}

// lockRecorderKey carries a test hook (export_test.go) that gets every
// lockOwners call and every guarded statement of a transaction.
type lockRecorderKey struct{}

// lockEvent is one call to the recorder: an ownerTx past its gate (Gated
// set), a lockOwners call (Owners set)
// or a statement (Query its sqlc name, Writes whether it needs the lock,
// Locked whether the lock was held).
type lockEvent struct {
	Gated  []int64 // an ownerTx took the gate of these owners
	Owners []int64
	Query  string
	Writes bool
	Locked bool
}

// readTx runs fn in a read-only transaction on the guarded connection
// (the relevance search's SET LOCAL). It holds no owner lock, so a
// writer statement in it is refused.
func (s *Store) readTx(ctx context.Context, fn func(guardedDB) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(guardedDB{d: tx, tx: &txLocks{}}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
