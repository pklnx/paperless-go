package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// lease is the lease of claims in these tests.
const lease = 5 * time.Minute

// jobsOf returns a document's jobs, newest first.
func jobsOf(t *testing.T, st *store.Store, docID string) []*store.Job {
	t.Helper()
	jobs, err := st.DocumentJobs(context.Background(), docID)
	if err != nil {
		t.Fatal(err)
	}
	return jobs
}

// claim claims the next job at now and fails the test without one.
func claim(t *testing.T, st *store.Store, now time.Time) *store.Job {
	t.Helper()
	j, err := st.ClaimJob(context.Background(), now, lease)
	if err != nil || j == nil {
		t.Fatalf("claim at %s: %v, %v", now, j, err)
	}
	return j
}

func noClaim(t *testing.T, st *store.Store, now time.Time) {
	t.Helper()
	if j, err := st.ClaimJob(context.Background(), now, lease); err != nil || j != nil {
		t.Fatalf("claim at %s: %+v, %v; want none", now, j, err)
	}
}

func TestCreateDocumentEnqueues(t *testing.T) {
	st := storetest.New(t)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}

	d := mustDoc(t, st, alice, newDoc("scan"))
	jobs := jobsOf(t, st, d.ID)
	if len(jobs) != 1 || jobs[0].State != store.JobQueued || jobs[0].Kind != store.JobProcess ||
		jobs[0].Attempts != 0 || jobs[0].MaxAttempts != 3 {
		t.Fatalf("jobs of a new document: %+v", jobs)
	}

	// A document with an archive PDF (an import) needs no processing.
	in := newDoc("archived")
	in.ArchiveSHA256, in.ArchiveSize = ptr(hashOf("archive")), ptr(int64(7))
	if jobs := jobsOf(t, st, mustDoc(t, st, alice, in).ID); len(jobs) != 0 {
		t.Fatalf("jobs of a document with an archive: %+v", jobs)
	}

	// A duplicate creates neither a document nor a job.
	if _, err := st.CreateDocument(context.Background(), alice, newDoc("scan")); !errors.Is(err, store.ErrDuplicate) {
		t.Fatalf("duplicate: %v", err)
	}
	if jobs := jobsOf(t, st, d.ID); len(jobs) != 1 {
		t.Fatalf("jobs after a duplicate: %+v", jobs)
	}
}

func TestEnqueueIsIdempotent(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	d := mustDoc(t, st, alice, newDoc("scan"))

	if queued, err := st.EnqueueDocument(ctx, d.ID); err != nil || queued {
		t.Fatalf("enqueue a queued document: %v, %v", queued, err)
	}
	now := time.Now().Truncate(time.Microsecond)
	j := claim(t, st, now)
	if queued, err := st.EnqueueDocument(ctx, d.ID); err != nil || queued {
		t.Fatalf("enqueue a running document: %v, %v", queued, err)
	}
	if err := st.CompleteJob(ctx, j, now, store.ProcessResult{PageCount: 1}); err != nil {
		t.Fatal(err)
	}
	if queued, err := st.EnqueueDocument(ctx, d.ID); err != nil || !queued {
		t.Fatalf("enqueue a processed document: %v, %v", queued, err)
	}
	if jobs := jobsOf(t, st, d.ID); len(jobs) != 2 || jobs[0].State != store.JobQueued || jobs[1].State != store.JobDone {
		t.Fatalf("jobs: %+v", jobs)
	}
	if _, err := st.EnqueueDocument(ctx, "00000000-0000-4000-8000-000000000000"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("enqueue an unknown document: %v", err)
	}
	if _, err := st.EnqueueDocument(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("enqueue a malformed ID: %v", err)
	}
}

func TestEnqueueNotifies(t *testing.T) {
	st := storetest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	l, err := st.ListenJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	mustDoc(t, st, alice, newDoc("scan"))
	if err := l.Wait(ctx); err != nil {
		t.Fatalf("no notification after a document was created: %v", err)
	}
}

func TestMigrationBackfillsJobs(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := storetest.User(t, st, "alice", false)
	if err := st.MigrateDownTo(ctx, 6); err != nil {
		t.Fatal(err)
	}
	if _, err := connect(t, url).Exec(ctx, "SELECT 1 FROM jobs"); err == nil {
		t.Fatal("jobs table still there after rolling back its migration")
	}
	conn := connect(t, url)
	var pending, done string
	if err := conn.QueryRow(ctx, `INSERT INTO documents (owner_id, source, original_sha256, original_size, original_mime)
		VALUES ($1, 'file', $2, 1, 'application/pdf') RETURNING id`, alice.ID, hashOf("a")).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `INSERT INTO documents (owner_id, source, original_sha256, original_size, original_mime, archive_sha256, archive_size)
		VALUES ($1, 'file', $2, 1, 'application/pdf', $2, 1) RETURNING id`, alice.ID, hashOf("b")).Scan(&done); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if jobs := jobsOf(t, st, pending); len(jobs) != 1 || jobs[0].State != store.JobQueued {
		t.Fatalf("document without an archive: %+v", jobs)
	}
	if jobs := jobsOf(t, st, done); len(jobs) != 0 {
		t.Fatalf("document with an archive: %+v", jobs)
	}
	d, err := st.GetDocument(ctx, store.Scope{All: true}, pending)
	if err != nil || d.ProcessingNote != "" || d.ThumbnailSHA256 != nil {
		t.Fatalf("new columns: %+v, %v", d, err)
	}
}

func TestMigrationRequeuesOCRJobs(t *testing.T) {
	// The messages of the version before OCR (#5, part 2), as it stored
	// them.
	const (
		ocrLater    = "This PDF has pages without text, and this version cannot run OCR yet; run reprocess --failed after the update."
		imagesLater = "Images cannot be processed by this version yet; run reprocess --failed after the update."
		ownerNote   = "The PDF is protected by an owner password, so pages without text were not OCRed."
		unreadable  = "The PDF cannot be read: it is damaged, or it needs a password to open."
	)
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	// Rolls back 00010 only, whatever comes before it.
	if err := st.MigrateDownTo(ctx, 9); err != nil {
		t.Fatal(err)
	}
	conn := connect(t, url)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// job adds a finished job of the document with its last error.
	job := func(doc, state, lastError string) {
		t.Helper()
		exec(`INSERT INTO jobs (document_id, state, attempts, last_error, finished_at) VALUES ($1, $2, 1, $3, now())`, doc, state, lastError)
	}
	doc := func(name string) string {
		t.Helper()
		d := mustDoc(t, st, alice, newDoc(name))
		exec(`DELETE FROM jobs WHERE document_id = $1`, d.ID)
		return d.ID
	}
	scan := doc("scan")
	job(scan, store.JobFailed, ocrLater)
	photo := doc("photo")
	job(photo, store.JobFailed, imagesLater)
	job(photo, store.JobFailed, imagesLater) // two failed jobs: queued once
	broken := doc("broken")
	job(broken, store.JobFailed, unreadable)
	later := doc("later") // failed once, then processed
	job(later, store.JobFailed, ocrLater)
	job(later, store.JobDone, "")
	owner := doc("owner")
	job(owner, store.JobDone, "")
	exec(`UPDATE documents SET processing_note = $2 WHERE id = $1`, owner, ownerNote)
	ownerQueued := doc("owner, queued") // queued already: no second job
	exec(`UPDATE documents SET processing_note = $2 WHERE id = $1`, ownerQueued, ownerNote)
	exec(`INSERT INTO jobs (document_id) VALUES ($1)`, ownerQueued)

	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		id     string
		jobs   int
		latest string
	}{
		{"scan", scan, 2, store.JobQueued},
		{"photo", photo, 3, store.JobQueued},
		{"broken", broken, 1, store.JobFailed},
		{"later", later, 2, store.JobDone},
		{"owner", owner, 2, store.JobQueued},
		{"owner, queued", ownerQueued, 1, store.JobQueued},
	} {
		jobs := jobsOf(t, st, tc.id)
		if len(jobs) != tc.jobs || jobs[0].State != tc.latest {
			t.Errorf("%s: %d jobs, latest %+v; want %d, %s", tc.name, len(jobs), jobs[0], tc.jobs, tc.latest)
		}
		if tc.latest == store.JobQueued && (jobs[0].Attempts != 0 || jobs[0].LastError != "") {
			t.Errorf("%s: requeued job %+v", tc.name, jobs[0])
		}
	}
	// Down and up again: nothing to undo, and no job is queued twice.
	if err := st.MigrateDownTo(ctx, 9); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if jobs := jobsOf(t, st, scan); len(jobs) != 2 {
		t.Fatalf("scan after down and up: %d jobs", len(jobs))
	}
}

func TestClaimSkipsLockedJobs(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	first := mustDoc(t, st, alice, newDoc("first"))
	second := mustDoc(t, st, alice, newDoc("second"))

	// Another claimer holds the first job's row: the claim takes the second
	// at once instead of waiting.
	conn := connect(t, url)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT 1 FROM jobs WHERE document_id = $1 FOR UPDATE", first.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Microsecond)
	j := claim(t, st, now)
	if j.DocumentID != second.ID || j.State != store.JobRunning || j.Attempts != 1 {
		t.Fatalf("claimed %+v, want the second document's job", j)
	}
	if j.LockedUntil == nil || !j.LockedUntil.Equal(now.Add(lease)) {
		t.Fatalf("lease until %v", j.LockedUntil)
	}
	noClaim(t, st, now)
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if j := claim(t, st, now); j.DocumentID != first.ID {
		t.Fatalf("claimed %+v after the lock ended", j)
	}
	noClaim(t, st, now)
}

func TestClaimWaitsForRunAfter(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	mustDoc(t, st, alice, newDoc("scan"))
	now := time.Now().Truncate(time.Microsecond)
	j := claim(t, st, now)
	if err := st.RetryJob(ctx, j, now.Add(time.Minute), "Failed."); err != nil {
		t.Fatal(err)
	}
	noClaim(t, st, now.Add(time.Minute-time.Second))
	again := claim(t, st, now.Add(time.Minute))
	if again.ID != j.ID || again.Attempts != 2 || again.LastError != "Failed." {
		t.Fatalf("second attempt: %+v", again)
	}
	c, err := st.JobCounts(ctx, now)
	if err != nil || c != (store.JobCounts{Running: 1}) {
		t.Fatalf("counts: %+v, %v", c, err)
	}
}

func TestReaperEndsExpiredLeases(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	d := mustDoc(t, st, alice, newDoc("scan"))
	backoff := func(attempts int32) time.Duration { return time.Duration(attempts) * time.Minute }
	reap := func(now time.Time, wantRequeued, wantFailed int) {
		t.Helper()
		r, f, err := st.ReapJobs(ctx, now, backoff, "expired")
		if err != nil || r != wantRequeued || f != wantFailed {
			t.Fatalf("reap at %s: %d requeued, %d failed, %v; want %d, %d", now, r, f, err, wantRequeued, wantFailed)
		}
	}

	t0 := time.Now().Truncate(time.Microsecond)
	j := claim(t, st, t0)
	reap(t0.Add(lease), 0, 0) // not expired yet
	// A heartbeat moves the lease.
	if err := st.ExtendJobLease(ctx, j, t0.Add(2*lease)); err != nil {
		t.Fatal(err)
	}
	reap(t0.Add(lease+time.Second), 0, 0)
	t1 := t0.Add(2*lease + time.Second)
	reap(t1, 1, 0)
	got := jobsOf(t, st, d.ID)[0]
	if got.State != store.JobQueued || got.Attempts != 1 || got.LastError != "expired" ||
		!got.RunAfter.Equal(t1.Add(time.Minute)) || got.LockedUntil != nil {
		t.Fatalf("after the first expiry: %+v", got)
	}
	// The old lease can neither renew nor write a result.
	if err := st.ExtendJobLease(ctx, j, t1.Add(time.Hour)); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("renew an expired lease: %v", err)
	}

	// The expired attempt counted: after the third, the job fails.
	t2 := t1.Add(time.Minute)
	j2 := claim(t, st, t2)
	if err := st.CompleteJob(ctx, j, t2, store.ProcessResult{PageCount: 9}); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("complete with a stale lease: %v", err)
	}
	reap(t2.Add(lease+time.Second), 1, 0)
	t3 := t2.Add(lease + time.Second + 2*time.Minute)
	claim(t, st, t3)
	reap(t3.Add(lease+time.Second), 0, 1)
	got = jobsOf(t, st, d.ID)[0]
	if got.State != store.JobFailed || got.Attempts != 3 || got.FinishedAt == nil || got.LastError != "expired" {
		t.Fatalf("after the last expiry: %+v", got)
	}
	if err := st.FailJob(ctx, j2, t3, "late"); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("fail with a stale lease: %v", err)
	}
	if doc, _ := st.GetDocument(ctx, alice, d.ID); doc.PageCount != 0 {
		t.Fatalf("a stale lease wrote a result: %+v", doc)
	}
}

func TestReleaseJobDoesNotCount(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	d := mustDoc(t, st, alice, newDoc("scan"))
	now := time.Now().Truncate(time.Microsecond)
	j := claim(t, st, now)
	if err := st.ReleaseJob(ctx, j, now); err != nil {
		t.Fatal(err)
	}
	got := jobsOf(t, st, d.ID)[0]
	if got.State != store.JobQueued || got.Attempts != 0 || got.LockedUntil != nil {
		t.Fatalf("released job: %+v", got)
	}
	if err := st.ReleaseJob(ctx, j, now); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("release twice: %v", err)
	}
}

func TestCompleteJob(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	d := mustDoc(t, st, alice, newDoc("scan"))
	now := time.Now().Truncate(time.Microsecond)

	// A failed job stays as the document's status until a new one is done.
	j := claim(t, st, now)
	if err := st.FailJob(ctx, j, now, "broken"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueDocument(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	j = claim(t, st, now)
	res := store.ProcessResult{
		PageCount: 2, Text: "Rechnung 4711", TextSource: store.TextSourcePDF,
		ArchiveSHA256: d.OriginalSHA256, ArchiveSize: d.OriginalSize, Note: "a note",
	}
	if err := st.CompleteJob(ctx, j, now, res); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetDocument(ctx, alice, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PageCount != 2 || got.ExtractedText != "Rechnung 4711" || got.TextSource != store.TextSourcePDF ||
		got.ArchiveSHA256 == nil || *got.ArchiveSHA256 != d.OriginalSHA256 || *got.ArchiveSize != d.OriginalSize ||
		got.ProcessingNote != "a note" {
		t.Fatalf("processed document: %+v", got)
	}
	if !got.ModifiedAt.Equal(d.ModifiedAt) {
		t.Errorf("modified_at changed from %s to %s", d.ModifiedAt, got.ModifiedAt)
	}
	jobs := jobsOf(t, st, d.ID)
	if len(jobs) != 1 || jobs[0].State != store.JobDone || jobs[0].FinishedAt == nil || jobs[0].LastError != "" {
		t.Fatalf("jobs after completion: %+v", jobs)
	}
	// Done jobs are deleted after a while, failed ones are not.
	if n, err := st.DeleteDoneJobs(ctx, now); err != nil || n != 0 {
		t.Fatalf("delete done jobs before they finished: %d, %v", n, err)
	}
	if n, err := st.DeleteDoneJobs(ctx, now.Add(time.Second)); err != nil || n != 1 {
		t.Fatalf("delete old done jobs: %d, %v", n, err)
	}
}

func TestCompleteJobKeepsForeignText(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	res := store.ProcessResult{PageCount: 1, Text: "new text", TextSource: store.TextSourceTesseract}

	for _, tc := range []struct {
		text, source       string
		wantText, wantFrom string
	}{
		{"", "", "new text", store.TextSourceTesseract},
		{"", "Mistral OCR", "new text", store.TextSourceTesseract}, // empty text is replaced
		{"old", store.TextSourcePDF, "new text", store.TextSourceTesseract},
		{"old", store.TextSourceTesseract, "new text", store.TextSourceTesseract},
		{"imported", "Mistral OCR", "imported", "Mistral OCR"},
		{"typed", "manual", "typed", "manual"},
	} {
		in := newDoc(tc.source + "/" + tc.text)
		in.ExtractedText, in.TextSource = tc.text, tc.source
		d := mustDoc(t, st, alice, in)
		j := claim(t, st, time.Now().Truncate(time.Microsecond))
		if j.DocumentID != d.ID {
			t.Fatalf("claimed another document's job")
		}
		if err := st.CompleteJob(ctx, j, time.Now().Truncate(time.Microsecond), res); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetDocument(ctx, alice, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.ExtractedText != tc.wantText || got.TextSource != tc.wantFrom || got.PageCount != 1 {
			t.Errorf("text %q from %q: got %q from %q, want %q from %q",
				tc.text, tc.source, got.ExtractedText, got.TextSource, tc.wantText, tc.wantFrom)
		}
	}
}

func TestCompleteJobOfDeletedDocument(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	d := mustDoc(t, st, alice, newDoc("scan"))
	now := time.Now().Truncate(time.Microsecond)
	j := claim(t, st, now)
	if err := st.DeleteDocument(ctx, alice, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteJob(ctx, j, now, store.ProcessResult{PageCount: 1}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("complete the job of a deleted document: %v", err)
	}
	if _, err := st.GetJob(ctx, j.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the job outlived its document: %v", err)
	}
}

func TestReprocess(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	ok := mustDoc(t, st, alice, newDoc("ok"))
	bad := mustDoc(t, st, alice, newDoc("bad"))
	now := time.Now().Truncate(time.Microsecond)
	for range 2 {
		j := claim(t, st, now)
		if j.DocumentID == ok.ID {
			if err := st.CompleteJob(ctx, j, now, store.ProcessResult{PageCount: 1}); err != nil {
				t.Fatal(err)
			}
		} else if err := st.FailJob(ctx, j, now, "broken"); err != nil {
			t.Fatal(err)
		}
	}

	reprocess := func(sel store.ReprocessSelection, want int64) {
		t.Helper()
		e := &store.AuditEntry{Actor: store.ActorCLI, Action: "document.reprocess"}
		n, err := st.Reprocess(store.WithAudit(ctx, e), sel)
		if err != nil || n != want {
			t.Fatalf("reprocess %+v: %d, %v; want %d", sel, n, err, want)
		}
		if e.Details["queued"] != want {
			t.Fatalf("audit details: %+v", e.Details)
		}
	}
	reprocess(store.ReprocessSelection{Failed: true}, 1)
	if jobs := jobsOf(t, st, bad.ID); jobs[0].State != store.JobQueued {
		t.Fatalf("failed document not queued: %+v", jobs)
	}
	reprocess(store.ReprocessSelection{Failed: true}, 0) // queued already
	reprocess(store.ReprocessSelection{All: true}, 1)    // ok; bad is queued
	reprocess(store.ReprocessSelection{IDs: []string{ok.ID, bad.ID, ok.ID}}, 0)

	missing := "00000000-0000-4000-8000-000000000000"
	for _, ids := range [][]string{{missing}, {"nope"}, {ok.ID, missing}, nil} {
		if _, err := st.Reprocess(ctx, store.ReprocessSelection{IDs: ids}); err == nil {
			t.Fatalf("reprocess %v: no error", ids)
		} else if len(ids) > 0 && (!errors.Is(err, store.ErrNotFound) || !strings.Contains(err.Error(), ids[len(ids)-1])) {
			t.Fatalf("reprocess %v: %v", ids, err)
		}
	}
	// One entry per call, in the same transaction.
	if rows := auditRows(t, connect(t, url)); len(rows) != 4 || rows[0].Action != "document.reprocess" {
		t.Fatalf("audit rows: %+v", rows)
	}
}

// The thumbnail of a result is stored; a result without one keeps the
// stored thumbnail.
func TestCompleteJobThumbnail(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	d := mustDoc(t, st, alice, newDoc("scan"))
	now := time.Now().Truncate(time.Microsecond)

	complete := func(thumb string) *store.Document {
		t.Helper()
		j := claim(t, st, now)
		res := store.ProcessResult{PageCount: 1, ArchiveSHA256: d.OriginalSHA256, ArchiveSize: d.OriginalSize, ThumbnailSHA256: thumb}
		if err := st.CompleteJob(ctx, j, now, res); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetDocument(ctx, alice, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.EnqueueDocument(ctx, d.ID); err != nil {
			t.Fatal(err)
		}
		return got
	}
	first, second := hashOf("thumbnail 1"), hashOf("thumbnail 2")
	if got := complete(first); got.ThumbnailSHA256 == nil || *got.ThumbnailSHA256 != first {
		t.Fatalf("thumbnail %v, want %s", got.ThumbnailSHA256, first)
	}
	if got := complete(""); got.ThumbnailSHA256 == nil || *got.ThumbnailSHA256 != first {
		t.Fatalf("thumbnail after a result without one: %v", got.ThumbnailSHA256)
	}
	if got := complete(second); got.ThumbnailSHA256 == nil || *got.ThumbnailSHA256 != second {
		t.Fatalf("thumbnail after a new one: %v", got.ThumbnailSHA256)
	}
}

// LatestJobs gives each document's newest job; documents without one and
// malformed IDs are missing.
func TestLatestJobs(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	failed := mustDoc(t, st, alice, newDoc("failed"))
	now := time.Now().Truncate(time.Microsecond)
	j := claim(t, st, now)
	if err := st.FailJob(ctx, j, now, "broken"); err != nil {
		t.Fatal(err)
	}
	// A new job after the failed one is the status.
	retried := mustDoc(t, st, alice, newDoc("retried"))
	j = claim(t, st, now) // retried's job
	if err := st.FailJob(ctx, j, now, "broken"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueDocument(ctx, retried.ID); err != nil {
		t.Fatal(err)
	}
	in := newDoc("archived")
	in.ArchiveSHA256, in.ArchiveSize = ptr(hashOf("archive")), ptr(int64(7))
	archived := mustDoc(t, st, alice, in) // no job

	got, err := st.LatestJobs(ctx, []string{failed.ID, retried.ID, archived.ID, "not-a-uuid"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[failed.ID].State != store.JobFailed || got[failed.ID].LastError != "broken" ||
		got[retried.ID].State != store.JobQueued || got[retried.ID].Attempts != 0 {
		t.Fatalf("latest jobs: %+v", got)
	}
	if got, err := st.LatestJobs(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("no IDs: %v, %v", got, err)
	}
}
