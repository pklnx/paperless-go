package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeDB struct{ err error }

func (f fakeDB) Ping(context.Context) error { return f.err }

// newTestServer is a server without a database: only requests that need
// neither a session nor a user reach a handler.
func newTestServer(db Pinger, hosts ...string) http.Handler {
	return New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{AllowedHosts: hosts, Pinger: db}).Handler()
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestHealth(t *testing.T) {
	rec := serve(newTestServer(fakeDB{}), httptest.NewRequest("GET", "http://localhost/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok\n" {
		t.Errorf("healthy: %d %q", rec.Code, rec.Body.String())
	}
	rec = serve(newTestServer(fakeDB{err: errors.New("down")}), httptest.NewRequest("GET", "http://localhost/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "down") {
		t.Errorf("database down: %d %q", rec.Code, rec.Body.String())
	}
}

func TestSecurityHeaders(t *testing.T) {
	rec := serve(newTestServer(fakeDB{}), httptest.NewRequest("GET", "http://localhost/healthz", nil))
	for name, want := range map[string]string{
		"X-Content-Type-Options":     "nosniff",
		"Referrer-Policy":            "no-referrer",
		"Cross-Origin-Opener-Policy": "same-origin",
	} {
		if got := rec.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestHostAllowed(t *testing.T) {
	h := newTestServer(fakeDB{})
	for _, host := range []string{"localhost", "localhost:8080", "127.0.0.1:8080", "[::1]:8080", "LOCALHOST"} {
		r := httptest.NewRequest("GET", "/healthz", nil)
		r.Host = host
		if rec := serve(h, r); rec.Code != http.StatusOK {
			t.Errorf("host %q: status %d", host, rec.Code)
		}
	}
	for _, host := range []string{"evil.example", "evil.example:8080", "localhost.evil.example"} {
		r := httptest.NewRequest("GET", "/healthz", nil)
		r.Host = host
		if rec := serve(h, r); rec.Code != http.StatusForbidden {
			t.Errorf("host %q: status %d, want 403", host, rec.Code)
		}
	}

	custom := newTestServer(fakeDB{}, " Docs.Example.ts.net ", "")
	r := httptest.NewRequest("GET", "http://docs.example.ts.net/healthz", nil)
	if rec := serve(custom, r); rec.Code != http.StatusOK {
		t.Errorf("configured host: status %d", rec.Code)
	}
	r = httptest.NewRequest("GET", "http://localhost/healthz", nil)
	if rec := serve(custom, r); rec.Code != http.StatusForbidden {
		t.Errorf("default host with a configured list: status %d, want 403", rec.Code)
	}
}

func TestStateChangingRequests(t *testing.T) {
	h := newTestServer(fakeDB{})
	post := func(origin, fetchSite, contentType string) int {
		r := httptest.NewRequest("POST", "http://localhost:8080/api/nothing", strings.NewReader("{}"))
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if fetchSite != "" {
			r.Header.Set("Sec-Fetch-Site", fetchSite)
		}
		r.Header.Set("Content-Type", contentType)
		return serve(h, r).Code
	}
	for _, c := range []struct {
		name                         string
		origin, fetchSite, mediaType string
		want                         int
	}{
		// Passed the checks, refused by the login check.
		{"same origin, JSON", "http://localhost:8080", "", "application/json", http.StatusUnauthorized},
		{"same origin by Sec-Fetch-Site", "", "same-origin", "application/json; charset=utf-8", http.StatusUnauthorized},
		{"other origin", "http://evil.example", "", "application/json", http.StatusForbidden},
		{"other port", "http://localhost:9999", "", "application/json", http.StatusForbidden},
		{"no origin", "", "", "application/json", http.StatusForbidden},
		{"cross-site fetch", "", "cross-site", "application/json", http.StatusForbidden},
		{"form", "http://localhost:8080", "", "application/x-www-form-urlencoded", http.StatusUnsupportedMediaType},
		{"text", "http://localhost:8080", "", "text/plain", http.StatusUnsupportedMediaType},
	} {
		if got := post(c.origin, c.fetchSite, c.mediaType); got != c.want {
			t.Errorf("%s: status %d, want %d", c.name, got, c.want)
		}
	}
}

// Without a login, an unknown API path answers 401 like every other
// session route, so the answer does not reveal which paths exist.
func TestUnknownAPIPathNeedsLogin(t *testing.T) {
	rec := serve(newTestServer(fakeDB{}), httptest.NewRequest("GET", "http://localhost/api/documents", nil))
	if rec.Code != http.StatusUnauthorized || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") ||
		rec.Body.String() != "{\"error\":\"login required\"}\n" {
		t.Errorf("unknown API path: %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}

func TestUIRoutes(t *testing.T) {
	h := newTestServer(fakeDB{})
	rec := serve(h, httptest.NewRequest("GET", "http://localhost/", nil))
	// Without a UI build (plain `go test`) the hint page answers.
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/: status %d", rec.Code)
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Error("/: no Content-Security-Policy")
	}
	// Other paths are session-only: 401 without a login.
	if rec := serve(h, httptest.NewRequest("GET", "http://localhost/somewhere", nil)); rec.Code != http.StatusUnauthorized {
		t.Errorf("/somewhere: status %d, want 401", rec.Code)
	}
}
