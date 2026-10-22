package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Text indexes and the database locale.
//
// A B-tree index on a text column is sorted by the database's collation,
// and an index on lower(name) stores values computed with its character
// classification. Both come from the C library (libc provider), ICU or
// PostgreSQL itself (builtin provider). When that code changes under an
// existing data directory, the indexes are out of order: lookups miss rows
// and unique indexes let duplicates in. PostgreSQL warns about a changed
// collation version only when it recorded one, and musl (the Alpine image)
// reports none, so the switch from postgres:18-alpine to the Debian image
// changes the sort order without any warning.
//
// Migrate therefore keeps a fingerprint of the locale in index_collation
// and rebuilds the collation-dependent indexes when it changes. The queries
// read the system catalogs, which sqlc does not model, so they live here.

// MinServerVersion is the oldest PostgreSQL major version paperless-go
// supports. The locale queries need 17 (pg_database.datlocale).
const MinServerVersion = 17

// checkServerVersion refuses a server older than MinServerVersion.
func checkServerVersion(num int, version string) error {
	if num/10000 < MinServerVersion {
		return fmt.Errorf("PostgreSQL %d or newer is required, found %s", MinServerVersion, version)
	}
	return nil
}

// CheckServerVersion refuses a PostgreSQL server older than
// MinServerVersion. Migrate runs it first; serve and process too.
func (s *Store) CheckServerVersion(ctx context.Context) error {
	var num int
	var version string
	if err := s.pool.QueryRow(ctx, `SELECT current_setting('server_version_num')::int, current_setting('server_version')`).Scan(&num, &version); err != nil {
		return fmt.Errorf("read the PostgreSQL version: %w", err)
	}
	return checkServerVersion(num, version)
}

// databaseLocale describes the current database's locale.
type databaseLocale struct {
	Database        string
	Provider        string // c (libc), i (ICU) or b (builtin)
	Collate, Ctype  string
	Locale          string // ICU or builtin locale
	ActualVersion   string // what the provider reports now
	RecordedVersion string // pg_database.datcollversion
	ServerMajor     int
}

// fingerprint changes whenever the order or the case mapping of text may
// have changed. The PostgreSQL major version is part of it because the
// builtin provider's case mapping follows PostgreSQL's Unicode tables.
func (l databaseLocale) fingerprint() string {
	return fmt.Sprintf("provider=%s collate=%s ctype=%s locale=%s version=%s server=%d",
		l.Provider, l.Collate, l.Ctype, l.Locale, l.ActualVersion, l.ServerMajor)
}

// stable reports whether text order and case mapping are fixed by
// PostgreSQL alone: the C or POSIX locale of libc (byte order, ASCII case
// mapping) or the builtin provider (code point order).
func (l databaseLocale) stable() bool {
	switch l.Provider {
	case "b":
		return true
	case "c":
		return isCLocale(l.Collate) && isCLocale(l.Ctype)
	}
	return false
}

func isCLocale(s string) bool { return s == "C" || s == "POSIX" }

// needsRebuild decides from the stored fingerprint (empty: none stored)
// whether the collation-dependent indexes must be rebuilt. Without a
// stored fingerprint nobody knows which library built them, so only a
// stable locale is trusted.
func needsRebuild(cur databaseLocale, stored string) bool {
	if stored == "" {
		return !cur.stable()
	}
	return stored != cur.fingerprint()
}

func (s *Store) databaseLocale(ctx context.Context) (databaseLocale, error) {
	var l databaseLocale
	err := s.pool.QueryRow(ctx, `
SELECT d.datname, d.datlocprovider::text, d.datcollate, d.datctype, coalesce(d.datlocale, ''),
       coalesce(pg_database_collation_actual_version(d.oid), ''),
       coalesce(d.datcollversion, ''),
       current_setting('server_version_num')::int / 10000
FROM pg_database d
WHERE d.datname = current_database()`).Scan(
		&l.Database, &l.Provider, &l.Collate, &l.Ctype, &l.Locale, &l.ActualVersion, &l.RecordedVersion, &l.ServerMajor)
	if err != nil {
		return l, fmt.Errorf("read the database locale: %w", err)
	}
	return l, nil
}

// storedFingerprint returns the stored fingerprint, "" if there is none,
// and whether the table exists (it does not before migration 00013).
func (s *Store) storedFingerprint(ctx context.Context) (fp string, exists bool, err error) {
	if err := s.pool.QueryRow(ctx, `SELECT to_regclass('public.index_collation') IS NOT NULL`).Scan(&exists); err != nil {
		return "", false, fmt.Errorf("look for index_collation: %w", err)
	}
	if !exists {
		return "", false, nil
	}
	err = s.pool.QueryRow(ctx, `SELECT fingerprint FROM public.index_collation`).Scan(&fp)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", true, nil
	}
	if err != nil {
		return "", true, fmt.Errorf("read index_collation: %w", err)
	}
	return fp, true, nil
}

// collationIndexes lists the indexes whose order follows the database
// locale: those with a key column or expression of a collatable type whose
// collation is not C or POSIX. Indexes on partitioned tables are listed
// per partition. Equality under these deterministic collations is byte
// equality, so partial-index predicates like target_type = 'user' do not
// depend on the locale.
func (s *Store) collationIndexes(ctx context.Context) ([]pgx.Identifier, error) {
	rows, err := s.pool.Query(ctx, `
SELECT n.nspname, c.relname
FROM pg_index i
JOIN pg_class c ON c.oid = i.indexrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind = 'i'
  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
  AND n.nspname NOT LIKE 'pg\_toast%'
  AND n.nspname NOT LIKE 'pg\_temp%'
  AND EXISTS (
      SELECT 1 FROM unnest(i.indcollation::oid[]) AS coll (oid)
      WHERE coll.oid <> 0 AND coll.oid NOT IN ('"C"'::regcollation, '"POSIX"'::regcollation))
ORDER BY n.nspname, c.relname`)
	if err != nil {
		return nil, fmt.Errorf("list collation-dependent indexes: %w", err)
	}
	defer rows.Close()
	var out []pgx.Identifier
	for rows.Next() {
		var schema, name string
		if err := rows.Scan(&schema, &name); err != nil {
			return nil, err
		}
		out = append(out, pgx.Identifier{schema, name})
	}
	return out, rows.Err()
}

// CollationNeedsRebuild reports whether the collation-dependent indexes may
// be out of order because the database locale changed since they were
// built (or is unknown and not stable). Migrate rebuilds them.
func (s *Store) CollationNeedsRebuild(ctx context.Context) (bool, error) {
	if err := s.CheckServerVersion(ctx); err != nil {
		return false, err
	}
	cur, err := s.databaseLocale(ctx)
	if err != nil {
		return false, err
	}
	stored, _, err := s.storedFingerprint(ctx)
	if err != nil {
		return false, err
	}
	return needsRebuild(cur, stored), nil
}

// reindexLockTimeout bounds how long a REINDEX waits for its table, so a
// running web server or a long transaction makes migrate fail fast instead
// of queueing every query on the table behind it. Tests shorten it.
var reindexLockTimeout = 30 * time.Second

// ensureCollation rebuilds the collation-dependent indexes if the locale
// changed, unless this run already rebuilt them for the same fingerprint
// (builtFor), and stores the fingerprint once the table exists. It returns
// the fingerprint the indexes are now built for, the rebuilt indexes and
// warnings for the operator.
func (s *Store) ensureCollation(ctx context.Context, builtFor string) (fp string, rebuilt, warnings []string, err error) {
	cur, err := s.databaseLocale(ctx)
	if err != nil {
		return "", nil, nil, err
	}
	fp = cur.fingerprint()
	stored, exists, err := s.storedFingerprint(ctx)
	if err != nil {
		return "", nil, nil, err
	}
	if needsRebuild(cur, stored) && builtFor != fp {
		indexes, err := s.collationIndexes(ctx)
		if err != nil {
			return "", nil, nil, err
		}
		// One index at a time, so a failure names its index and the others
		// still get rebuilt; every failure is reported at the end. A unique
		// index fails if duplicates got in while it was out of order.
		var failed []error
		for _, ix := range indexes {
			if err := s.reindex(ctx, ix); err != nil {
				failed = append(failed, err)
				continue
			}
			rebuilt = append(rebuilt, ix.Sanitize())
		}
		if len(failed) > 0 {
			return "", rebuilt, nil, fmt.Errorf("rebuild the text indexes after a change of the database locale: %w", errors.Join(failed...))
		}
	}
	// The indexes now match the current locale.
	if exists && stored != fp {
		if _, err := s.pool.Exec(ctx, `
INSERT INTO public.index_collation (fingerprint) VALUES ($1)
ON CONFLICT (singleton) DO UPDATE SET fingerprint = EXCLUDED.fingerprint, built_at = now()`, fp); err != nil {
			return "", rebuilt, nil, fmt.Errorf("store the index collation: %w", err)
		}
	}
	// Record the provider's new version so PostgreSQL stops warning about
	// a mismatch. It refuses to set one where none was recorded (musl),
	// which is why the fingerprint is kept in a table. Only the database
	// owner may do it; on your own server that can be an administrator,
	// and the indexes are correct either way, so that is only a warning.
	if cur.RecordedVersion != "" && cur.ActualVersion != "" && cur.RecordedVersion != cur.ActualVersion {
		stmt := "ALTER DATABASE " + pgx.Identifier{cur.Database}.Sanitize() + " REFRESH COLLATION VERSION"
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "42501" { // insufficient_privilege
				return fp, rebuilt, nil, fmt.Errorf("refresh the collation version: %w", err)
			}
			warnings = append(warnings, fmt.Sprintf(
				"the text indexes were rebuilt, but only the owner of the database may record the new collation version "+
					"(PostgreSQL warns about a mismatch until then); have an administrator run: %s;", stmt))
		}
	}
	return fp, rebuilt, warnings, nil
}

// reindex rebuilds one index in a transaction with a lock timeout. A
// failure because of duplicates lists the duplicated keys.
func (s *Store) reindex(ctx context.Context, ix pgx.Identifier) error {
	name := ix.Sanitize()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL lock_timeout = %d", reindexLockTimeout.Milliseconds())); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "REINDEX INDEX "+name)
		return err
	})
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "55P03": // lock_not_available
			return fmt.Errorf("index %s: its table stayed locked for %s; stop the web server and other users of the database and run migrate again: %w",
				name, reindexLockTimeout, err)
		case "23505": // unique_violation
			keys, kerr := s.duplicateKeys(ctx, ix)
			if kerr != nil || len(keys) == 0 {
				return fmt.Errorf("index %s: %w: %s", name, err, pgErr.Detail)
			}
			return fmt.Errorf("index %s: these keys are duplicated, rename or remove all but one row of each: %s",
				name, strings.Join(keys, ", "))
		}
	}
	return fmt.Errorf("index %s: %w", name, err)
}

// maxDuplicateKeys caps the keys one error lists.
const maxDuplicateKeys = 50

// duplicateKeys lists the key values of the unique index ix that occur
// more than once among the rows it covers (its predicate, and NULLs only
// with NULLS NOT DISTINCT), as (columns)=(values).
func (s *Store) duplicateKeys(ctx context.Context, ix pgx.Identifier) ([]string, error) {
	var table, pred string
	var nkeys int
	var nullsNotDistinct bool
	if err := s.pool.QueryRow(ctx, `
SELECT i.indrelid::regclass::text, coalesce(pg_get_expr(i.indpred, i.indrelid), ''), i.indnkeyatts, i.indnullsnotdistinct
FROM pg_index i WHERE i.indexrelid = to_regclass($1)`, ix.Sanitize()).Scan(&table, &pred, &nkeys, &nullsNotDistinct); err != nil {
		return nil, err
	}
	cols := make([]string, nkeys)
	for k := range cols {
		if err := s.pool.QueryRow(ctx, `SELECT pg_get_indexdef(to_regclass($1), $2, true)`, ix.Sanitize(), k+1).Scan(&cols[k]); err != nil {
			return nil, err
		}
	}
	var where []string
	if pred != "" {
		where = append(where, "("+pred+")")
	}
	if !nullsNotDistinct {
		for _, c := range cols {
			where = append(where, "("+c+") IS NOT NULL")
		}
	}
	list := strings.Join(cols, ", ")
	q := "SELECT ROW(" + list + ")::text FROM " + table
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " GROUP BY " + list + " HAVING count(*) > 1 ORDER BY 1 LIMIT " + fmt.Sprint(maxDuplicateKeys)
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	for i, k := range keys {
		keys[i] = "(" + list + ")=" + k
	}
	return keys, nil
}
