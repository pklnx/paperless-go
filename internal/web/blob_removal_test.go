package web

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/sweep"
)

// blobFixture is a logged-in alice on a server whose store checks the
// server's blob store, and a sweeper on both whose clock is past the
// grace of everything queued.
func newBlobFixture(t *testing.T) (*authFixture, *sweep.Sweeper, *pgx.Conn) {
	t.Helper()
	f := newAuthFixtureBlobs(t, Options{}, nil, true)
	f.addUser("alice", keyPassword, false)
	f.loginOK("alice", keyPassword)
	sw := &sweep.Sweeper{Store: f.st, Blobs: f.blobs, Now: func() time.Time { return time.Now().Add(2 * store.BlobGrace) },
		Retention: f.st.TrashRetention}
	conn, err := pgx.Connect(context.Background(), f.dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return f, sw, conn
}

// uploadAndPurge uploads data, deletes the document permanently and runs
// the row pass: the file is queued for removal. It returns the hash.
func uploadAndPurge(t *testing.T, f *authFixture, data []byte) string {
	t.Helper()
	id := f.uploadOK("scan.pdf", data)
	doc, err := f.st.GetDocument(context.Background(), store.Scope{All: true}, id)
	if err != nil {
		t.Fatal(err)
	}
	f.expect("DELETE", docPath(id), nil, 204)
	f.expect("POST", docPath(id)+"/purge", nil, 204)
	if _, err := f.st.PurgeDueDocuments(context.Background(), time.Now(), f.st.TrashRetention, 100); err != nil {
		t.Fatal(err)
	}
	return doc.OriginalSHA256
}

// uploadRemoved uploads data and expects the 503 of a file being cleaned
// up.
func uploadRemoved(t *testing.T, f *authFixture, data []byte) {
	t.Helper()
	resp, out := f.upload("/api/documents", "application/pdf", url.PathEscape("again.pdf"), data)
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != "1" ||
		out["error"] != "the file is being cleaned up; try again" {
		t.Fatalf("upload while the file is removed: %d %v %v", resp.StatusCode, resp.Header, out)
	}
}

// An upload of a file whose last document was just purged, while the
// sweeper's step 1 has marked it (the file still there): 503 with
// Retry-After: 1 and nothing stored. Once the sweep finished, the retry
// stores a new copy and succeeds.
func TestUploadWhileTheFileIsMarked(t *testing.T) {
	f, sw, _ := newBlobFixture(t)
	ctx := context.Background()
	data := pdf("purged and uploaded again")
	h := uploadAndPurge(t, f, data)
	if m, err := f.st.MarkBlobRemoval(ctx, h, sw.Now()); err != nil || m != store.BlobMarked {
		t.Fatalf("mark: %v, %v", m, err)
	}
	uploadRemoved(t, f, data)
	if has, err := f.st.HasDocuments(ctx); err != nil || has {
		t.Fatalf("documents after the refused upload: %v, %v", has, err)
	}
	if q, err := sw.QueuePass(ctx, false); err != nil || q.Removed != 1 {
		t.Fatalf("sweep: %+v, %v", q, err)
	}
	if f.blobs.Exists(h) {
		t.Fatal("the file stayed")
	}
	f.uploadOK("again.pdf", data)
	if !f.blobs.Exists(h) {
		t.Error("the retry did not store a new copy")
	}
}

// The order of the review through the upload API: the upload's Put finds
// the file, its transaction waits for the owner lock, the sweeper removes
// the file and its row meanwhile, then the upload's transaction: 503, and
// the retry succeeds with a new copy.
func TestUploadPutSweepInsert(t *testing.T) {
	f, sw, conn := newBlobFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	data := pdf("the review's order")
	h := uploadAndPurge(t, f, data)
	alice, err := f.st.GetUserByName(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	// Alice's owner lock, held by another session: the upload waits for
	// it after its Put.
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('paperless-go:tags:' || $1::bigint::text, 0))`, alice.ID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		resp *http.Response
		out  map[string]any
	}
	done := make(chan result, 1)
	go func() {
		resp, out := f.upload("/api/documents", "application/pdf", url.PathEscape("again.pdf"), data)
		done <- result{resp, out}
	}()
	for {
		var waiting int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND wait_event = 'advisory'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		select {
		case r := <-done:
			t.Fatalf("the upload finished without waiting: %d %v", r.resp.StatusCode, r.out)
		case <-ctx.Done():
			t.Fatal("the upload never waited for the owner lock")
		default:
		}
	}
	if q, err := sw.QueuePass(ctx, false); err != nil || q.Removed != 1 {
		t.Fatalf("sweep: %+v, %v", q, err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock_all()`); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.resp.StatusCode != http.StatusServiceUnavailable || r.resp.Header.Get("Retry-After") != "1" {
		t.Fatalf("upload after the sweep: %d %v", r.resp.StatusCode, r.out)
	}
	if f.blobs.Exists(h) {
		t.Fatal("the file is back without a retry")
	}
	f.uploadOK("again.pdf", data)
	if !f.blobs.Exists(h) {
		t.Error("the retry did not store a new copy")
	}
}
