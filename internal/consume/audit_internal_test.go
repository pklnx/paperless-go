package consume

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/paperless-go/internal/ingest"
	"github.com/pklnx/paperless-go/internal/store"
)

// auditStub records the entries written; the next fail writes fail.
type auditStub struct {
	entries []store.AuditEntry
	fail    int
	// cancelled counts writes that got a cancelled context.
	cancelled int
}

func (a *auditStub) write(ctx context.Context, e store.AuditEntry) error {
	if ctx.Err() != nil {
		a.cancelled++
	}
	if a.fail > 0 {
		a.fail--
		return errors.New("audit log down")
	}
	a.entries = append(a.entries, e)
	return nil
}

// scriptImporter reads the body and answers by file name: imported unless
// results or errs name the file. after runs after each answer.
type scriptImporter struct {
	results map[string]ingest.Result
	errs    map[string]error
	after   func(name string)
	calls   []string
}

func (s *scriptImporter) Import(_ context.Context, req ingest.Request) (ingest.Result, error) {
	s.calls = append(s.calls, req.FileName)
	if s.after != nil {
		defer s.after(req.FileName)
	}
	if _, err := io.Copy(io.Discard, req.Body); err != nil {
		return ingest.Result{}, err
	}
	if err := s.errs[req.FileName]; err != nil {
		return ingest.Result{}, err
	}
	if r, ok := s.results[req.FileName]; ok {
		return r, nil
	}
	return ingest.Result{Outcome: ingest.Imported, Document: &store.Document{ID: "doc-" + req.FileName}}, nil
}

var refused = ingest.Result{Outcome: ingest.Refused, Refusal: &ingest.RefusedError{Reason: ingest.ReasonUnsupported}}

type auditEnv struct {
	t     *testing.T
	dir   string
	now   time.Time
	im    *scriptImporter
	audit *auditStub
	logs  *bytes.Buffer
	c     *Consumer
}

var (
	alice = &store.User{ID: 1, Name: "alice"}
	bob   = &store.User{ID: 2, Name: "bob"}
)

func newAuditEnv(t *testing.T) *auditEnv {
	t.Helper()
	e := &auditEnv{
		t: t, dir: t.TempDir(), now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC),
		im: &scriptImporter{}, audit: &auditStub{}, logs: &bytes.Buffer{},
	}
	e.c = New(e.dir, staticUsers{alice, bob}, e.im, Options{
		Now: func() time.Time { return e.now }, Audit: e.audit.write,
		Log: slog.New(slog.NewTextHandler(e.logs, nil)),
	})
	return e
}

func (e *auditEnv) write(rel, content string) string {
	e.t.Helper()
	p := filepath.Join(e.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *auditEnv) scan(ctx context.Context) error {
	e.t.Helper()
	_, err := e.c.Scan(ctx)
	return err
}

func (e *auditEnv) mustScan() {
	e.t.Helper()
	if err := e.scan(context.Background()); err != nil {
		e.t.Fatalf("scan: %v", err)
	}
}

// settle scans (the files wait), moves the clock past the settle time and
// scans again.
func (e *auditEnv) settle() {
	e.t.Helper()
	e.mustScan()
	e.now = e.now.Add(DefaultSettle)
	e.mustScan()
}

// wantEntries checks the entries written so far.
func (e *auditEnv) wantEntries(want ...string) {
	e.t.Helper()
	var got []string
	for _, a := range e.audit.entries {
		got = append(got, describe(e.t, a))
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		e.t.Fatalf("audit entries:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// describe is an entry's fields as one line; it fails the test for an
// entry a system entry must not have.
func describe(t *testing.T, a store.AuditEntry) string {
	t.Helper()
	if a.Actor != store.ActorSystem || a.UserID != nil || a.UserName != "" || a.APIKeyID != nil || a.APIKeyName != "" ||
		a.ClientIP.IsValid() || a.UserAgent != "" || a.TargetType != store.TargetUser {
		t.Errorf("not a system entry about a user: %+v", a)
	}
	var d []string
	for _, k := range slices.Sorted(maps.Keys(a.Details)) {
		d = append(d, fmt.Sprintf("%s=%v", k, a.Details[k]))
	}
	return fmt.Sprintf("%s %s/%s %s", a.Action, a.TargetID, a.TargetName, strings.Join(d, " "))
}

// One entry per user and scan that changed something, with the counts.
func TestConsumeAuditOneEntryPerUserAndScan(t *testing.T) {
	e := newAuditEnv(t)
	e.im.results = map[string]ingest.Result{"b.pdf": duplicate, "notes.txt": refused}
	e.write("alice/a.pdf", "a")
	e.write("alice/b.pdf", "b")
	e.write("bob/c.pdf", "c")
	e.write("bob/notes.txt", "text")

	e.mustScan() // everything waits
	e.wantEntries()
	e.now = e.now.Add(DefaultSettle)
	e.mustScan()
	e.wantEntries(
		"document.consume 1/alice duplicates=1 failed=0 imported=1",
		"document.consume 2/bob duplicates=0 failed=1 imported=1",
	)
	// Empty folders write nothing.
	e.now = e.now.Add(DefaultSettle)
	e.mustScan()
	if len(e.audit.entries) != 2 {
		t.Fatalf("%d entries after an empty scan, want 2", len(e.audit.entries))
	}
}

// A file that keeps changing only waits and writes nothing.
func TestConsumeAuditWaitingWritesNothing(t *testing.T) {
	e := newAuditEnv(t)
	e.write("alice/a.pdf", "a")
	for range 3 {
		e.mustScan()
		if err := os.WriteFile(filepath.Join(e.dir, "alice", "a.pdf"), []byte("a"+e.now.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		e.now = e.now.Add(DefaultSettle)
	}
	e.wantEntries()
}

// A transient error stops the folder's pass: the entry records the import
// before it and that the pass was interrupted; the other file stays.
func TestConsumeAuditTransientError(t *testing.T) {
	e := newAuditEnv(t)
	e.im.errs = map[string]error{"b.pdf": errors.New("database is down")}
	e.write("alice/a.pdf", "a")
	b := e.write("alice/b.pdf", "b")
	e.mustScan()
	e.now = e.now.Add(DefaultSettle)
	if err := e.scan(context.Background()); err == nil {
		t.Fatal("scan: no error, want the transient one")
	}
	e.wantEntries("document.consume 1/alice duplicates=0 failed=0 imported=1 interrupted=true")
	if _, err := os.Lstat(b); err != nil {
		t.Fatalf("b.pdf: %v, want it left in place", err)
	}
}

// A scan cancelled after the first file (shutdown) still writes the entry
// for it, with a context that is not cancelled.
func TestConsumeAuditCancelledScan(t *testing.T) {
	e := newAuditEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.im.after = func(string) { cancel() }
	e.write("alice/a.pdf", "a")
	b := e.write("alice/b.pdf", "b")
	e.mustScan()
	e.now = e.now.Add(DefaultSettle)
	if err := e.scan(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("scan: %v, want context.Canceled", err)
	}
	e.wantEntries("document.consume 1/alice duplicates=0 failed=0 imported=1 interrupted=true")
	if e.audit.cancelled != 0 {
		t.Fatalf("%d writes with a cancelled context", e.audit.cancelled)
	}
	if _, err := os.Lstat(b); err != nil {
		t.Fatalf("b.pdf: %v, want it left in place", err)
	}
}

// A user removed while the scan runs: what was consumed before is still
// recorded, under the user's name.
func TestConsumeAuditUnknownUser(t *testing.T) {
	e := newAuditEnv(t)
	e.im.errs = map[string]error{"b.pdf": fmt.Errorf("ingest: create document: %w", store.ErrUnknownUser)}
	e.write("alice/a.pdf", "a")
	e.write("alice/b.pdf", "b")
	e.settle()
	e.wantEntries("document.consume 1/alice duplicates=0 failed=0 imported=1")
}

// An entry that cannot be written is logged once and kept: its counts go
// into the next entry, which the following scans write even if they
// change nothing themselves.
func TestConsumeAuditWriteFailsThenWorks(t *testing.T) {
	e := newAuditEnv(t)
	e.audit.fail = 2
	a := e.write("alice/a.pdf", "a")
	e.settle()
	if _, err := os.Lstat(a); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a.pdf: %v, want it imported and deleted", err)
	}
	e.wantEntries()
	// b waits at this scan, and the kept entry fails again.
	e.write("alice/b.pdf", "b")
	e.mustScan()
	e.wantEntries()
	if n := strings.Count(e.logs.String(), "audit log not written"); n != 1 {
		t.Fatalf("%d errors logged, want 1:\n%s", n, e.logs)
	}
	e.now = e.now.Add(DefaultSettle)
	e.mustScan()
	e.wantEntries("document.consume 1/alice duplicates=0 failed=0 imported=2")
	if !strings.Contains(e.logs.String(), "audit log written again") {
		t.Errorf("no recovery logged:\n%s", e.logs)
	}

	// Kept counts are written by a scan of an empty folder too.
	e.audit.fail = 1
	e.write("bob/c.pdf", "c")
	e.settle()
	e.mustScan()
	e.wantEntries(
		"document.consume 1/alice duplicates=0 failed=0 imported=2",
		"document.consume 2/bob duplicates=0 failed=0 imported=1",
	)
	if len(e.c.pending) != 0 {
		t.Fatalf("pending: %v", e.c.pending)
	}
}

// Outcomes are counted, not attempts: a duplicate that cannot be deleted
// writes no entry, also when it is tried again after HoldTime, and an
// imported file that cannot be deleted is counted once.
func TestConsumeAuditCountsOutcomes(t *testing.T) {
	e := newAuditEnv(t)
	e.im.results = map[string]ingest.Result{"dup.pdf": duplicate}
	e.c.removeFile = func(*os.Root, string) error { return fs.ErrPermission }
	e.write("alice/dup.pdf", "dup")
	e.write("bob/new.pdf", "new")
	e.settle()
	e.wantEntries("document.consume 2/bob duplicates=0 failed=0 imported=1")

	// After HoldTime both are tried again, now both as duplicates.
	e.im.results["new.pdf"] = duplicate
	e.now = e.now.Add(HoldTime)
	e.mustScan()
	if len(e.im.calls) != 4 {
		t.Fatalf("imports %q, want each file twice", e.im.calls)
	}
	e.wantEntries("document.consume 2/bob duplicates=0 failed=0 imported=1")

	// A refused file that cannot be moved is not counted either.
	e.im.results["notes.txt"] = refused
	e.write("alice/failed", "a file, not a folder")
	e.write("alice/notes.txt", "text")
	e.settle()
	e.now = e.now.Add(HoldTime)
	e.mustScan()
	e.wantEntries("document.consume 2/bob duplicates=0 failed=0 imported=1")
}

// Without Audit nothing is kept or written.
func TestConsumeAuditNil(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "alice"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alice", "a.pdf"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	c := New(dir, staticUsers{alice}, &scriptImporter{}, Options{Now: func() time.Time { return now }})
	for range 2 {
		if _, err := c.Scan(context.Background()); err != nil {
			t.Fatal(err)
		}
		now = now.Add(DefaultSettle)
	}
	if _, err := os.Lstat(filepath.Join(dir, "alice", "a.pdf")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a.pdf: %v, want it imported", err)
	}
	if len(c.pending) != 0 {
		t.Fatalf("pending without Audit: %v", c.pending)
	}
}
