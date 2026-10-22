package web

import (
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/pklnx/paperless-go/internal/config"
)

// A login alone does not protect the server: any web page open in the
// browser can send requests to it (CSRF), and with DNS rebinding a page can
// even read the responses of a server on localhost. The middleware below
// closes both holes:
//
//   - The Host header must be one of the allowed host names. A rebinding
//     attack uses the attacker's domain as Host, so it is rejected.
//   - State-changing requests must come from the same origin and carry a
//     JSON body, which a cross-site form or simple request cannot send.
//     The upload route (bodyFile in the route table) takes the raw file
//     instead, with one of uploadContentTypes: none of them is
//     CORS-safelisted either, so a cross-site page cannot send them
//     without a preflight, and form and text types stay refused.
//   - Requests with an API key are exempt from both: they carry their
//     credential in a header a cross-site page cannot set, and never use
//     the cookie (see bearerKey and authenticate).

// uploadContentTypes are the content types a bodyFile route accepts. The
// type of the file is detected from its content; the declared one only
// has to be one of these.
var uploadContentTypes = map[string]bool{
	"application/pdf":          true,
	"image/jpeg":               true,
	"image/png":                true,
	"image/heic":               true,
	"image/heif":               true,
	"application/octet-stream": true,
}

// hostAllowed reports whether the request's Host (without port) is allowed.
func (s *Server) hostAllowed(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	for _, h := range s.allowedHosts {
		if host == h {
			return true
		}
	}
	return false
}

// protect applies the browser protections; bodyOf names the body kind of
// the request's route.
func (s *Server) protect(next http.Handler, bodyOf func(*http.Request) body) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")

		if !s.hostAllowed(r) {
			s.log.Info("host not allowed; add it to "+config.EnvAllowedHosts+" if it is yours", "host", r.Host)
			http.Error(w, "host not allowed", http.StatusForbidden)
			return
		}
		// An API key request carries its credential in a header that a
		// cross-site page cannot set, never in a cookie (see bearerKey), so
		// it needs neither Origin nor a particular content type.
		_, keyRequest := bearerKey(r)
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if keyRequest {
				break
			}
			if !sameOrigin(r) {
				http.Error(w, "cross-origin request rejected", http.StatusForbidden)
				return
			}
			mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			switch bodyOf(r) {
			case bodyFile:
				if !uploadContentTypes[mt] {
					http.Error(w, "content type must be application/pdf, image/jpeg, image/png, image/heic, image/heif or application/octet-stream",
						http.StatusUnsupportedMediaType)
					return
				}
			default:
				if mt != "application/json" {
					http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin checks Origin against Host, falling back to Sec-Fetch-Site for
// clients that omit Origin. Requests with neither header are rejected.
func sameOrigin(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		return err == nil && strings.EqualFold(u.Host, r.Host)
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin"
}
