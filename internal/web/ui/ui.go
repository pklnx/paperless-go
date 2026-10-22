// Package ui embeds the web UI built from web/ (see web/package.json).
//
// The dist directory is produced by `make web` (pnpm build). The repository
// only contains dist/.keep, so plain `go build` works and serves a hint page
// until the UI is built; Docker images and CI always build it.
package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// contentSecurityPolicy for the UI: only same-origin scripts, styles and
// requests. The PDF viewer (pdf.js) works under it unchanged: its worker is
// a file of the build (workers fall back to script-src), its data files are
// under /assets/, it needs no eval, and it runs without WebAssembly (which
// would need 'wasm-unsafe-eval'). TestContentSecurityPolicy pins this.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// permissionsPolicy turns off browser features the UI never uses.
const permissionsPolicy = "camera=(), microphone=(), geolocation=()"

// Handler serves index.html at "/" and the hashed build assets at
// "/assets/".
func Handler() http.Handler {
	files, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	index, err := fs.ReadFile(files, "index.html")
	if err != nil {
		index = nil // UI not built
	}
	assets := http.FileServerFS(files)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Frame-Options", "DENY")
		h.Set("Permissions-Policy", permissionsPolicy)
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			// File names contain a content hash, so they never change.
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
			assets.ServeHTTP(w, r)
			return
		}
		h.Set("Cache-Control", "no-cache")
		if index == nil {
			h.Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("The web UI is not built. Run `make web` and rebuild, or use the Docker image.\n"))
			return
		}
		h.Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
}
