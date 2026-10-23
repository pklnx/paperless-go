package store_test

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/blobstore"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
	"github.com/pklnx/paperless-go/internal/sweep"
)

// A store needs its blob store: without it the writers of a hash could
// not check that the file is still there.
func TestOpenRequiresBlobs(t *testing.T) {
	_, url := storetest.NewWithURL(t)
	if st, err := store.Open(context.Background(), url, nil); !errors.Is(err, store.ErrNoBlobs) {
		if st != nil {
			st.Close()
		}
		t.Fatalf("Open without blobs: %v", err)
	}
}

// openBlobStore opens another store on dbURL that checks files in blobs,
// with application_name app (for pg_stat_activity and pg_locks).
func openBlobStore(t *testing.T, dbURL, app string, blobs store.Blobs) *store.Store {
	t.Helper()
	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("application_name", app)
	u.RawQuery = q.Encode()
	s, err := store.Open(context.Background(), u.String(), blobs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// blobEnv is a database whose stores check the files of a real blob store,
// a writer store ("writer") and a sweeper on a store of its own
// ("sweeper"), and a fixed clock.
type blobEnv struct {
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	writer *store.Store
	swSt   *store.Store
	sw     *sweep.Sweeper
	blobs  *blobstore.Store
	conn   *pgx.Conn
	url    string
	now    time.Time
	alice  store.Scope
	bob    store.Scope
}

func newBlobEnv(t *testing.T) *blobEnv {
	t.Helper()
	blobs, err := blobstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, dbURL := storetest.NewWithOptions(t, storetest.Options{Blobs: blobs})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	e := &blobEnv{
		t: t, ctx: ctx, st: st, blobs: blobs, conn: connect(t, dbURL), url: dbURL, now: time.Now().Truncate(time.Microsecond),
		writer: openBlobStore(t, dbURL, "writer", blobs), swSt: openBlobStore(t, dbURL, "sweeper", blobs),
		alice: store.Scope{UserID: storetest.User(t, st, "alice", false).ID},
		bob:   store.Scope{UserID: storetest.User(t, st, "bob", false).ID},
	}
	e.sw = &sweep.Sweeper{Store: e.swSt, Blobs: blobs, Now: func() time.Time { return e.now }}
	// Registered last, so it runs first: a failed test's held writer or
	// sweeper lets go before the stores close.
	t.Cleanup(cancel)
	return e
}

// put stores content and returns its hash.
func (e *blobEnv) put(content string) string {
	e.t.Helper()
	b, _, err := e.blobs.Put(strings.NewReader(content))
	if err != nil {
		e.t.Fatal(err)
	}
	return b.SHA256
}

// orphan stores content, queues it due now and returns its hash: a file
// the next queue pass removes unless a writer names it first.
func (e *blobEnv) orphan(content string) string {
	e.t.Helper()
	h := e.put(content)
	if n, err := e.st.QueueUnreferencedBlobs(e.ctx, []string{h}, e.now.Add(-time.Minute)); err != nil || n != 1 {
		e.t.Fatalf("queue %s: %d, %v", h, n, err)
	}
	return h
}

// queued reports whether hash has a queue row.
func (e *blobEnv) queued(hash string) bool {
	e.t.Helper()
	var n int
	if err := e.conn.QueryRow(e.ctx, `SELECT count(*) FROM blob_removals WHERE sha256 = $1`, hash).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n > 0
}

// named reports whether a document names hash.
func (e *blobEnv) named(hash string) bool {
	e.t.Helper()
	var n int
	if err := e.conn.QueryRow(e.ctx, `SELECT count(*) FROM documents
		WHERE original_sha256 = $1 OR archive_sha256 = $1 OR thumbnail_sha256 = $1`, hash).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n > 0
}

// raceWriter is a writer of a hash: prepare runs before the race and
// returns the write, which names hash through st.
type raceWriter struct {
	name    string
	prepare func(e *blobEnv) func(ctx context.Context, st *store.Store, hash string) error
}

var raceWriters = []raceWriter{
	{"CreateDocument", func(e *blobEnv) func(context.Context, *store.Store, string) error {
		return func(ctx context.Context, st *store.Store, hash string) error {
			in := newDoc("upload")
			in.OriginalSHA256 = hash
			_, err := st.CreateDocument(ctx, e.alice, in)
			return err
		}
	}},
	{"CompleteJob", func(e *blobEnv) func(context.Context, *store.Store, string) error {
		in := newDoc("scan")
		in.OriginalSHA256 = e.put("scan original")
		mustDoc(e.t, e.st, e.alice, in)
		j := claim(e.t, e.st, e.now)
		return func(ctx context.Context, st *store.Store, hash string) error {
			return st.CompleteJob(ctx, j, e.now, store.ProcessResult{PageCount: 1, Text: "t", TextSource: store.TextSourceTesseract,
				ArchiveSHA256: hash, ArchiveSize: 1})
		}
	}},
}

// The writer of a hash and the sweeper's removal of its file in every
// order (#12, part 2), for both writers. A writer that names the hash
// either commits with the file on disk, or fails with ErrBlobRemoved and
// writes nothing; storing the file again makes the retry work.
func TestBlobRemovalRaces(t *testing.T) {
	for _, w := range raceWriters {
		// (a) The writer holds its shared blob lock (after its owner
		// lock) while the sweeper's step 1 waits for the exclusive one:
		// the writer commits, the sweeper finds the hash taken and removes
		// nothing.
		t.Run(w.name+"/a writer first", func(t *testing.T) {
			e := newBlobEnv(t)
			write := w.prepare(e)
			h := e.orphan("a")
			held, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			wctx := store.WithAfterBlobHold(e.ctx, func() {
				once.Do(func() {
					close(held)
					select {
					case <-release:
					case <-e.ctx.Done():
					}
				})
			})
			done := startOp(func() error { return write(wctx, e.writer, h) })
			select {
			case <-held:
			case err := <-done:
				t.Fatalf("the writer finished without holding: %v", err)
			}
			var qs sweep.QueueStats
			swept := startOp(func() error {
				var err error
				qs, err = e.sw.QueuePass(e.ctx, false)
				return err
			})
			waitForAdvisory(e.ctx, t, e.conn, "sweeper", 1, swept)
			close(release)
			if err := <-done; err != nil {
				t.Fatalf("writer: %v", err)
			}
			if err := <-swept; err != nil || qs != (sweep.QueueStats{}) {
				t.Fatalf("sweep: %+v, %v", qs, err)
			}
			if !e.blobs.Exists(h) || e.queued(h) || !e.named(h) {
				t.Errorf("file %v, queued %v, named %v; want the file kept and named, the row gone",
					e.blobs.Exists(h), e.queued(h), e.named(h))
			}
		})

		// (b) The sweeper's step 1 committed (the row is marked) while the
		// file is still there, then the writer: ErrBlobRemoved. Between
		// step 2 and 3 (file gone, row marked) the same. After step 3 the
		// writer's retry stores the file again and succeeds.
		t.Run(w.name+"/b marked first", func(t *testing.T) {
			e := newBlobEnv(t)
			write := w.prepare(e)
			h := e.orphan("b")
			if m, err := e.swSt.MarkBlobRemoval(e.ctx, h, e.now); err != nil || m != store.BlobMarked {
				t.Fatalf("mark: %v, %v", m, err)
			}
			if err := write(e.ctx, e.writer, h); !errors.Is(err, store.ErrBlobRemoved) {
				t.Fatalf("write after step 1: %v, want ErrBlobRemoved", err)
			}
			if err := e.blobs.Remove(h); err != nil {
				t.Fatal(err)
			}
			if err := write(e.ctx, e.writer, h); !errors.Is(err, store.ErrBlobRemoved) {
				t.Fatalf("write after step 2: %v, want ErrBlobRemoved", err)
			}
			if err := e.swSt.FinishBlobRemoval(e.ctx, h, e.now); err != nil {
				t.Fatal(err)
			}
			if e.named(h) {
				t.Fatal("a failed write named the hash")
			}
			retry(t, e, write, h, "b")
		})

		// (c) The order of the review: the writer's Put finds the file
		// (created=false), then the sweeper runs steps 1–3 to the end, then
		// the writer's transaction: Exists fails, nothing is written, and
		// the retry succeeds with a new copy.
		t.Run(w.name+"/c put, sweep, insert", func(t *testing.T) {
			e := newBlobEnv(t)
			write := w.prepare(e)
			h := e.orphan("c")
			if _, created, err := e.blobs.Put(strings.NewReader("c")); err != nil || created {
				t.Fatalf("Put: created %v, %v; want the file found", created, err)
			}
			if qs, err := e.sw.QueuePass(e.ctx, false); err != nil || qs != (sweep.QueueStats{Removed: 1}) {
				t.Fatalf("sweep: %+v, %v", qs, err)
			}
			if e.blobs.Exists(h) || e.queued(h) {
				t.Fatal("the sweep left the file or its row")
			}
			if err := write(e.ctx, e.writer, h); !errors.Is(err, store.ErrBlobRemoved) {
				t.Fatalf("write after the sweep: %v, want ErrBlobRemoved", err)
			}
			if e.named(h) {
				t.Fatal("a failed write named the hash")
			}
			retry(t, e, write, h, "c")
		})

		// (d) The sweeper holds the exclusive blob lock while the writer
		// arrives: the writer waits for it and fails after the sweeper's
		// commit.
		t.Run(w.name+"/d sweeper first", func(t *testing.T) {
			e := newBlobEnv(t)
			write := w.prepare(e)
			h := e.orphan("d")
			locked, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			sctx := store.WithAfterBlobLock(e.ctx, func() {
				once.Do(func() {
					close(locked)
					select {
					case <-release:
					case <-e.ctx.Done():
					}
				})
			})
			var qs sweep.QueueStats
			swept := startOp(func() error {
				var err error
				qs, err = e.sw.QueuePass(sctx, false)
				return err
			})
			select {
			case <-locked:
			case err := <-swept:
				t.Fatalf("the sweep finished without locking: %v", err)
			}
			done := startOp(func() error { return write(e.ctx, e.writer, h) })
			waitForAdvisory(e.ctx, t, e.conn, "writer", 1, done)
			close(release)
			if err := <-swept; err != nil || qs != (sweep.QueueStats{Removed: 1}) {
				t.Fatalf("sweep: %+v, %v", qs, err)
			}
			if err := <-done; !errors.Is(err, store.ErrBlobRemoved) {
				t.Fatalf("writer: %v, want ErrBlobRemoved", err)
			}
			if e.blobs.Exists(h) || e.named(h) {
				t.Fatal("the file stayed or the failed write named it")
			}
			retry(t, e, write, h, "d")
		})
	}
}

// retry stores content again (a new copy) and writes hash: it succeeds,
// and the file stays through another queue pass.
func retry(t *testing.T, e *blobEnv, write func(context.Context, *store.Store, string) error, hash, content string) {
	t.Helper()
	if _, created, err := e.blobs.Put(strings.NewReader(content)); err != nil || !created {
		t.Fatalf("Put of the retry: created %v, %v; want a new copy", created, err)
	}
	if err := write(e.ctx, e.writer, hash); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if qs, err := e.sw.QueuePass(e.ctx, false); err != nil || qs != (sweep.QueueStats{}) {
		t.Fatalf("sweep after the retry: %+v, %v", qs, err)
	}
	if !e.blobs.Exists(hash) || !e.named(hash) {
		t.Errorf("after the retry: file %v, named %v", e.blobs.Exists(hash), e.named(hash))
	}
}

// blobLock is one blob lock of a backend in pg_locks.
type blobLock struct {
	key     int32
	mode    string
	granted bool
}

// lockState lists the owner locks (single bigint keys) and the blob locks
// (two int4, class store.BlobLockClass) of application app's backends.
func lockState(t *testing.T, conn *pgx.Conn, app string) (owners []advisoryLock, blobs []blobLock) {
	t.Helper()
	rows, err := conn.Query(context.Background(), `SELECT l.classid::bigint, l.objid::bigint, l.objsubid, l.mode, l.granted
		FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
		WHERE l.locktype = 'advisory' AND a.datname = current_database() AND a.application_name = $1
		ORDER BY l.granted DESC, l.objid`, app)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var hi, lo int64
		var sub int16
		var mode string
		var granted bool
		if err := rows.Scan(&hi, &lo, &sub, &mode, &granted); err != nil {
			t.Fatal(err)
		}
		switch {
		case sub == 1:
			owners = append(owners, advisoryLock{int64(uint64(hi)<<32 | uint64(lo)), granted})
		case sub == 2 && hi == int64(store.BlobLockClass):
			blobs = append(blobs, blobLock{int32(uint32(lo)), mode, granted}) //nolint:gosec // the int4 as stored
		default:
			t.Errorf("unknown advisory lock %d/%d/%d", hi, lo, sub)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return owners, blobs
}

// twoHashes stores two files and returns their hashes in ascending blob
// lock key.
func twoHashes(e *blobEnv, a, b string) (lo, hi string) {
	lo, hi = e.put(a), e.put(b)
	if store.BlobLockKey(lo) == store.BlobLockKey(hi) {
		e.t.Fatal("pick other contents: equal keys")
	}
	if store.BlobLockKey(lo) > store.BlobLockKey(hi) {
		lo, hi = hi, lo
	}
	return lo, hi
}

// holdBlobs takes the blob locks shared, after the owner lock, in
// ascending key, as pg_locks shows: with the higher key held exclusively
// elsewhere, the writer holds its owner lock and the lower key and waits
// for the higher one; with the lower key held, it holds only its owner
// lock.
func TestHoldBlobsLockOrder(t *testing.T) {
	e := newBlobEnv(t)
	lo, hi := twoHashes(e, "first file", "second file")
	other := connect(t, e.url)
	ownerOf := func(sc store.Scope) advisoryLock { return advisoryLock{ownerKey(t, e.conn, sc.UserID), true} }
	write := func(sc store.Scope, title string) chan error {
		in := newDoc(title)
		in.OriginalSHA256, in.ArchiveSHA256, in.ArchiveSize = hi, ptr(lo), ptr(int64(1))
		return startOp(func() error { _, err := e.writer.CreateDocument(e.ctx, sc, in); return err })
	}
	lockKey := func(sql, hash string) {
		if _, err := other.Exec(e.ctx, sql, store.BlobLockClass, store.BlobLockKey(hash)); err != nil {
			t.Fatal(err)
		}
	}

	lockKey(`SELECT pg_advisory_lock($1::int, $2::int)`, hi)
	done := write(e.alice, "alice")
	waitForAdvisory(e.ctx, t, e.conn, "writer", 1, done)
	owners, blobs := lockState(t, e.conn, "writer")
	wantBlobs := []blobLock{{store.BlobLockKey(lo), "ShareLock", true}, {store.BlobLockKey(hi), "ShareLock", false}}
	if !slices.Equal(owners, []advisoryLock{ownerOf(e.alice)}) || !slices.Equal(blobs, wantBlobs) {
		t.Errorf("waiting for the higher key: owner locks %+v, blob locks %+v; want %+v", owners, blobs, wantBlobs)
	}
	lockKey(`SELECT pg_advisory_unlock($1::int, $2::int)`, hi)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	lockKey(`SELECT pg_advisory_lock($1::int, $2::int)`, lo)
	done = write(e.bob, "bob")
	waitForAdvisory(e.ctx, t, e.conn, "writer", 1, done)
	owners, blobs = lockState(t, e.conn, "writer")
	wantBlobs = []blobLock{{store.BlobLockKey(lo), "ShareLock", false}}
	if !slices.Equal(owners, []advisoryLock{ownerOf(e.bob)}) || !slices.Equal(blobs, wantBlobs) {
		t.Errorf("waiting for the lower key: owner locks %+v, blob locks %+v; want %+v", owners, blobs, wantBlobs)
	}
	lockKey(`SELECT pg_advisory_unlock($1::int, $2::int)`, lo)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Each writer of a hash holds exactly the hashes it writes, after its
// owner lock and before the statement that names them.
func TestHashWritersHoldTheirHashes(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	check := func(name, query string, want []string, run func(context.Context) error) {
		t.Helper()
		var events []store.LockEvent
		if err := run(store.WithLockRecorder(ctx, func(e store.LockEvent) { events = append(events, e) })); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		slices.SortFunc(want, func(a, b string) int { return int(store.BlobLockKey(a)) - int(store.BlobLockKey(b)) })
		owners, held, wrote := -1, -1, -1
		for i, e := range events {
			switch {
			case e.Owners != nil:
				owners = i
			case e.Held:
				held = i
				if !slices.Equal(e.Blobs, want) {
					t.Errorf("%s: held %v, want %v", name, e.Blobs, want)
				}
			case e.Query == query:
				wrote = i
			}
		}
		if owners < 0 || held < owners || wrote < held {
			t.Errorf("%s: owner lock at %d, hold at %d, %s at %d; want them in this order", name, owners, held, query, wrote)
		}
	}
	in := newDoc("with archive")
	in.ArchiveSHA256, in.ArchiveSize = ptr(hashOf("its archive")), ptr(int64(1))
	check("CreateDocument", "CreateDocument", []string{in.OriginalSHA256, *in.ArchiveSHA256}, func(ctx context.Context) error {
		_, err := st.CreateDocument(ctx, alice, in)
		return err
	})
	mustDoc(t, st, alice, newDoc("to process"))
	j := claim(t, st, time.Now())
	r := store.ProcessResult{PageCount: 1, TextSource: store.TextSourceTesseract, ArchiveSHA256: hashOf("archive"), ArchiveSize: 1,
		ThumbnailSHA256: hashOf("thumbnail")}
	check("CompleteJob", "SetDocumentProcessed", []string{r.ArchiveSHA256, r.ThumbnailSHA256}, func(ctx context.Context) error {
		return st.CompleteJob(ctx, j, time.Now(), r)
	})
}

// The guard refuses a hash writer without holdBlobs, and holdBlobs before
// the owner lock or twice.
func TestBlobGuard(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := storetest.User(t, st, "alice", false).ID
	sql := `UPDATE documents SET archive_sha256 = archive_sha256 WHERE false`
	if err := store.GuardedExec(ctx, st, sql, [][]int64{{alice}}); !errors.Is(err, store.ErrUnheldBlobWrite) {
		t.Errorf("hash write without the hold: %v", err)
	}
	if err := store.GuardedExec(ctx, st, `UPDATE documents SET title = title WHERE false`, [][]int64{{alice}}); err != nil {
		t.Errorf("a write of other columns: %v", err)
	}
	if err := store.HoldBlobsBeforeOwner(ctx, st); !errors.Is(err, store.ErrBlobsBeforeOwner) {
		t.Errorf("hold before the owner lock: %v", err)
	}
	if err := store.HoldBlobsTwice(ctx, st, alice); !errors.Is(err, store.ErrBlobsHeldTwice) {
		t.Errorf("hold twice: %v", err)
	}
}

// A writer whose file is missing (a store that lost it, or a hash nobody
// stored) fails with ErrBlobRemoved and writes nothing.
func TestHoldBlobsMissingFile(t *testing.T) {
	e := newBlobEnv(t)
	in := newDoc("never stored")
	if _, err := e.st.CreateDocument(e.ctx, e.alice, in); !errors.Is(err, store.ErrBlobRemoved) {
		t.Fatalf("create with a missing file: %v", err)
	}
	if e.named(in.OriginalSHA256) {
		t.Error("the failed create named the hash")
	}
	in.OriginalSHA256 = e.put("never stored")
	if _, err := e.st.CreateDocument(e.ctx, e.alice, in); err != nil {
		t.Fatalf("create after storing the file: %v", err)
	}
}

// A writer that rolls back (a duplicate) rolls back its dequeue too: the
// file stays queued.
func TestRolledBackWriterKeepsTheQueueRow(t *testing.T) {
	e := newBlobEnv(t)
	h := e.put("dup")
	in := newDoc("dup")
	in.OriginalSHA256 = h
	mustDoc(t, e.st, e.alice, in)
	// Queue it the way a purge would, although a document names it.
	if _, err := e.conn.Exec(e.ctx, `INSERT INTO blob_removals (sha256, due_at) VALUES ($1, $2)`, h, e.now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateDocument(e.ctx, e.alice, in); !errors.Is(err, store.ErrDuplicate) {
		t.Fatalf("duplicate: %v", err)
	}
	if !e.queued(h) {
		t.Error("the rolled-back writer took the queue row")
	}
	// The queue pass sees the reference and drops the row; the file stays.
	if qs, err := e.sw.QueuePass(e.ctx, false); err != nil || qs != (sweep.QueueStats{InUse: 1}) {
		t.Fatalf("sweep: %+v, %v", qs, err)
	}
	if !e.blobs.Exists(h) || e.queued(h) {
		t.Error("the file of a live document went, or its row stayed")
	}
}

// MarkBlobRemoval runs at READ COMMITTED also when the database's default
// is REPEATABLE READ: its UPDATE must see a writer that committed while
// the sweeper waited for the hash's lock. Here the hash is queued after
// the writer's dequeue and before its commit; a snapshot from before the
// wait would miss the new document and remove its file.
func TestMarkBlobRemovalReadCommitted(t *testing.T) {
	e := newBlobEnv(t)
	var dbName string
	if err := e.conn.QueryRow(e.ctx, `SELECT current_database()`).Scan(&dbName); err != nil {
		t.Fatal(err)
	}
	if _, err := e.conn.Exec(e.ctx, `ALTER DATABASE `+pgx.Identifier{dbName}.Sanitize()+
		` SET default_transaction_isolation = 'repeatable read'`); err != nil {
		t.Fatal(err)
	}
	rr := openBlobStore(t, e.url, "sweeper-rr", e.blobs)
	if iso, err := store.PoolSetting(e.ctx, rr, "default_transaction_isolation"); err != nil || iso != "repeatable read" {
		t.Fatalf("isolation of the new store: %q, %v", iso, err)
	}
	sw := &sweep.Sweeper{Store: rr, Blobs: e.blobs, Now: func() time.Time { return e.now }}
	h := e.put("late queue")
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	wctx := store.WithAfterBlobHold(e.ctx, func() {
		once.Do(func() {
			close(held)
			select {
			case <-release:
			case <-e.ctx.Done():
			}
		})
	})
	done := startOp(func() error {
		in := newDoc("late queue")
		in.OriginalSHA256 = h
		_, err := e.writer.CreateDocument(wctx, e.alice, in)
		return err
	})
	<-held
	if _, err := e.conn.Exec(e.ctx, `INSERT INTO blob_removals (sha256, due_at) VALUES ($1, $2)`, h, e.now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	var qs sweep.QueueStats
	swept := startOp(func() error {
		var err error
		qs, err = sw.QueuePass(e.ctx, false)
		return err
	})
	waitForAdvisory(e.ctx, t, e.conn, "sweeper-rr", 1, swept)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-swept; err != nil || qs != (sweep.QueueStats{InUse: 1}) {
		t.Fatalf("sweep: %+v, %v", qs, err)
	}
	if !e.blobs.Exists(h) || !e.named(h) {
		t.Error("the new document's file was removed")
	}
}

// failingLookup is a blob store whose lookups fail.
type failingLookup struct{}

func (failingLookup) Present(string) (bool, error) { return false, errors.New("permission denied") }

// A lookup that fails (permissions, I/O) is an error of its own, not a
// file being cleaned up: nothing is written, and the caller does not
// retry as if the sweeper were at work.
func TestHoldBlobsLookupError(t *testing.T) {
	st, _ := storetest.NewWithOptions(t, storetest.Options{Blobs: failingLookup{}})
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	_, err := st.CreateDocument(context.Background(), alice, newDoc("x"))
	if err == nil || errors.Is(err, store.ErrBlobRemoved) || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("create with a failing lookup: %v", err)
	}
}

// NewestDocumentChange is the latest creation, edit or job end of any
// document, none without documents and jobs; a trash changes nothing
// (deleted_at is no change: it writes no file), and a trashed document
// keeps counting.
func TestNewestDocumentChange(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if got, ok, err := st.NewestDocumentChange(ctx); err != nil || ok {
		t.Fatalf("empty database: %v, %v, %v", got, ok, err)
	}
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	doc := mustDoc(t, st, alice, newDoc("first"))
	newest := func(want time.Time, what string) {
		t.Helper()
		got, ok, err := st.NewestDocumentChange(ctx)
		if err != nil || !ok || !got.Equal(want) {
			t.Fatalf("after %s: %v, %v, %v; want %v", what, got, ok, err, want)
		}
	}
	newest(doc.CreatedAt, "the creation")
	patched, err := st.PatchDocument(ctx, alice, doc.ID, store.DocumentPatch{Title: ptr("edited")})
	if err != nil {
		t.Fatal(err)
	}
	newest(patched.ModifiedAt, "an edit")
	// A processing job ends later; its result does not touch modified_at.
	end := patched.ModifiedAt.Add(time.Hour).Truncate(time.Microsecond)
	j, err := st.ClaimJob(ctx, end, time.Minute)
	if err != nil || j == nil {
		t.Fatalf("claim: %+v, %v", j, err)
	}
	if err := st.CompleteJob(ctx, j, end, store.ProcessResult{PageCount: 1, TextSource: store.TextSourceTesseract}); err != nil {
		t.Fatal(err)
	}
	newest(end, "a job's end")
	if err := st.TrashDocument(ctx, alice, doc.ID); err != nil {
		t.Fatal(err)
	}
	newest(end, "a trash")
}
