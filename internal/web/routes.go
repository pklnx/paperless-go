package web

import (
	"net/http"

	"github.com/pklnx/paperless-go/internal/web/ui"
)

// access says who may call a route. The same table registers the routes
// and authorizes requests, so the two cannot drift apart.
type access int

const (
	accessSession access = iota // a session only; the zero value, so a forgotten field is the strictest
	accessLogin                 // the login endpoints: no session needed
	accessPublic                // no login: the health check and the UI code
)

// body says what a state-changing request to a route may carry. protect
// enforces it per route, so a file body is allowed only where the table
// says so.
type body int

const (
	bodyJSON body = iota // application/json; the zero value, so a forgotten field is the strictest
	bodyFile             // a raw file (the upload): one of uploadContentTypes, never a form or text
)

type route struct {
	pattern string // "GET /api/users"
	handler http.Handler
	access  access
	body    body
}

// routes lists every route of the server.
func (s *Server) routes() []route {
	h := func(pattern string, f http.HandlerFunc, a access) route {
		return route{pattern: pattern, handler: f, access: a}
	}
	app := ui.Handler()
	return []route{
		h("GET /healthz", s.handleHealth, accessPublic),

		h("GET /api/session", s.handleGetSession, accessLogin),
		h("POST /api/session", s.handleLogin, accessLogin),
		h("DELETE /api/session", s.handleLogout, accessLogin),
		h("POST /api/session/2fa", s.handleTwoFactorLogin, accessLogin),
		h("POST "+passkeyLoginBeginPath, s.handleBeginPasskeyLogin, accessLogin),
		h("POST "+passkeyLoginFinishPath, s.handleFinishPasskeyLogin, accessLogin),

		h("PUT "+profilePasswordPath, s.handleChangeOwnPassword, accessSession),
		h("GET /api/profile/2fa", s.handleGetTwoFactor, accessSession),
		h("POST /api/profile/2fa/setup", s.handleBeginTwoFactor, accessSession),
		h("POST /api/profile/2fa/confirm", s.handleConfirmTwoFactor, accessSession),
		h("DELETE /api/profile/2fa", s.handleDisableTwoFactor, accessSession),
		h("POST /api/profile/2fa/recovery-codes", s.handleRegenerateRecoveryCodes, accessSession),
		h("GET /api/profile/passkeys", s.handleListPasskeys, accessSession),
		h("POST /api/profile/passkeys/begin", s.handleBeginPasskeyRegistration, accessSession),
		h("POST /api/profile/passkeys/finish", s.handleFinishPasskeyRegistration, accessSession),
		h("DELETE /api/profile/passkeys/{id}", s.handleRemovePasskey, accessSession),

		h("GET /api/audit", s.handleAudit, accessSession),

		{pattern: "POST /api/documents", handler: http.HandlerFunc(s.handleUpload), access: accessSession, body: bodyFile},
		h("GET /api/documents", s.handleListDocuments, accessSession),
		h("GET /api/documents/{id}", s.handleGetDocument, accessSession),
		h("GET /api/documents/{id}/file", s.handleDocumentFile, accessSession),

		h("GET /api/users", s.handleListUsers, accessSession),
		h("POST /api/users", s.handleCreateUser, accessSession),
		h("PATCH /api/users/{name}", s.handleUpdateUser, accessSession),
		h("DELETE /api/users/{name}", s.handleDeleteUser, accessSession),
		h("POST /api/users/{name}/password", s.handleResetUserPassword, accessSession),
		h("POST /api/users/{name}/2fa/reset", s.handleResetUserTwoFactor, accessSession),
		h("DELETE /api/users/{name}/passkeys", s.handleRemoveUserPasskeys, accessSession),

		// Every other path under /api/: a JSON 404, after the login.
		h("/api/", s.handleUnknownAPI, accessSession),

		{pattern: "GET /{$}", handler: app, access: accessPublic},
		{pattern: "GET /assets/", handler: app, access: accessPublic},
	}
}
