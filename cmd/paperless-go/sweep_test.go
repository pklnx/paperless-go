package main

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/blobstore"
	"github.com/pklnx/paperless-go/internal/config"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
	"github.com/pklnx/paperless-go/internal/sweep"
)

// Every command's store checks the files of the configured data
// directory before a writer names a hash: openApp passes it, and nothing
// else in the program opens a store (must-do B of the plan re-review).
func TestOpenAppChecksTheDataDir(t *testing.T) {
	_, url := storetest.NewWithURL(t)
	dir := t.TempDir()
	t.Setenv(config.EnvDatabaseURL, url)
	t.Setenv(config.EnvDataDir, dir)
	ctx := context.Background()
	a, err := openApp(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	u := storetest.User(t, a.store, "alice", false)
	in := store.NewDocument{Title: "x", Source: store.SourceFile, OriginalMIME: "application/pdf",
		OriginalSHA256: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"} // sha256("test")
	if _, err := a.store.CreateDocument(ctx, store.Scope{UserID: u.ID}, in); !errors.Is(err, store.ErrBlobRemoved) {
		t.Fatalf("a hash whose file is not in the data directory: %v", err)
	}
	blobs, err := blobstore.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := blobs.Put(strings.NewReader("test")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.CreateDocument(ctx, store.Scope{UserID: u.ID}, in); err != nil {
		t.Fatalf("with the file stored: %v", err)
	}
}

// store.Open is called by openApp alone in the program (tests open stores
// through storetest; the demo server in tools/demo, never shipped, opens
// its own).
func TestOnlyOpenAppOpensTheStore(t *testing.T) {
	root := filepath.Join("..", "..")
	var callers []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root &&
			(d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".") || path == filepath.Join(root, "web")) {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok {
				return true
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Open" {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "store" {
						callers = append(callers, filepath.ToSlash(path)+":"+fn.Name.Name)
					}
				}
				return true
			})
			return false
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// tools/demo opens the store of its temporary data directory with
	// blobstore.OpenFiles, as openApp does (tools/demo/main_test.go checks
	// the argument).
	want := []string{
		"../../cmd/paperless-go/commands.go:openApp", "../../internal/store/storetest/storetest.go:NewWithOptions",
		"../../tools/demo/main.go:run",
	}
	if strings.Join(callers, " ") != strings.Join(want, " ") {
		t.Errorf("store.Open called from %v, want %v", callers, want)
	}
}

// migrate binds a fresh data directory, and only a fresh one: one with
// files stays unbound with a hint, one of another installation is left
// alone with a warning.
func TestMigrateBindsAFreshDataDir(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	inst, err := st.Installation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(t.TempDir(), "data")
	t.Setenv(config.EnvDataDir, fresh)
	out, err := run(t, url, "migrate")
	if err != nil || !strings.Contains(out, "bound the data directory "+fresh) {
		t.Fatalf("migrate: %q, %v", out, err)
	}
	if err := sweep.CheckBinding(fresh, inst.ID); err != nil {
		t.Fatalf("after migrate: %v", err)
	}
	if out, err := run(t, url, "migrate"); err != nil || strings.Contains(out, "bound") || strings.Contains(out, "warning") {
		t.Fatalf("migrate again: %q, %v", out, err)
	}

	used := t.TempDir()
	blobs, err := blobstore.New(used)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := blobs.Put(strings.NewReader("a document")); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvDataDir, used)
	out, err = run(t, url, "migrate")
	if err != nil || !strings.Contains(out, "warning: the data directory "+used+" is not bound") || !strings.Contains(out, "sweep --bind") {
		t.Fatalf("migrate with files: %q, %v", out, err)
	}
	if err := sweep.CheckBinding(used, inst.ID); !errors.Is(err, sweep.ErrUnbound) {
		t.Fatalf("migrate bound a data directory with files: %v", err)
	}

	if err := os.WriteFile(filepath.Join(used, sweep.MarkerFile), []byte("00000000-0000-4000-8000-000000000000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, url, "migrate")
	if err != nil || !strings.Contains(out, "belongs to another installation") {
		t.Fatalf("migrate with another installation's marker: %q, %v", out, err)
	}
}

// ./pgo sweep: the walk refuses an unbound data directory; --bind needs
// documents; then a dry run reports and a run queues; files no document
// names are removed after the grace.
func TestSweepCommand(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	dir := t.TempDir()
	t.Setenv(config.EnvDataDir, dir)
	blobs, err := blobstore.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	orphan, _, err := blobs.Put(strings.NewReader("orphan"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := run(t, url, "sweep")
	if err == nil || !strings.Contains(out, "walk: refused: the data directory "+dir+" is not bound") {
		t.Fatalf("sweep unbound: %q, %v", out, err)
	}
	if out, err := run(t, url, "sweep", "--bind"); !errors.Is(err, sweep.ErrEmptyDatabase) {
		t.Fatalf("bind without documents: %q, %v", out, err)
	}
	alice := storetest.User(t, st, "alice", false)
	kept, _, err := blobs.Put(strings.NewReader("kept"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateDocument(context.Background(), store.Scope{UserID: alice.ID}, store.NewDocument{Title: "kept",
		Source: store.SourceFile, OriginalSHA256: kept.SHA256, OriginalMIME: "application/pdf"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"sweep", "--bind", "--dry-run"}, "would bind the data directory"},
		{[]string{"sweep", "--bind"}, "bound the data directory"},
		{[]string{"sweep", "--bind"}, "is already bound"},
		{[]string{"sweep", "--dry-run"}, "walk: files: 2, unreferenced: 1, would queue: 1, stale temporary files: 0"},
		{[]string{"sweep"}, "queue: removed: 0, in use again: 0, failed: 0\nwalk: files: 2, unreferenced: 1, queued: 1, temporary files removed: 0, unexpected entries: 0\n"},
	} {
		out, err := run(t, url, c.args...)
		if err != nil || !strings.Contains(out, c.want) {
			t.Fatalf("%v: %q, %v; want %q", c.args, out, err, c.want)
		}
	}
	if !blobs.Exists(orphan.SHA256) {
		t.Fatal("removed before the grace")
	}
	// An hour later the queue pass removes it.
	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if _, err := conn.Exec(context.Background(), `UPDATE blob_removals SET due_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, url, "sweep")
	if err != nil || !strings.Contains(out, "queue: removed: 1") || blobs.Exists(orphan.SHA256) || !blobs.Exists(kept.SHA256) {
		t.Fatalf("sweep after the grace: %q, %v", out, err)
	}
	// A file no document names that is newer than the newest document
	// change (an older dump restored with a newer data directory): kept,
	// with a warning, unless --force.
	newer, _, err := blobs.Put(strings.NewReader("newer"))
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(dir, newer.Path), future, future); err != nil {
		t.Fatal(err)
	}
	warning := "walk: warning: kept 1 unreferenced files that are newer than the newest document change"
	for _, c := range []struct {
		args       []string
		want       string
		warns      bool
		queuedRows int
	}{
		{[]string{"sweep", "--dry-run"}, "walk: files: 2, unreferenced: 1, would queue: 0,", true, 0},
		{[]string{"sweep", "--dry-run", "--force"}, "walk: files: 2, unreferenced: 1, would queue: 1,", false, 0},
		{[]string{"sweep"}, "walk: files: 2, unreferenced: 1, queued: 0,", true, 0},
		{[]string{"sweep", "--force"}, "walk: files: 2, unreferenced: 1, queued: 1,", false, 1},
	} {
		out, err := run(t, url, c.args...)
		if err != nil || !strings.Contains(out, c.want) || strings.Contains(out, warning) != c.warns {
			t.Fatalf("%v: %q, %v; want %q, warning %v", c.args, out, err, c.want, c.warns)
		}
		var n int
		if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM blob_removals WHERE sha256 = $1`, newer.SHA256).Scan(&n); err != nil || n != c.queuedRows {
			t.Fatalf("%v: %d rows, %v", c.args, n, err)
		}
	}
	// A data directory of another installation: the queue pass removes
	// nothing and the command exits 1.
	marker := filepath.Join(dir, sweep.MarkerFile)
	bound, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("00000000-0000-4000-8000-000000000000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, url, "sweep"); err == nil || !strings.Contains(out, "queue: refused: ") ||
		!strings.Contains(out, "no file was removed") {
		t.Fatalf("sweep with another installation's marker: %q, %v", out, err)
	}
	if err := os.WriteFile(marker, bound, 0o600); err != nil {
		t.Fatal(err)
	}
	// While another sweep holds the sweep lock (store.ExclusiveSweep's
	// class "pgos"), the command does nothing and says so.
	if _, err := conn.Exec(context.Background(), `SELECT pg_advisory_lock(1885826931, 0)`); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, url, "sweep"); !errors.Is(err, errSweepRunning) {
		t.Fatalf("sweep while another runs: %q, %v", out, err)
	}
}

// serve warns at start while the data directory is unbound, and logs an
// error when it belongs to another installation.
func TestWarnBinding(t *testing.T) {
	st := storetest.New(t)
	dir := t.TempDir()
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	warnBinding(context.Background(), st, dir, log)
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "sweep --bind") {
		t.Errorf("unbound: %s", logs.String())
	}
	logs.Reset()
	if err := os.WriteFile(filepath.Join(dir, sweep.MarkerFile), []byte("00000000-0000-4000-8000-000000000000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	warnBinding(context.Background(), st, dir, log)
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "another installation") {
		t.Errorf("another installation: %s", logs.String())
	}
	inst, err := st.Installation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sweep.MarkerFile), []byte(inst.ID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	warnBinding(context.Background(), st, dir, log)
	if logs.Len() != 0 {
		t.Errorf("bound: %s", logs.String())
	}
}

// An import of a file whose stored copy the sweeper is removing fails
// for that file (a transient failure: run it again).
func TestImportFileBeingCleanedUp(t *testing.T) {
	st, url := importEnv(t)
	ctx := context.Background()
	alice := storetest.User(t, st, "alice", false)
	src := t.TempDir()
	writeFile(t, src, "a.pdf", tinyPDF("cleaned up"))
	if out, err := run(t, url, "import", src, "--user", "alice"); err != nil {
		t.Fatalf("import: %q, %v", out, err)
	}
	docs, err := st.ListDocuments(ctx, store.Scope{UserID: alice.ID})
	if err != nil || len(docs) != 1 {
		t.Fatalf("documents: %v, %v", docs, err)
	}
	sc := store.Scope{UserID: alice.ID}
	if err := st.TrashDocument(ctx, sc, docs[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := st.PurgeDocument(ctx, sc, docs[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PurgeDueDocuments(ctx, time.Now(), store.DefaultTrashRetention, 100); err != nil {
		t.Fatal(err)
	}
	marked := time.Now().Add(2 * store.BlobGrace)
	if m, err := st.MarkBlobRemoval(ctx, docs[0].OriginalSHA256, marked); err != nil || m != store.BlobMarked {
		t.Fatalf("mark: %v, %v", m, err)
	}
	out, err := run(t, url, "import", src, "--user", "alice")
	if err == nil || !strings.Contains(out, "a.pdf: failed: the stored copy of this file was being cleaned up") {
		t.Fatalf("import while cleaned up: %q, %v", out, err)
	}
	if err := st.FinishBlobRemoval(ctx, docs[0].OriginalSHA256, marked); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, url, "import", src, "--user", "alice"); err != nil || !strings.Contains(out, "a.pdf: imported") {
		t.Fatalf("import after the clean-up: %q, %v", out, err)
	}
}
