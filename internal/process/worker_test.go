package process

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pklnx/paperless-go/internal/blobstore"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// clock is a test clock that moves only when told.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type env struct {
	st     *store.Store
	blobs  *blobstore.Store
	w      *Worker
	clock  *clock
	runner *fakeRunner
	ticks  chan time.Time // fires the heartbeat
	owner  store.Scope
	log    *bytes.Buffer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st := storetest.New(t)
	data := t.TempDir()
	blobs, err := blobstore.New(data)
	if err != nil {
		t.Fatal(err)
	}
	var logBuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&syncWriter{w: &logBuf}, nil))
	wd, err := OpenWorkDir(data, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wd.Close() })
	e := &env{
		st: st, blobs: blobs, clock: &clock{now: time.Now().Truncate(time.Microsecond)},
		runner: &fakeRunner{handle: poppler(pdf{pages: []string{longText}})},
		ticks:  make(chan time.Time), log: &logBuf,
		owner: store.Scope{UserID: storetest.User(t, st, "alice", false).ID},
	}
	e.w = &Worker{
		Store: st, Blobs: blobs, Pipeline: &Pipeline{Runner: e.runner}, WorkDir: wd, Log: log,
		Workers: 1, Timeout: time.Hour, Now: e.clock.Now,
		after: func(time.Duration) <-chan time.Time { return e.ticks },
	}
	return e
}

// syncWriter serializes writes of the worker's goroutines to the log.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// doc stores content as a PDF original and creates its document, which
// queues it.
func (e *env) doc(t *testing.T, content string) *store.Document {
	t.Helper()
	return e.docAs(t, content, mimePDF)
}

// docAs is doc with an original of the given MIME type.
func (e *env) docAs(t *testing.T, content, mime string) *store.Document {
	t.Helper()
	title := content
	if mime != mimePDF {
		title = "image"
	}
	b, _, err := e.blobs.Put(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.st.CreateDocument(context.Background(), e.owner, store.NewDocument{
		Title: title, Source: store.SourceFile, OriginalSHA256: b.SHA256, OriginalSize: b.Size,
		OriginalMIME: mime, IsInbox: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (e *env) job(t *testing.T, docID string) *store.Job {
	t.Helper()
	jobs, err := e.st.DocumentJobs(context.Background(), docID)
	if err != nil || len(jobs) == 0 {
		t.Fatalf("jobs of %s: %v, %v", docID, jobs, err)
	}
	return jobs[0]
}

func (e *env) runOnce(t *testing.T, ctx context.Context, want Outcome) {
	t.Helper()
	got, err := e.w.RunOnce(ctx)
	if err != nil || got != want {
		t.Fatalf("RunOnce: %v, %v; want %v", got, err, want)
	}
}

func (e *env) workDirEmpty(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(e.w.WorkDir.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range entries {
		if en.Name() != lockName {
			t.Fatalf("left in the work directory: %s", en.Name())
		}
	}
}

func TestWorkerProcessesPDF(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	d := e.doc(t, "%PDF-1.4 invoice")
	var sawOriginal []byte
	e.runner.handle = func(ctx context.Context, c Command) ([]byte, error) {
		if c.Name == "pdfinfo" {
			sawOriginal, _ = os.ReadFile(filepath.Join(c.Dir, c.Args[0]))
		}
		return poppler(pdf{pages: []string{longText}})(ctx, c)
	}
	e.runOnce(t, ctx, Done)
	e.runOnce(t, ctx, NoJob)
	if string(sawOriginal) != "%PDF-1.4 invoice" {
		t.Fatalf("the tools saw %q", sawOriginal)
	}
	got, err := e.st.GetDocument(ctx, e.owner, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PageCount != 1 || got.ExtractedText != longText || got.TextSource != store.TextSourcePDF ||
		got.ArchiveSHA256 == nil || *got.ArchiveSHA256 != d.OriginalSHA256 || *got.ArchiveSize != d.OriginalSize {
		t.Fatalf("processed: %+v", got)
	}
	if j := e.job(t, d.ID); j.State != store.JobDone {
		t.Fatalf("job: %+v", j)
	}
	e.workDirEmpty(t)
}

func TestWorkerStoresArchive(t *testing.T) {
	// OCR and image conversion make an archive PDF of their own: it is
	// stored in the blob store, and the document points to it.
	for _, tc := range []struct {
		name, mime string
		original   []byte
		archive    string // what the fake tools write as the archive
	}{
		{"scan", mimePDF, []byte("%PDF-1.4 scan"), "%PDF-1.4 OCRed"},
		{"photo", mimePNG, pngOf(t, 40, 20, 0), "%PDF-1.4 OCRed"},
		{"photo without text", mimePNG, pngOf(t, 40, 20, 0), "%PDF-1.4 image"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			d := e.docAs(t, string(tc.original), tc.mime)
			ocr := []string{longText}
			if tc.archive == "%PDF-1.4 image" {
				ocr = []string{""}
			}
			e.runner.handle = poppler(pdf{pages: []string{""}, ocr: ocr})
			e.runOnce(t, ctx, Done)
			got, err := e.st.GetDocument(ctx, e.owner, d.ID)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte(tc.archive))
			want := hex.EncodeToString(sum[:])
			if got.ArchiveSHA256 == nil || *got.ArchiveSHA256 != want || *got.ArchiveSize != int64(len(tc.archive)) {
				t.Fatalf("archive %v, want %s", got.ArchiveSHA256, want)
			}
			if wantSource := map[bool]string{true: store.TextSourceTesseract, false: ""}[ocr[0] != ""]; got.TextSource != wantSource || got.PageCount != 1 {
				t.Fatalf("processed: %+v", got)
			}
			f, err := e.blobs.OpenHash(want)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(f)
			_ = f.Close()
			if string(b) != tc.archive {
				t.Fatalf("stored archive %q", b)
			}
			if got.OriginalSHA256 != d.OriginalSHA256 || !e.blobs.Exists(d.OriginalSHA256) {
				t.Fatal("the original changed")
			}
			e.workDirEmpty(t)
		})
	}
}

func TestWorkerRetriesWithBackoff(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	d := e.doc(t, "%PDF flaky")
	e.runner.handle = poppler(pdf{info: &ToolError{Tool: "pdfinfo", ExitCode: 99, Stderr: "secret details"}})

	e.runOnce(t, ctx, Retry)
	j := e.job(t, d.ID)
	if j.State != store.JobQueued || j.Attempts != 1 || !j.RunAfter.Equal(e.clock.Now().Add(time.Minute)) ||
		j.LastError != "pdfinfo failed with exit status 99." {
		t.Fatalf("after the first attempt: %+v", j)
	}
	if !strings.Contains(e.log.String(), "secret details") {
		t.Errorf("standard error not logged: %s", e.log.String())
	}
	e.runOnce(t, ctx, NoJob) // not due yet
	e.clock.Add(time.Minute)
	e.runOnce(t, ctx, Retry)
	if j = e.job(t, d.ID); j.Attempts != 2 || !j.RunAfter.Equal(e.clock.Now().Add(5*time.Minute)) {
		t.Fatalf("after the second attempt: %+v", j)
	}
	e.clock.Add(5 * time.Minute)
	e.runOnce(t, ctx, Failed)
	if j = e.job(t, d.ID); j.State != store.JobFailed || j.Attempts != 3 || j.FinishedAt == nil ||
		strings.Contains(j.LastError, "secret") {
		t.Fatalf("after the last attempt: %+v", j)
	}
	e.workDirEmpty(t)

	// A retry that works stores the result and clears the failure.
	if _, err := e.st.Reprocess(ctx, store.ReprocessSelection{Failed: true}); err != nil {
		t.Fatal(err)
	}
	e.runner.handle = poppler(pdf{pages: []string{longText}})
	e.runOnce(t, ctx, Done)
	if jobs, _ := e.st.DocumentJobs(ctx, d.ID); len(jobs) != 1 || jobs[0].State != store.JobDone {
		t.Fatalf("jobs after a successful retry: %+v", jobs)
	}
}

func TestWorkerPermanentErrorIsNotRetried(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	d := e.doc(t, "%PDF broken")
	e.runner.handle = poppler(pdf{info: &ToolError{Tool: "pdfinfo", ExitCode: 1, Stderr: "Syntax Error /secret"}})
	e.runOnce(t, ctx, Failed)
	j := e.job(t, d.ID)
	if j.State != store.JobFailed || j.Attempts != 1 || j.LastError != msgUnreadablePDF {
		t.Fatalf("job: %+v", j)
	}
	if got, _ := e.st.GetDocument(ctx, e.owner, d.ID); got.ArchiveSHA256 != nil || got.PageCount != 0 {
		t.Fatalf("a failed job wrote a result: %+v", got)
	}
}

func TestWorkerDamagedOriginal(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	d := e.doc(t, "%PDF original")
	h := d.OriginalSHA256
	path := filepath.Join(e.blobs.Root(), "documents", h[0:2], h[2:4], h)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("%PDF changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.runOnce(t, ctx, Failed)
	if j := e.job(t, d.ID); j.LastError != msgDamagedOriginal {
		t.Fatalf("job: %+v", j)
	}
	if len(e.runner.names()) != 0 {
		t.Fatalf("tools ran on a damaged original: %v", e.runner.names())
	}
}

// blocking makes every tool wait until its context ends, after signalling
// started.
func blocking(started chan<- struct{}) func(context.Context, Command) ([]byte, error) {
	return func(ctx context.Context, c Command) ([]byte, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
}

func TestWorkerTimeout(t *testing.T) {
	e := newEnv(t)
	d := e.doc(t, "%PDF slow")
	e.w.Timeout = time.Nanosecond // over before the tool starts waiting
	started := make(chan struct{}, 1)
	e.runner.handle = blocking(started)
	e.runOnce(t, context.Background(), Retry)
	j := e.job(t, d.ID)
	if j.State != store.JobQueued || j.LastError != "Processing took longer than the limit of 1ns." {
		t.Fatalf("job: %+v", j)
	}
}

func TestWorkerShutdownGivesJobBack(t *testing.T) {
	e := newEnv(t)
	d := e.doc(t, "%PDF long")
	started := make(chan struct{})
	e.runner.handle = blocking(started)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Outcome)
	go func() {
		o, err := e.w.RunOnce(ctx)
		if err != nil {
			t.Error(err)
		}
		done <- o
	}()
	<-started
	cancel()
	if o := <-done; o != Released {
		t.Fatalf("outcome %v", o)
	}
	j := e.job(t, d.ID)
	if j.State != store.JobQueued || j.Attempts != 0 || j.LastError != "" {
		t.Fatalf("job after shutdown: %+v", j)
	}
	e.workDirEmpty(t)
}

func TestWorkerShutdownWhileStoring(t *testing.T) {
	e := newEnv(t)
	d := e.doc(t, "%PDF done")
	ctx, cancel := context.WithCancel(context.Background())
	// The tools are done; the shutdown comes as the result is stored.
	e.w.completing = cancel
	if o, err := e.w.RunOnce(ctx); err != nil || o != Released {
		t.Fatalf("RunOnce: %v, %v", o, err)
	}
	j := e.job(t, d.ID)
	if j.State != store.JobQueued || j.Attempts != 0 || j.LockedUntil != nil {
		t.Fatalf("job after shutdown: %+v", j)
	}
	if got, _ := e.st.GetDocument(context.Background(), e.owner, d.ID); got.ArchiveSHA256 != nil {
		t.Fatalf("result stored: %+v", got)
	}
}

func TestWorkerRunShutdown(t *testing.T) {
	e := newEnv(t)
	d := e.doc(t, "%PDF long")
	started := make(chan struct{})
	e.runner.handle = blocking(started)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		e.w.Run(ctx)
		close(stopped)
	}()
	<-started
	cancel()
	<-stopped
	if j := e.job(t, d.ID); j.State != store.JobQueued || j.Attempts != 0 {
		t.Fatalf("job after Run stopped: %+v", j)
	}
}

func TestWorkerDeletedDocumentDropsResult(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	d := e.doc(t, "%PDF doomed")
	e.runner.handle = func(ctx context.Context, c Command) ([]byte, error) {
		if c.Name == "pdfinfo" {
			if err := e.st.DeleteDocument(ctx, e.owner, d.ID); err != nil {
				return nil, err
			}
		}
		return poppler(pdf{pages: []string{longText}})(ctx, c)
	}
	e.runOnce(t, ctx, Dropped)
	if _, err := e.st.GetDocument(ctx, e.owner, d.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("document: %v", err)
	}
	e.runOnce(t, ctx, NoJob)
	e.workDirEmpty(t)
}

func TestWorkerHeartbeat(t *testing.T) {
	e := newEnv(t)
	d := e.doc(t, "%PDF long")
	started := make(chan struct{})
	beats := make(chan error)
	e.w.heartbeat = func(err error) { beats <- err }
	release := make(chan struct{})
	e.runner.handle = func(ctx context.Context, c Command) ([]byte, error) {
		if c.Name == "pdfinfo" {
			started <- struct{}{}
			<-release
		}
		return poppler(pdf{pages: []string{longText}})(ctx, c)
	}
	done := make(chan Outcome)
	go func() {
		o, _ := e.w.RunOnce(context.Background())
		done <- o
	}()
	<-started
	e.clock.Add(DefaultLease / 3)
	e.ticks <- time.Time{}
	if err := <-beats; err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	j := e.job(t, d.ID)
	if j.LockedUntil == nil || !j.LockedUntil.Equal(e.clock.Now().Add(DefaultLease)) {
		t.Fatalf("lease until %v, want %v", j.LockedUntil, e.clock.Now().Add(DefaultLease))
	}
	close(release)
	if o := <-done; o != Done {
		t.Fatalf("outcome %v", o)
	}
}

func TestWorkerLeaseLost(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	d := e.doc(t, "%PDF stuck")
	started := make(chan struct{})
	beats := make(chan error)
	e.w.heartbeat = func(err error) { beats <- err }
	e.runner.handle = blocking(started)
	done := make(chan Outcome)
	go func() {
		o, _ := e.w.RunOnce(ctx)
		done <- o
	}()
	<-started
	// The heartbeat did not get through in time: the reaper ends the
	// attempt, and the next heartbeat finds the lease gone.
	e.clock.Add(DefaultLease + time.Second)
	e.w.Housekeeping(ctx)
	e.ticks <- time.Time{}
	if err := <-beats; !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("heartbeat: %v", err)
	}
	if o := <-done; o != Dropped {
		t.Fatalf("outcome %v", o)
	}
	j := e.job(t, d.ID)
	if j.State != store.JobQueued || j.Attempts != 1 || j.LastError != msgLeaseExpired {
		t.Fatalf("job: %+v", j)
	}
}

func TestWorkerListenWakes(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	listening := make(chan struct{}, 1)
	e.w.listening = func() { listening <- struct{}{} }
	wakes := make(chan struct{}, 10)
	stopped := make(chan struct{})
	go func() {
		e.w.listen(ctx, func() { wakes <- struct{}{} })
		close(stopped)
	}()
	<-listening
	<-wakes // the wake-up for jobs queued before it listened
	e.doc(t, "%PDF new")
	<-wakes
	cancel()
	<-stopped
}
