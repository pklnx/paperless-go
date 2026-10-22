package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
