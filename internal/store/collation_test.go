package store_test

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// relfilenodes maps the collation-dependent test indexes to their storage
// file numbers; REINDEX gives an index a new one.
func relfilenodes(t *testing.T, conn *pgx.Conn, names ...string) map[string]uint32 {
	t.Helper()
	out := map[string]uint32{}
	for _, n := range names {
		var node uint32
		if err := conn.QueryRow(context.Background(),
			"SELECT pg_relation_filenode(to_regclass($1))", n).Scan(&node); err != nil {
			t.Fatalf("filenode of %s: %v", n, err)
		}
		out[n] = node
	}
	return out
}

func TestMigrateStoresIndexCollation(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var fp string
	if err := conn.QueryRow(ctx, "SELECT fingerprint FROM index_collation").Scan(&fp); err != nil {
		t.Fatalf("no fingerprint after migrate: %v", err)
	}
	if !strings.Contains(fp, "provider=") || !strings.Contains(fp, "server=") {
		t.Errorf("fingerprint %q", fp)
	}
	if rebuild, err := st.CollationNeedsRebuild(ctx); err != nil || rebuild {
		t.Errorf("CollationNeedsRebuild after migrate = %v, %v; want false", rebuild, err)
	}

	// A second migrate with the same locale rebuilds nothing.
	before := relfilenodes(t, conn, "users_name_key", "tags_owner_parent_name_key")
	applied, rebuilt, err := st.MigrateAndRebuild(ctx)
	if err != nil || len(applied) != 0 || len(rebuilt) != 0 {
		t.Fatalf("migrate again: applied %v, rebuilt %v, %v", applied, rebuilt, err)
	}
	if after := relfilenodes(t, conn, "users_name_key", "tags_owner_parent_name_key"); !maps.Equal(before, after) {
		t.Errorf("indexes rebuilt without a locale change: %v -> %v", before, after)
	}
}

// The database was built under another locale (here: a fingerprint of
// another libc version), as after the switch from the Alpine to the
// Debian image: migrate rebuilds every collation-dependent index once,
// including the partitions of the audit log, and leaves the others alone.
func TestMigrateRebuildsIndexesAfterLocaleChange(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	storetest.User(t, st, "alice", false)

	var fp string
	if err := conn.QueryRow(ctx, "SELECT fingerprint FROM index_collation").Scan(&fp); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "UPDATE index_collation SET fingerprint = 'provider=c collate=en_US.utf8 ctype=en_US.utf8 locale= version= server=18'"); err != nil {
		t.Fatal(err)
	}
	if rebuild, err := st.CollationNeedsRebuild(ctx); err != nil || !rebuild {
		t.Fatalf("CollationNeedsRebuild after a locale change = %v, %v; want true", rebuild, err)
	}

	textIdx := []string{"users_name_key", "tags_owner_parent_name_key", "correspondents_owner_name_key", "documents_owner_original_sha256_key"}
	otherIdx := []string{"users_pkey", "documents_search_idx", "documents_owner_date_idx"}
	beforeText := relfilenodes(t, conn, textIdx...)
	beforeOther := relfilenodes(t, conn, otherIdx...)

	applied, rebuilt, err := st.MigrateAndRebuild(ctx)
	if err != nil || len(applied) != 0 {
		t.Fatalf("migrate: applied %v, %v", applied, err)
	}
	for _, n := range textIdx {
		if !slices.Contains(rebuilt, "\"public\".\""+n+"\"") {
			t.Errorf("%s not in rebuilt %v", n, rebuilt)
		}
	}
	partition := false
	for _, n := range rebuilt {
		if strings.Contains(n, "audit_log_") && strings.Contains(n, "target") {
			partition = true
		}
		if slices.Contains(otherIdx, strings.Trim(strings.TrimPrefix(n, "\"public\"."), "\"")) {
			t.Errorf("rebuilt %s, which does not depend on the collation", n)
		}
	}
	if !partition {
		t.Errorf("no audit log partition index in rebuilt %v", rebuilt)
	}
	afterText := relfilenodes(t, conn, textIdx...)
	for _, n := range textIdx {
		if afterText[n] == beforeText[n] {
			t.Errorf("%s keeps filenode %d: not rebuilt", n, afterText[n])
		}
	}
	if afterOther := relfilenodes(t, conn, otherIdx...); !maps.Equal(beforeOther, afterOther) {
		t.Errorf("other indexes rebuilt: %v -> %v", beforeOther, afterOther)
	}

	var now string
	if err := conn.QueryRow(ctx, "SELECT fingerprint FROM index_collation").Scan(&now); err != nil || now != fp {
		t.Errorf("fingerprint after rebuild = %q, %v; want %q", now, err, fp)
	}
	if rebuild, err := st.CollationNeedsRebuild(ctx); err != nil || rebuild {
		t.Errorf("CollationNeedsRebuild after the rebuild = %v, %v; want false", rebuild, err)
	}
	// The data is still there and found through the rebuilt index.
	if _, err := st.GetUserByName(ctx, "alice"); err != nil {
		t.Errorf("user after rebuild: %v", err)
	}
}

// A unique index that let a duplicate in while it was out of order cannot
// be rebuilt: migrate fails, names the index and keeps the old
// fingerprint, so the next run tries again.
func TestMigrateRebuildFailureKeepsFingerprint(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()

	const stale = "provider=c collate=en_US.utf8 ctype=en_US.utf8 locale= version= server=18"
	// Simulate the duplicate the out-of-order index let in: drop the
	// constraint, add the duplicate and put a plain unique index back
	// without validating it (an invalid index is rebuilt by REINDEX too).
	for _, q := range []string{
		"UPDATE index_collation SET fingerprint = '" + stale + "'",
		"INSERT INTO users (name, password_hash) VALUES ('bob', 'x')",
		"ALTER TABLE users DROP CONSTRAINT users_name_key",
		"INSERT INTO users (name, password_hash) VALUES ('bob', 'y')",
		"CREATE UNIQUE INDEX CONCURRENTLY users_name_key ON users (name)",
	} {
		if _, err := conn.Exec(ctx, q); err != nil && !strings.HasPrefix(q, "CREATE UNIQUE INDEX CONCURRENTLY") {
			t.Fatalf("%s: %v", q, err)
		}
	}

	_, _, err = st.MigrateAndRebuild(ctx)
	if err == nil || !strings.Contains(err.Error(), "users_name_key") || !strings.Contains(err.Error(), "(name)=(bob) is duplicated") {
		t.Fatalf("migrate with a duplicate: %v; want an error naming users_name_key and the key", err)
	}
	var now string
	if err := conn.QueryRow(ctx, "SELECT fingerprint FROM index_collation").Scan(&now); err != nil || now != stale {
		t.Errorf("fingerprint after a failed rebuild = %q, %v; want the old one", now, err)
	}
	if rebuild, err := st.CollationNeedsRebuild(ctx); err != nil || !rebuild {
		t.Errorf("CollationNeedsRebuild after a failed rebuild = %v, %v; want true", rebuild, err)
	}
}
