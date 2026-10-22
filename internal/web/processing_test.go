package web

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/store"
)

// processing returns the processing status in a document's JSON.
func processing(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	p, ok := doc["processing"].(map[string]any)
	if !ok {
		t.Fatalf("no processing status in %v", doc)
	}
	return p
}

// listedDoc returns the document with id from a list answer.
func listedDoc(t *testing.T, out map[string]any, id string) map[string]any {
	t.Helper()
	for _, d := range out["documents"].([]any) {
		if doc := d.(map[string]any); doc["id"] == id {
			return doc
		}
	}
	t.Fatalf("%s not listed in %v", id, out)
	return nil
}

// statusOf checks the processing status of document id in its own JSON
// and in the list.
func (f *authFixture) statusOf(id string) map[string]any {
	f.t.Helper()
	p := processing(f.t, f.expect("GET", "/api/documents/"+id, nil, 200))
	if l := processing(f.t, listedDoc(f.t, f.expect("GET", "/api/documents", nil, 200), id)); l["state"] != p["state"] || l["error"] != p["error"] {
		f.t.Fatalf("list status %v, document status %v", l, p)
	}
	return p
}

// The document JSON carries the state of its newest job, with the error
// of a failed attempt and when a retry runs.
func TestDocumentProcessingStatus(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	ctx := context.Background()
	now := time.Now().Truncate(time.Microsecond)

	resp, out := f.upload("/api/documents", "application/pdf", "scan.pdf", pdf("scan"))
	if resp.StatusCode != 201 {
		t.Fatalf("upload: %d %v", resp.StatusCode, out)
	}
	id := out["id"].(string)
	if p := processing(t, out); p["state"] != "queued" || p["attempts"] != 0.0 || p["maxAttempts"] != 3.0 || p["error"] != nil {
		t.Fatalf("after the upload: %v", p)
	}
	if out["thumbnailUrl"] != nil {
		t.Fatalf("thumbnail before processing: %v", out["thumbnailUrl"])
	}
	if p := f.statusOf(id); p["state"] != "queued" || p["retryAt"] != nil {
		t.Fatalf("queued: %v", p)
	}

	j, err := f.st.ClaimJob(ctx, now, time.Minute)
	if err != nil || j == nil {
		t.Fatalf("claim: %v, %v", j, err)
	}
	if p := f.statusOf(id); p["state"] != "running" || p["attempts"] != 1.0 {
		t.Fatalf("running: %v", p)
	}
	retryAt := now.Add(time.Minute)
	if err := f.st.RetryJob(ctx, j, retryAt, "pdfinfo failed with exit status 99."); err != nil {
		t.Fatal(err)
	}
	p := f.statusOf(id)
	if at, _ := time.Parse(time.RFC3339Nano, p["retryAt"].(string)); p["state"] != "queued" || p["error"] != "pdfinfo failed with exit status 99." || !at.Equal(retryAt) {
		t.Fatalf("waiting for a retry: %v", p)
	}

	if j, err = f.st.ClaimJob(ctx, retryAt, time.Minute); err != nil || j == nil {
		t.Fatalf("claim: %v, %v", j, err)
	}
	if err := f.st.FailJob(ctx, j, retryAt, "The PDF cannot be read."); err != nil {
		t.Fatal(err)
	}
	if p := f.statusOf(id); p["state"] != "failed" || p["error"] != "The PDF cannot be read." || p["retryAt"] != nil {
		t.Fatalf("failed: %v", p)
	}

	// Done, and later without any job (done jobs are deleted).
	if _, err := f.st.EnqueueDocument(ctx, id); err != nil {
		t.Fatal(err)
	}
	if j, err = f.st.ClaimJob(ctx, retryAt, time.Minute); err != nil || j == nil {
		t.Fatalf("claim: %v, %v", j, err)
	}
	if err := f.st.CompleteJob(ctx, j, retryAt, store.ProcessResult{PageCount: 1}); err != nil {
		t.Fatal(err)
	}
	if p := f.statusOf(id); p["state"] != "done" || p["error"] != nil {
		t.Fatalf("done: %v", p)
	}
	if _, err := f.st.DeleteDoneJobs(ctx, retryAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if p := f.statusOf(id); p["state"] != "done" {
		t.Fatalf("without a job: %v", p)
	}
}

// setThumbnail stores content as the thumbnail of document id.
func (f *authFixture) setThumbnail(id string, content []byte) string {
	f.t.Helper()
	ctx := context.Background()
	b, _, err := f.blobs.Put(bytes.NewReader(content))
	if err != nil {
		f.t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, f.dbURL)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, `UPDATE documents SET thumbnail_sha256 = $1 WHERE id = $2`, b.SHA256, id); err != nil {
		f.t.Fatal(err)
	}
	return b.SHA256
}

func TestThumbnail(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct horse battery", false)
	f.addUser("bob", "correct horse battery", false)
	f.addUser("root", "correct horse battery", true)
	f.loginOK("alice", "correct horse battery")
	id := f.uploadOK("Vertrag.pdf", pdf("contract"))

	if resp, _ := f.download("/api/documents/" + id + "/thumbnail"); resp.StatusCode != 404 {
		t.Fatalf("before processing: %d", resp.StatusCode)
	}
	jpeg := []byte("\xff\xd8\xff\xe0 a JPEG")
	hash := f.setThumbnail(id, jpeg)
	versioned := "/api/documents/" + id + "/thumbnail?v=" + hash[:16]
	if got := f.expect("GET", "/api/documents/"+id, nil, 200)["thumbnailUrl"]; got != versioned {
		t.Fatalf("thumbnailUrl %v, want %s", got, versioned)
	}
	if got := listedDoc(t, f.expect("GET", "/api/documents", nil, 200), id)["thumbnailUrl"]; got != versioned {
		t.Fatalf("listed thumbnailUrl %v", got)
	}

	for path, cache := range map[string]string{
		versioned:                                 "private, max-age=86400, immutable",
		"/api/documents/" + id + "/thumbnail":     "private, no-cache",
		"/api/documents/" + id + "/thumbnail?v=0": "private, no-cache",
	} {
		resp, got := f.download(path)
		if resp.StatusCode != 200 || !bytes.Equal(got, jpeg) {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
		for name, want := range map[string]string{
			"Content-Type":                 "image/jpeg",
			"Cache-Control":                cache,
			"ETag":                         `"` + hash + `"`,
			"Content-Security-Policy":      "sandbox",
			"X-Content-Type-Options":       "nosniff",
			"Cross-Origin-Resource-Policy": "same-origin",
			"Content-Disposition":          "",
		} {
			if got := resp.Header.Get(name); got != want {
				t.Errorf("%s: %s = %q, want %q", path, name, got, want)
			}
		}
	}

	// A revalidation with the ETag answers 304 without the image.
	req, _ := http.NewRequest("GET", f.srv.URL+"/api/documents/"+id+"/thumbnail", nil)
	req.Header.Set("If-None-Match", `"`+hash+`"`)
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("revalidation: %d", resp.StatusCode)
	}

	b := f.as()
	b.loginOK("bob", "correct horse battery")
	if resp, _ := b.download(versioned); resp.StatusCode != 404 {
		t.Fatalf("bob: %d", resp.StatusCode)
	}
	r := f.as()
	r.loginOK("root", "correct horse battery")
	if resp, got := r.download(versioned); resp.StatusCode != 200 || !bytes.Equal(got, jpeg) {
		t.Fatalf("admin: %d", resp.StatusCode)
	}
	anon := f.as()
	if resp, _ := anon.download(versioned); resp.StatusCode != 401 {
		t.Fatalf("without a login: %d", resp.StatusCode)
	}
}

// POST /api/documents/{id}/reprocess queues a document of the scope again
// and writes document.reprocess in the same transaction.
func TestReprocessDocument(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.addUser("alice", "correct horse battery", false)
	f.addUser("bob", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	ctx := context.Background()
	now := time.Now().Truncate(time.Microsecond)
	id := f.uploadOK("Rechnung.pdf", pdf("invoice"))
	path := "/api/documents/" + id + "/reprocess"

	// Queued already: nothing new is queued.
	n := len(auditLog(t, f.st))
	if p := processing(t, f.expect("POST", path, map[string]any{}, 200)); p["state"] != "queued" {
		t.Fatalf("queued: %v", p)
	}
	entries := newEntries(t, f.st, n)
	if len(entries) != 1 || entries[0].Action != "document.reprocess" || *entries[0].UserID != alice.ID ||
		entries[0].TargetType != store.TargetDocument || entries[0].TargetID != id || entries[0].TargetName != "Rechnung" ||
		details(t, entries[0])["selection"] != "ids" || details(t, entries[0])["queued"] != 0.0 {
		t.Fatalf("entries: %+v", entries)
	}

	// Failed: Retry queues it with fresh attempts.
	j, err := f.st.ClaimJob(ctx, now, time.Minute)
	if err != nil || j == nil {
		t.Fatalf("claim: %v, %v", j, err)
	}
	if err := f.st.FailJob(ctx, j, now, "The PDF cannot be read."); err != nil {
		t.Fatal(err)
	}
	n = len(auditLog(t, f.st))
	out := f.expect("POST", path, map[string]any{}, 202)
	if p := processing(t, out); out["id"] != id || p["state"] != "queued" || p["attempts"] != 0.0 || p["error"] != nil {
		t.Fatalf("after Retry: %v", out)
	}
	if entries := newEntries(t, f.st, n); len(entries) != 1 || details(t, entries[0])["queued"] != 1.0 {
		t.Fatalf("entries: %+v", entries)
	}

	// Another user's document and an unknown one answer 404 and write
	// nothing.
	b := f.as()
	b.loginOK("bob", "correct horse battery")
	n = len(auditLog(t, f.st))
	b.expect("POST", path, map[string]any{}, 404)
	b.expect("POST", "/api/documents/00000000-0000-0000-0000-000000000000/reprocess", map[string]any{}, 404)
	b.expect("POST", "/api/documents/x/reprocess", map[string]any{}, 404)
	if got := len(auditLog(t, f.st)); got != n {
		t.Fatalf("entries for refused requests: %d", got-n)
	}

	// Without the audit log nothing is queued.
	if j, err = f.st.ClaimJob(ctx, now, time.Minute); err != nil || j == nil {
		t.Fatalf("claim: %v, %v", j, err)
	}
	if err := f.st.FailJob(ctx, j, now, "The PDF cannot be read."); err != nil {
		t.Fatal(err)
	}
	breakAudit(t, f.dbURL)
	if out := f.expect("POST", path, map[string]any{}, 503); out["error"] != errAuditUnavailable {
		t.Fatalf("without the audit log: %v", out)
	}
	if jobs, err := f.st.DocumentJobs(ctx, id); err != nil || jobs[0].State != store.JobFailed {
		t.Fatalf("jobs without the audit log: %+v, %v", jobs, err)
	}
}
