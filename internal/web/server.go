// Package web serves the JSON API and the UI over HTTP.
package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/pklnx/paperless-go/internal/web/ui"
)

// DefaultAllowedHosts are the host names accepted without configuration.
var DefaultAllowedHosts = []string{"localhost", "127.0.0.1", "::1"}

// Pinger reports whether the database answers.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Server holds the dependencies of the HTTP handlers.
type Server struct {
	db           Pinger
	log          *slog.Logger
	allowedHosts []string
}

// Options configure a Server.
type Options struct {
	// AllowedHosts are host names (without port) accepted in the Host
	// header; empty means DefaultAllowedHosts.
	AllowedHosts []string
}

// New creates a Server.
func New(db Pinger, log *slog.Logger, opts Options) *Server {
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
	return &Server{db: db, log: log, allowedHosts: hosts}
}

// Handler returns the HTTP handler with all routes and protections.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	// Unknown API paths answer in JSON, not with the UI.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		s.fail(w, r, http.StatusNotFound, "not found", nil)
	})
	uiHandler := ui.Handler()
	mux.Handle("GET /{$}", uiHandler)
	mux.Handle("GET /assets/", uiHandler)
	return s.protect(mux)
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
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
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
