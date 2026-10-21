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
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/config"
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
		"imported: 3, duplicates: 1, failed: 1\n",
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
	if !strings.Contains(out, "imported: 0, duplicates: 4, failed: 1\n") || strings.Contains(out, ": imported") {
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
	if err != nil || !strings.Contains(out, "one.pdf: imported\n") || !strings.Contains(out, "imported: 1, duplicates: 0, failed: 0\n") {
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
	for _, want := range []string{"a.pdf: would import\n", "b.png: would import\n", "bad.txt: failed: unsupported file type", "would import: 2, duplicates: 0, failed: 1\n"} {
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
	if err != nil || !strings.Contains(out, "a.pdf: duplicate of \"a\"\n") || !strings.Contains(out, "would import: 0, duplicates: 2, failed: 0") {
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
