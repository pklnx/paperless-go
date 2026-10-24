package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestUIAssetsAnswers sends the paths that are no asset file to /assets/
// through the real route table (pklnx/mail-archive#250). Each answers 404 and
// is not cached, immutable included; the UI's policies are on every answer,
// also those of the hint page or index at "/". The embedded build is whatever
// `make web` left (CI: only dist/.keep), so only headers are compared at "/",
// and the files themselves are tested in internal/web/ui.
func TestUIAssetsAnswers(t *testing.T) {
	h := newTestServer(fakeDB{})
	get := func(path string) *httptest.ResponseRecorder {
		return serve(h, httptest.NewRequest("GET", "http://localhost"+path, nil))
	}
	// "/%61ssets/" is the folder under another spelling: the mux routes on
	// the decoded path, so it reaches the UI handler.
	for _, path := range []string{
		"/%61ssets/nope.js", "/assets/nope.js", "/assets/x/index.html", "/assets/%2e%2e/index.html",
		"/assets/", "/assets/sub/",
	} {
		rec := get(path)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s: Cache-Control %q, want no-cache", path, got)
		}
		if rec.Header().Get("Location") != "" {
			t.Errorf("%s: redirects to %q", path, rec.Header().Get("Location"))
		}
		checkUIPolicies(t, path, rec)
	}
	// "/" is the index or, without a build, the hint page (status not pinned).
	rec := get("/")
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("/: Cache-Control %q", got)
	}
	checkUIPolicies(t, "/", rec)
	// The mux sends "/assets" to "/assets/" itself, never as immutable.
	rec = get("/assets")
	if rec.Code != http.StatusTemporaryRedirect || strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("/assets: status %d, Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
}

// checkUIPolicies checks the headers the UI handler puts on every answer.
func checkUIPolicies(t *testing.T, path string, rec *httptest.ResponseRecorder) {
	t.Helper()
	for name, want := range map[string]string{
		"Cross-Origin-Resource-Policy": "same-origin",
		"X-Frame-Options":              "DENY",
	} {
		if got := rec.Header().Get(name); got != want {
			t.Errorf("%s: %s = %q, want %q", path, name, got, want)
		}
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		t.Errorf("%s: Content-Security-Policy = %q", path, rec.Header().Get("Content-Security-Policy"))
	}
}
