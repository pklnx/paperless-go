package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/blobstore"
	"github.com/pklnx/paperless-go/internal/config"
	"github.com/pklnx/paperless-go/internal/ingest"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// Files are generated here; no real documents.

func tinyPDF(tag string) []byte { return []byte("%PDF-1.4\n% " + tag + "\n%%EOF\n") }

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 3, 3))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func writeFile(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func importEnv(t *testing.T) (*store.Store, string) {
	t.Helper()
	st, url := storetest.NewWithURL(t)
	t.Setenv(config.EnvDataDir, t.TempDir())
	t.Setenv(config.EnvOperator, "patrick")
	return st, url
}

func docCount(t *testing.T, st *store.Store, owner int64) int {
	t.Helper()
	docs, err := st.ListDocuments(context.Background(), store.Scope{UserID: owner})
	if err != nil {
		t.Fatal(err)
	}
	return len(docs)
}

func TestImportCommand(t *testing.T) {
	st, url := importEnv(t)
	alice := storetest.User(t, st, "alice", false)
	src := t.TempDir()
	writeFile(t, src, "a.pdf", tinyPDF("a"))
	writeFile(t, src, "sub/deep/b.pdf", tinyPDF("b"))
	writeFile(t, src, "scan.dat", tinyPNG(t)) // type from content, not the name
	writeFile(t, src, "notes.txt", []byte("just text"))
	writeFile(t, src, "sub/copy-of-a.pdf", tinyPDF("a")) // same content as a.pdf
	outside := t.TempDir()
	writeFile(t, outside, "secret.pdf", tinyPDF("secret"))
	if err := os.Symlink(filepath.Join(outside, "secret.pdf"), filepath.Join(src, "link.pdf")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(src, "linkdir")); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, url, "import", src)
	var ee *exitError
	if !errors.As(err, &ee) || ee.exitCode() != 1 {
		t.Fatalf("want exit status 1, got %v", err)
	}
	for _, want := range []string{
		"a.pdf: imported\n",
		"scan.dat: imported\n",
		"sub/deep/b.pdf: imported\n",
		"sub/copy-of-a.pdf: duplicate of \"a\"\n",
		"notes.txt: failed: unsupported file type",
		"imported: 3, duplicates: 1, restored: 0, failed: 1\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "link") || strings.Contains(out, "secret") {
		t.Errorf("symlinks must be skipped:\n%s", out)
	}
	if n := docCount(t, st, alice.ID); n != 3 {
		t.Errorf("documents = %d, want 3", n)
	}
	// Sources are untouched.
	if _, err := os.Stat(filepath.Join(src, "a.pdf")); err != nil {
		t.Error(err)
	}

	// Again: only duplicates, plus the file that still fails.
	out, err = run(t, url, "import", src, "--user", "ALICE")
	if !errors.As(err, &ee) {
		t.Fatalf("second run: %v", err)
	}
	if !strings.Contains(out, "imported: 0, duplicates: 4, restored: 0, failed: 1\n") || strings.Contains(out, ": imported") {
		t.Errorf("second run:\n%s", out)
	}
	if n := docCount(t, st, alice.ID); n != 3 {
		t.Errorf("documents after second run = %d, want 3", n)
	}

	// The audit log has one summary entry for the first run only.
	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var rows int
	if err := conn.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE action = 'document.import'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("document.import entries = %d, want 1", rows)
	}
}

func TestImportSingleFileAndCleanRun(t *testing.T) {
	st, url := importEnv(t)
	alice := storetest.User(t, st, "alice", false)
	src := t.TempDir()
	writeFile(t, src, "one.pdf", tinyPDF("one"))
	out, err := run(t, url, "import", filepath.Join(src, "one.pdf"))
	if err != nil || !strings.Contains(out, "one.pdf: imported\n") || !strings.Contains(out, "imported: 1, duplicates: 0, restored: 0, failed: 0\n") {
		t.Fatalf("%q, %v", out, err)
	}
	if n := docCount(t, st, alice.ID); n != 1 {
		t.Errorf("documents = %d", n)
	}
}

func TestImportUserRules(t *testing.T) {
	st, url := importEnv(t)
	src := t.TempDir()
	writeFile(t, src, "a.pdf", tinyPDF("a"))

	if _, err := run(t, url, "import", src); err == nil || !strings.Contains(err.Error(), "no users") {
		t.Errorf("no users: %v", err)
	}
	alice := storetest.User(t, st, "alice", false)
	if out, err := run(t, url, "import", src); err != nil || !strings.Contains(out, "a.pdf: imported") {
		t.Fatalf("single user is the default: %q, %v", out, err)
	}
	bob := storetest.User(t, st, "bob", false)
	if _, err := run(t, url, "import", src); err == nil || !strings.Contains(err.Error(), "--user") {
		t.Errorf("several users need --user: %v", err)
	}
	if _, err := run(t, url, "import", src, "--user", "carol"); err == nil || !strings.Contains(err.Error(), `unknown user "carol"`) {
		t.Errorf("unknown user: %v", err)
	}
	// The same file is new for another owner, even for an admin.
	if out, err := run(t, url, "import", src, "--user", "bob"); err != nil || !strings.Contains(out, "a.pdf: imported") {
		t.Fatalf("bob: %q, %v", out, err)
	}
	if err := st.SetUserLocked(context.Background(), bob.ID, true); err != nil {
		t.Fatal(err)
	}
	writeFile(t, src, "new.pdf", tinyPDF("new"))
	if _, err := run(t, url, "import", src, "--user", "bob"); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Errorf("locked user: %v", err)
	}
	if n := docCount(t, st, bob.ID); n != 1 {
		t.Errorf("locked user's documents = %d, want 1", n)
	}
	if n := docCount(t, st, alice.ID); n != 1 {
		t.Errorf("alice's documents = %d, want 1", n)
	}
}

func TestImportDryRunStoresNothing(t *testing.T) {
	st, url := importEnv(t)
	alice := storetest.User(t, st, "alice", false)
	src := t.TempDir()
	writeFile(t, src, "a.pdf", tinyPDF("a"))
	writeFile(t, src, "b.png", tinyPNG(t))
	writeFile(t, src, "bad.txt", []byte("text"))
	out, err := run(t, url, "import", src, "--dry-run")
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("dry run with a bad file exits 1: %v", err)
	}
	for _, want := range []string{"a.pdf: would import\n", "b.png: would import\n", "bad.txt: failed: unsupported file type", "would import: 2, duplicates: 0, would restore: 0, failed: 1\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if n := docCount(t, st, alice.ID); n != 0 {
		t.Errorf("documents = %d, want 0", n)
	}
	if entries, _ := os.ReadDir(filepath.Join(os.Getenv(config.EnvDataDir), "documents")); len(entries) != 0 {
		t.Errorf("dry run stored files: %v", entries)
	}

	// After a real import, the dry run reports duplicates.
	if err := os.Remove(filepath.Join(src, "bad.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, url, "import", src); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, url, "import", src, "--dry-run")
	if err != nil || !strings.Contains(out, "a.pdf: duplicate of \"a\"\n") || !strings.Contains(out, "would import: 0, duplicates: 2, would restore: 0, failed: 0") {
		t.Errorf("%q, %v", out, err)
	}
}

func TestImportRefusesBadInput(t *testing.T) {
	_, url := importEnv(t)
	if _, err := run(t, url, "import", filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("a missing path must fail")
	}
	if _, err := run(t, url, "import"); err == nil {
		t.Error("PATH is required")
	}
}

func TestImportSizeLimit(t *testing.T) {
	st, url := importEnv(t)
	alice := storetest.User(t, st, "alice", false)
	t.Setenv(config.EnvMaxUploadSize, "64")
	src := t.TempDir()
	ok := append(tinyPDF("ok"), bytes.Repeat([]byte{' '}, 64-len(tinyPDF("ok")))...)
	writeFile(t, src, "ok.pdf", ok)
	writeFile(t, src, "big.pdf", append(ok, ' '))
	for _, args := range [][]string{{"import", src}, {"import", src, "--dry-run"}} {
		out, _ := run(t, url, args...)
		if !strings.Contains(out, "big.pdf: failed: file too large") || strings.Contains(out, "ok.pdf: failed") {
			t.Errorf("%v:\n%s", args, out)
		}
	}
	if n := docCount(t, st, alice.ID); n != 1 {
		t.Errorf("documents = %d, want 1", n)
	}
}

// cancelAfter cancels the run once its output contains n "imported" lines.
type cancelAfter struct {
	bytes.Buffer
	n      int
	cancel context.CancelFunc
}

func (w *cancelAfter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	if strings.Count(w.String(), ": imported\n") >= w.n {
		w.cancel()
	}
	return n, err
}

// An interrupted run still records the documents it imported.
func TestImportInterruptedKeepsAuditEntry(t *testing.T) {
	st, url := importEnv(t)
	alice := storetest.User(t, st, "alice", false)
	dir := t.TempDir()
	writeFile(t, dir, "a.pdf", tinyPDF("a"))
	writeFile(t, dir, "b.pdf", tinyPDF("b"))
	writeFile(t, dir, "c.pdf", tinyPDF("c"))

	t.Setenv(config.EnvDatabaseURL, url)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &cancelAfter{n: 1, cancel: cancel}
	cmd := newRootCmd()
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"import", dir})
	if err := cmd.ExecuteContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted run: %v\n%s", err, out.String())
	}
	if n := docCount(t, st, alice.ID); n != 1 {
		t.Fatalf("%d documents after the interrupt, want 1", n)
	}
	var found bool
	for _, r := range auditLog(t, st) {
		if r.Action == "document.import" {
			found = true
			var d map[string]any
			if err := json.Unmarshal(r.Details, &d); err != nil || d["interrupted"] != true || d["imported"] != float64(1) {
				t.Errorf("details %s", r.Details)
			}
		}
	}
	if !found {
		t.Fatal("no document.import entry for the interrupted run")
	}
}

// A file whose document is in the trash restores it, audited with via
// import, and the dry run says it would; one whose document is due for
// purge (here through PAPERLESS_GO_TRASH_RETENTION=0, which every command
// reads) fails, to be imported again later, and restores nothing.
func TestImportRestoresTrashedDocument(t *testing.T) {
	st, url := importEnv(t)
	ctx := context.Background()
	alice := storetest.User(t, st, "alice", false)
	src := t.TempDir()
	writeFile(t, src, "a.pdf", tinyPDF("a"))
	if _, err := run(t, url, "import", src); err != nil {
		t.Fatal(err)
	}
	docs, err := st.ListDocuments(ctx, store.Scope{UserID: alice.ID})
	if err != nil || len(docs) != 1 {
		t.Fatalf("documents: %v, %v", docs, err)
	}
	id := docs[0].ID
	if err := st.TrashDocument(ctx, store.Scope{UserID: alice.ID}, id); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, url, "import", src, "--dry-run")
	if err != nil || !strings.Contains(out, "a.pdf: would restore \"a\" from the trash\n") ||
		!strings.Contains(out, "would import: 0, duplicates: 0, would restore: 1, failed: 0\n") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if d, err := st.GetDocument(ctx, store.Scope{UserID: alice.ID}, id); err != nil || d.DeletedAt == nil {
		t.Fatalf("restored by the dry run: %+v, %v", d, err)
	}

	// In the trash with retention 0: due at once, not restored.
	t.Setenv(config.EnvTrashRetention, "0")
	out, err = run(t, url, "import", src)
	var ee *exitError
	if !errors.As(err, &ee) || !strings.Contains(out, "a.pdf: failed: the existing document is being removed permanently") {
		t.Fatalf("due: %v\n%s", err, out)
	}
	t.Setenv(config.EnvTrashRetention, "")
	out, err = run(t, url, "import", src)
	if err != nil || !strings.Contains(out, "a.pdf: restored \"a\" from the trash\n") ||
		!strings.Contains(out, "imported: 0, duplicates: 0, restored: 1, failed: 0\n") {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	if d, err := st.GetDocument(ctx, store.Scope{UserID: alice.ID}, id); err != nil || d.DeletedAt != nil {
		t.Errorf("after the import: %+v, %v", d, err)
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var actor, target string
	var details, summary map[string]any
	if err := conn.QueryRow(ctx, `SELECT actor, target_id, details FROM audit_log WHERE action = 'document.restore'`).Scan(&actor, &target, &details); err != nil {
		t.Fatal(err)
	}
	if actor != store.ActorCLI || target != id || details["via"] != "import" || details["operator"] != "patrick" {
		t.Errorf("restore entry: %s %s %v", actor, target, details)
	}
	if err := conn.QueryRow(ctx, `SELECT details FROM audit_log WHERE action = 'document.import' ORDER BY id DESC LIMIT 1`).Scan(&summary); err != nil {
		t.Fatal(err)
	}
	if summary["restored"] != float64(1) || summary["imported"] != float64(0) {
		t.Errorf("summary: %v", summary)
	}
}

// A document that becomes due for purge between the duplicate check and
// the restore (the store's clock passes the retention between the two
// decisions) fails the file, which a later run imports as new; it is not
// counted as restored, and nothing is restored.
func TestImportRestoreOfDocumentDueMeanwhile(t *testing.T) {
	st, _ := importEnv(t)
	ctx := context.Background()
	alice := storetest.User(t, st, "alice", false)
	var calls atomic.Int32
	jumpAfter := int32(1 << 30)
	st.Now = func() time.Time {
		if calls.Add(1) > jumpAfter {
			return time.Now().Add(store.DefaultTrashRetention + time.Hour)
		}
		return time.Now()
	}
	blobs, err := blobstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	im, err := ingest.New(st, blobs, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	m := &importer{
		ctx: ctx, out: &out, st: st, im: im, owner: alice, maxSize: 1 << 20,
		entry: func(action string) *store.AuditEntry { return &store.AuditEntry{Actor: store.ActorCLI, Action: action} },
	}
	src := t.TempDir()
	writeFile(t, src, "a.pdf", tinyPDF("a"))
	m.file(filepath.Join(src, "a.pdf"), "a.pdf", "a.pdf")
	docs, err := st.ListDocuments(ctx, store.Scope{UserID: alice.ID})
	if err != nil || len(docs) != 1 {
		t.Fatalf("documents: %v, %v", docs, err)
	}
	if err := st.TrashDocument(ctx, store.Scope{UserID: alice.ID}, docs[0].ID); err != nil {
		t.Fatal(err)
	}

	// The duplicate check is the first decision; the restore comes after.
	m.tally, out = importTally{}, bytes.Buffer{}
	calls.Store(0)
	jumpAfter = 1
	m.file(filepath.Join(src, "a.pdf"), "a.pdf", "a.pdf")
	jumpAfter = 1 << 30
	if m.tally != (importTally{failed: 1}) || !strings.Contains(out.String(), "a.pdf: failed: the existing document is being removed permanently") {
		t.Errorf("tally %+v, output:\n%s", m.tally, out.String())
	}
	if d, err := st.GetDocument(ctx, store.Scope{UserID: alice.ID}, docs[0].ID); err != nil || d.DeletedAt == nil {
		t.Errorf("document: %+v, %v", d, err)
	}
}
