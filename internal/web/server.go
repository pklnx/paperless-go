// Package web serves the JSON API and the UI over HTTP.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/pklnx/paperless-go/internal/auth"
	"github.com/pklnx/paperless-go/internal/blobstore"
	"github.com/pklnx/paperless-go/internal/config"
	"github.com/pklnx/paperless-go/internal/crypto"
	"github.com/pklnx/paperless-go/internal/ingest"
	"github.com/pklnx/paperless-go/internal/search"
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
	// forwardedWarned is set once the ignored X-Forwarded-For has been
	// logged; serve runs one Server, so that is once per process.
	forwardedWarned atomic.Bool
	// sealer and secretKey protect TOTP secrets and recovery codes.
	sealer     *crypto.Sealer
	secretKey  []byte
	require2FA bool
	// webauthn is nil without a public URL: no passkeys. publicOrigin is
	// where they work.
	webauthn      *webauthn.WebAuthn
	publicOrigin  string
	passkeyBegins *auth.Rate
	// apiKeyRate counts requests per API key, badKeyRate invalid keys per
	// client.
	apiKeyRate *auth.Rate
	badKeyRate *auth.Rate
	// unknownKeyAudit allows one audit entry for an unknown API key per
	// client and window; openLoginLog one log line per client and minute
	// for the caps on open passkey logins.
	unknownKeyAudit *auth.Rate
	openLoginLog    *auth.Rate
	// blobs holds the document files and importer imports uploads into
	// it; without blobs the document routes answer 503.
	blobs    *blobstore.Store
	importer *ingest.Importer
	// sweepWake wakes the sweeper after a purge request (nil: none).
	sweepWake func()
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
	// PublicURL is the address users open (PAPERLESS_GO_PUBLIC_URL). It
	// turns passkeys on; its host is accepted in addition to AllowedHosts.
	PublicURL string
	// Blobs holds the document files. Without it the document routes
	// answer 503.
	Blobs *blobstore.Store
	// MaxUploadSize is the largest upload in bytes
	// (PAPERLESS_GO_MAX_UPLOAD_SIZE); 0 means config.DefaultMaxUploadSize.
	MaxUploadSize int64
	// SweepWake wakes the sweeper of this process after a purge was
	// asked for, so "delete permanently" takes effect within seconds; nil
	// leaves it to the sweeper's next round. It must not block.
	SweepWake func()
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
	wa, origin, err := newWebAuthn(opts.PublicURL)
	if err != nil {
		// config.Load checked the URL already; this is a programming error.
		log.Error("passkeys are off", "err", err)
	}
	if wa != nil && !slices.Contains(hosts, wa.Config.RPID) {
		hosts = append(hosts, wa.Config.RPID)
	}
	passkeyBegins := auth.NewRate(passkeyBeginLimit, passkeyBeginWindow)
	passkeyBegins.SetClock(now)
	apiKeyRate := auth.NewRate(apiKeyLimit, apiKeyWindow)
	apiKeyRate.SetClock(now)
	badKeyRate := auth.NewRate(badAPIKeyLimit, apiKeyWindow)
	badKeyRate.SetClock(now)
	unknownKeyAudit := auth.NewRate(1, apiKeyWindow)
	unknownKeyAudit.SetClock(now)
	openLoginLog := auth.NewRate(1, time.Minute)
	openLoginLog.SetClock(now)
	s := &Server{
		store: st, db: pinger, log: log, allowedHosts: hosts, hasher: hasher, now: now, limiter: limiter,
		trustedProxies: opts.TrustedProxies, sealer: opts.Sealer, secretKey: opts.SecretKey, require2FA: opts.Require2FA,
		webauthn: wa, publicOrigin: origin, passkeyBegins: passkeyBegins, apiKeyRate: apiKeyRate, badKeyRate: badKeyRate,
		unknownKeyAudit: unknownKeyAudit, openLoginLog: openLoginLog, sweepWake: opts.SweepWake,
	}
	if opts.Blobs != nil && st != nil {
		maxSize := opts.MaxUploadSize
		if maxSize <= 0 {
			maxSize = config.DefaultMaxUploadSize
		}
		// maxSize is positive, so New cannot fail.
		s.importer, _ = ingest.New(st, opts.Blobs, maxSize)
		s.blobs = opts.Blobs
	}
	return s
}

// Handler returns the HTTP handler with all routes and protections.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	accessOf := make(map[string]access)
	bodyOf := make(map[string]body)
	for _, rt := range s.routes() {
		mux.Handle(rt.pattern, rt.handler)
		accessOf[rt.pattern] = rt.access
		bodyOf[rt.pattern] = rt.body
	}
	routeBody := func(r *http.Request) body {
		_, pattern := mux.Handler(r)
		return bodyOf[pattern]
	}
	h := s.protect(s.authenticate(mux, accessOf), routeBody)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.warnIgnoredForwardedFor(r)
		// Every request's writes wait at most ownerWaitBound for other
		// changes of the same owner (store.ErrBusy, 503).
		h.ServeHTTP(w, r.WithContext(store.WithOwnerWait(r.Context(), ownerWaitBound)))
	})
}

// ownerWait is how long a request's write waits for other changes of the
// same owner before it answers 503 with Retry-After (busyRetryAfter):
// about five bulk changes of MaxBulkMatches queued ahead, well below a
// reverse proxy's usual timeout. It is no setting.
const ownerWait = 15 * time.Second

// purgeRetryAfter is the Retry-After, in seconds, of an upload whose
// duplicate is due for purge: the sweeper deletes it within a minute.
const purgeRetryAfter = "60"

// busyRetryAfter is the Retry-After of that 503, in seconds.
const busyRetryAfter = "5"

// ownerWaitBound is ownerWait; tests shorten it.
var ownerWaitBound = ownerWait

// warnIgnoredForwardedFor logs once when a request carries X-Forwarded-For
// from a peer that is not a trusted proxy, also from loopback: behind a
// reverse proxy without PAPERLESS_GO_TRUSTED_PROXIES every client gets the
// proxy's address and all share one login limit. Only the peer is logged;
// the header comes from the client and could be anything.
func (s *Server) warnIgnoredForwardedFor(r *http.Request) {
	if s.forwardedWarned.Load() || len(r.Header.Values("X-Forwarded-For")) == 0 {
		return
	}
	peer := peerAddr(r)
	if s.trustedProxy(peer) || !s.forwardedWarned.CompareAndSwap(false, true) {
		return
	}
	s.log.Warn("X-Forwarded-For from a peer that is not a trusted proxy is ignored; behind a reverse proxy set "+
		config.EnvTrustedProxies+", else all clients share one login limit", "peer", peer)
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

// newHTTPServer is the HTTP server for handler. A request has 10 seconds
// for its headers (at most 64 KiB) and 30 for its body, which is enough for
// every JSON body (at most maxBody); uploads and downloads set their own
// hour-long read and write deadlines (transferTimeout).
func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
}

// ListenAndServe serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := newHTTPServer(addr, s.Handler())
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
// the audit log partitions ahead and deletes expired sessions, 2FA login
// challenges and passkey ceremonies, and API keys revoked or expired 30
// days ago.
func (s *Server) housekeeping(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		s.ensureAuditPartitions(ctx)
		s.deleteExpiredSessions(ctx)
		s.deleteExpiredTwoFactorChallenges(ctx)
		s.deleteExpiredWebAuthnCeremonies(ctx)
		s.deleteOldAPIKeys(ctx)
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

func (s *Server) deleteExpiredWebAuthnCeremonies(ctx context.Context) {
	n, err := s.store.DeleteExpiredWebAuthnCeremonies(ctx, s.now())
	switch {
	case err != nil && ctx.Err() == nil:
		s.log.Warn("delete expired passkey ceremonies", "err", err)
	case n > 0:
		s.log.Debug("deleted expired passkey ceremonies", "count", n)
	}
}

func (s *Server) deleteOldAPIKeys(ctx context.Context) {
	n, err := s.store.DeleteOldAPIKeys(ctx, s.now())
	switch {
	case err != nil && ctx.Err() == nil:
		s.log.Warn("delete old API keys", "err", err)
	case n > 0:
		s.log.Debug("deleted API keys revoked or expired 30 days ago", "count", n)
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
	// Code names an error the UI explains in its own words.
	Code string `json:"code,omitempty"`
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
	var ve *store.ValidationError
	switch {
	case errors.Is(err, store.ErrAuditUnavailable):
		s.failAudit(w, r, err)
	case errors.Is(err, store.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "not found", err)
	case errors.Is(err, store.ErrInvalidCursor):
		s.fail(w, r, http.StatusBadRequest, "invalid cursor", err)
	case errors.Is(err, store.ErrInvalidSort):
		s.fail(w, r, http.StatusBadRequest, "invalid sort", err)
	case errors.Is(err, search.ErrTooLong):
		s.fail(w, r, http.StatusBadRequest, "query too long", err)
	case errors.Is(err, store.ErrSearchTooBroad):
		// The search ran into store.Store.SearchTimeout: the query asks
		// too much (many short words, a broad word ranked), not a fault.
		s.log.Warn("search too broad: stopped at the time limit", "method", r.Method, "path", r.URL.Path)
		s.writeJSON(w, http.StatusUnprocessableEntity, apiError{
			Error: "search too broad; use fewer or longer words, or a filter", Code: "search_too_broad"})
	case errors.Is(err, search.ErrInvalid), errors.As(err, &ve) && ve.Field == "q":
		s.fail(w, r, http.StatusBadRequest, "invalid query", err)
	case errors.Is(err, store.ErrInvalid):
		s.fail(w, r, http.StatusBadRequest, "invalid input", err)
	case errors.Is(err, store.ErrRetry):
		// A deadlock with a concurrent change: nothing was written.
		w.Header().Set("Retry-After", "1")
		s.fail(w, r, http.StatusServiceUnavailable, "a concurrent change got in the way; try again", err)
	case errors.Is(err, store.ErrBusy):
		// Another change of the owner ran longer than ownerWait: nothing
		// was written.
		// It is load, not a fault: a warning, not "request failed".
		s.log.Warn("request busy: another change of the owner is still running", "method", r.Method, "path", r.URL.Path, "err", err)
		w.Header().Set("Retry-After", busyRetryAfter)
		s.writeJSON(w, http.StatusServiceUnavailable, apiError{Error: "another change of these documents is still running; try again"})
	case errors.Is(err, store.ErrPurgePending):
		// The owner's document with this file is being removed for good:
		// not a duplicate any more, not importable yet.
		w.Header().Set("Retry-After", purgeRetryAfter)
		s.writeJSON(w, http.StatusServiceUnavailable, apiError{Error: "the document with this file is being removed permanently; try again in a minute"})
	case errors.Is(err, store.ErrBlobRemoved):
		// The sweeper is removing a file of this change (an upload of a
		// file whose last document was just purged): nothing was
		// written, and the retry stores the file again.
		// Retry-After: a second when the file is gone (the retry stores
		// it again), longer while the sweeper's mark is on it.
		retry := 1
		var br *store.BlobRemovedError
		if errors.As(err, &br) {
			retry = max(1, int(math.Ceil(br.RetryAfter.Seconds())))
		}
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		s.writeJSON(w, http.StatusServiceUnavailable, apiError{Error: "the file is being cleaned up; try again"})
	case errors.Is(err, store.ErrUnknownUser):
		// The user was removed while the request ran; the session is gone
		// with it.
		s.fail(w, r, http.StatusUnauthorized, "login required", err)
	default:
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
	}
}

// maxBody is far more than any JSON request of the API needs.
const maxBody = 64 << 10

// decode reads a JSON body into v and answers 400 if it cannot.
func (s *Server) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	return s.decodeLimit(w, r, v, maxBody)
}

// decodeLimit is decode for a body of up to limit bytes.
func (s *Server) decodeLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		s.fail(w, r, http.StatusBadRequest, "invalid request body: "+err.Error(), nil)
		return false
	}
	return true
}
