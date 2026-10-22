package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/paperless-go/internal/store/db"
)

// RecordSearchSQL makes s hand the SQL and arguments of each search query
// to rec before running it, so a test can EXPLAIN exactly what the store
// runs.
func RecordSearchSQL(s *Store, rec func(sql string, args []any)) {
	s.sq = db.New(recorder{unnamedStatements{s.pool}, rec})
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
