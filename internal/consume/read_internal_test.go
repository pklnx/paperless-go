package consume

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pklnx/paperless-go/internal/ingest"
	"github.com/pklnx/paperless-go/internal/store"
)

type staticUsers []*store.User

func (s staticUsers) ListUsers(context.Context) ([]*store.User, error) { return s, nil }

// readingImporter reads the whole body like the real importer and fails
// with a wrapped read error.
type readingImporter struct{ calls []string }

func (r *readingImporter) Import(_ context.Context, req ingest.Request) (ingest.Result, error) {
	r.calls = append(r.calls, req.FileName)
	if _, err := io.Copy(io.Discard, req.Body); err != nil {
		return ingest.Result{}, err
	}
	return ingest.Result{}, errors.New("unexpected: the body was readable")
}

// brokenFile is an opened file whose content cannot be read.
type brokenFile struct{ *os.File }

func (brokenFile) Read([]byte) (int, error) { return 0, errors.New("input/output error") }

// A file that cannot be read stays in place, is logged once and does not
// stop the scan: the next file is still tried.
func TestUnreadableFileDoesNotStopTheScan(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.pdf", "b.pdf"} {
		p := filepath.Join(dir, "anna", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("%PDF-1.7\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	im := &readingImporter{}
	c := New(dir, staticUsers{{ID: 1, Name: "anna"}}, im, Options{Now: func() time.Time { return now }})
	c.open = func(r *os.Root, name string) (file, error) {
		f, err := openInput(r, name)
		if err != nil {
			return nil, err
		}
		return brokenFile{f.(*os.File)}, nil
	}
	ctx := context.Background()
	if _, err := c.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(DefaultSettle)
	for range 2 {
		if _, err := c.Scan(ctx); err != nil {
			t.Fatalf("scan: %v, want the read error handled per file", err)
		}
	}
	if want := []string{"a.pdf", "b.pdf", "a.pdf", "b.pdf"}; len(im.calls) != len(want) {
		t.Fatalf("imports: %q, want %q", im.calls, want)
	}
	for _, name := range []string{"a.pdf", "b.pdf"} {
		if _, err := os.Lstat(filepath.Join(dir, "anna", name)); err != nil {
			t.Errorf("%s: %v, want it left in place", name, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "anna", FailedDir)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("failed/: %v, want none", err)
	}
}

// countingImporter reads the body and gives a fixed result.
type countingImporter struct {
	res   ingest.Result
	calls int
}

func (c *countingImporter) Import(_ context.Context, req ingest.Request) (ingest.Result, error) {
	c.calls++
	if _, err := io.Copy(io.Discard, req.Body); err != nil {
		return ingest.Result{}, err
	}
	return c.res, nil
}

// holdEnv has one user folder with one file and a settled first scan.
func holdEnv(t *testing.T, res ingest.Result) (c *Consumer, im *countingImporter, now *time.Time, userDir string) {
	t.Helper()
	dir := t.TempDir()
	userDir = filepath.Join(dir, "anna")
	if err := os.Mkdir(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "a.pdf"), []byte("%PDF-1.7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	now = &t0
	im = &countingImporter{res: res}
	c = New(dir, staticUsers{{ID: 1, Name: "anna"}}, im, Options{Now: func() time.Time { return *now }})
	return c, im, now, userDir
}

func scanN(t *testing.T, c *Consumer, now *time.Time, n int) {
	t.Helper()
	for range n {
		if _, err := c.Scan(context.Background()); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(DefaultSettle)
	}
}

// A refused file that cannot be moved is not read again at every scan.
func TestRefusedFileThatCannotMoveIsHeld(t *testing.T) {
	c, im, now, userDir := holdEnv(t, ingest.Result{Outcome: ingest.Refused, Refusal: &ingest.RefusedError{Reason: ingest.ReasonUnsupported}})
	// failed/ is a file, so nothing can move there.
	if err := os.WriteFile(filepath.Join(userDir, FailedDir), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	scanN(t, c, now, 5)
	if im.calls != 1 {
		t.Fatalf("%d imports, want 1", im.calls)
	}
	if _, err := os.Lstat(filepath.Join(userDir, "a.pdf")); err != nil {
		t.Fatalf("a.pdf: %v, want it left in place", err)
	}
	*now = now.Add(HoldTime)
	scanN(t, c, now, 1)
	if im.calls != 2 {
		t.Fatalf("%d imports after the hold time, want 2", im.calls)
	}
	// Fixed: it moves at the next try.
	if err := os.Remove(filepath.Join(userDir, FailedDir)); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(HoldTime)
	scanN(t, c, now, 1)
	if _, err := os.Lstat(filepath.Join(userDir, FailedDir, "a.pdf")); err != nil {
		t.Fatalf("failed/a.pdf: %v", err)
	}
}

// A changed file is not held: its new version is read once it settles.
func TestHeldFileChangedIsReadAgain(t *testing.T) {
	c, im, now, userDir := holdEnv(t, ingest.Result{Outcome: ingest.Refused, Refusal: &ingest.RefusedError{Reason: ingest.ReasonUnsupported}})
	if err := os.WriteFile(filepath.Join(userDir, FailedDir), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	scanN(t, c, now, 2)
	if err := os.WriteFile(filepath.Join(userDir, "a.pdf"), []byte("%PDF-1.7\nnew version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scanN(t, c, now, 2)
	if im.calls != 2 {
		t.Fatalf("%d imports, want 2", im.calls)
	}
}

// A consumed file that cannot be deleted (a folder that is not writable)
// is not imported again at every scan.
func TestConsumedFileThatCannotBeDeletedIsHeld(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can delete from a read-only folder")
	}
	c, im, now, userDir := holdEnv(t, ingest.Result{Outcome: ingest.Duplicate})
	if err := os.Chmod(userDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(userDir, 0o755) })
	scanN(t, c, now, 5)
	if im.calls != 1 {
		t.Fatalf("%d imports, want 1", im.calls)
	}
	if err := os.Chmod(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(HoldTime)
	scanN(t, c, now, 1)
	if im.calls != 2 {
		t.Fatalf("%d imports after the hold time, want 2", im.calls)
	}
	if _, err := os.Lstat(filepath.Join(userDir, "a.pdf")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a.pdf: %v, want it deleted", err)
	}
}
