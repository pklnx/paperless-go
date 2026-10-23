package sweep_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
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

// RunOnce loops over batches until nothing is due: 250 documents whose
// retention passed go in one call, a document still within it stays, and
// every purge is audited as a retention purge.
func TestRunOnceLoopsBatches(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := storetest.User(t, st, "alice", false)
	bob := storetest.User(t, st, "bob", false)
	now := time.Now().Truncate(time.Microsecond)
	st.Now = func() time.Time { return now }
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, `
		INSERT INTO documents (owner_id, title, source, original_sha256, original_size, original_mime, deleted_at)
		SELECT CASE WHEN i % 2 = 0 THEN $1::bigint ELSE $2::bigint END, 'doc ' || i, 'file', encode(sha256(i::text::bytea), 'hex'), 1,
		       'application/pdf', CASE WHEN i <= 250 THEN $3::timestamptz - interval '31 days' ELSE $3::timestamptz - interval '1 day' END
		FROM generate_series(1, 251) i`, alice.ID, bob.ID, now); err != nil {
		t.Fatal(err)
	}
	s := &sweep.Sweeper{Store: st, Blobs: newBlobs(t), Now: st.Now, Retention: store.DefaultTrashRetention}
	got, err := s.RunOnce(ctx)
	if err != nil || got != (sweep.Stats{Purged: 250, Retention: 250}) {
		t.Fatalf("RunOnce: %+v, %v", got, err)
	}
	var left, queued, audited int
	if err := conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM documents), (SELECT count(*) FROM blob_removals),
		(SELECT count(*) FROM audit_log WHERE action = 'document.purge' AND actor = 'system')`).Scan(&left, &queued, &audited); err != nil {
		t.Fatal(err)
	}
	if left != 1 || queued != 250 || audited != 250 {
		t.Errorf("left %d, queued %d, audited %d", left, queued, audited)
	}
	if got, err := s.RunOnce(ctx); err != nil || got != (sweep.Stats{}) {
		t.Errorf("second RunOnce: %+v, %v", got, err)
	}
	// A cancelled context stops before the first batch.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.RunOnce(cancelled); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

// fakeStore answers each PurgeDueDocuments with the next result and
// reports the call.
type fakeStore struct {
	mu      sync.Mutex
	results []fakeResult
	calls   chan time.Time
}

type fakeResult struct {
	res store.PurgeResult
	err error
}

func (f *fakeStore) PurgeDueDocuments(_ context.Context, now time.Time, _ time.Duration, limit int) (store.PurgeResult, error) {
	f.mu.Lock()
	var r fakeResult
	if len(f.results) > 0 {
		r, f.results = f.results[0], f.results[1:]
	}
	f.mu.Unlock()
	if limit != sweep.BatchSize {
		r.err = errors.New("wrong limit")
	}
	f.calls <- now
	return r.res, r.err
}

// The file passes of the fake store find nothing to do.
func (f *fakeStore) BlobRemovalsDue(context.Context, time.Time, int) ([]string, error) {
	return nil, nil
}

// ExclusiveSweep runs fn without a session: the fake queue is empty.
func (f *fakeStore) ExclusiveSweep(_ context.Context, fn func(*store.SweepSession) error) error {
	return fn(nil)
}
func (f *fakeStore) UnreferencedBlobs(context.Context, []string) ([]string, error) {
	return nil, nil
}
func (f *fakeStore) WalkCandidates(context.Context, []string) ([]string, error) { return nil, nil }
func (f *fakeStore) QueueUnreferencedBlobs(context.Context, []string, time.Time) (int64, error) {
	return 0, nil
}
func (f *fakeStore) HasDocuments(context.Context) (bool, error) { return true, nil }
func (f *fakeStore) Installation(context.Context) (*store.Installation, error) {
	return &store.Installation{ID: "00000000-0000-4000-8000-000000000001"}, nil
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Run sweeps at once, at every tick and at every wake; a full batch is
// followed by another at once; an error is logged and the next round
// runs; it ends with its context.
func TestRunTicksWakesAndErrors(t *testing.T) {
	full := store.PurgeResult{Scanned: sweep.BatchSize, Purged: sweep.BatchSize}
	f := &fakeStore{
		calls: make(chan time.Time),
		results: []fakeResult{
			{res: full}, {res: store.PurgeResult{Scanned: 3, Purged: 3}}, // first round: two batches
			{err: errors.New("database down")}, // after the tick
			{},                                 // after the wake
		},
	}
	var logs syncBuf
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	s := &sweep.Sweeper{Store: f, Blobs: newBlobs(t), Log: slog.New(slog.NewTextHandler(&logs, nil)), Now: func() time.Time { return now }}
	tick := make(chan time.Time)
	wake, wakeFn := sweep.Wake()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.Run(ctx, tick, wake)
		close(done)
	}()
	for i := range 2 {
		if got := <-f.calls; !got.Equal(now) {
			t.Errorf("batch %d at %v", i, got)
		}
	}
	tick <- now
	<-f.calls
	wakeFn()
	<-f.calls
	cancel()
	<-done
	out := logs.String()
	if !strings.Contains(out, "purged=103") || !strings.Contains(out, "database down") {
		t.Errorf("log:\n%s", out)
	}
}

// newBlobs is a blob store in a fresh directory.
func newBlobs(t *testing.T) *blobstore.Store {
	t.Helper()
	b, err := blobstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return b
}
