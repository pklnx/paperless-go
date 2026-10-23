package sweep_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/blobstore"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
	"github.com/pklnx/paperless-go/internal/sweep"
)

// fileEnv is a database whose store checks the files of a real blob
// store, a sweeper on both with a clock the test moves, and alice and bob.
type fileEnv struct {
	t     *testing.T
	ctx   context.Context
	st    *store.Store
	blobs *blobstore.Store
	conn  *pgx.Conn
	sw    *sweep.Sweeper
	url   string
	logs  *syncBuf
	clock atomic.Pointer[time.Time]
	alice store.Scope
	bob   store.Scope
}

func newFileEnv(t *testing.T) *fileEnv {
	t.Helper()
	blobs := newBlobs(t)
	st, url := storetest.NewWithOptions(t, storetest.Options{Blobs: blobs})
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	e := &fileEnv{
		t: t, ctx: ctx, st: st, blobs: blobs, conn: conn, url: url, logs: &syncBuf{},
		alice: store.Scope{UserID: storetest.User(t, st, "alice", false).ID},
		bob:   store.Scope{UserID: storetest.User(t, st, "bob", false).ID},
	}
	e.setNow(time.Now().Truncate(time.Microsecond))
	st.Now = e.now
	e.sw = &sweep.Sweeper{Store: st, Blobs: blobs, Log: slog.New(slog.NewTextHandler(e.logs, nil)), Now: st.Now,
		Retention: store.DefaultTrashRetention}
	return e
}

// now is the clock of the store and the sweeper; setNow and advance move
// it (also while Run reads it).
func (e *fileEnv) now() time.Time          { return *e.clock.Load() }
func (e *fileEnv) setNow(t time.Time)      { e.clock.Store(&t) }
func (e *fileEnv) advance(d time.Duration) { e.setNow(e.now().Add(d)) }

// bind writes the installation marker.
func (e *fileEnv) bind() {
	e.t.Helper()
	inst, err := e.st.Installation(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	if res, err := sweep.BindFresh(e.blobs.Root(), inst.ID); err != nil || (res != sweep.Bound && res != sweep.AlreadyBound) {
		// BindFresh refuses a data directory with files: write it by hand.
		if err := os.WriteFile(filepath.Join(e.blobs.Root(), sweep.MarkerFile), []byte(inst.ID+"\n"), 0o600); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *fileEnv) put(content string) string {
	e.t.Helper()
	b, _, err := e.blobs.Put(strings.NewReader(content))
	if err != nil {
		e.t.Fatal(err)
	}
	return b.SHA256
}

// doc creates and processes a document of sc whose original is content,
// with an archive and a thumbnail when they are not "".
func (e *fileEnv) doc(sc store.Scope, content, archive, thumb string) *store.Document {
	e.t.Helper()
	in := store.NewDocument{Title: content, Source: store.SourceFile, OriginalFileName: "f.pdf", OriginalSHA256: e.put(content),
		OriginalSize: 1, OriginalMIME: "application/pdf"}
	d, err := e.st.CreateDocument(e.ctx, sc, in)
	if err != nil {
		e.t.Fatal(err)
	}
	// Each document is processed at once, so its job is the only one due.
	j, err := e.st.ClaimJob(e.ctx, e.now(), time.Minute)
	if err != nil || j == nil || j.DocumentID != d.ID {
		e.t.Fatalf("claim: %+v, %v", j, err)
	}
	r := store.ProcessResult{PageCount: 1, TextSource: store.TextSourceTesseract}
	if archive != "" {
		r.ArchiveSHA256, r.ArchiveSize = e.put(archive), 1
	}
	if thumb != "" {
		r.ThumbnailSHA256 = e.put(thumb)
	}
	if err := e.st.CompleteJob(e.ctx, j, e.now(), r); err != nil {
		e.t.Fatal(err)
	}
	d, err = e.st.GetDocument(e.ctx, store.Scope{All: true}, d.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return d
}

func (e *fileEnv) purge(sc store.Scope, id string) {
	e.t.Helper()
	if err := e.st.TrashDocument(e.ctx, sc, id); err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.PurgeDocument(e.ctx, sc, id); err != nil {
		e.t.Fatal(err)
	}
}

func (e *fileEnv) queued() int {
	e.t.Helper()
	var n int
	if err := e.conn.QueryRow(e.ctx, `SELECT count(*) FROM blob_removals`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *fileEnv) exists(content string) bool {
	e.t.Helper()
	return e.blobs.Exists(hashOf(content))
}

func hashOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// A purge removes the three files of the document an hour later (the
// grace), and only the files no other document names: an original shared
// with another user's live document, an archive equal to a live
// document's original and a thumbnail shared by two documents stay.
func TestPurgeRemovesFilesAfterTheGrace(t *testing.T) {
	e := newFileEnv(t)
	gone := e.doc(e.alice, "gone original", "gone archive", "gone thumb")
	shared := e.doc(e.alice, "shared original", "shared archive", "shared thumb")
	e.doc(e.bob, "shared original", "", "")                      // the same file, uploaded by bob
	e.doc(e.bob, "shared archive", "", "")                       // bob's original is alice's archive
	e.doc(e.bob, "bob's other", "bob's archive", "shared thumb") // the same thumbnail
	e.purge(e.alice, gone.ID)
	e.purge(e.alice, shared.ID)
	if st, err := e.sw.RunOnce(e.ctx); err != nil || st.Purged != 2 || st.Queue != (sweep.QueueStats{}) {
		t.Fatalf("first run: %+v, %v", st, err)
	}
	if e.queued() != 6 || !e.exists("gone original") || !e.exists("gone archive") || !e.exists("gone thumb") {
		t.Fatalf("before the grace: %d queued, files gone", e.queued())
	}
	e.advance(store.BlobGrace)
	st, err := e.sw.RunOnce(e.ctx)
	if err != nil || st.Queue != (sweep.QueueStats{Removed: 3, InUse: 3}) {
		t.Fatalf("after the grace: %+v, %v", st, err)
	}
	for content, want := range map[string]bool{
		"gone original": false, "gone archive": false, "gone thumb": false,
		"shared original": true, "shared archive": true, "shared thumb": true,
	} {
		if e.exists(content) != want {
			t.Errorf("%s: exists %v, want %v", content, !want, want)
		}
	}
	if e.queued() != 0 {
		t.Errorf("%d rows left", e.queued())
	}
	if st, err := e.sw.RunOnce(e.ctx); err != nil || st != (sweep.Stats{}) {
		t.Errorf("third run: %+v, %v", st, err)
	}
}

// failingRemove fails Remove of a hash as often as fails says.
type failingRemove struct {
	*blobstore.Store
	fails map[string]int
}

func (f *failingRemove) Remove(hash string) error {
	if f.fails[hash] > 0 {
		f.fails[hash]--
		return errors.New("disk on fire")
	}
	return f.Store.Remove(hash)
}

// rowState is a queue row's removed_at and due_at (ok false: no row).
func (e *fileEnv) rowState(hash string) (removed *time.Time, due time.Time, ok bool) {
	e.t.Helper()
	err := e.conn.QueryRow(e.ctx, `SELECT removed_at, due_at FROM blob_removals WHERE sha256 = $1`, hash).Scan(&removed, &due)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, time.Time{}, false
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return removed, due, true
}

// queue stores content and queues it due now.
func (e *fileEnv) queue(content string) string {
	e.t.Helper()
	h := e.put(content)
	if n, err := e.st.QueueUnreferencedBlobs(e.ctx, []string{h}, e.now()); err != nil || n != 1 {
		e.t.Fatalf("queue: %d, %v", n, err)
	}
	return h
}

func (e *fileEnv) create(sc store.Scope, hash string) error {
	_, err := e.st.CreateDocument(e.ctx, sc, store.NewDocument{Title: "x", Source: store.SourceFile, OriginalSHA256: hash,
		OriginalMIME: "application/pdf"})
	return err
}

// A failed removal unmarks the row and makes it due again after the
// grace, logged at error level: uploads of the file are not refused
// meanwhile, and a later pass retries.
func TestQueuePassFailedRemovalRequeues(t *testing.T) {
	e := newFileEnv(t)
	stuck, retried := e.queue("stuck"), e.queue("retried")
	e.sw.Blobs = &failingRemove{Store: e.blobs, fails: map[string]int{stuck: 1, retried: 1}}
	if q, err := e.sw.QueuePass(e.ctx, false); err != nil || q != (sweep.QueueStats{Failed: 2}) {
		t.Fatalf("failing removes: %+v, %v", q, err)
	}
	if !strings.Contains(e.logs.String(), "level=ERROR") || !strings.Contains(e.logs.String(), "disk on fire") {
		t.Errorf("not logged as an error:\n%s", e.logs)
	}
	for _, h := range []string{stuck, retried} {
		removed, due, ok := e.rowState(h)
		if !ok || removed != nil || !due.Equal(e.now().Add(store.BlobGrace)) || !e.blobs.Exists(h) {
			t.Fatalf("after the failed removal: row %v, removed %v, due %v, file %v", ok, removed, due, e.blobs.Exists(h))
		}
	}
	if q, err := e.sw.QueuePass(e.ctx, false); err != nil || q != (sweep.QueueStats{}) {
		t.Fatalf("pass before the backoff: %+v, %v", q, err)
	}
	// An upload of the file is not refused: it takes the row away.
	if err := e.create(e.alice, stuck); err != nil {
		t.Fatalf("a writer after the failed removal: %v", err)
	}
	e.advance(store.BlobGrace)
	if q, err := e.sw.QueuePass(e.ctx, false); err != nil || q != (sweep.QueueStats{Removed: 1}) {
		t.Fatalf("pass after the backoff: %+v, %v", q, err)
	}
	if !e.blobs.Exists(stuck) || e.blobs.Exists(retried) || e.queued() != 0 {
		t.Errorf("stuck %v, retried %v, %d rows", e.blobs.Exists(stuck), e.blobs.Exists(retried), e.queued())
	}
}

// Crash seams: a pass that died after step 1 leaves the row marked
// (writers of the hash fail meanwhile), and one that died after step 2 a
// marked row whose file is gone. A pass takes such a mark over only once
// it is store.MarkStale old (a younger one could be another pass's), then
// finishes it; Remove of a file already gone is fine.
func TestQueuePassCrashSeams(t *testing.T) {
	e := newFileEnv(t)
	h := e.queue("crash one")
	if m, err := e.st.MarkBlobRemoval(e.ctx, h, e.now()); err != nil || m != store.BlobMarked {
		t.Fatalf("step 1 of the crashed pass: %v, %v", m, err)
	}
	if err := e.create(e.alice, h); !errors.Is(err, store.ErrBlobRemoved) {
		t.Fatalf("a writer while the row is marked: %v", err)
	}
	if q, err := e.sw.QueuePass(e.ctx, false); err != nil || q != (sweep.QueueStats{}) || !e.blobs.Exists(h) {
		t.Fatalf("pass on a fresh mark: %+v, %v", q, err)
	}
	e.advance(store.MarkStale)
	if q, err := e.sw.QueuePass(e.ctx, false); err != nil || q != (sweep.QueueStats{Removed: 1}) {
		t.Fatalf("pass on a stale mark: %+v, %v", q, err)
	}
	if e.blobs.Exists(h) || e.queued() != 0 {
		t.Fatal("the stale mark was not finished")
	}

	h = e.queue("crash two")
	ctx, cancel := context.WithCancel(e.ctx)
	sweep.SetQueueSeams(e.sw, nil, func(string) { cancel() })
	if _, err := e.sw.QueuePass(ctx, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("pass cancelled after step 2: %v", err)
	}
	if e.blobs.Exists(h) || e.queued() != 1 {
		t.Fatalf("after step 2: file %v, %d rows", e.blobs.Exists(h), e.queued())
	}
	sweep.SetQueueSeams(e.sw, nil, nil)
	e.advance(store.MarkStale)
	if q, err := e.sw.QueuePass(e.ctx, false); err != nil || q != (sweep.QueueStats{Removed: 1}) {
		t.Fatalf("next pass: %+v, %v", q, err)
	}
	if e.queued() != 0 {
		t.Fatal("the marked row stayed")
	}
	// The retry of a writer stores a new copy and succeeds.
	if err := e.create(e.alice, e.put("crash two")); err != nil {
		t.Fatal(err)
	}
}

// Two queue passes at once (serve's sweeper and ./pgo sweep) in the order
// of the review of #136: A marks H, B takes H too, A removes the file and
// deletes the row, a writer stores the file again and commits its
// document, then B would remove that file. The sweep lock keeps B out
// while A runs (B does nothing and says so), and a mark of another pass is
// never taken over, so the committed document keeps its file.
func TestConcurrentQueuePassesKeepACommittedFile(t *testing.T) {
	e := newFileEnv(t)
	other, err := store.Open(e.ctx, e.url, e.blobs)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	h := e.queue("contended")
	a := e.sw
	b := &sweep.Sweeper{Store: other, Blobs: e.blobs, Now: e.now}
	aMarked, aGo := make(chan struct{}), make(chan struct{})
	sweep.SetQueueSeams(a, func(string) {
		close(aMarked)
		<-aGo
	}, nil)
	bMarked, bGo := make(chan struct{}), make(chan struct{})
	sweep.SetQueueSeams(b, func(string) {
		close(bMarked)
		<-bGo
	}, nil)

	aDone := make(chan error, 1)
	go func() {
		_, err := a.QueuePass(e.ctx, false)
		aDone <- err
	}()
	<-aMarked
	bDone := make(chan error, 1)
	var bStats sweep.QueueStats
	go func() {
		var err error
		bStats, err = b.QueuePass(e.ctx, false)
		bDone <- err
	}()
	bFinished := false
	select {
	case <-bMarked: // without the fix: B marked H as well and waits before its Remove
	case err := <-bDone:
		bFinished = true
		if !errors.Is(err, store.ErrSweepRunning) || bStats != (sweep.QueueStats{}) {
			t.Errorf("the second pass: %+v, %v; want ErrSweepRunning and nothing done", bStats, err)
		}
	}
	close(aGo)
	if err := <-aDone; err != nil {
		t.Fatal(err)
	}
	if e.blobs.Exists(h) || e.queued() != 0 {
		t.Fatal("A did not finish")
	}
	// A writer stores the file again and commits.
	if _, created, err := e.blobs.Put(strings.NewReader("contended")); err != nil || !created {
		t.Fatalf("Put: %v, %v", created, err)
	}
	if err := e.create(e.alice, h); err != nil {
		t.Fatal(err)
	}
	close(bGo)
	if !bFinished {
		<-bDone
	}
	if !e.blobs.Exists(h) {
		t.Fatal("a committed document's file was removed by the second pass")
	}
	// Once A is done, B may run: there is nothing left for it.
	if q, err := b.QueuePass(e.ctx, false); err != nil || q != (sweep.QueueStats{}) {
		t.Errorf("B afterwards: %+v, %v", q, err)
	}
}

// A file named only by a trashed, not yet purged document is in use: the
// queue pass keeps it (a purge of another document that shared it queued
// it) and drops its row, the walk does not count it, and the document is
// restored with its files.
func TestTrashedDocumentKeepsItsFiles(t *testing.T) {
	e := newFileEnv(t)
	e.bind()
	trashed := e.doc(e.alice, "trashed original", "trashed archive", "trashed thumb")
	// Bob has the same original and his document is purged: it queues
	// the hash, which alice's trashed document still names.
	bobs := e.doc(e.bob, "trashed original", "", "")
	e.purge(e.bob, bobs.ID)
	if err := e.st.TrashDocument(e.ctx, e.alice, trashed.ID); err != nil {
		t.Fatal(err)
	}
	// The other two hashes are queued as a stray walk or purge would.
	if _, err := e.conn.Exec(e.ctx, `INSERT INTO blob_removals (sha256, due_at) VALUES ($1, $3), ($2, $3)`,
		*trashed.ArchiveSHA256, *trashed.ThumbnailSHA256, e.now().Add(store.BlobGrace)); err != nil {
		t.Fatal(err)
	}
	if st, err := e.sw.RunOnce(e.ctx); err != nil || st != (sweep.Stats{Purged: 1}) {
		t.Fatalf("row pass: %+v, %v", st, err)
	}
	e.advance(2 * store.BlobGrace)
	st, err := e.sw.RunOnce(e.ctx)
	if err != nil || st.Queue != (sweep.QueueStats{InUse: 3}) {
		t.Fatalf("queue pass past the grace: %+v, %v", st, err)
	}
	if w, err := e.sw.Walk(e.ctx, sweep.WalkOptions{}); err != nil || w.Blobs != 3 || w.Candidates != 0 || w.Queued != 0 {
		t.Fatalf("walk: %+v, %v", w, err)
	}
	e.advance(2 * store.BlobGrace)
	if st, err := e.sw.RunOnce(e.ctx); err != nil || st != (sweep.Stats{}) {
		t.Fatalf("pass after the walk: %+v, %v", st, err)
	}
	for _, c := range []string{"trashed original", "trashed archive", "trashed thumb"} {
		if !e.exists(c) {
			t.Errorf("%s was removed", c)
		}
	}
	d, err := e.st.RestoreDocument(e.ctx, e.alice, trashed.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{d.OriginalSHA256, *d.ArchiveSHA256, *d.ThumbnailSHA256} {
		if !e.blobs.Exists(h) {
			t.Errorf("the restored document's file %s is gone", h)
		}
	}
}

// A dry run of the queue pass counts and changes nothing.
func TestQueuePassDryRun(t *testing.T) {
	e := newFileEnv(t)
	free := e.put("free")
	used := e.doc(e.alice, "used", "", "").OriginalSHA256
	if _, err := e.conn.Exec(e.ctx, `INSERT INTO blob_removals (sha256, due_at) VALUES ($1, $3), ($2, $3)`, free, used, e.now()); err != nil {
		t.Fatal(err)
	}
	if q, err := e.sw.QueuePass(e.ctx, true); err != nil || q != (sweep.QueueStats{Removed: 1, InUse: 1}) {
		t.Fatalf("dry run: %+v, %v", q, err)
	}
	if !e.blobs.Exists(free) || e.queued() != 2 {
		t.Error("the dry run changed something")
	}
}

// The walk queues an unreferenced file (due after the grace, so the queue
// pass leaves it for an hour), leaves a file that a writer names within
// the grace, removes a temporary file of Put older than a day and keeps a
// younger one, and reports symlinks and odd names without touching them.
// A dry run changes nothing.
func TestWalk(t *testing.T) {
	e := newFileEnv(t)
	e.bind()
	e.doc(e.alice, "kept", "", "")
	orphan := e.put("orphan")
	late := e.put("late")
	root := e.blobs.Root()
	oldTemp, newTemp := filepath.Join(root, "tmp", "put-old"), filepath.Join(root, "tmp", "put-new")
	for _, p := range []string{oldTemp, newTemp} {
		if err := os.WriteFile(p, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(oldTemp, e.now().Add(-25*time.Hour), e.now().Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newTemp, e.now().Add(-time.Hour), e.now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	odd := filepath.Join(root, "documents", "odd.pdf")
	if err := os.WriteFile(odd, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "documents", "ab")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}

	dry, err := e.sw.Walk(e.ctx, sweep.WalkOptions{DryRun: true})
	if err != nil || dry != (sweep.WalkStats{Blobs: 3, Candidates: 2, Temp: 1, Unexpected: 2}) {
		t.Fatalf("dry run: %+v, %v", dry, err)
	}
	if e.queued() != 0 {
		t.Fatal("the dry run queued")
	}
	if _, err := os.Stat(oldTemp); err != nil {
		t.Fatal("the dry run removed a temporary file")
	}

	w, err := e.sw.Walk(e.ctx, sweep.WalkOptions{})
	if err != nil || w != (sweep.WalkStats{Blobs: 3, Candidates: 2, Queued: 2, Temp: 1, Unexpected: 2}) {
		t.Fatalf("walk: %+v, %v", w, err)
	}
	if _, err := os.Stat(oldTemp); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old temporary file: %v", err)
	}
	for _, p := range []string{newTemp, odd, link} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if !strings.Contains(e.logs.String(), "odd.pdf") || !strings.Contains(e.logs.String(), "symlink") {
		t.Errorf("unexpected entries not logged:\n%s", e.logs)
	}
	// A second walk counts no candidate twice: queued files are not new.
	if w, err := e.sw.Walk(e.ctx, sweep.WalkOptions{}); err != nil || w.Candidates != 0 || w.Queued != 0 {
		t.Errorf("second walk: %+v, %v", w, err)
	}
	// Within the grace a writer names one of them.
	if q, err := e.sw.QueuePass(e.ctx, false); err != nil || q != (sweep.QueueStats{}) {
		t.Fatalf("queue pass within the grace: %+v, %v", q, err)
	}
	if _, err := e.st.CreateDocument(e.ctx, e.bob, store.NewDocument{Title: "late", Source: store.SourceFile,
		OriginalSHA256: late, OriginalMIME: "application/pdf"}); err != nil {
		t.Fatal(err)
	}
	e.advance(store.BlobGrace)
	if q, err := e.sw.QueuePass(e.ctx, false); err != nil || q != (sweep.QueueStats{Removed: 1}) {
		t.Fatalf("queue pass after the grace: %+v, %v", q, err)
	}
	if e.blobs.Exists(orphan) || !e.blobs.Exists(late) || e.queued() != 0 {
		t.Errorf("orphan %v, late %v, %d rows", e.blobs.Exists(orphan), e.blobs.Exists(late), e.queued())
	}
}

// Each guard of the walk refuses and queues nothing: no marker, a marker
// of another installation, an empty database with files, and a run over
// 10 % of the files (with more than 100 of them). --force overrides the
// last two, never the binding.
func TestWalkRefusals(t *testing.T) {
	t.Run("no marker", func(t *testing.T) {
		e := newFileEnv(t)
		e.put("orphan")
		for _, force := range []bool{false, true} {
			if _, err := e.sw.Walk(e.ctx, sweep.WalkOptions{Force: force}); !errors.Is(err, sweep.ErrUnbound) {
				t.Errorf("force %v: %v", force, err)
			}
		}
		if e.queued() != 0 {
			t.Error("queued")
		}
	})
	t.Run("another installation", func(t *testing.T) {
		e := newFileEnv(t)
		e.put("orphan")
		if err := os.WriteFile(filepath.Join(e.blobs.Root(), sweep.MarkerFile),
			[]byte("00000000-0000-4000-8000-000000000000\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, force := range []bool{false, true} {
			if _, err := e.sw.Walk(e.ctx, sweep.WalkOptions{Force: force}); !errors.Is(err, sweep.ErrOtherInstallation) {
				t.Errorf("force %v: %v", force, err)
			}
		}
		if e.queued() != 0 {
			t.Error("queued")
		}
	})
	t.Run("empty database", func(t *testing.T) {
		e := newFileEnv(t)
		e.bind()
		e.put("orphan")
		w, err := e.sw.Walk(e.ctx, sweep.WalkOptions{})
		if !errors.Is(err, sweep.ErrEmptyDatabase) || w.Blobs != 1 || w.Candidates != 1 || e.queued() != 0 {
			t.Fatalf("walk: %+v, %v, %d queued", w, err, e.queued())
		}
		if w, err := e.sw.Walk(e.ctx, sweep.WalkOptions{Force: true}); err != nil || w.Queued != 1 {
			t.Fatalf("forced: %+v, %v", w, err)
		}
	})
	t.Run("over 10 %", func(t *testing.T) {
		e := newFileEnv(t)
		e.bind()
		e.doc(e.alice, "kept", "", "")
		for i := range sweep.MinRefusal + 1 {
			e.put(fmt.Sprint("orphan ", i))
		}
		w, err := e.sw.Walk(e.ctx, sweep.WalkOptions{})
		if !errors.Is(err, sweep.ErrTooMany) || w.Candidates != sweep.MinRefusal+1 || e.queued() != 0 {
			t.Fatalf("walk: %+v, %v, %d queued", w, err, e.queued())
		}
		if !sweep.Refused(err) {
			t.Error("not a refusal")
		}
		if w, err := e.sw.Walk(e.ctx, sweep.WalkOptions{Force: true}); err != nil || w.Queued != sweep.MinRefusal+1 {
			t.Fatalf("forced: %+v, %v", w, err)
		}
	})
	t.Run("up to 100 files is no refusal", func(t *testing.T) {
		e := newFileEnv(t)
		e.bind()
		e.doc(e.alice, "kept", "", "")
		for i := range sweep.MinRefusal {
			e.put(fmt.Sprint("orphan ", i))
		}
		if w, err := e.sw.Walk(e.ctx, sweep.WalkOptions{}); err != nil || w.Queued != sweep.MinRefusal {
			t.Fatalf("walk: %+v, %v", w, err)
		}
	})
}

// migrate binds only a fresh data directory; --bind needs documents and
// --force to replace another installation's marker.
func TestBinding(t *testing.T) {
	e := newFileEnv(t)
	inst, err := e.st.Installation(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(t.TempDir(), "data")
	if res, err := sweep.BindFresh(fresh, inst.ID); err != nil || res != sweep.Bound {
		t.Fatalf("fresh: %v, %v", res, err)
	}
	if err := sweep.CheckBinding(fresh, inst.ID); err != nil {
		t.Fatalf("after binding: %v", err)
	}
	if res, err := sweep.BindFresh(fresh, inst.ID); err != nil || res != sweep.AlreadyBound {
		t.Fatalf("again: %v, %v", res, err)
	}
	other := "00000000-0000-4000-8000-000000000000"
	if res, err := sweep.BindFresh(fresh, other); !errors.Is(err, sweep.ErrOtherInstallation) || res != sweep.BoundElsewhere {
		t.Fatalf("another installation: %v, %v", res, err)
	}

	e.put("a file")
	root := e.blobs.Root()
	if res, err := sweep.BindFresh(root, inst.ID); err != nil || res != sweep.LeftUnbound {
		t.Fatalf("with files: %v, %v", res, err)
	}
	if _, err := sweep.Bind(e.ctx, e.st, root, false, false); !errors.Is(err, sweep.ErrEmptyDatabase) {
		t.Fatalf("bind without documents: %v", err)
	}
	if err := sweep.CheckBinding(root, inst.ID); !errors.Is(err, sweep.ErrUnbound) {
		t.Fatalf("a refused bind wrote the marker: %v", err)
	}
	e.doc(e.alice, "a document", "", "")
	if wrote, err := sweep.Bind(e.ctx, e.st, root, false, true); err != nil || !wrote {
		t.Fatalf("dry run: %v, %v", wrote, err)
	}
	if err := sweep.CheckBinding(root, inst.ID); !errors.Is(err, sweep.ErrUnbound) {
		t.Fatalf("the dry run wrote the marker: %v", err)
	}
	if wrote, err := sweep.Bind(e.ctx, e.st, root, false, false); err != nil || !wrote {
		t.Fatalf("bind: %v, %v", wrote, err)
	}
	if wrote, err := sweep.Bind(e.ctx, e.st, root, false, false); err != nil || wrote {
		t.Fatalf("bind again: %v, %v", wrote, err)
	}
	if err := os.WriteFile(filepath.Join(root, sweep.MarkerFile), []byte(other+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := sweep.Bind(e.ctx, e.st, root, false, false); !errors.Is(err, sweep.ErrOtherInstallation) {
		t.Fatalf("bind over another installation: %v", err)
	}
	if wrote, err := sweep.Bind(e.ctx, e.st, root, true, false); err != nil || !wrote {
		t.Fatalf("forced bind: %v, %v", wrote, err)
	}
	if err := sweep.CheckBinding(root, inst.ID); err != nil {
		t.Fatalf("after the forced bind: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, sweep.MarkerFile), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sweep.CheckBinding(root, inst.ID); err == nil || sweep.Refused(err) {
		t.Errorf("a broken marker: %v", err)
	}
}

// Run walks at the first round and then once a day on its clock; while
// the data directory is unbound the walk only warns.
func TestRunWalksDaily(t *testing.T) {
	e := newFileEnv(t)
	orphan := e.put("orphan")
	walks := make(chan struct{}, 8)
	sweep.SetWalkSeam(e.sw, func() { walks <- struct{}{} })
	tick := make(chan time.Time)
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() {
		e.sw.Run(ctx, tick, nil)
		close(done)
	}()
	<-walks // the first round walks
	// A send returns once Run waits for the tick, so after two the round
	// of the first one has run, without a walk on an unchanged clock.
	tick <- e.now()
	tick <- e.now()
	if len(walks) != 0 {
		t.Fatal("walked twice within a day")
	}
	e.bind()
	e.doc(e.alice, "kept", "", "")
	e.advance(sweep.WalkInterval)
	tick <- e.now()
	<-walks
	tick <- e.now() // the round of that walk has finished
	cancel()
	<-done
	var n int
	if err := e.conn.QueryRow(e.ctx, `SELECT count(*) FROM blob_removals WHERE sha256 = $1`, orphan).Scan(&n); err != nil || n != 1 {
		t.Errorf("the daily walk queued %d, %v", n, err)
	}
	if !strings.Contains(e.logs.String(), "not bound") {
		t.Errorf("no warning while unbound:\n%s", e.logs)
	}
}

// The queue pass checks the data directory's marker first: with a
// database of another installation (a wrong PAPERLESS_GO_DATABASE_URL, a
// dump of another installation) it removes nothing, also in a dry run,
// though that database queued the hash. An unbound data directory is
// fine: the queue names only what this database queued.
func TestQueuePassRefusesAnotherInstallation(t *testing.T) {
	e := newFileEnv(t)
	e.bind() // bound to e's installation
	h := e.put("the real installation's file")
	other, _ := storetest.NewWithOptions(t, storetest.Options{Blobs: e.blobs})
	if _, err := other.QueueUnreferencedBlobs(e.ctx, []string{h}, e.now()); err != nil {
		t.Fatal(err)
	}
	sw := &sweep.Sweeper{Store: other, Blobs: e.blobs, Now: e.now}
	for _, dry := range []bool{true, false} {
		if q, err := sw.QueuePass(e.ctx, dry); !errors.Is(err, sweep.ErrOtherInstallation) || q != (sweep.QueueStats{}) {
			t.Fatalf("dry run %v: %+v, %v; want ErrOtherInstallation", dry, q, err)
		}
	}
	if due, err := other.BlobRemovalsDue(e.ctx, e.now(), 10); err != nil || len(due) != 1 || !e.blobs.Exists(h) {
		t.Fatalf("after the refusal: rows %v, %v, file %v", due, err, e.blobs.Exists(h))
	}
	// Unbound, the same pass removes the file this database queued.
	if err := os.Remove(filepath.Join(e.blobs.Root(), sweep.MarkerFile)); err != nil {
		t.Fatal(err)
	}
	if q, err := sw.QueuePass(e.ctx, false); err != nil || q != (sweep.QueueStats{Removed: 1}) {
		t.Fatalf("unbound: %+v, %v", q, err)
	}
}
