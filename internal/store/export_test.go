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
	s.searchDB = func(d db.DBTX) db.DBTX { return recorder{unnamedStatements{d}, rec} }
	s.sq = db.New(s.searchDB(s.pool))
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

// WithAfterLock makes the next patch of a tag, correspondent or document
// type call f with the row locked, before it writes: a test holds the
// patch there to make a concurrent one wait for the lock.
func WithAfterLock(ctx context.Context, f func()) context.Context {
	return context.WithValue(ctx, afterLockKey{}, f)
}
