package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/store"
)

// pdf is a generated file that detection takes for a PDF; content makes
// it unique.
func pdf(content string) []byte { return []byte("%PDF-1.7\n% " + content + "\n%%EOF\n") }

// upload posts data as the raw body of an upload, with the file name
// header set to fileName (already encoded) unless it is empty.
func (f *authFixture) upload(path, contentType, fileName string, data []byte) (*http.Response, map[string]any) {
	f.t.Helper()
	return f.uploadBody(path, contentType, fileName, bytes.NewReader(data), int64(len(data)))
}

// uploadBody is upload with a body of the given length; -1 sends it
// chunked, without a Content-Length.
func (f *authFixture) uploadBody(path, contentType, fileName string, body io.Reader, length int64) (*http.Response, map[string]any) {
	f.t.Helper()
	req, _ := http.NewRequest("POST", f.srv.URL+path, body)
	req.ContentLength = length
	if length < 0 {
		req.Body = io.NopCloser(body) // hide the length from the client
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Origin", f.srv.URL)
	if fileName != "" {
		req.Header.Set(fileNameHeader, fileName)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

// uploadOK uploads a PDF and returns the new document's ID.
func (f *authFixture) uploadOK(name string, data []byte) string {
	f.t.Helper()
	resp, out := f.upload("/api/documents", "application/pdf", url.PathEscape(name), data)
	if resp.StatusCode != http.StatusCreated {
		f.t.Fatalf("upload %q: %d %v", name, resp.StatusCode, out)
	}
	id, _ := out["id"].(string)
	if id == "" || resp.Header.Get("Location") != "/api/documents/"+id {
		f.t.Fatalf("upload %q: id %q, Location %q", name, id, resp.Header.Get("Location"))
	}
	return id
}

func (f *authFixture) download(path string) (*http.Response, []byte) {
	f.t.Helper()
	resp, err := f.client.Get(f.srv.URL + path)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return resp, b
}

func listedIDs(t *testing.T, out map[string]any) []string {
	t.Helper()
	docs, ok := out["documents"].([]any)
	if !ok {
		t.Fatalf("no documents in %v", out)
	}
	var ids []string
	for _, d := range docs {
		ids = append(ids, d.(map[string]any)["id"].(string))
	}
	return ids
}

// The uploader owns the new document, also an admin; another user gets 404
// for it and its file, and an admin sees everyone's.
func TestUploadOwnerAndIsolation(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.addUser("alice", "correct horse battery", false)
	f.addUser("bob", "correct horse battery", false)
	root := f.addUser("root", "correct horse battery", true)
	ctx := context.Background()

	a := f.as()
	a.loginOK("alice", "correct horse battery")
	data := pdf("invoice")
	id := a.uploadOK("Rechnung März.pdf", data)
	doc, err := f.st.GetDocument(ctx, store.Scope{UserID: alice.ID}, id)
	if err != nil || doc.OwnerID != alice.ID || !doc.IsInbox || doc.Title != "Rechnung März" ||
		doc.OriginalFileName != "Rechnung März.pdf" || doc.OriginalMIME != "application/pdf" {
		t.Fatalf("alice's upload: %+v, %v", doc, err)
	}
	out := a.expect("GET", "/api/documents/"+id, nil, 200)
	if out["title"] != "Rechnung März" || out["hasArchive"] != false || out["isInbox"] != true {
		t.Fatalf("GET: %v", out)
	}
	if ids := listedIDs(t, a.expect("GET", "/api/documents", nil, 200)); len(ids) != 1 || ids[0] != id {
		t.Fatalf("alice's list: %v", ids)
	}

	b := f.as()
	b.loginOK("bob", "correct horse battery")
	b.expect("GET", "/api/documents/"+id, nil, 404)
	if resp, _ := b.download("/api/documents/" + id + "/file"); resp.StatusCode != 404 {
		t.Fatalf("bob's download: %d", resp.StatusCode)
	}
	if ids := listedIDs(t, b.expect("GET", "/api/documents", nil, 200)); len(ids) != 0 {
		t.Fatalf("bob's list: %v", ids)
	}

	r := f.as()
	r.loginOK("root", "correct horse battery")
	r.expect("GET", "/api/documents/"+id, nil, 200)
	if resp, got := r.download("/api/documents/" + id + "/file"); resp.StatusCode != 200 || !bytes.Equal(got, data) {
		t.Fatalf("admin's download: %d", resp.StatusCode)
	}
	// The admin uploads the same file: a new document of their own, not a
	// duplicate of alice's.
	rootID := r.uploadOK("copy.pdf", data)
	if rootID == id {
		t.Fatal("the admin's upload is alice's document")
	}
	if d, err := f.st.GetDocument(ctx, store.Scope{UserID: root.ID}, rootID); err != nil || d.OwnerID != root.ID {
		t.Fatalf("the admin's upload: %+v, %v", d, err)
	}
	if ids := listedIDs(t, r.expect("GET", "/api/documents", nil, 200)); len(ids) != 2 {
		t.Fatalf("admin's list: %v", ids)
	}
	b.expect("GET", "/api/documents/"+rootID, nil, 404)
	a.expect("GET", "/api/documents/"+rootID, nil, 404)
	// A malformed ID is a missing document too.
	a.expect("GET", "/api/documents/not-a-uuid", nil, 404)
}

func TestUploadDuplicate(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	id := f.uploadOK("letter.pdf", pdf("letter"))
	resp, out := f.upload("/api/documents", "application/octet-stream", "again.pdf", pdf("letter"))
	existing, _ := out["existing"].(map[string]any)
	if resp.StatusCode != 200 || out["duplicate"] != true || existing["id"] != id || existing["title"] != "letter" {
		t.Fatalf("duplicate: %d %v", resp.StatusCode, out)
	}
	if ids := listedIDs(t, f.expect("GET", "/api/documents", nil, 200)); len(ids) != 1 {
		t.Fatalf("documents after a duplicate: %v", ids)
	}
}

// The content decides the type: a PDF declared as PNG is stored as a PDF.
func TestUploadDetectsTypeFromContent(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	resp, out := f.upload("/api/documents", "image/png", "scan.html", pdf("declared as png"))
	if resp.StatusCode != 201 || out["originalMime"] != "application/pdf" || out["source"] != "file" {
		t.Fatalf("mismatched type: %d %v", resp.StatusCode, out)
	}
	// The download carries the detected type and its extension, not the name's.
	dl, _ := f.download("/api/documents/" + out["id"].(string) + "/file")
	if dl.Header.Get("Content-Type") != "application/pdf" ||
		dl.Header.Get("Content-Disposition") != `attachment; filename="scan.pdf"; filename*=UTF-8''scan.pdf` {
		t.Fatalf("download: %q %q", dl.Header.Get("Content-Type"), dl.Header.Get("Content-Disposition"))
	}
}

func TestUploadRefusals(t *testing.T) {
	f := newAuthFixtureOpts(t, Options{MaxUploadSize: 64})
	f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	for _, c := range []struct {
		name   string
		data   []byte
		status int
		reason string
	}{
		{"unsupported", []byte("just some text, no PDF"), 415, "unsupported"},
		{"unreadable PNG", []byte("\x89PNG\r\n\x1a\nbroken"), 422, "unreadable"},
		{"too large", pdf(strings.Repeat("x", 64)), 413, "too_large"},
	} {
		resp, out := f.upload("/api/documents", "application/octet-stream", "f", c.data)
		if resp.StatusCode != c.status || out["reason"] != c.reason || out["error"] == "" {
			t.Errorf("%s: %d %v, want %d %s", c.name, resp.StatusCode, out, c.status, c.reason)
		}
	}
	if ids := listedIDs(t, f.expect("GET", "/api/documents", nil, 200)); len(ids) != 0 {
		t.Fatalf("refused files were imported: %v", ids)
	}
}

// The size limit holds for a chunked body without Content-Length too:
// exactly the limit passes, one byte more is refused; a Content-Length
// above the limit is refused before the body is read.
func TestUploadSizeLimit(t *testing.T) {
	const limit = 128
	f := newAuthFixtureOpts(t, Options{MaxUploadSize: limit})
	f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	exact := pdf("x")
	exact = append(exact, bytes.Repeat([]byte{' '}, limit-len(exact))...)
	if resp, out := f.uploadBody("/api/documents", "application/pdf", "exact", bytes.NewReader(exact), -1); resp.StatusCode != 201 {
		t.Fatalf("exactly the limit, chunked: %d %v", resp.StatusCode, out)
	}
	over := append(pdf("y"), bytes.Repeat([]byte{' '}, limit)...)
	if resp, out := f.uploadBody("/api/documents", "application/pdf", "over", bytes.NewReader(over), -1); resp.StatusCode != 413 || out["reason"] != "too_large" {
		t.Fatalf("over the limit, chunked: %d %v", resp.StatusCode, out)
	}
	// A declared length over the limit is refused without reading: the
	// body here would be far shorter.
	f2 := f.as()
	f2.loginOK("alice", "correct horse battery")
	req := httptest.NewRequest("POST", "http://127.0.0.1/api/documents", bytes.NewReader(pdf("z")))
	req.ContentLength = limit + 1
	req.Header.Set("Content-Type", "application/pdf")
	req.Header.Set("Origin", "http://127.0.0.1")
	u, _ := url.Parse(f2.srv.URL)
	for _, c := range f2.client.Jar.Cookies(u) {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != 413 || !strings.Contains(rec.Body.String(), "too_large") {
		t.Fatalf("Content-Length over the limit: %d %s", rec.Code, rec.Body)
	}
	if ids := listedIDs(t, f.expect("GET", "/api/documents", nil, 200)); len(ids) != 1 {
		t.Fatalf("documents: %v", ids)
	}
}

// errAfter returns data, then fails like a connection that broke.
type errAfter struct {
	data []byte
	err  error
}

func (e *errAfter) Read(p []byte) (int, error) {
	if len(e.data) == 0 {
		return 0, e.err
	}
	n := copy(p, e.data)
	e.data = e.data[n:]
	return n, nil
}

// A body that breaks off is the client's problem: 400, nothing created.
func TestUploadInterrupted(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	req := httptest.NewRequest("POST", "http://127.0.0.1/api/documents", &errAfter{data: pdf("cut"), err: errors.New("connection reset")})
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/pdf")
	req.Header.Set("Origin", "http://127.0.0.1")
	u, _ := url.Parse(f.srv.URL)
	for _, c := range f.client.Jar.Cookies(u) {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "interrupted") {
		t.Fatalf("interrupted upload: %d %s", rec.Code, rec.Body)
	}
	if ids := listedIDs(t, f.expect("GET", "/api/documents", nil, 200)); len(ids) != 0 {
		t.Fatalf("documents: %v", ids)
	}
}

// Only same-origin requests with a file content type reach the upload;
// forms and text, which a cross-site page can send without a preflight,
// answer 415, and JSON routes still take JSON only.
func TestUploadRequestChecks(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	for _, ct := range []string{"multipart/form-data; boundary=x", "text/plain", "application/x-www-form-urlencoded", "application/json", ""} {
		if resp, _ := f.upload("/api/documents", ct, "a.pdf", pdf("form")); resp.StatusCode != 415 {
			t.Errorf("content type %q: %d, want 415", ct, resp.StatusCode)
		}
	}
	req, _ := http.NewRequest("POST", f.srv.URL+"/api/documents", bytes.NewReader(pdf("cross")))
	req.Header.Set("Content-Type", "application/pdf")
	req.Header.Set("Origin", "http://evil.example")
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("cross-origin upload: %d, want 403", resp.StatusCode)
	}
	if resp, _ := f.upload("/api/users", "application/octet-stream", "", []byte(`{"name":"x"}`)); resp.StatusCode != 415 {
		t.Errorf("octet-stream to a JSON route: %d, want 415", resp.StatusCode)
	}
	if resp, _ := f.upload("/api/documents", "application/pdf", "%zz.pdf", pdf("bad name")); resp.StatusCode != 400 {
		t.Errorf("malformed file name: %d, want 400", resp.StatusCode)
	}
	if ids := listedIDs(t, f.expect("GET", "/api/documents", nil, 200)); len(ids) != 0 {
		t.Fatalf("documents: %v", ids)
	}

	// Without a session: 401.
	anon := f.as()
	if resp, out := anon.upload("/api/documents", "application/pdf", "a.pdf", pdf("anon")); resp.StatusCode != 401 || out["error"] != "login required" {
		t.Errorf("without a session: %d %v", resp.StatusCode, out)
	}
}

// Only the upload route takes a file body.
func TestRouteTableBodies(t *testing.T) {
	var files []string
	for _, rt := range (&Server{}).routes() {
		if rt.body == bodyFile {
			files = append(files, rt.pattern)
		}
	}
	if len(files) != 1 || files[0] != "POST /api/documents" {
		t.Fatalf("routes with a file body: %v", files)
	}
	var zero route
	if zero.body != bodyJSON {
		t.Fatal("a route without body is not JSON-only")
	}
}

// Without X-File-Name the title is upload-<time> and the file name stays
// empty; the download is named after the title.
func TestUploadWithoutFileName(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	resp, out := f.upload("/api/documents", "application/pdf", "", pdf("nameless"))
	want := "upload-" + f.clock.Now().UTC().Format("20060102-150405")
	if resp.StatusCode != 201 || out["title"] != want || out["originalFileName"] != "" {
		t.Fatalf("without a name: %d %v, want title %s", resp.StatusCode, out, want)
	}
	// A name that is empty after cleaning counts as missing.
	resp, out = f.upload("/api/documents", "application/pdf", url.PathEscape("dir/\x00\x01"), pdf("nameless 2"))
	if resp.StatusCode != 201 || out["title"] != want || out["originalFileName"] != "" {
		t.Fatalf("empty after cleaning: %d %v", resp.StatusCode, out)
	}
	dl, _ := f.download("/api/documents/" + out["id"].(string) + "/file")
	if got := dl.Header.Get("Content-Disposition"); got != `attachment; filename="`+want+`.pdf"; filename*=UTF-8''`+want+`.pdf` {
		t.Fatalf("Content-Disposition %q", got)
	}
}

func TestDocumentFile(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	data := pdf("contract")
	id := f.uploadOK(`Vertrag "Müller".pdf`, data)

	for _, path := range []string{"/file", "/file?kind=original"} {
		resp, got := f.download("/api/documents/" + id + path)
		if resp.StatusCode != 200 || !bytes.Equal(got, data) {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
		for name, want := range map[string]string{
			"Content-Type":            "application/pdf",
			"Content-Disposition":     `attachment; filename="Vertrag \"M_ller\".pdf"; filename*=UTF-8''Vertrag%20%22M%C3%BCller%22.pdf`,
			"Content-Security-Policy": "sandbox",
			"X-Content-Type-Options":  "nosniff",
		} {
			if got := resp.Header.Get(name); got != want {
				t.Errorf("%s: %s = %q, want %q", path, name, got, want)
			}
		}
	}
	// No archive PDF before processing.
	if resp, _ := f.download("/api/documents/" + id + "/file?kind=archive"); resp.StatusCode != 404 {
		t.Fatalf("archive before processing: %d", resp.StatusCode)
	}
	if resp, _ := f.download("/api/documents/" + id + "/file?kind=thumbnail"); resp.StatusCode != 400 {
		t.Fatalf("unknown kind: %d", resp.StatusCode)
	}
	// Once processed, the archive PDF is served under the original's stem.
	ctx := context.Background()
	archive := pdf("contract with text")
	blob, _, err := f.blobs.Put(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, f.dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, `UPDATE documents SET archive_sha256 = $1, archive_size = $2 WHERE id = $3`,
		blob.SHA256, blob.Size, id); err != nil {
		t.Fatal(err)
	}
	resp, got := f.download("/api/documents/" + id + "/file?kind=archive")
	if resp.StatusCode != 200 || !bytes.Equal(got, archive) ||
		resp.Header.Get("Content-Disposition") != `attachment; filename="Vertrag \"M_ller\".pdf"; filename*=UTF-8''Vertrag%20%22M%C3%BCller%22.pdf` {
		t.Fatalf("archive: %d %q", resp.StatusCode, resp.Header.Get("Content-Disposition"))
	}
}

// An upload writes document.create with the new document in the same
// transaction; without the audit log nothing is created.
func TestUploadAudit(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	n := len(auditLog(t, f.st))
	id := f.uploadOK("Steuer 2025.pdf", pdf("tax"))
	entries := newEntries(t, f.st, n)
	if len(entries) != 1 {
		t.Fatalf("entries: %+v", entries)
	}
	e := entries[0]
	if e.Action != "document.create" || e.Actor != store.ActorSession || *e.UserID != alice.ID ||
		e.TargetType != store.TargetDocument || e.TargetID != id || e.TargetName != "Steuer 2025" || details(t, e)["via"] != "upload" {
		t.Fatalf("entry: %+v %v", e, details(t, e))
	}
	// A duplicate creates nothing and writes nothing.
	f.upload("/api/documents", "application/pdf", "again.pdf", pdf("tax"))
	if got := len(auditLog(t, f.st)); got != n+1 {
		t.Fatalf("entries after a duplicate: %d", got-n)
	}

	breakAudit(t, f.dbURL)
	if resp, out := f.upload("/api/documents", "application/pdf", "other.pdf", pdf("other")); resp.StatusCode != 503 || out["error"] != errAuditUnavailable {
		t.Fatalf("without the audit log: %d %v", resp.StatusCode, out)
	}
	if ids := listedIDs(t, f.expect("GET", "/api/documents", nil, 200)); len(ids) != 1 {
		t.Fatalf("documents without the audit log: %v", ids)
	}
}

func TestUploadFileName(t *testing.T) {
	long := strings.Repeat("ä", 200) // 400 bytes
	for _, c := range []struct{ header, want string }{
		{"", ""},
		{"invoice.pdf", "invoice.pdf"},
		{url.PathEscape("Rechnung März.pdf"), "Rechnung März.pdf"},
		{"a%2Fb%2F..%2Fc.pdf", "c.pdf"},
		{"a%5Cb.pdf", "b.pdf"},
		{"x%00y%0A%1Bz.pdf", "xyz.pdf"},
		{"a%CC%88.pdf", "ä.pdf"}, // decomposed umlaut, NFC
		{"..", ""},
		{"%2F", ""},
		{url.PathEscape(long), strings.Repeat("ä", 127)},
	} {
		got, err := uploadFileName(c.header)
		if err != nil || got != c.want {
			t.Errorf("uploadFileName(%q) = %q, %v; want %q", c.header, got, err, c.want)
		}
	}
	if _, err := uploadFileName("%zz"); err == nil {
		t.Error("malformed encoding accepted")
	}
}

func TestContentDisposition(t *testing.T) {
	for _, c := range []struct{ name, want string }{
		{"a.pdf", `attachment; filename="a.pdf"; filename*=UTF-8''a.pdf`},
		{`say "hi"\.pdf`, `attachment; filename="say \"hi\"\\.pdf"; filename*=UTF-8''say%20%22hi%22%5C.pdf`},
		{"Größe; x=1.pdf", `attachment; filename="Gr__e; x=1.pdf"; filename*=UTF-8''Gr%C3%B6%C3%9Fe%3B%20x%3D1.pdf`},
	} {
		if got := contentDisposition(c.name); got != c.want {
			t.Errorf("contentDisposition(%q) = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestDownloadName(t *testing.T) {
	for _, c := range []struct {
		doc      store.Document
		original string
		archive  string
	}{
		{store.Document{OriginalFileName: "scan.jpeg", OriginalMIME: "image/jpeg"}, "scan.jpg", "scan.pdf"},
		// The stored type decides the extension, not the client's name.
		{store.Document{OriginalFileName: "x.html", OriginalMIME: "application/pdf"}, "x.pdf", "x.pdf"},
		{store.Document{OriginalFileName: "photo.pdf", OriginalMIME: "image/heic"}, "photo.heic", "photo.pdf"},
		{store.Document{OriginalFileName: "a.b.png", OriginalMIME: "image/png"}, "a.b.png", "a.b.pdf"},
		{store.Document{Title: "a/b", OriginalMIME: "image/png"}, "a_b.png", "a_b.pdf"},
		{store.Document{Title: "Stand 2026.03", OriginalMIME: "application/pdf"}, "Stand 2026.03.pdf", "Stand 2026.03.pdf"},
		{store.Document{OriginalFileName: ".pdf", OriginalMIME: "application/pdf"}, "document.pdf", "document.pdf"},
		{store.Document{OriginalMIME: "application/pdf"}, "document.pdf", "document.pdf"},
		// A type the importer never stores gets no extension.
		{store.Document{OriginalFileName: "page.html", OriginalMIME: "text/html"}, "page", "page.pdf"},
	} {
		if got := downloadName(&c.doc, false); got != c.original {
			t.Errorf("%+v original: %q, want %q", c.doc, got, c.original)
		}
		if got := downloadName(&c.doc, true); got != c.archive {
			t.Errorf("%+v archive: %q, want %q", c.doc, got, c.archive)
		}
	}
}

func TestServedType(t *testing.T) {
	for in, want := range map[string]string{
		"application/pdf": "application/pdf", "image/jpeg": "image/jpeg", "image/png": "image/png",
		"image/heic": "image/heic", "image/heif": "image/heif",
		"text/html": "application/octet-stream", "image/svg+xml": "application/octet-stream",
		"application/pdf; charset=utf-8": "application/octet-stream", "": "application/octet-stream",
	} {
		if got := servedType(in); got != want {
			t.Errorf("servedType(%q) = %q, want %q", in, got, want)
		}
	}
}

// A stored type the importer does not produce (here an HTML file from
// elsewhere) is sent as application/octet-stream, without its extension.
func TestDocumentFileUnknownType(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	data := []byte("<html><script>alert(1)</script></html>")
	blob, _, err := f.blobs.Put(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := f.st.CreateDocument(context.Background(), store.Scope{UserID: alice.ID}, store.NewDocument{
		Title: "page", Source: store.SourceFile, OriginalFileName: "page.html", OriginalSHA256: blob.SHA256,
		OriginalSize: blob.Size, OriginalMIME: "text/html", IsInbox: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, got := f.download("/api/documents/" + doc.ID + "/file")
	if resp.StatusCode != 200 || !bytes.Equal(got, data) || resp.Header.Get("Content-Type") != "application/octet-stream" ||
		resp.Header.Get("Content-Disposition") != `attachment; filename="page"; filename*=UTF-8''page` {
		t.Fatalf("unknown type: %d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Content-Disposition"))
	}
}

// The list holds the newest documents, at most limit (default and
// maximum 100), without their text.
func TestListDocumentsLimit(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	ctx := context.Background()
	sc := store.Scope{UserID: alice.ID}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	conn, err := pgx.Connect(ctx, f.dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var newest string
	for i := range 102 {
		in := store.NewDocument{
			Title: fmt.Sprintf("doc %d", i), Source: store.SourceFile, OriginalSHA256: fmt.Sprintf("%064x", i+1),
			OriginalMIME: "application/pdf", ExtractedText: "secret text",
		}
		d, err := f.st.CreateDocument(ctx, sc, in)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, `UPDATE documents SET created_at = $1 WHERE id = $2`, base.Add(time.Duration(i)*time.Minute), d.ID); err != nil {
			t.Fatal(err)
		}
		newest = d.ID
	}
	out := f.expect("GET", "/api/documents", nil, 200)
	if ids := listedIDs(t, out); len(ids) != 100 || ids[0] != newest {
		t.Fatalf("default: %d documents, first %v", len(ids), ids[:1])
	}
	if docs := out["documents"].([]any); docs[0].(map[string]any)["extractedText"] != nil {
		t.Fatalf("the list carries the text: %v", docs[0])
	}
	if ids := listedIDs(t, f.expect("GET", "/api/documents?limit=10", nil, 200)); len(ids) != 10 || ids[0] != newest {
		t.Fatalf("limit=10: %d documents", len(ids))
	}
	if ids := listedIDs(t, f.expect("GET", "/api/documents?limit=1000", nil, 200)); len(ids) != 100 {
		t.Fatalf("limit=1000: %d documents", len(ids))
	}
	for _, bad := range []string{"0", "-1", "x"} {
		f.expect("GET", "/api/documents?limit="+bad, nil, 400)
	}
}

// Without a blob store the document routes answer 503 instead of failing.
func TestDocumentsWithoutBlobStore(t *testing.T) {
	f := newAuthFixture(t)
	s := New(f.st, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{AllowedHosts: []string{"127.0.0.1"}})
	rec := httptest.NewRecorder()
	s.handleListDocuments(rec, httptest.NewRequest("GET", "http://127.0.0.1/api/documents", nil))
	if rec.Code != 503 {
		t.Fatalf("status %d", rec.Code)
	}
}
