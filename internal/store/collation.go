package store

import (
	"context"
	"errors"
	"fmt"

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

// ensureCollation rebuilds the collation-dependent indexes if the locale
// changed, unless this run already rebuilt them for the same fingerprint
// (builtFor), and stores the fingerprint once the table exists. It returns
// the fingerprint the indexes are now built for and the rebuilt indexes.
func (s *Store) ensureCollation(ctx context.Context, builtFor string) (string, []string, error) {
	cur, err := s.databaseLocale(ctx)
	if err != nil {
		return "", nil, err
	}
	fp := cur.fingerprint()
	stored, exists, err := s.storedFingerprint(ctx)
	if err != nil {
		return "", nil, err
	}
	var rebuilt []string
	if needsRebuild(cur, stored) && builtFor != fp {
		indexes, err := s.collationIndexes(ctx)
		if err != nil {
			return "", nil, err
		}
		for _, ix := range indexes {
			name := ix.Sanitize()
			// One statement each, so a failure names its index and the
			// ones before it stay rebuilt. A unique index fails here if
			// duplicates got in while it was out of order.
			if _, err := s.pool.Exec(ctx, "REINDEX INDEX "+name); err != nil {
				// The detail names the duplicated key.
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Detail != "" {
					err = fmt.Errorf("%w: %s", err, pgErr.Detail)
				}
				return "", rebuilt, fmt.Errorf("rebuild index %s after a change of the database locale: %w", name, err)
			}
			rebuilt = append(rebuilt, name)
		}
	}
	// The indexes now match the current locale. Record the provider's new
	// version so PostgreSQL stops warning about a mismatch. It refuses to
	// set one where none was recorded (musl), which is why the fingerprint
	// is kept in a table.
	if cur.RecordedVersion != "" && cur.ActualVersion != "" && cur.RecordedVersion != cur.ActualVersion {
		if _, err := s.pool.Exec(ctx, "ALTER DATABASE "+pgx.Identifier{cur.Database}.Sanitize()+" REFRESH COLLATION VERSION"); err != nil {
			return "", rebuilt, fmt.Errorf("refresh the collation version: %w", err)
		}
	}
	if exists && stored != fp {
		if _, err := s.pool.Exec(ctx, `
INSERT INTO public.index_collation (fingerprint) VALUES ($1)
ON CONFLICT (singleton) DO UPDATE SET fingerprint = EXCLUDED.fingerprint, built_at = now()`, fp); err != nil {
			return "", rebuilt, fmt.Errorf("store the index collation: %w", err)
		}
	}
	return fp, rebuilt, nil
}
