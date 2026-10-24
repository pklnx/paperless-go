package ui

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func TestHandler(t *testing.T) {
	h := Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe") {
		t.Errorf("CSP = %q", csp)
	}
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("missing X-Frame-Options")
	}
	checkPolicies(t, rec)
	// Without a UI build (plain `go test`), a hint page is served instead.
	switch rec.Code {
	case http.StatusOK:
		if !strings.Contains(rec.Body.String(), `<div id="root">`) {
			t.Errorf("unexpected index: %s", rec.Body.String())
		}
	case http.StatusServiceUnavailable:
		if !strings.Contains(rec.Body.String(), "make web") {
			t.Errorf("unexpected hint: %s", rec.Body.String())
		}
	default:
		t.Fatalf("status %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/assets/does-not-exist.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing asset: status %d", rec.Code)
	}
	checkPolicies(t, rec)
}

// checkPolicies checks the headers every UI answer carries, also assets.
func checkPolicies(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	for name, want := range map[string]string{
		"Permissions-Policy":           "camera=(), microphone=(), geolocation=()",
		"Cross-Origin-Resource-Policy": "same-origin",
	} {
		if got := rec.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// TestContentSecurityPolicy pins the CSP the PDF viewer (pdf.js, #79)
// works under: no eval of any kind, scripts and workers from the server
// only, and nothing widened for it.
func TestContentSecurityPolicy(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/assets/pdf.worker.min.mjs", nil))
	csp := rec.Header().Get("Content-Security-Policy")
	directives := map[string]string{}
	for _, d := range strings.Split(csp, ";") {
		name, value, _ := strings.Cut(strings.TrimSpace(d), " ")
		directives[name] = value
	}
	// The quoted keywords: 'wasm-unsafe-eval' contains unsafe-eval unquoted.
	for _, bad := range []string{"'unsafe-eval'", "'wasm-unsafe-eval'", "'unsafe-inline'", "blob:", "*", "http:", "https:"} {
		for name, value := range directives {
			for _, v := range strings.Fields(value) {
				if v == bad {
					t.Errorf("%s allows %s: %q", name, bad, csp)
				}
			}
		}
	}
	for name, want := range map[string]string{
		"default-src": "'self'",
		// Workers fall back to script-src: pdf.js's worker is a file of the build.
		"script-src":  "'self'",
		"connect-src": "'self'",
		"img-src":     "'self' data:",
		"object-src":  "'none'",
	} {
		if got := directives[name]; got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{"worker-src", "child-src", "font-src", "style-src"} {
		if v, ok := directives[name]; ok && v != "'self'" {
			t.Errorf("%s = %q, want 'self' or unset", name, v)
		}
	}
}

// rootBuild is a build root with index.html, every root file (distinct bytes
// each), a hashed asset and a file that is never served by name.
func rootBuild() fstest.MapFS {
	files := fstest.MapFS{
		"index.html":      {Data: []byte(`<!doctype html><div id="root"></div>`)},
		".keep":           {Data: []byte("keep")},
		"assets/app-1.js": {Data: []byte("console.log(1)")},
	}
	for _, path := range RootPaths() {
		files[strings.TrimPrefix(path, "/")] = &fstest.MapFile{Data: []byte("root file " + path)}
	}
	return files
}

// TestRootFilesServed checks the web app manifest and its icons: each is
// served at its exact path only, with its type and the UI's headers.
func TestRootFilesServed(t *testing.T) {
	files := rootBuild()
	h := handler(files)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	checkUI := func(t *testing.T, rec *httptest.ResponseRecorder) {
		t.Helper()
		if got := rec.Header().Get("Content-Security-Policy"); got != contentSecurityPolicy {
			t.Errorf("CSP = %q", got)
		}
		if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("X-Frame-Options = %q", got)
		}
		checkPolicies(t, rec)
	}

	if len(RootPaths()) != 6 {
		t.Fatalf("root paths: %v", RootPaths())
	}
	for _, path := range RootPaths() {
		rec := get(path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		if want := files[strings.TrimPrefix(path, "/")].Data; !bytes.Equal(rec.Body.Bytes(), want) {
			t.Errorf("%s: body %q, want %q", path, rec.Body.Bytes(), want)
		}
		if got := rec.Header().Get("Content-Type"); got != rootFiles[path] {
			t.Errorf("%s: Content-Type %q, want %q", path, got, rootFiles[path])
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s: Cache-Control %q", path, got)
		}
		checkUI(t, rec)
	}

	// Near misses never return a root file, neither its bytes nor its type.
	for _, path := range []string{
		"/.keep", "/icon-192.png/", "/assets/../icon-192.png", "/assets/%2e%2e/icon-192.png",
		"/Icon-192.png", "/icon-192.PNG", "/manifest.webmanifest/",
	} {
		rec := get(path)
		body := rec.Body.String()
		if strings.HasPrefix(body, "root file ") || body == "keep" {
			t.Errorf("%s: served a root file: %d %q", path, rec.Code, body)
		}
		if typ := rec.Header().Get("Content-Type"); typ == "image/png" || typ == "application/manifest+json" {
			t.Errorf("%s: Content-Type %q", path, typ)
		}
		checkUI(t, rec)
	}

	// /assets/ still serves the build's assets, immutable.
	rec := get("/assets/app-1.js")
	if rec.Code != http.StatusOK || rec.Body.String() != "console.log(1)" {
		t.Errorf("asset: %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("asset: Cache-Control %q", got)
	}
	checkUI(t, rec)

	// The index runs here too, which a test without a UI build never sees.
	rec = get("/")
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), files["index.html"].Data) {
		t.Errorf("index: %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("index: Content-Type %q", got)
	}
	checkUI(t, rec)

	// A root file missing from the build answers 404, not index.html.
	delete(files, "favicon-32.png")
	h = handler(files)
	rec = get("/favicon-32.png")
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing root file: status %d", rec.Code)
	}
	if bytes.Equal(rec.Body.Bytes(), files["index.html"].Data) || strings.Contains(rec.Body.String(), `<div id="root">`) {
		t.Errorf("missing root file: index.html body %q", rec.Body.String())
	}
	if typ := rec.Header().Get("Content-Type"); strings.HasPrefix(typ, "text/html") || typ == "image/png" {
		t.Errorf("missing root file: Content-Type %q", typ)
	}
	checkUI(t, rec)
}

// TestRootFilesMatchPublic: the files in web/public are exactly the
// rootFiles, each with the type of its extension, so a new icon cannot be
// added without its route.
func TestRootFilesMatchPublic(t *testing.T) {
	entries, err := os.ReadDir("../../../web/public")
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]string{".png": "image/png", ".webmanifest": "application/manifest+json"}
	var names []string
	for _, e := range entries {
		names = append(names, "/"+e.Name())
		if want, ok := types[filepath.Ext(e.Name())]; !ok || rootFiles["/"+e.Name()] != want {
			t.Errorf("%s: rootFiles type %q, want %q", e.Name(), rootFiles["/"+e.Name()], want)
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, RootPaths()) {
		t.Errorf("web/public = %v, rootFiles = %v", names, RootPaths())
	}
}
