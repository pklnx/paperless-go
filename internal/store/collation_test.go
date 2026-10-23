package store_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"maps"
	neturl "net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/store"
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

	if err := st.CheckServerVersion(ctx); err != nil {
		t.Errorf("CheckServerVersion on the test server: %v", err)
	}
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
	r, err := st.MigrateAndRebuild(ctx)
	applied, rebuilt := r.Applied, r.Rebuilt
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

	textIdx := []string{"users_name_key", "tags_owner_parent_name_key", "correspondents_owner_name_key", "documents_owner_original_sha256_key",
		"search_vocabulary_pkey", "search_vocabulary_trgm_idx"}
	otherIdx := []string{"users_pkey", "documents_search_idx", "documents_owner_date_idx"}
	beforeText := relfilenodes(t, conn, textIdx...)
	beforeOther := relfilenodes(t, conn, otherIdx...)

	r, err := st.MigrateAndRebuild(ctx)
	applied, rebuilt := r.Applied, r.Rebuilt
	if err != nil || len(applied) != 0 || len(r.Warnings) != 0 {
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

// Unique indexes that let duplicates in while they were out of order
// cannot be rebuilt: migrate still rebuilds the other indexes, then fails
// listing every duplicated key of every such index, and keeps the old
// fingerprint, so the next run tries again.
func TestMigrateRebuildFailureListsDuplicates(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	owner := storetest.User(t, st, "owner", false)

	const stale = "provider=c collate=en_US.utf8 ctype=en_US.utf8 locale= version= server=18"
	// Simulate the duplicates the out-of-order indexes let in: drop the
	// unique index, add the duplicates and create it again, which fails
	// and leaves it invalid (REINDEX rebuilds invalid indexes too).
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec("UPDATE index_collation SET fingerprint = '" + stale + "'")
	exec("ALTER TABLE users DROP CONSTRAINT users_name_key")
	exec("INSERT INTO users (name, password_hash) VALUES ('bob', 'x'), ('bob', 'y'), ('carol', 'x'), ('carol', 'y'), ('dave', 'x')")
	exec("DROP INDEX tags_owner_parent_name_key")
	exec("INSERT INTO tags (owner_id, name) VALUES ($1, 'Rechnung'), ($1, 'rechnung')", owner.ID)
	for _, q := range []string{
		"CREATE UNIQUE INDEX CONCURRENTLY users_name_key ON users (name)",
		"CREATE UNIQUE INDEX CONCURRENTLY tags_owner_parent_name_key ON tags (owner_id, parent_id, lower(name)) NULLS NOT DISTINCT",
	} {
		if _, err := conn.Exec(ctx, q); err == nil {
			t.Fatalf("%s: no duplicate", q)
		}
	}
	before := relfilenodes(t, conn, "correspondents_owner_name_key")

	r, err := st.MigrateAndRebuild(ctx)
	if err == nil {
		t.Fatal("migrate with duplicates: no error")
	}
	for _, want := range []string{
		`"public"."users_name_key"`, "(name)=(bob)", "(name)=(carol)",
		`"public"."tags_owner_parent_name_key"`, fmt.Sprintf("(owner_id, parent_id, lower(name))=(%d,,rechnung)", owner.ID),
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "dave") {
		t.Errorf("error %q names a key that is not duplicated", err)
	}
	if !slices.Contains(r.Rebuilt, `"public"."correspondents_owner_name_key"`) ||
		relfilenodes(t, conn, "correspondents_owner_name_key")["correspondents_owner_name_key"] == before["correspondents_owner_name_key"] {
		t.Errorf("the other indexes were not rebuilt: %v", r.Rebuilt)
	}
	var now string
	if err := conn.QueryRow(ctx, "SELECT fingerprint FROM index_collation").Scan(&now); err != nil || now != stale {
		t.Errorf("fingerprint after a failed rebuild = %q, %v; want the old one", now, err)
	}
	if rebuild, err := st.CollationNeedsRebuild(ctx); err != nil || !rebuild {
		t.Errorf("CollationNeedsRebuild after a failed rebuild = %v, %v; want true", rebuild, err)
	}
}

// A table another session holds locked (a running web server, a long
// transaction) makes the rebuild fail after the lock timeout with advice,
// instead of waiting and queueing every query on the table behind it.
func TestMigrateRebuildLockTimeout(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	store.SetReindexLockTimeout(t, 50*time.Millisecond)
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "UPDATE index_collation SET fingerprint = 'provider=c collate=en_US.utf8 ctype=en_US.utf8 locale= version= server=18'"); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "LOCK TABLE users IN ROW EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}

	_, err = st.MigrateAndRebuild(ctx)
	if err == nil || !strings.Contains(err.Error(), `"public"."users_name_key"`) ||
		!strings.Contains(err.Error(), "stop the web server") {
		t.Fatalf("migrate with a locked table: %v", err)
	}
	if rebuild, err := st.CollationNeedsRebuild(ctx); err != nil || !rebuild {
		t.Errorf("CollationNeedsRebuild after a timeout = %v, %v; want true", rebuild, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MigrateAndRebuild(ctx); err != nil {
		t.Errorf("migrate after the lock is gone: %v", err)
	}
}

// On your own server the database may belong to an administrator, so the
// app user cannot record the new collation version. The indexes are
// rebuilt and the fingerprint stored anyway; migrate warns with the
// statement to run, and serve is not blocked.
func TestMigrateRefreshNotOwner(t *testing.T) {
	_, url := storetest.NewWithURL(t)
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()

	// A role that owns the schema objects but not the database.
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	role := "paperless_app_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, "CREATE ROLE "+role+" LOGIN PASSWORD 'app'"); err != nil {
		t.Skipf("cannot create a role: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP OWNED BY "+role)
		_, _ = admin.Exec(ctx, "DROP ROLE "+role)
	})
	var dbName string
	if err := admin.QueryRow(ctx, "SELECT current_database()").Scan(&dbName); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"GRANT CONNECT, CREATE, TEMPORARY ON DATABASE " + pgx.Identifier{dbName}.Sanitize() + " TO " + role,
		"GRANT ALL ON SCHEMA public TO " + role,
		`DO $$ DECLARE r record; BEGIN
		   FOR r IN SELECT c.oid::regclass AS rel FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		            WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p', 'v', 'm') LOOP
		     EXECUTE format('ALTER TABLE %s OWNER TO ` + role + `', r.rel);
		   END LOOP; END $$`,
		// A recorded version that differs from the provider's, as after
		// a glibc update, and a fingerprint from before it.
		"UPDATE pg_database SET datcollversion = '0.1' WHERE datname = current_database()",
		"UPDATE index_collation SET fingerprint = 'stale'",
	} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var actual string
	if err := admin.QueryRow(ctx, "SELECT coalesce(pg_database_collation_actual_version(oid), '') FROM pg_database WHERE datname = current_database()").Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual == "" {
		t.Skip("the database locale has no collation version")
	}

	u, err := neturl.Parse(url)
	if err != nil {
		t.Fatal(err)
	}
	u.User = neturl.UserPassword(role, "app")
	app, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	r, err := app.MigrateAndRebuild(ctx)
	if err != nil {
		t.Fatalf("migrate as a non-owner: %v", err)
	}
	if !slices.Contains(r.Rebuilt, `"public"."users_name_key"`) {
		t.Errorf("rebuilt %v", r.Rebuilt)
	}
	want := "ALTER DATABASE " + pgx.Identifier{dbName}.Sanitize() + " REFRESH COLLATION VERSION;"
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], want) {
		t.Errorf("warnings %q; want one naming %q", r.Warnings, want)
	}
	if rebuild, err := app.CollationNeedsRebuild(ctx); err != nil || rebuild {
		t.Errorf("CollationNeedsRebuild after the rebuild = %v, %v; want false", rebuild, err)
	}
	// The next run rebuilds nothing and still reminds.
	r, err = app.MigrateAndRebuild(ctx)
	if err != nil || len(r.Rebuilt) != 0 || len(r.Warnings) != 1 {
		t.Errorf("migrate again: %+v, %v", r, err)
	}
}
