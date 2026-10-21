// Package web serves the JSON API and the UI over HTTP.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/pklnx/paperless-go/internal/auth"
	"github.com/pklnx/paperless-go/internal/crypto"
	"github.com/pklnx/paperless-go/internal/store"
)

// DefaultAllowedHosts are the host names accepted without configuration.
var DefaultAllowedHosts = []string{"localhost", "127.0.0.1", "::1"}

// Pinger reports whether the database answers.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Server holds the dependencies of the HTTP handlers.
type Server struct {
	store        *store.Store
	db           Pinger
	log          *slog.Logger
	allowedHosts []string
	hasher       *auth.Hasher
	now          func() time.Time
	limiter      *auth.Limiter
	// trustedProxies may name the client in X-Forwarded-For.
	trustedProxies []netip.Prefix
	// sealer and secretKey protect TOTP secrets and recovery codes.
	sealer     *crypto.Sealer
	secretKey  []byte
	require2FA bool
}

// Options configure a Server.
type Options struct {
	// AllowedHosts are host names (without port) accepted in the Host
	// header; empty means DefaultAllowedHosts.
	AllowedHosts []string
	// Hasher verifies passwords; nil means auth.DefaultParams.
	Hasher *auth.Hasher
	// Now is the clock for sessions and login limits; nil means time.Now.
	// Tests inject one.
	Now func() time.Time
	// TrustedProxies are reverse proxies whose X-Forwarded-For names the
	// client (PAPERLESS_GO_TRUSTED_PROXIES), for the login limits.
	TrustedProxies []netip.Prefix
	// Pinger answers the health check; nil means the store. Tests use it
	// to simulate a database that is down.
	Pinger Pinger
	// Sealer seals TOTP secrets and SecretKey keys the recovery code
	// hashes (both from PAPERLESS_GO_SECRET_KEY). Without them the 2FA
	// endpoints answer 503; serve refuses to start without the key.
	Sealer    *crypto.Sealer
	SecretKey []byte
	// Require2FA makes 2FA required for every user, not only admins
	// (PAPERLESS_GO_REQUIRE_2FA).
	Require2FA bool
}

// New creates a Server.
func New(st *store.Store, log *slog.Logger, opts Options) *Server {
	allowed := opts.AllowedHosts
	if len(allowed) == 0 {
		allowed = DefaultAllowedHosts
	}
	hosts := make([]string, 0, len(allowed))
	for _, h := range allowed {
		if h = strings.Trim(strings.ToLower(strings.TrimSpace(h)), "[]"); h != "" {
			hosts = append(hosts, h)
		}
	}
	hasher := opts.Hasher
	if hasher == nil {
		hasher = auth.NewHasher(auth.DefaultParams)
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	limiter := auth.NewLimiter()
	limiter.SetClock(now)
	var pinger Pinger = st
	if opts.Pinger != nil {
		pinger = opts.Pinger
	}
	return &Server{
		store: st, db: pinger, log: log, allowedHosts: hosts, hasher: hasher, now: now, limiter: limiter,
		trustedProxies: opts.TrustedProxies, sealer: opts.Sealer, secretKey: opts.SecretKey, require2FA: opts.Require2FA,
	}
}

// Handler returns the HTTP handler with all routes and protections.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	accessOf := make(map[string]access)
	for _, rt := range s.routes() {
		mux.Handle(rt.pattern, rt.handler)
		accessOf[rt.pattern] = rt.access
	}
	return s.protect(s.authenticate(mux, accessOf))
}

// handleHealth answers 200 while the database answers, for container
// health checks and monitoring. It is public and contains no data.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.db.Ping(ctx); err != nil {
		s.log.Warn("health check: database unavailable", "err", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("database unavailable\n"))
		return
	}
	_, _ = w.Write([]byte("ok\n"))
}

// handleUnknownAPI answers paths under /api/ without a route in JSON, not
// with the UI. It is session-only like every route without an access, so
// without a login it answers 401 and does not reveal which paths exist.
func (s *Server) handleUnknownAPI(w http.ResponseWriter, r *http.Request) {
	s.fail(w, r, http.StatusNotFound, "not found", nil)
}

// ListenAndServe serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute, // document uploads
		WriteTimeout:      5 * time.Minute, // document downloads
		IdleTimeout:       2 * time.Minute,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	go s.housekeeping(ctx)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// auditMonthsAhead is how many months of audit log partitions serve keeps
// ready beyond the current one.
const auditMonthsAhead = 3

// housekeeping runs at start and then every hour until ctx ends: it creates
// the audit log partitions ahead and deletes expired sessions and 2FA
// login challenges.
func (s *Server) housekeeping(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		s.ensureAuditPartitions(ctx)
		s.deleteExpiredSessions(ctx)
		s.deleteExpiredTwoFactorChallenges(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) ensureAuditPartitions(ctx context.Context) {
	n, err := s.store.EnsureAuditPartitions(ctx, auditMonthsAhead)
	switch {
	case err != nil && ctx.Err() == nil:
		s.log.Error("create audit log partitions", "err", err)
	case n > 0:
		s.log.Info("created audit log partitions", "count", n)
	}
}

func (s *Server) deleteExpiredSessions(ctx context.Context) {
	now := s.now()
	n, err := s.store.DeleteExpiredSessions(ctx, now, now.Add(-auth.IdleTimeout))
	switch {
	case err != nil && ctx.Err() == nil:
		s.log.Warn("delete expired sessions", "err", err)
	case n > 0:
		s.log.Debug("deleted expired sessions", "count", n)
	}
}

func (s *Server) deleteExpiredTwoFactorChallenges(ctx context.Context) {
	n, err := s.store.DeleteExpiredTwoFactorChallenges(ctx, s.now())
	switch {
	case err != nil && ctx.Err() == nil:
		s.log.Warn("delete expired two-factor challenges", "err", err)
	case n > 0:
		s.log.Debug("deleted expired two-factor challenges", "count", n)
	}
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.Debug("write response", "err", err)
	}
}

type apiError struct {
	Error string `json:"error"`
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, msg string, err error) {
	if status >= 500 {
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	}
	s.writeJSON(w, status, apiError{Error: msg})
}

// failStore answers a store error. Answers use fixed wording only: a store
// or database message never reaches the client; an error without a known
// meaning is a generic 500 and only logged.
func (s *Server) failStore(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrAuditUnavailable):
		s.failAudit(w, r, err)
	case errors.Is(err, store.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "not found", err)
	case errors.Is(err, store.ErrInvalid):
		s.fail(w, r, http.StatusBadRequest, "invalid input", err)
	default:
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
	}
}

// maxBody is far more than any JSON request of the API needs.
const maxBody = 64 << 10

// decode reads a JSON body into v and answers 400 if it cannot.
func (s *Server) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		s.fail(w, r, http.StatusBadRequest, "invalid request body: "+err.Error(), nil)
		return false
	}
	return true
}
