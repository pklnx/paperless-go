package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

const keyPassword = "correct horse battery"

// createKey creates an API key for the logged-in user of f and returns it.
// The key is in the answer to the creation, and only there.
func (f *authFixture) createKey(name string, scopes []string, code string) string {
	f.t.Helper()
	body := map[string]any{"name": name, "scopes": scopes, "currentPassword": keyPassword}
	if code != "" {
		body["code"] = code
	}
	out := f.expect("POST", "/api/profile/api-keys", body, 201)
	key, _ := out["key"].(string)
	if !strings.HasPrefix(key, "pl_") || len(key) != 55 {
		f.t.Fatalf("created key: %v", out)
	}
	return key
}

// keyRequest sends a request with only an API key: no Origin, and no
// Content-Type unless given, like curl. cookie, if set, is sent too.
func (f *authFixture) keyRequest(key, method, path string, body []byte, contentType string, cookie *http.Cookie) (*http.Response, map[string]any) {
	f.t.Helper()
	var r io.Reader = http.NoBody
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, _ := http.NewRequest(method, f.srv.URL+path, r)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func (f *authFixture) keyStatus(key, method, path string) int {
	f.t.Helper()
	resp, _ := f.keyRequest(key, method, path, nil, "", nil)
	return resp.StatusCode
}

// keyUpload uploads a PDF with a key, like curl --data-binary.
func (f *authFixture) keyUpload(key, name string, data []byte) (*http.Response, map[string]any) {
	f.t.Helper()
	req, _ := http.NewRequest("POST", f.srv.URL+"/api/documents", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/pdf")
	req.Header.Set(fileNameHeader, url.PathEscape(name))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

// sessionCookie returns the session cookie of f's browser.
func (f *authFixture) sessionCookie() *http.Cookie {
	f.t.Helper()
	u, err := url.Parse(f.srv.URL)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, c := range f.client.Jar.Cookies(u) {
		if c.Name == cookieName {
			return c
		}
	}
	f.t.Fatal("no session cookie")
	return nil
}

func (f *authFixture) keyID(name string) int64 {
	f.t.Helper()
	list := f.expect("GET", "/api/profile/api-keys", nil, 200)
	for _, item := range list["keys"].([]any) {
		m := item.(map[string]any)
		if m["name"] == name && m["state"] == "active" {
			return int64(m["id"].(float64))
		}
	}
	f.t.Fatalf("no active key %q in %v", name, list)
	return 0
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// TestAPIKeyAdminWithTwoFactor is the case the keys are for: an admin with
// 2FA uploads and reads documents from a script with a key and nothing
// else. The audit log names the key.
func TestAPIKeyAdminWithTwoFactor(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("admin", keyPassword, true)
	f.loginOK("admin", keyPassword)

	// The password and the second factor are needed to create a key.
	f.expect("POST", "/api/profile/api-keys", map[string]any{"name": "cron", "scopes": []string{"read"}, "currentPassword": keyPassword}, 401)
	f.expect("POST", "/api/profile/api-keys", map[string]any{"name": "cron", "scopes": []string{"read"}, "currentPassword": "wrong wrong wrong", "code": f.code(f.secrets["admin"])}, 401)
	n := len(auditLog(t, f.st))
	key := f.createKey("cron", []string{"read", "write"}, f.code(f.secrets["admin"]))

	data := pdf("scanned")
	resp, out := f.keyUpload(key, "Scan.pdf", data)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: %d %v", resp.StatusCode, out)
	}
	id, _ := out["id"].(string)
	if s := f.keyStatus(key, "GET", "/api/documents/"+id); s != 200 {
		t.Fatalf("read: %d", s)
	}
	resp, _ = f.keyRequest(key, "GET", "/api/documents/"+id+"/file", nil, "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("download: %d", resp.StatusCode)
	}

	entries := newEntries(t, f.st, n)
	var actions []string
	for _, r := range entries {
		actions = append(actions, r.Action)
	}
	if strings.Join(actions, " ") != "api_key.create document.create" {
		t.Fatalf("entries %v", actions)
	}
	if r := entries[0]; r.Actor != store.ActorSession || r.UserName != "admin" || r.TargetType != store.TargetAPIKey ||
		r.TargetName != "cron" || r.TargetID == "" || details(t, r)["scopes"] == nil {
		t.Errorf("create entry %+v %v", r, details(t, r))
	}
	if r := entries[1]; r.Actor != store.ActorAPIKey || r.APIKeyName != "cron" || r.UserName != "admin" || r.TargetID != id {
		t.Errorf("upload entry %+v", r)
	}
	// The key's last use is listed.
	list := f.expect("GET", "/api/profile/api-keys", nil, 200)
	if k := list["keys"].([]any)[0].(map[string]any); k["lastUsedAt"] == nil || k["key"] != nil {
		t.Errorf("listed key %v", k)
	}
	// The log page shows the key's name.
	page := f.expect("GET", "/api/audit?action=document.create", nil, 200)
	if e := auditEntries(t, page); len(e) != 1 || e[0]["actor"] != "api_key" || e[0]["apiKey"] != "cron" {
		t.Errorf("log page %v", e)
	}
}

// TestAPIKeyConflictKeepsRecoveryCode checks the name before the second
// factor: a creation that fails on a taken name does not use up the
// recovery code it was sent with.
func TestAPIKeyConflictKeepsRecoveryCode(t *testing.T) {
	f := newAuthFixture(t)
	u := f.addUserWithout2FA("kim", keyPassword, false)
	_, codes := f.enableTOTP(u)
	f.loginOK("kim", keyPassword)

	f.createKey("cron", []string{"read"}, codes[0])
	body := func(name string) map[string]any {
		return map[string]any{"name": name, "scopes": []string{"read"}, "currentPassword": keyPassword, "code": codes[1]}
	}
	f.expect("POST", "/api/profile/api-keys", body("cron"), 409)
	// The code still works, once.
	f.expect("POST", "/api/profile/api-keys", body("backup"), 201)
	f.expect("POST", "/api/profile/api-keys", body("third"), 401)
}

// TestAPIKeyRouteAccess calls every route of the table with a key of
// every scope combination. Driven by routes(), so new routes are covered
// as they come: a key works only on read, upload and write routes with the
// matching scope (write includes upload), never on login, profile, user,
// key or audit routes; a refusal names the missing scope in the body and
// in WWW-Authenticate.
func TestAPIKeyRouteAccess(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("kim", keyPassword, false)
	f.loginOK("kim", keyPassword)
	combos := [][]string{
		{"read"}, {"upload"}, {"write"}, {"read", "upload"}, {"read", "write"}, {"upload", "write"}, {"read", "upload", "write"},
	}
	type namedKey struct {
		scopes []string
		key    string
	}
	var keys []namedKey
	for _, c := range combos {
		keys = append(keys, namedKey{c, f.createKey(strings.Join(c, "+"), c, "")})
	}
	params := strings.NewReplacer("{$}", "", "{name}", "nothing", "{id}", "00000000-0000-0000-0000-000000000000")
	seen := map[access]int{}
	for _, rt := range (&Server{}).routes() {
		if rt.access == accessPublic {
			continue
		}
		seen[rt.access]++
		method, path, ok := strings.Cut(rt.pattern, " ")
		if !ok {
			method, path = "GET", rt.pattern+"x"
		}
		path = params.Replace(path)
		scope := requiredScope(rt.access)
		for _, k := range keys {
			resp, out := f.keyRequest(k.key, method, path, nil, "", nil)
			got, auth := resp.StatusCode, resp.Header.Get("WWW-Authenticate")
			allowed := scope != "" && (&store.APIKey{Scopes: k.scopes}).Allows(scope)
			switch {
			case allowed && (got == 401 || got == 403):
				t.Errorf("%s with a %v key: %d %v, want access", rt.pattern, k.scopes, got, out)
			case allowed:
			case scope == "":
				if got != 403 || out["error"] != "not available with an API key" || !strings.Contains(auth, `error="insufficient_scope"`) || strings.Contains(auth, "scope=") {
					t.Errorf("%s with a %v key: %d %v %q, want 403 not available", rt.pattern, k.scopes, got, out, auth)
				}
			default:
				if got != 403 || out["scope"] != scope || out["error"] != "this API key lacks the "+scope+" scope" ||
					!strings.Contains(auth, `error="insufficient_scope", scope="`+scope+`"`) {
					t.Errorf("%s with a %v key: %d %v %q, want 403 for %s", rt.pattern, k.scopes, got, out, auth, scope)
				}
			}
		}
	}
	if seen[accessRead] == 0 || seen[accessUpload] == 0 || seen[accessWrite] == 0 {
		t.Errorf("routes per access: %v", seen)
	}
	// Every non-GET route that a key may use needs upload or write, and
	// upload is the upload route only.
	for _, rt := range (&Server{}).routes() {
		if method, _, _ := strings.Cut(rt.pattern, " "); method != "GET" && rt.access == accessRead {
			t.Errorf("%s: a read key could change something", rt.pattern)
		}
		if rt.access == accessUpload && rt.pattern != "POST /api/documents" {
			t.Errorf("%s: an upload key could use it", rt.pattern)
		}
	}
	// The upload's refusal names upload, the scope a key needs at least.
	read := keys[0].key
	resp, out := f.keyUpload(read, "a.pdf", pdf("a"))
	if resp.StatusCode != 403 || out["scope"] != "upload" || out["error"] != "this API key lacks the upload scope" ||
		!strings.Contains(resp.Header.Get("WWW-Authenticate"), `scope="upload"`) {
		t.Errorf("read key upload: %d %v %q", resp.StatusCode, out, resp.Header.Get("WWW-Authenticate"))
	}
	if resp, _ := f.keyUpload(keys[2].key, "a.pdf", pdf("a")); resp.StatusCode != 201 {
		t.Errorf("write key upload: %d", resp.StatusCode)
	}
	if s := f.keyStatus(keys[2].key, "GET", "/api/documents"); s != 403 {
		t.Errorf("write key reads: %d", s)
	}
	// Public routes work with a key too.
	if s := f.keyStatus(read, "GET", "/healthz"); s != 200 {
		t.Errorf("healthz: %d", s)
	}
}

// TestAPIKeyUploadScope: an upload key uploads (201, then 200 for the
// same file) and gets 403 with the missing scope on the reads and changes
// of the document it uploaded; a write key is stored as [write] and still
// uploads; the audit entry of the creation records the scopes.
func TestAPIKeyUploadScope(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("kim", keyPassword, false)
	f.loginOK("kim", keyPassword)
	n := len(auditLog(t, f.st))
	phone := f.createKey("iPhone", []string{"upload"}, "")
	resp, out := f.keyUpload(phone, "scan.pdf", pdf("scan"))
	if resp.StatusCode != 201 {
		t.Fatalf("upload: %d %v", resp.StatusCode, out)
	}
	id, _ := out["id"].(string)
	// The duplicate answer does not name the existing document to a key
	// without read; a key with read gets it.
	if resp, out := f.keyUpload(phone, "again.pdf", pdf("scan")); resp.StatusCode != 200 || len(out) != 1 || out["duplicate"] != true {
		t.Errorf("duplicate upload with an upload key: %d %v, want only duplicate", resp.StatusCode, out)
	}
	rw := f.createKey("rw", []string{"read", "write"}, "")
	resp, out = f.keyUpload(rw, "again.pdf", pdf("scan"))
	if ex, _ := out["existing"].(map[string]any); resp.StatusCode != 200 || out["duplicate"] != true || ex["id"] != id || ex["title"] != "scan" {
		t.Errorf("duplicate upload with a read+write key: %d %v", resp.StatusCode, out)
	}
	tag := f.expect("POST", "/api/tags", map[string]any{"name": "Bills"}, 201)["id"].(string)
	refused := []struct{ method, path, scope string }{
		{"GET", "/api/documents", "read"},
		{"GET", "/api/documents/" + id, "read"},
		{"GET", "/api/documents/" + id + "/file", "read"},
		{"GET", "/api/documents/" + id + "/thumbnail", "read"},
		{"GET", "/api/documents/counts", "read"},
		{"GET", "/api/tags", "read"},
		{"POST", "/api/tags", "write"},
		{"PATCH", "/api/tags/" + tag, "write"},
		{"DELETE", "/api/tags/" + tag, "write"},
		{"POST", "/api/documents/" + id + "/reprocess", "write"},
	}
	for _, c := range refused {
		resp, out := f.keyRequest(phone, c.method, c.path, []byte("{}"), "application/json", nil)
		if resp.StatusCode != 403 || out["scope"] != c.scope || !strings.Contains(resp.Header.Get("WWW-Authenticate"), `scope="`+c.scope+`"`) {
			t.Errorf("%s %s with an upload key: %d %v %q", c.method, c.path, resp.StatusCode, out, resp.Header.Get("WWW-Authenticate"))
		}
	}
	for _, p := range []string{"/api/profile/api-keys", "/api/users", "/api/audit"} {
		if resp, out := f.keyRequest(phone, "GET", p, nil, "", nil); resp.StatusCode != 403 || out["error"] != "not available with an API key" {
			t.Errorf("GET %s with an upload key: %d %v", p, resp.StatusCode, out)
		}
	}
	// write with upload is stored as [write], which uploads.
	w := f.expect("POST", "/api/profile/api-keys", map[string]any{"name": "script", "scopes": []string{"upload", "write"}, "currentPassword": keyPassword}, 201)
	if fmt.Sprint(w["scopes"]) != "[write]" {
		t.Errorf("write+upload stored as %v", w["scopes"])
	}
	if resp, out := f.keyUpload(w["key"].(string), "w.pdf", pdf("w")); resp.StatusCode != 201 {
		t.Errorf("write key upload: %d %v", resp.StatusCode, out)
	}
	list := f.expect("GET", "/api/profile/api-keys", nil, 200)["keys"].([]any)
	scopes := map[string]string{}
	for _, k := range list {
		k := k.(map[string]any)
		scopes[k["name"].(string)] = fmt.Sprint(k["scopes"])
	}
	if scopes["iPhone"] != "[upload]" || scopes["script"] != "[write]" || scopes["rw"] != "[read write]" {
		t.Errorf("listed scopes %v", scopes)
	}

	var created []string
	for _, r := range newEntries(t, f.st, n) {
		if r.Action == "api_key.create" {
			created = append(created, r.TargetName+"="+fmt.Sprint(details(t, r)["scopes"]))
		}
	}
	if strings.Join(created, " ") != "iPhone=[upload] rw=[read write] script=[write]" {
		t.Errorf("api_key.create entries: %v", created)
	}

	// An unknown scope or none is refused, naming the three.
	for _, bad := range [][]string{{"admin"}, {}} {
		out := f.expect("POST", "/api/profile/api-keys", map[string]any{"name": "bad", "scopes": bad, "currentPassword": keyPassword}, 400)
		if out["error"] != "choose one or more of the scopes read, upload and write" {
			t.Errorf("%v: %v", bad, out)
		}
	}
}

// TestAPIKeyOwnerScope: a key sees and creates only its owner's
// documents, also with another user's cookie in the request, and an
// admin's key sees everyone's.
func TestAPIKeyOwnerScope(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.addUser("alice", keyPassword, false)
	f.addUser("bob", keyPassword, false)
	f.addUser("root", keyPassword, true)
	f.loginOK("alice", keyPassword)
	aliceKey := f.createKey("k", []string{"read", "write"}, "")
	b := f.as()
	b.loginOK("bob", keyPassword)
	bobDoc := b.uploadOK("bob.pdf", pdf("bob"))
	bobCookie := b.sessionCookie()
	// Bob's document failed, so a reprocess that got through would queue
	// a new job.
	now := time.Now().Truncate(time.Microsecond)
	j, err := f.st.ClaimJob(t.Context(), now, time.Minute)
	if err != nil || j == nil || j.DocumentID != bobDoc {
		t.Fatalf("claim bob's job: %+v, %v", j, err)
	}
	if err := f.st.FailJob(t.Context(), j, now, "The PDF cannot be read."); err != nil {
		t.Fatal(err)
	}
	bobJobs := func() []*store.Job {
		t.Helper()
		jobs, err := f.st.DocumentJobs(t.Context(), bobDoc)
		if err != nil {
			t.Fatal(err)
		}
		return jobs
	}
	onlyFailed := func(what string) {
		t.Helper()
		if jobs := bobJobs(); len(jobs) != 1 || jobs[0].ID != j.ID || jobs[0].State != store.JobFailed {
			t.Errorf("bob's jobs after %s: %+v", what, jobs)
		}
	}

	resp, out := f.keyUpload(aliceKey, "alice.pdf", pdf("alice"))
	if resp.StatusCode != 201 {
		t.Fatalf("upload: %d %v", resp.StatusCode, out)
	}
	aliceDoc := out["id"].(string)
	if d, err := f.st.GetDocument(t.Context(), store.Scope{UserID: alice.ID}, aliceDoc); err != nil || d.OwnerID != alice.ID {
		t.Fatalf("owner of the key's upload: %+v %v", d, err)
	}
	for _, p := range []string{"/api/documents/" + bobDoc, "/api/documents/" + bobDoc + "/file", "/api/documents/" + bobDoc + "/thumbnail"} {
		if s := f.keyStatus(aliceKey, "GET", p); s != 404 {
			t.Errorf("GET %s with alice's key: %d, want 404", p, s)
		}
	}
	reprocess := "/api/documents/" + bobDoc + "/reprocess"
	if resp, _ := f.keyRequest(aliceKey, "POST", reprocess, []byte("{}"), "application/json", nil); resp.StatusCode != 404 {
		t.Errorf("POST %s with alice's key: %d, want 404", reprocess, resp.StatusCode)
	}
	onlyFailed("alice's reprocess")
	// The key's user counts, not the cookie's.
	resp, out = f.keyRequest(aliceKey, "GET", "/api/documents", nil, "", bobCookie)
	if ids := listedIDs(t, out); resp.StatusCode != 200 || len(ids) != 1 || ids[0] != aliceDoc {
		t.Fatalf("key with bob's cookie: %d %v", resp.StatusCode, ids)
	}

	r := f.as()
	r.loginOK("root", keyPassword)
	rootKey := r.createKey("all", []string{"read"}, r.code(f.secrets["root"]))
	if _, out := r.keyRequest(rootKey, "GET", "/api/documents", nil, "", nil); len(listedIDs(t, out)) != 2 {
		t.Errorf("admin key list: %v", out)
	}
	if s := r.keyStatus(rootKey, "GET", "/api/documents/"+bobDoc); s != 200 {
		t.Errorf("admin key reads bob's document: %d", s)
	}
	// Reprocess needs the write scope, also for an admin's key.
	if resp, _ := r.keyRequest(rootKey, "POST", reprocess, []byte("{}"), "application/json", nil); resp.StatusCode != 403 {
		t.Errorf("POST %s with the admin's read key: %d, want 403", reprocess, resp.StatusCode)
	}
	onlyFailed("the admin's reprocess")
}

// TestAPIKeyNoCookieFallback: a key request is decided by the key alone. A
// wrong key with a valid cookie is refused, and a key request needs no
// Origin; other Authorization headers are cookie requests with all their
// checks.
func TestAPIKeyNoCookieFallback(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("bob", keyPassword, false)
	f.loginOK("bob", keyPassword)
	bobCookie := f.sessionCookie()

	for _, bad := range []string{"pl_" + strings.Repeat("a", 52), "pl_short"} {
		resp, out := f.keyRequest(bad, "GET", "/api/documents", nil, "", bobCookie)
		if resp.StatusCode != 401 || out["error"] != "invalid API key" || !strings.Contains(resp.Header.Get("WWW-Authenticate"), "invalid_token") {
			t.Fatalf("invalid key %q with a cookie: %d %v", bad, resp.StatusCode, out)
		}
		// Also for a write: the cookie does not make it a browser request.
		if resp, _ := f.keyRequest(bad, "POST", "/api/documents", pdf("x"), "application/pdf", bobCookie); resp.StatusCode != 401 {
			t.Fatalf("invalid key upload with a cookie: %d", resp.StatusCode)
		}
		// And for the login routes.
		if resp, _ := f.keyRequest(bad, "POST", "/api/session", []byte(`{"username":"bob","password":"`+keyPassword+`"}`), "application/json", nil); resp.StatusCode != 401 {
			t.Fatalf("login with an invalid key: %d", resp.StatusCode)
		}
	}
	key := f.createKey("k", []string{"read", "write"}, "")
	// A key cannot log in or out.
	if resp, _ := f.keyRequest(key, "POST", "/api/session", []byte(`{"username":"bob","password":"`+keyPassword+`"}`), "application/json", nil); resp.StatusCode != 403 {
		t.Fatalf("login with a key: %d", resp.StatusCode)
	}
	if resp, _ := f.keyRequest(key, "DELETE", "/api/session", nil, "", bobCookie); resp.StatusCode != 403 {
		t.Fatalf("logout with a key: %d", resp.StatusCode)
	}
	if s := f.keyStatus("", "GET", sessionCheckPath); s != 401 {
		t.Fatalf("no credentials: %d", s)
	}
	f.expect("GET", sessionCheckPath, nil, 200) // the session is still there

	// Other Authorization headers are not key requests: the cookie works
	// and writes still need the Origin.
	req, _ := http.NewRequest("GET", f.srv.URL+"/api/documents", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwdw==")
	req.AddCookie(bobCookie)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 200 {
		t.Fatalf("Basic with a cookie: %v %v", resp, err)
	}
	req, _ = http.NewRequest("POST", f.srv.URL+"/api/documents", bytes.NewReader(pdf("x")))
	req.Header.Set("Authorization", "Bearer some-proxy-token")
	req.Header.Set("Content-Type", "application/pdf")
	req.AddCookie(bobCookie)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 403 {
		t.Fatalf("other Bearer without Origin: %v %v", resp, err)
	}
	// A cross-site cookie request is refused as before.
	req, _ = http.NewRequest("POST", f.srv.URL+"/api/documents", bytes.NewReader(pdf("x")))
	req.Header.Set("Content-Type", "application/pdf")
	req.Header.Set("Origin", "https://evil.example")
	req.AddCookie(bobCookie)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 403 {
		t.Fatalf("cross-site cookie upload: %v %v", resp, err)
	}
	// The scheme is case-insensitive.
	req, _ = http.NewRequest("GET", f.srv.URL+"/api/documents", nil)
	req.Header.Set("Authorization", "bearer "+key)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 200 {
		t.Fatalf("lower-case scheme: %v %v", resp, err)
	}
}

func TestAPIKeyLifecycle(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("admin", keyPassword, true)
	f.addUser("kim", keyPassword, false)
	f.loginOK("admin", keyPassword)
	k := f.as()
	k.loginOK("kim", keyPassword)

	// Revoking an own key needs no password; another user's id is 404.
	key := k.createKey("one", []string{"read"}, "")
	list := k.expect("GET", "/api/profile/api-keys", nil, 200)
	keys, _ := list["keys"].([]any)
	if len(keys) != 1 || list["max"] != float64(store.MaxAPIKeys) {
		t.Fatalf("list %v", list)
	}
	first, _ := keys[0].(map[string]any)
	id := int64(first["id"].(float64))
	if first["hint"] != key[51:] || first["state"] != "active" || first["key"] != nil || first["expiresAt"] != nil {
		t.Fatalf("listed %v", first)
	}
	adminKey := f.createKey("admin key", []string{"read"}, f.code(f.secrets["admin"]))
	f.expect("DELETE", "/api/profile/api-keys/"+itoa(id), nil, 404)
	k.expect("DELETE", "/api/profile/api-keys/"+itoa(id), nil, 204)
	if s := k.keyStatus(key, "GET", "/api/documents"); s != 401 {
		t.Fatalf("revoked: %d", s)
	}
	k.expect("DELETE", "/api/profile/api-keys/"+itoa(id), nil, 404)
	k.expect("DELETE", "/api/profile/api-keys/abc", nil, 404)

	// Names are unique among keys that are not revoked; scopes and expiry
	// are checked.
	key = k.createKey("one", []string{"read"}, "")
	k.expect("POST", "/api/profile/api-keys", map[string]any{"name": "one", "scopes": []string{"read"}, "currentPassword": keyPassword}, 409)
	k.expect("POST", "/api/profile/api-keys", map[string]any{"name": "two", "scopes": []string{"admin"}, "currentPassword": keyPassword}, 400)
	k.expect("POST", "/api/profile/api-keys", map[string]any{"name": "two", "scopes": []string{}, "currentPassword": keyPassword}, 400)
	k.expect("POST", "/api/profile/api-keys", map[string]any{"name": "two", "scopes": []string{"read"}, "expiresInDays": 0, "currentPassword": keyPassword}, 400)
	k.expect("POST", "/api/profile/api-keys", map[string]any{"name": "two", "scopes": []string{"read"}, "expiresInDays": 3651, "currentPassword": keyPassword}, 400)
	k.expect("POST", "/api/profile/api-keys", map[string]any{"name": " ", "scopes": []string{"read"}, "currentPassword": keyPassword}, 400)
	k.expect("POST", "/api/profile/api-keys", map[string]any{"name": "two", "scopes": []string{"read"}, "currentPassword": "wrong wrong wrong"}, 401)

	// Admins list and revoke every key; users may not.
	k.expect("GET", "/api/api-keys", nil, 403)
	k.expect("DELETE", "/api/api-keys/"+itoa(id), nil, 403)
	all := f.expect("GET", "/api/api-keys", nil, 200)
	if s := toJSON(all); !strings.Contains(s, `"user":"kim"`) || !strings.Contains(s, `"user":"admin"`) || strings.Contains(s, key) {
		t.Fatalf("admin list %s", s)
	}
	users := toJSON(f.expect("GET", "/api/users", nil, 200))
	if !strings.Contains(users, `"name":"kim"`) || !strings.Contains(users, `"apiKeys":1`) {
		t.Fatalf("users %s", users)
	}

	// An expiring key works until the server clock passes its expiry.
	out := k.expect("POST", "/api/profile/api-keys", map[string]any{"name": "expiring", "scopes": []string{"read"}, "expiresInDays": 1, "currentPassword": keyPassword}, 201)
	expiring := out["key"].(string)
	if s := k.keyStatus(expiring, "GET", "/api/documents"); s != 200 {
		t.Fatalf("before expiry: %d", s)
	}
	f.clock.Advance(24*time.Hour + time.Second)
	if s := k.keyStatus(expiring, "GET", "/api/documents"); s != 401 {
		t.Fatalf("expired: %d", s)
	}
	for _, item := range k.expect("GET", "/api/profile/api-keys", nil, 200)["keys"].([]any) {
		if m := item.(map[string]any); m["name"] == "expiring" && m["state"] != "expired" {
			t.Fatalf("expired key listed as %v", m)
		}
	}

	// Locking suspends, unlocking restores.
	f.expect("PATCH", "/api/users/kim", map[string]any{"locked": true}, 204)
	if s := k.keyStatus(key, "GET", "/api/documents"); s != 401 {
		t.Fatalf("locked: %d", s)
	}
	f.expect("PATCH", "/api/users/kim", map[string]any{"locked": false}, 204)
	if s := k.keyStatus(key, "GET", "/api/documents"); s != 200 {
		t.Fatalf("unlocked: %d", s)
	}

	// A password reset with the opt-out keeps the key, but the owner must
	// choose a password first; a plain reset revokes it.
	f.expect("POST", "/api/users/kim/password", map[string]any{"revokeApiKeys": false}, 200)
	resp, body := k.keyRequest(key, "GET", "/api/documents", nil, "", nil)
	if resp.StatusCode != 403 || body["passwordChangeRequired"] != true {
		t.Fatalf("must change password: %d %v", resp.StatusCode, body)
	}
	f.expect("POST", "/api/users/kim/password", nil, 200)
	if s := k.keyStatus(key, "GET", "/api/documents"); s != 401 {
		t.Fatalf("after reset: %d", s)
	}

	// An admin's 2FA reset keeps the keys, which wait for 2FA again.
	f.addUser("other", keyPassword, true)
	o := f.as()
	o.loginOK("other", keyPassword)
	otherKey := o.createKey("k", []string{"read"}, o.code(f.secrets["other"]))
	f.expect("POST", "/api/users/other/2fa/reset", nil, 204)
	resp, body = o.keyRequest(otherKey, "GET", "/api/documents", nil, "", nil)
	if resp.StatusCode != 403 || body["twoFactorSetupRequired"] != true {
		t.Fatalf("2FA reset: %d %v", resp.StatusCode, body)
	}

	// An admin revokes any key, also their own; removal deletes the keys.
	f.expect("DELETE", "/api/api-keys/999999", nil, 404)
	var adminKeyID int64
	for _, item := range all["keys"].([]any) {
		m := item.(map[string]any)
		if m["user"] == "admin" {
			adminKeyID = int64(m["id"].(float64))
		}
	}
	f.expect("DELETE", "/api/api-keys/"+itoa(adminKeyID), nil, 204)
	if s := f.keyStatus(adminKey, "GET", "/api/documents"); s != 401 {
		t.Fatalf("admin revoked own key: %d", s)
	}
	f.expect("DELETE", "/api/users/other", nil, 204)
	if s := o.keyStatus(otherKey, "GET", "/api/documents"); s != 401 {
		t.Fatalf("removed owner: %d", s)
	}
}

// syncWriter is a log sink that is safe for the server's goroutines.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestAPIKeyRates(t *testing.T) {
	f := newAuthFixture(t)
	logs := &syncWriter{}
	srv := New(f.st, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Options{
		AllowedHosts: []string{"127.0.0.1"}, Hasher: storetest.Hasher, Now: f.clock.Now, Sealer: f.sealer, SecretKey: f.secretKey, Blobs: f.blobs,
	})
	f.srv.Config.Handler = srv.Handler()
	f.addUser("kim", keyPassword, false)
	f.loginOK("kim", keyPassword)
	key := f.createKey("busy", []string{"read"}, "")
	for i := range apiKeyLimit {
		if s := f.keyStatus(key, "GET", "/api/documents"); s != 200 {
			t.Fatalf("request %d: %d", i+1, s)
		}
	}
	resp, _ := f.keyRequest(key, "GET", "/api/documents", nil, "", nil)
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("over the key rate: %d", resp.StatusCode)
	}
	// The window ends on the server clock.
	f.clock.Advance(apiKeyWindow)
	if s := f.keyStatus(key, "GET", "/api/documents"); s != 200 {
		t.Fatalf("next window: %d", s)
	}

	n := len(auditLog(t, f.st))
	bad := "pl_" + strings.Repeat("b", 52)
	for i := range badAPIKeyLimit {
		if s := f.keyStatus(bad, "GET", "/api/documents"); s != 401 {
			t.Fatalf("invalid key %d: %d", i+1, s)
		}
	}
	if s := f.keyStatus(bad, "GET", "/api/documents"); s != 429 {
		t.Fatalf("over the invalid-key rate: %d", s)
	}
	// Even a valid key from that client waits: the client is blocked.
	if s := f.keyStatus(key, "GET", "/api/documents"); s != 429 {
		t.Fatalf("valid key from a blocked client: %d", s)
	}
	// Password logins are not affected.
	f.as().loginOK("kim", keyPassword)

	entries := newEntries(t, f.st, n)
	if len(entries) < badAPIKeyLimit {
		t.Fatalf("%d entries for refused keys", len(entries))
	}
	if r := entries[0]; r.Action != "auth.login_failed" || details(t, r)["method"] != "api_key" || details(t, r)["reason"] != "invalid" || r.UserID != nil {
		t.Errorf("refused key entry %+v %v", r, details(t, r))
	}
	// Neither key appears in the log.
	if l := logs.String(); l == "" || strings.Contains(l, key) || strings.Contains(l, bad) {
		t.Errorf("an API key is in the log (or nothing was logged): %d bytes", len(l))
	}
	for _, r := range auditLog(t, f.st) {
		if strings.Contains(string(r.Details), key) || strings.Contains(string(r.Details), bad) || strings.Contains(r.TargetName, key) {
			t.Error("an API key is in the audit log")
		}
	}
}

func TestAPIKeyRefusedAudit(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("kim", keyPassword, false)
	f.loginOK("kim", keyPassword)
	key := f.createKey("old", []string{"read"}, "")
	id := f.keyID("old")
	n := len(auditLog(t, f.st))
	f.expect("DELETE", "/api/profile/api-keys/"+itoa(id), nil, 204)
	f.keyStatus(key, "GET", "/api/documents")
	entries := newEntries(t, f.st, n)
	if len(entries) != 2 {
		t.Fatalf("entries %v", entries)
	}
	if r := entries[0]; r.Action != "api_key.revoke" || r.Actor != store.ActorSession || r.UserName != "kim" || r.TargetType != store.TargetAPIKey ||
		r.TargetID != itoa(id) || r.TargetName != "old" || details(t, r)["reason"] != "user" || details(t, r)["owner"] != "kim" {
		t.Errorf("revoke entry %+v %v", r, details(t, r))
	}
	if r := entries[1]; r.Action != "auth.login_failed" || r.UserName != "kim" || details(t, r)["reason"] != "revoked" || r.TargetName != "old" {
		t.Errorf("refused entry %+v %v", r, details(t, r))
	}
}

// TestAPIKeyChangesFailClosed: without the audit log, no key is created or
// revoked.
func TestAPIKeyChangesFailClosed(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("kim", keyPassword, false)
	f.loginOK("kim", keyPassword)
	key := f.createKey("one", []string{"read"}, "")
	id := f.keyID("one")
	breakAudit(t, f.dbURL)
	if out := f.expect("POST", "/api/profile/api-keys", map[string]any{"name": "two", "scopes": []string{"read"}, "currentPassword": keyPassword}, 503); out["key"] != nil {
		t.Fatalf("a key without its audit entry: %v", out)
	}
	f.expect("DELETE", "/api/profile/api-keys/"+itoa(id), nil, 503)
	if s := f.keyStatus(key, "GET", "/api/documents"); s != 200 {
		t.Fatalf("key after a failed revoke: %d", s)
	}
	if list := f.expect("GET", "/api/profile/api-keys", nil, 200); len(list["keys"].([]any)) != 1 {
		t.Fatalf("keys: %v", list)
	}
}
