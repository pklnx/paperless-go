package web

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pklnx/paperless-go/internal/store"
)

func TestUserAdminNeedsAdmin(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("admin", "correct horse battery", true)
	f.addUser("kim", "correct horse battery", false)
	f.loginOK("kim", "correct horse battery")
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/users", nil},
		{"POST", "/api/users", map[string]any{"name": "new"}},
		{"PATCH", "/api/users/admin", map[string]any{"locked": true}},
		{"DELETE", "/api/users/admin", nil},
		{"POST", "/api/users/admin/password", nil},
	} {
		f.expect(c.method, c.path, c.body, 403)
	}
}

func TestCreatedUserMustChangePassword(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("admin", "correct horse battery", true)
	f.loginOK("admin", "correct horse battery")

	out := f.expect("POST", "/api/users", map[string]any{"name": " Kim ", "admin": false}, 201)
	pw, _ := out["password"].(string)
	if out["name"] != "kim" || len(pw) != 23 {
		t.Fatalf("created: %v", out)
	}
	f.expect("POST", "/api/users", map[string]any{"name": "kim"}, 409)
	f.expect("POST", "/api/users", map[string]any{"name": "not valid"}, 400)
	list := f.expect("GET", "/api/users", nil, 200)
	if users, _ := list["users"].([]any); len(users) != 2 || !strings.Contains(toJSON(users), `"mustChangePassword":true`) {
		t.Fatalf("list: %v", list)
	}

	kim := f.as()
	kim.loginOK("kim", pw)
	if out := kim.expect("GET", "/api/session", nil, 200); out["user"].(map[string]any)["mustChangePassword"] != true {
		t.Fatalf("session: %v", out)
	}
	// Nothing but the password change works yet.
	if out := kim.expect("GET", "/api/users", nil, 403); out["passwordChangeRequired"] != true {
		t.Fatalf("before change: %v", out)
	}
	if out := kim.expect("GET", "/api/does-not-exist", nil, 403); out["passwordChangeRequired"] != true {
		t.Fatalf("unknown path before change: %v", out)
	}
	kim.expect("PUT", "/api/profile/password", map[string]string{"current": pw, "new": pw}, 400)
	kim.expect("PUT", "/api/profile/password", map[string]string{"current": pw, "new": "kims own passphrase"}, 204)
	kim.expect("GET", "/api/users", nil, 403) // not an admin, but no longer restricted
	if out := kim.expect("GET", "/api/session", nil, 200); out["user"].(map[string]any)["mustChangePassword"] != false {
		t.Fatalf("session after change: %v", out)
	}
	if out := kim.expect("GET", "/api/users", nil, 403); out["passwordChangeRequired"] == true {
		t.Fatalf("still restricted: %v", out)
	}
	// The generated password no longer works.
	if resp := f.as().login("kim", pw); resp.StatusCode != 401 {
		t.Fatalf("old password: %d", resp.StatusCode)
	}
}

func TestResetPasswordEndsSessions(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("admin", "correct horse battery", true)
	f.addUser("kim", "kims own passphrase", false)
	f.loginOK("admin", "correct horse battery")
	kim := f.as()
	kim.loginOK("kim", "kims own passphrase")

	out := f.expect("POST", "/api/users/kim/password", nil, 200)
	pw, _ := out["password"].(string)
	kim.expect("GET", sessionCheckPath, nil, 401)
	if resp := f.as().login("kim", "kims own passphrase"); resp.StatusCode != 401 {
		t.Fatalf("old password after reset: %d", resp.StatusCode)
	}
	again := f.as()
	again.loginOK("kim", pw)
	if out := again.expect("GET", "/api/users", nil, 403); out["passwordChangeRequired"] != true {
		t.Fatalf("after reset: %v", out)
	}
	f.expect("POST", "/api/users/nobody/password", nil, 404)
}

func TestAdminCannotChangeOwnLogin(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("admin", "correct horse battery", true)
	f.addUser("second", "correct horse battery", true)
	f.loginOK("admin", "correct horse battery")
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"PATCH", "/api/users/admin", map[string]any{"locked": true}},
		{"PATCH", "/api/users/admin", map[string]any{"admin": false}},
		{"DELETE", "/api/users/admin", nil},
		{"POST", "/api/users/admin/password", nil},
	} {
		f.expect(c.method, c.path, c.body, 403)
	}
	// Other admins can be changed; one field per request.
	f.expect("PATCH", "/api/users/second", map[string]any{"admin": false, "locked": true}, 400)
	f.expect("PATCH", "/api/users/second", map[string]any{}, 400)
	f.expect("PATCH", "/api/users/second", map[string]any{"admin": false}, 204)
	f.expect("PATCH", "/api/users/second", map[string]any{"locked": true}, 204)
	f.expect("PATCH", "/api/users/second", map[string]any{"locked": false}, 204)
	f.expect("PATCH", "/api/users/nobody", map[string]any{"locked": true}, 404)
	// The demoted user cannot use the admin endpoints anymore.
	second := f.as()
	second.loginOK("second", "correct horse battery")
	second.expect("GET", "/api/users", nil, 403)

	list := f.expect("GET", "/api/users", nil, 200)
	users, _ := list["users"].([]any)
	if len(users) != 2 || users[0].(map[string]any)["self"] != true || users[1].(map[string]any)["self"] != false {
		t.Fatalf("list: %v", list)
	}
}

func TestDeleteUserWithDocuments(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	f.addUser("admin", "correct horse battery", true)
	kim := f.addUser("kim", "correct horse battery", false)
	f.addUser("empty", "correct horse battery", false)
	if _, err := f.st.CreateTag(ctx, store.Scope{UserID: kim.ID}, store.TagInput{Name: "Tax"}); err != nil {
		t.Fatal(err)
	}
	// A user with a session who owns nothing can be removed.
	empty := f.as()
	empty.loginOK("empty", "correct horse battery")

	f.loginOK("admin", "correct horse battery")
	list := f.expect("GET", "/api/users", nil, 200)
	users, _ := list["users"].([]any)
	if len(users) != 3 || toJSON(users[2].(map[string]any)["owned"]) != `{"correspondents":0,"documentTypes":0,"documents":0,"tags":1}` {
		t.Fatalf("list: %v", list)
	}
	if out := f.expect("DELETE", "/api/users/kim", nil, 409); out["error"] != `"kim" still owns 1 tag; delete them first` {
		t.Fatalf("delete an owner: %v", out)
	}
	f.expect("DELETE", "/api/users/empty", nil, 204)
	f.expect("DELETE", "/api/users/empty", nil, 404)
	empty.expect("GET", sessionCheckPath, nil, 401)
}

func TestChangeOwnPassword(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("kim", "kims own passphrase", false)
	f.loginOK("kim", "kims own passphrase")
	other := f.as()
	other.loginOK("kim", "kims own passphrase")

	f.expect("PUT", "/api/profile/password", map[string]string{"current": "kims own passphrase", "new": "short"}, 400)
	f.expect("PUT", "/api/profile/password", map[string]string{"current": "kims own passphrase", "new": "a brand new passphrase"}, 204)
	// This browser stays logged in, the other one is logged out.
	f.expect("GET", sessionCheckPath, nil, 200)
	other.expect("GET", sessionCheckPath, nil, 401)

	// Wrong current passwords count like failed logins.
	for range 5 {
		f.expect("PUT", "/api/profile/password", map[string]string{"current": "wrong wrong wrong", "new": "yet another passphrase"}, 401)
	}
	f.expect("PUT", "/api/profile/password", map[string]string{"current": "a brand new passphrase", "new": "yet another passphrase"}, 429)
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
