package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// ownerLockPrefix is the key text of the store's owner lock
// (store.OwnerLockPrefix, visible to store tests only): the tags_no_cycle
// trigger's, migrations/00002_documents.sql.
const ownerLockPrefix = "paperless-go:tags:"

// holdOwnerLock holds owner's write lock in an open transaction of a
// connection outside the store (as another process would) until the
// returned function ends it.
func holdOwnerLock(t *testing.T, ctx context.Context, dbURL string, owner int64) func() {
	t.Helper()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text || $2::bigint::text, 0))`,
		ownerLockPrefix, owner); err != nil {
		t.Fatal(err)
	}
	return func() { _ = tx.Rollback(ctx) }
}

// call sends a JSON request with f's client and returns the status, the
// Retry-After header and the body; unlike f.do it may run in a goroutine.
func (f *authFixture) call(ctx context.Context, method, path string, body any) (int, string, string, error) {
	var r io.Reader = http.NoBody
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, f.srv.URL+path, r)
	if err != nil {
		return 0, "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", f.srv.URL)
	resp, err := f.client.Do(req)
	if err != nil {
		return 0, "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Retry-After"), string(bytes.TrimSpace(b)), err
}

// #117 through the handler: while a holder outside the server holds
// alice's write lock, pool size + 2 of her PATCH requests wait, one of
// them at the lock and the others at the owner gate without a pool
// connection, so /healthz and bob's GET of his document still answer 200.
// Afterwards all of alice's requests succeed.
func TestOtherUserNotDelayedByWaitingRequests(t *testing.T) {
	const pool = 4
	f := newAuthFixtureDB(t, Options{}, map[string]string{"pool_max_conns": strconv.Itoa(pool), "application_name": "web-gate"})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	aliceU := f.addUser("alice", "alice password", false)
	f.addUser("bob", "bob password", false)
	alice, bob := f.as(), f.as()
	alice.loginOK("alice", "alice password")
	bob.loginOK("bob", "bob password")
	aliceDoc := alice.uploadOK("alice.pdf", pdf("alice"))
	bobDoc := bob.uploadOK("bob.pdf", pdf("bob"))
	watch, err := pgx.Connect(ctx, f.dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = watch.Close(context.Background()) }()

	end := holdOwnerLock(t, ctx, f.dbURL, aliceU.ID)
	ended := false
	defer func() {
		if !ended {
			end()
		}
	}()
	n := pool + 2
	done := make(chan int, n)
	for i := range n {
		go func() {
			status, _, _, err := alice.call(ctx, "PATCH", "/api/documents/"+aliceDoc, map[string]any{"notes": "waited " + strconv.Itoa(i)})
			if err != nil {
				t.Errorf("alice's request %d: %v", i, err)
			}
			done <- status
		}()
	}
	// One request waits at the lock, the others at the gate.
	for {
		var atLock int
		if err := watch.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND application_name = 'web-gate'
			  AND wait_event_type = 'Lock' AND wait_event = 'advisory'`).Scan(&atLock); err != nil {
			t.Fatal(err)
		}
		if atLock == 1 && f.st.OwnerGateWaiters() == n-1 {
			break
		}
		select {
		case status := <-done:
			t.Fatalf("a request of alice finished while her lock was held: %d", status)
		case <-ctx.Done():
			t.Fatalf("alice's requests never all waited: %d at the lock, %d at the gate", atLock, f.st.OwnerGateWaiters())
		default:
		}
		runtime.Gosched()
	}

	// The failure bound of the other requests: they need no waiting.
	bctx, bcancel := context.WithTimeout(ctx, 5*time.Second)
	defer bcancel()
	if status, _, body, err := bob.call(bctx, "GET", "/api/documents/"+bobDoc, nil); err != nil || status != http.StatusOK {
		t.Errorf("bob's GET while alice's requests wait: %d %s %v", status, body, err)
	}
	if status, _, body, err := f.call(bctx, "GET", "/healthz", nil); err != nil || status != http.StatusOK {
		t.Errorf("/healthz while alice's requests wait: %d %s %v", status, body, err)
	}

	ended = true
	end()
	for range n {
		if status := <-done; status != http.StatusOK {
			t.Errorf("alice's request: %d, want 200", status)
		}
	}
}

// Through Handler() every request's write is bounded: behind a holder of
// alice's lock outside the server, her PATCH, upload and tag create
// answer 503 with Retry-After: 5 once the bound passes, and write nothing.
func TestOwnerBusyAnswers503(t *testing.T) {
	f := newAuthFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	aliceU := f.addUser("alice", "alice password", false)
	f.loginOK("alice", "alice password")
	doc := f.uploadOK("doc.pdf", pdf("doc"))
	old := ownerWaitBound
	ownerWaitBound = 50 * time.Millisecond
	t.Cleanup(func() { ownerWaitBound = old })
	if ownerWait != 15*time.Second || busyRetryAfter != "5" {
		t.Errorf("bound %v, Retry-After %s; decided were 15s and 5", ownerWait, busyRetryAfter)
	}

	end := holdOwnerLock(t, ctx, f.dbURL, aliceU.ID)
	const want = `{"error":"another change of these documents is still running; try again"}`
	status, retry, body, err := f.call(ctx, "PATCH", "/api/documents/"+doc, map[string]any{"title": "changed"})
	if err != nil || status != http.StatusServiceUnavailable || retry != "5" || body != want {
		t.Errorf("PATCH behind the held lock: %d, Retry-After %q, %s, %v", status, retry, body, err)
	}
	status, retry, body, err = f.call(ctx, "POST", "/api/tags", map[string]any{"name": "new"})
	if err != nil || status != http.StatusServiceUnavailable || retry != "5" || body != want {
		t.Errorf("tag create behind the held lock: %d, Retry-After %q, %s, %v", status, retry, body, err)
	}
	resp, out := f.upload("/api/documents", "application/pdf", url.PathEscape("new.pdf"), pdf("new"))
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != "5" {
		t.Errorf("upload behind the held lock: %d, Retry-After %q, %v", resp.StatusCode, resp.Header.Get("Retry-After"), out)
	}
	end()

	got := f.expect("GET", "/api/documents/"+doc, nil, http.StatusOK)
	if got["title"] != "doc" {
		t.Errorf("the refused PATCH wrote: %v", got["title"])
	}
	list := f.expect("GET", "/api/documents", nil, http.StatusOK)
	if ids := listedIDs(t, list); len(ids) != 1 {
		t.Errorf("documents after the refused upload: %v, want only the first", ids)
	}
	tags := f.expect("GET", "/api/tags", nil, http.StatusOK)
	if b, _ := json.Marshal(tags); bytes.Contains(b, []byte(`"new"`)) {
		t.Errorf("the refused tag create wrote: %s", b)
	}
}
