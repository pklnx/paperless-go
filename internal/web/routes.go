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
	accessLogin                 // the login endpoints: no session needed, refused with a key
	accessPublic                // no login: the health check and the UI code
	accessRead                  // a session or an API key with "read"
	accessUpload                // a session or an API key with "upload" or "write" (the upload only)
	accessWrite                 // a session or an API key with "write"
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

		h("GET /api/profile/api-keys", s.handleListAPIKeys, accessSession),
		h("POST /api/profile/api-keys", s.handleCreateAPIKey, accessSession),
		h("DELETE /api/profile/api-keys/{id}", s.handleRevokeOwnAPIKey, accessSession),

		// Users see their own entries, admins everyone's.
		h("GET /api/audit", s.handleAudit, accessSession),

		{pattern: "POST /api/documents", handler: http.HandlerFunc(s.handleUpload), access: accessUpload, body: bodyFile},
		h("GET /api/documents", s.handleListDocuments, accessRead),
		h("GET /api/documents/{id}", s.handleGetDocument, accessRead),
		h("PATCH /api/documents/{id}", s.handlePatchDocument, accessWrite),
		h("POST /api/documents/bulk", s.handleBulkDocuments, accessWrite),
		h("GET /api/documents/{id}/file", s.handleDocumentFile, accessRead),
		h("GET /api/documents/{id}/thumbnail", s.handleThumbnail, accessRead),
		h("POST /api/documents/{id}/reprocess", s.handleReprocess, accessWrite),
		h("DELETE /api/documents/{id}", s.handleTrashDocument, accessWrite),
		h("POST /api/documents/{id}/restore", s.handleRestoreDocument, accessWrite),
		h("POST /api/documents/{id}/purge", s.handlePurgeDocument, accessWrite),
		h("GET /api/documents/counts", s.handleDocumentCounts, accessRead),

		h("GET /api/tags", s.handleListTags, accessRead),
		h("POST /api/tags", s.handleCreateTag, accessWrite),
		h("PATCH /api/tags/{id}", s.handlePatchTag, accessWrite),
		h("DELETE /api/tags/{id}", s.handleDeleteTag, accessWrite),
		h("GET /api/correspondents", s.handleListCorrespondents, accessRead),
		h("POST /api/correspondents", s.handleCreateCorrespondent, accessWrite),
		h("PATCH /api/correspondents/{id}", s.handlePatchCorrespondent, accessWrite),
		h("DELETE /api/correspondents/{id}", s.handleDeleteCorrespondent, accessWrite),
		h("GET /api/document-types", s.handleListDocumentTypes, accessRead),
		h("POST /api/document-types", s.handleCreateDocumentType, accessWrite),
		h("PATCH /api/document-types/{id}", s.handlePatchDocumentType, accessWrite),
		h("DELETE /api/document-types/{id}", s.handleDeleteDocumentType, accessWrite),

		// Saved views are the caller's own, also for an admin.
		h("GET /api/saved-views", s.handleListSavedViews, accessRead),
		h("POST /api/saved-views", s.handleCreateSavedView, accessWrite),
		h("PATCH /api/saved-views/{id}", s.handlePatchSavedView, accessWrite),
		h("DELETE /api/saved-views/{id}", s.handleDeleteSavedView, accessWrite),

		h("GET /api/users", s.handleListUsers, accessSession),
		h("POST /api/users", s.handleCreateUser, accessSession),
		h("PATCH /api/users/{name}", s.handleUpdateUser, accessSession),
		h("DELETE /api/users/{name}", s.handleDeleteUser, accessSession),
		h("POST /api/users/{name}/password", s.handleResetUserPassword, accessSession),
		h("POST /api/users/{name}/2fa/reset", s.handleResetUserTwoFactor, accessSession),
		h("DELETE /api/users/{name}/passkeys", s.handleRemoveUserPasskeys, accessSession),
		h("GET /api/api-keys", s.handleListAllAPIKeys, accessSession),
		h("DELETE /api/api-keys/{id}", s.handleRevokeAnyAPIKey, accessSession),

		// Every other path under /api/: a JSON 404, after the login.
		h("/api/", s.handleUnknownAPI, accessSession),

		{pattern: "GET /{$}", handler: app, access: accessPublic},
		{pattern: "GET /assets/", handler: app, access: accessPublic},
	}
}
