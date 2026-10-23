package process

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

// A document moved to the trash while its job runs still gets the result;
// one purged meanwhile (trash, "delete permanently", the sweeper's row
// pass, which cascades to the job) drops it without an error or a retry,
// and its archive and thumbnail are left to the orphan walk (#12, part 2).
func TestWorkerTrashedAndPurgedDocument(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	trashed := e.doc(t, "%PDF trashed")
	e.runner.handle = func(ctx context.Context, c Command) ([]byte, error) {
		if c.Name == "pdfinfo" {
			if err := e.st.TrashDocument(ctx, e.owner, trashed.ID); err != nil {
				return nil, err
			}
		}
		return poppler(pdf{pages: []string{longText}})(ctx, c)
	}
	e.runOnce(t, ctx, Done)
	if d, err := e.st.GetDocument(ctx, e.owner, trashed.ID); err != nil || d.DeletedAt == nil || d.PageCount != 1 {
		t.Fatalf("trashed document after its job: %+v, %v", d, err)
	}

	purged := e.doc(t, "%PDF purged")
	e.runner.handle = func(ctx context.Context, c Command) ([]byte, error) {
		if c.Name == "pdfinfo" {
			if err := e.st.TrashDocument(ctx, e.owner, purged.ID); err != nil {
				return nil, err
			}
			if err := e.st.PurgeDocument(ctx, e.owner, purged.ID); err != nil {
				return nil, err
			}
			if res, err := e.st.PurgeDueDocuments(ctx, time.Now(), store.DefaultTrashRetention, 100); err != nil || res.Purged != 1 {
				return nil, fmt.Errorf("purge: %+v, %w", res, err)
			}
		}
		return poppler(pdf{pages: []string{longText}})(ctx, c)
	}
	e.runOnce(t, ctx, Dropped)
	if _, err := e.st.GetDocument(ctx, store.Scope{All: true}, purged.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("purged document: %v", err)
	}
	if jobs, err := e.st.DocumentJobs(ctx, purged.ID); err != nil || len(jobs) != 0 {
		t.Errorf("jobs of the purged document: %+v, %v", jobs, err)
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

// blobOf returns the hash of content, as the blob store names it.
func blobOf(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// The thumbnail is rendered from the archive (the original PDF, OCRmyPDF's
// output or the image's PDF) and stored with the result.
func TestWorkerStoresThumbnail(t *testing.T) {
	for _, tc := range []struct {
		name, mime string
		original   []byte
		pages      []string
		from       string // the PDF pdftoppm renders
	}{
		{"text PDF", mimePDF, []byte("%PDF-1.4 text"), []string{longText}, "original"},
		{"scan", mimePDF, []byte("%PDF-1.4 scan"), []string{""}, archiveFile},
		{"photo", mimePNG, pngOf(t, 40, 20, 0), []string{""}, archiveFile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			d := e.docAs(t, string(tc.original), tc.mime)
			thumb := jpegOf(300, 512)
			e.runner.handle = poppler(pdf{pages: tc.pages, ocr: []string{longText}, thumb: thumb})
			e.runOnce(t, ctx, Done)
			got, err := e.st.GetDocument(ctx, e.owner, d.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.ThumbnailSHA256 == nil || *got.ThumbnailSHA256 != blobOf(thumb) {
				t.Fatalf("thumbnail %v, want %s", got.ThumbnailSHA256, blobOf(thumb))
			}
			f, err := e.blobs.OpenHash(*got.ThumbnailSHA256)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(f)
			_ = f.Close()
			if !bytes.Equal(b, thumb) {
				t.Fatal("stored thumbnail differs")
			}
			if c := call(t, e.runner, "pdftoppm"); c.Args[len(c.Args)-2] != tc.from {
				t.Fatalf("thumbnail rendered from %q, want %q", c.Args[len(c.Args)-2], tc.from)
			}
			e.workDirEmpty(t)
		})
	}
}

// A thumbnail that cannot be made costs nothing else: the result is
// stored without it, and a later failure keeps the earlier thumbnail.
func TestWorkerThumbnailFails(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	d := e.doc(t, "%PDF-1.4 no thumbnail")
	e.runner.handle = poppler(pdf{pages: []string{longText}, thumbErr: &ToolError{Tool: "pdftoppm", ExitCode: 99, Stderr: "render details"}})
	e.runOnce(t, ctx, Done)
	got, err := e.st.GetDocument(ctx, e.owner, d.ID)
	if err != nil || got.ThumbnailSHA256 != nil || got.ExtractedText != longText || got.ArchiveSHA256 == nil {
		t.Fatalf("processed without a thumbnail: %+v, %v", got, err)
	}
	if j := e.job(t, d.ID); j.State != store.JobDone || j.LastError != "" {
		t.Fatalf("job: %+v", j)
	}
	if log := e.log.String(); !strings.Contains(log, "no thumbnail") || !strings.Contains(log, "render details") {
		t.Fatalf("log: %s", log)
	}

	reprocess := func(p pdf) *store.Document {
		t.Helper()
		if _, err := e.st.Reprocess(ctx, store.ReprocessSelection{IDs: []string{d.ID}}); err != nil {
			t.Fatal(err)
		}
		e.runner.handle = poppler(p)
		e.runOnce(t, ctx, Done)
		got, err := e.st.GetDocument(ctx, e.owner, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	thumb := jpegOf(362, 512)
	if got := reprocess(pdf{pages: []string{longText}, thumb: thumb}); got.ThumbnailSHA256 == nil || *got.ThumbnailSHA256 != blobOf(thumb) {
		t.Fatalf("thumbnail after a retry: %v", got.ThumbnailSHA256)
	}
	if got := reprocess(pdf{pages: []string{longText}, thumb: []byte("not a JPEG")}); got.ThumbnailSHA256 == nil || *got.ThumbnailSHA256 != blobOf(thumb) {
		t.Fatalf("thumbnail after a failed one: %v", got.ThumbnailSHA256)
	}
}

// A shutdown while the thumbnail is rendered gives the job back; nothing
// is stored.
func TestWorkerShutdownDuringThumbnail(t *testing.T) {
	e := newEnv(t)
	d := e.doc(t, "%PDF-1.4 shutdown")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, 1)
	tools := poppler(pdf{pages: []string{longText}})
	e.runner.handle = func(ctx context.Context, c Command) ([]byte, error) {
		if c.Name != "pdftoppm" {
			return tools(ctx, c)
		}
		started <- struct{}{}
		<-ctx.Done()
		return nil, &ToolError{Tool: "pdftoppm", ExitCode: -1}
	}
	go func() {
		<-started
		cancel()
	}()
	e.runOnce(t, ctx, Released)
	got, err := e.st.GetDocument(context.Background(), e.owner, d.ID)
	if err != nil || got.ArchiveSHA256 != nil || got.ThumbnailSHA256 != nil {
		t.Fatalf("stored at shutdown: %+v, %v", got, err)
	}
	if j := e.job(t, d.ID); j.State != store.JobQueued || j.Attempts != 0 {
		t.Fatalf("job: %+v", j)
	}
}

// A pdftoppm that runs out of the thumbnail's own limit costs only the
// thumbnail: the text is stored, the job is done, and on a reprocess the
// earlier thumbnail stays.
func TestWorkerThumbnailTimeout(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.w.Pipeline.ThumbnailTimeout = time.Millisecond // e.w.Timeout is an hour
	d := e.doc(t, "%PDF-1.4 hangs pdftoppm")
	started := make(chan struct{}, 1)
	e.runner.handle = blockingThumbnail(started, poppler(pdf{pages: []string{longText}}))
	e.runOnce(t, ctx, Done)
	<-started
	got, err := e.st.GetDocument(ctx, e.owner, d.ID)
	if err != nil || got.ThumbnailSHA256 != nil || got.ExtractedText != longText || got.PageCount != 1 || got.ArchiveSHA256 == nil {
		t.Fatalf("processed without a thumbnail: %+v, %v", got, err)
	}
	if j := e.job(t, d.ID); j.State != store.JobDone || j.LastError != "" || j.Attempts != 1 {
		t.Fatalf("job: %+v", j)
	}
	if log := e.log.String(); !strings.Contains(log, "no thumbnail") || !strings.Contains(log, errThumbnailTimeout.Error()) {
		t.Fatalf("log: %s", log)
	}

	// Reprocess with a thumbnail, then once more with pdftoppm hanging:
	// the thumbnail of the first stays.
	thumb := jpegOf(362, 512)
	if _, err := e.st.Reprocess(ctx, store.ReprocessSelection{IDs: []string{d.ID}}); err != nil {
		t.Fatal(err)
	}
	e.runner.handle = poppler(pdf{pages: []string{longText}, thumb: thumb})
	e.runOnce(t, ctx, Done)
	if _, err := e.st.Reprocess(ctx, store.ReprocessSelection{IDs: []string{d.ID}}); err != nil {
		t.Fatal(err)
	}
	e.runner.handle = blockingThumbnail(started, poppler(pdf{pages: []string{longText}}))
	e.runOnce(t, ctx, Done)
	<-started
	if got, err := e.st.GetDocument(ctx, e.owner, d.ID); err != nil || got.ThumbnailSHA256 == nil || *got.ThumbnailSHA256 != blobOf(thumb) {
		t.Fatalf("thumbnail after a timed-out one: %+v, %v", got, err)
	}
	e.workDirEmpty(t)
}

// expiringContext is an attempt context that the test expires by hand: its
// Err is then context.DeadlineExceeded, as for an attempt that ran out of
// its limit.
type expiringContext struct {
	context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	expired bool
}

func newExpiringContext(parent context.Context) *expiringContext {
	ctx, cancel := context.WithCancel(parent)
	return &expiringContext{Context: ctx, cancel: cancel}
}

func (c *expiringContext) expire() {
	c.mu.Lock()
	c.expired = true
	c.mu.Unlock()
	c.cancel()
}

func (c *expiringContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.expired {
		return context.DeadlineExceeded
	}
	return c.Context.Err()
}

// When the attempt's own limit ends while the thumbnail is rendered, the
// attempt fails as a timeout and is retried; nothing is stored.
func TestWorkerAttemptTimeoutDuringThumbnail(t *testing.T) {
	e := newEnv(t)
	d := e.doc(t, "%PDF-1.4 slow")
	attempt := make(chan *expiringContext, 1)
	e.w.attemptContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		c := newExpiringContext(parent)
		attempt <- c
		return c, c.cancel
	}
	e.w.Pipeline.ThumbnailTimeout = time.Hour
	started := make(chan struct{})
	e.runner.handle = blockingThumbnail(started, poppler(pdf{pages: []string{longText}}))
	go func() {
		c := <-attempt
		<-started
		c.expire()
	}()
	e.runOnce(t, context.Background(), Retry)
	j := e.job(t, d.ID)
	if j.State != store.JobQueued || j.Attempts != 1 || j.LastError != "Processing took longer than the limit of 1h0m0s." {
		t.Fatalf("job: %+v", j)
	}
	got, err := e.st.GetDocument(context.Background(), e.owner, d.ID)
	if err != nil || got.ArchiveSHA256 != nil || got.ThumbnailSHA256 != nil || got.ExtractedText != "" || got.PageCount != 0 {
		t.Fatalf("stored after the attempt's limit: %+v, %v", got, err)
	}
	e.workDirEmpty(t)
}

// A lost lease while the thumbnail is rendered drops the result.
func TestWorkerLeaseLostDuringThumbnail(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	d := e.doc(t, "%PDF-1.4 stuck in pdftoppm")
	started := make(chan struct{})
	beats := make(chan error)
	e.w.heartbeat = func(err error) { beats <- err }
	e.runner.handle = blockingThumbnail(started, poppler(pdf{pages: []string{longText}}))
	done := make(chan Outcome)
	go func() {
		o, _ := e.w.RunOnce(ctx)
		done <- o
	}()
	<-started
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
	got, err := e.st.GetDocument(ctx, e.owner, d.ID)
	if err != nil || got.ArchiveSHA256 != nil || got.ThumbnailSHA256 != nil || got.ExtractedText != "" {
		t.Fatalf("stored after the lease was lost: %+v, %v", got, err)
	}
	e.workDirEmpty(t)
}
