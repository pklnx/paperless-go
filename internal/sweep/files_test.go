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
		t: t, ctx: ctx, st: st, blobs: blobs, conn: conn, logs: &syncBuf{},
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

// failingRemove fails the first Remove of each hash with err, or cancels
// a context right after a Remove.
type failingRemove struct {
	*blobstore.Store
	failed map[string]bool
}

func (f *failingRemove) Remove(hash string) error {
	if !f.failed[hash] {
		f.failed[hash] = true
		return errors.New("disk on fire")
	}
	return f.Store.Remove(hash)
}

// Crash seams: a removal that fails after step 1 leaves the row marked
// (writers of the hash fail meanwhile) and the next pass finishes it; a
// pass stopped after step 2 leaves the marked row of a removed file, and
// the next pass deletes it (Remove of a file already gone is fine).
func TestQueuePassCrashSeams(t *testing.T) {
	e := newFileEnv(t)
	h := e.put("crash one")
	if _, err := e.st.QueueUnreferencedBlobs(e.ctx, []string{h}, e.now()); err != nil {
		t.Fatal(err)
	}
	e.sw.Blobs = &failingRemove{Store: e.blobs, failed: map[string]bool{}}
	if q, err := e.sw.QueuePass(e.ctx, false); err != nil || q != (sweep.QueueStats{Failed: 1}) {
		t.Fatalf("failing remove: %+v, %v", q, err)
	}
	if !e.blobs.Exists(h) || e.queued() != 1 || !strings.Contains(e.logs.String(), "disk on fire") {
		t.Fatalf("after the failed remove: file %v, %d rows, log %s", e.blobs.Exists(h), e.queued(), e.logs)
	}
	in := store.NewDocument{Title: "x", Source: store.SourceFile, OriginalSHA256: h, OriginalMIME: "application/pdf"}
	if _, err := e.st.CreateDocument(e.ctx, e.alice, in); !errors.Is(err, store.ErrBlobRemoved) {
		t.Fatalf("a writer while the row is marked: %v", err)
	}
	if q, err := e.sw.QueuePass(e.ctx, false); err != nil || q != (sweep.QueueStats{Removed: 1}) {
		t.Fatalf("next pass: %+v, %v", q, err)
	}
	if e.blobs.Exists(h) || e.queued() != 0 {
		t.Fatal("the next pass did not finish")
	}

	h = e.put("crash two")
	if _, err := e.st.QueueUnreferencedBlobs(e.ctx, []string{h}, e.now()); err != nil {
		t.Fatal(err)
	}
	e.sw.Blobs = e.blobs
	ctx, cancel := context.WithCancel(e.ctx)
	sweep.SetQueueSeams(e.sw, nil, func(string) { cancel() })
	if _, err := e.sw.QueuePass(ctx, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("pass cancelled after step 2: %v", err)
	}
	if e.blobs.Exists(h) || e.queued() != 1 {
		t.Fatalf("after step 2: file %v, %d rows", e.blobs.Exists(h), e.queued())
	}
	sweep.SetQueueSeams(e.sw, nil, nil)
	if q, err := e.sw.QueuePass(e.ctx, false); err != nil || q != (sweep.QueueStats{Removed: 1}) {
		t.Fatalf("next pass: %+v, %v", q, err)
	}
	if e.queued() != 0 {
		t.Fatal("the marked row stayed")
	}
	// The retry of a writer stores a new copy and succeeds.
	if _, err := e.st.CreateDocument(e.ctx, e.alice, store.NewDocument{Title: "y", Source: store.SourceFile,
		OriginalSHA256: e.put("crash two"), OriginalMIME: "application/pdf"}); err != nil {
		t.Fatal(err)
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
	e.sw.Store = &countingStore{Store: e.st, walks: walks}
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

// countingStore reports the start of each walk (its Installation call).
type countingStore struct {
	*store.Store
	walks chan struct{}
}

func (c *countingStore) Installation(ctx context.Context) (*store.Installation, error) {
	c.walks <- struct{}{}
	return c.Store.Installation(ctx)
}
