package web

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/paperless-go/internal/search"
	"github.com/pklnx/paperless-go/internal/store"
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

// HSTS is sent exactly where the request came over HTTPS.
func TestStrictTransportSecurity(t *testing.T) {
	h := New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Pinger: fakeDB{}, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
	}).Handler()
	for _, c := range []struct {
		name   string
		remote string
		proto  string
		tls    bool
		want   string
	}{
		{"direct TLS", "192.0.2.1:1234", "", true, "max-age=31536000"},
		{"loopback proxy", "127.0.0.1:4000", "https", false, "max-age=31536000"},
		{"trusted proxy", "10.1.2.3:4000", "https", false, "max-age=31536000"},
		{"plain HTTP", "192.0.2.1:1234", "", false, ""},
		{"plain HTTP from a trusted proxy", "10.1.2.3:4000", "http", false, ""},
		{"untrusted peer claims HTTPS", "192.0.2.1:1234", "https", false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://localhost/healthz", nil)
			r.RemoteAddr = c.remote
			if c.proto != "" {
				r.Header.Set("X-Forwarded-Proto", c.proto)
			}
			if c.tls {
				r.TLS = &tls.ConnectionState{}
			}
			rec := serve(h, r)
			if got := rec.Header().Values("Strict-Transport-Security"); c.want == "" && len(got) != 0 || c.want != "" && (len(got) != 1 || got[0] != c.want) {
				t.Errorf("Strict-Transport-Security = %q, want %q", got, c.want)
			}
		})
	}
}

// The server's timeouts: ListenAndServe uses newHTTPServer.
func TestNewHTTPServer(t *testing.T) {
	h := http.NotFoundHandler()
	srv := newHTTPServer(":1234", h)
	if srv.Addr != ":1234" || srv.Handler == nil || srv.ReadHeaderTimeout != 10*time.Second || srv.ReadTimeout != 30*time.Second ||
		srv.WriteTimeout != 5*time.Minute || srv.IdleTimeout != 2*time.Minute || srv.MaxHeaderBytes != 64<<10 {
		t.Errorf("server %+v", srv)
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

// failStore answers with fixed wording: a validation error is a 400, and an
// unknown error (for example a raw database error) a 500 that names
// nothing of the cause.
func TestFailStore(t *testing.T) {
	var logs strings.Builder
	s := New(nil, slog.New(slog.NewTextHandler(&logs, nil)), Options{})
	raw := &pgconn.PgError{Code: "23505", ConstraintName: "secret_constraint", Message: "duplicate key value violates unique constraint"}
	for _, tc := range []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{"validation", &store.ValidationError{Field: "source", Constraint: "documents_source_check"}, http.StatusBadRequest, `{"error":"invalid input"}`},
		{"not found", store.ErrNotFound, http.StatusNotFound, `{"error":"not found"}`},
		{"user removed meanwhile", fmt.Errorf("ingest: create document: %w", store.ErrUnknownUser), http.StatusUnauthorized, `{"error":"login required"}`},
		{"invalid cursor", store.ErrInvalidCursor, http.StatusBadRequest, `{"error":"invalid cursor"}`},
		{"invalid sort", store.ErrInvalidSort, http.StatusBadRequest, `{"error":"invalid sort"}`},
		{"query too long", fmt.Errorf("x: %w", search.ErrTooLong), http.StatusBadRequest, `{"error":"query too long"}`},
		{"invalid query", search.ErrInvalid, http.StatusBadRequest, `{"error":"invalid query"}`},
		{"invalid query text", &store.ValidationError{Field: "q"}, http.StatusBadRequest, `{"error":"invalid query"}`},
		{"deadlock", fmt.Errorf("set tags: %w", store.ErrRetry), http.StatusServiceUnavailable, `{"error":"a concurrent change got in the way; try again"}`},
		{"timestamp out of range", &pgconn.PgError{Code: "22008", Message: "timestamp out of range"}, http.StatusInternalServerError, `{"error":"internal error"}`},
		{"raw database error", raw, http.StatusInternalServerError, `{"error":"internal error"}`},
		{"wrapped unknown error", fmt.Errorf("query: %w", raw), http.StatusInternalServerError, `{"error":"internal error"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.failStore(rec, httptest.NewRequest("GET", "http://localhost/api/x", nil), tc.err)
			if rec.Code != tc.status || strings.TrimSpace(rec.Body.String()) != tc.body {
				t.Errorf("answer = %d %s, want %d %s", rec.Code, rec.Body, tc.status, tc.body)
			}
			if retry := rec.Header().Get("Retry-After"); (tc.status == http.StatusServiceUnavailable) != (retry == "1") {
				t.Errorf("Retry-After %q", retry)
			}
		})
	}
	if !strings.Contains(logs.String(), "SQLSTATE 23505") {
		t.Errorf("the 500's cause is not logged: %s", logs.String())
	}
}
