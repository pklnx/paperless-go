package web

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pklnx/paperless-go/internal/store"
)

const viewPassword = "correct horse battery"

// webFilter is a filter with the web's part under the paperless-go key.
func webFilter(params map[string]any) map[string]any {
	p := map[string]any{"v": 1}
	for k, v := range params {
		p[k] = v
	}
	return map[string]any{savedViewKey: p}
}

// viewNames returns the names of GET /api/saved-views in the order listed.
func viewNames(t *testing.T, f *authFixture) []string {
	t.Helper()
	out := f.expect("GET", "/api/saved-views", nil, 200)
	rows, ok := out["savedViews"].([]any)
	if !ok {
		t.Fatalf("no savedViews in %v", out)
	}
	names := []string{}
	for _, r := range rows {
		names = append(names, r.(map[string]any)["name"].(string))
	}
	return names
}

// sameValue reports whether two decoded JSON values are equal.
func sameValue(t *testing.T, a, b any) bool {
	t.Helper()
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	var va, vb any
	_ = json.Unmarshal(ja, &va)
	_ = json.Unmarshal(jb, &vb)
	return reflect.DeepEqual(va, vb)
}

// A view round-trips through create, list, patch and delete; new views
// go last, a patch changes only the fields sent, the app's keys of the
// filter are kept, and names may repeat.
func TestSavedViewsRoundTrip(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", viewPassword, false)
	f.loginOK("alice", viewPassword)

	if names := viewNames(t, f); len(names) != 0 {
		t.Fatalf("new user has views: %v", names)
	}
	tag := "6f9619ff-8b86-d011-b42d-00c04fc964ff"
	filter := webFilter(map[string]any{"q": "invoice 2026", "tag": []string{tag}, "inbox": true, "favorite": nil, "sort": "date", "order": "asc", "everyone": false})
	// A key of the app (DocumentFilter.swift) next to the web's part.
	filter["tags"] = []any{map[string]any{"id": "6F9619FF-8B86-D011-B42D-00C04FC964FF", "name": "Tax", "key": "tax"}}
	created := f.expect("POST", "/api/saved-views", map[string]any{"name": "  Taxes  ", "filter": filter}, 201)
	id, _ := created["id"].(string)
	if id == "" || created["name"] != "Taxes" || created["symbol"] != store.DefaultSavedViewSymbol || created["sortIndex"] != float64(0) ||
		!sameValue(t, created["filter"], filter) || created["createdAt"] == nil {
		t.Fatalf("created %v", created)
	}
	second := f.expect("POST", "/api/saved-views", map[string]any{"name": "Taxes", "filter": webFilter(nil)}, 201)
	if second["sortIndex"] != float64(1) {
		t.Errorf("second view's sortIndex = %v, want 1 (after the last)", second["sortIndex"])
	}
	// An explicit sortIndex is kept; without a filter the view has {}.
	first := f.expect("POST", "/api/saved-views", map[string]any{"name": "Top", "sortIndex": -5}, 201)
	if first["sortIndex"] != float64(-5) || !sameValue(t, first["filter"], map[string]any{}) {
		t.Errorf("explicit sortIndex: %v", first)
	}
	if got := viewNames(t, f); !reflect.DeepEqual(got, []string{"Top", "Taxes", "Taxes"}) {
		t.Errorf("order %v", got)
	}

	renamed := f.expect("PATCH", "/api/saved-views/"+id, map[string]any{"name": "Steuern"}, 200)
	if renamed["name"] != "Steuern" || renamed["sortIndex"] != float64(0) || !sameValue(t, renamed["filter"], filter) {
		t.Errorf("renamed %v", renamed)
	}
	moved := f.expect("PATCH", "/api/saved-views/"+id, map[string]any{"sortIndex": 7}, 200)
	if moved["name"] != "Steuern" || moved["sortIndex"] != float64(7) || !sameValue(t, moved["filter"], filter) {
		t.Errorf("moved %v", moved)
	}
	newFilter := webFilter(map[string]any{"q": "tag:Tax"})
	newFilter["laterAppKey"] = map[string]any{"a": []any{1.0, "b"}}
	updated := f.expect("PATCH", "/api/saved-views/"+id, map[string]any{"filter": newFilter}, 200)
	if updated["name"] != "Steuern" || updated["sortIndex"] != float64(7) || !sameValue(t, updated["filter"], newFilter) {
		t.Errorf("filter update %v", updated)
	}
	if got := viewNames(t, f); !reflect.DeepEqual(got, []string{"Top", "Taxes", "Steuern"}) {
		t.Errorf("order after the move %v", got)
	}

	f.expect("PATCH", "/api/saved-views/"+id, map[string]any{}, 400)
	f.expect("PATCH", "/api/saved-views/"+id, map[string]any{"symbol": "star"}, 400)
	f.expect("PATCH", "/api/saved-views/"+id, map[string]any{"sortIndex": nil}, 400)
	f.expect("POST", "/api/saved-views", map[string]any{"name": "X", "symbol": "star"}, 400)

	f.expect("DELETE", "/api/saved-views/"+id, nil, 204)
	f.expect("DELETE", "/api/saved-views/"+id, nil, 404)
	f.expect("PATCH", "/api/saved-views/"+id, map[string]any{"name": "x"}, 404)
	f.expect("DELETE", "/api/saved-views/not-a-uuid", nil, 404)
	if got := viewNames(t, f); !reflect.DeepEqual(got, []string{"Top", "Taxes"}) {
		t.Errorf("after delete %v", got)
	}
}

// Names are checked as metadata names; the web's part of the filter is
// checked with the list's parameter parser, the rest is not.
func TestSavedViewsValidation(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", viewPassword, false)
	f.loginOK("alice", viewPassword)
	view := f.expect("POST", "/api/saved-views", map[string]any{"name": "View"}, 201)
	path := "/api/saved-views/" + view["id"].(string)

	code := func(method, p string, body any, want string) {
		t.Helper()
		out := f.expect(method, p, body, 400)
		if out["code"] != want {
			t.Errorf("%s %v: code %v, want %s (%v)", method, body, out["code"], want, out)
		}
	}
	for _, name := range []any{"", "   ", strings.Repeat("x", 129), "a\nb", nil} {
		code("POST", "/api/saved-views", map[string]any{"name": name}, "invalid_name")
		code("PATCH", path, map[string]any{"name": name}, "invalid_name")
	}

	tooLarge := map[string]any{"appKey": strings.Repeat("x", maxSavedViewFilter)}
	for _, bad := range []any{
		[]any{}, "x", 1.0,
		map[string]any{savedViewKey: nil},
		map[string]any{savedViewKey: []any{}},
		map[string]any{savedViewKey: map[string]any{"q": "x"}},               // no version
		map[string]any{savedViewKey: map[string]any{"v": 2}},                 // a version this server does not know
		webFilter(map[string]any{"unknown": true}),                           // a field the web does not set
		webFilter(map[string]any{"q": "a\x00b"}),                             // the parser refuses NUL
		webFilter(map[string]any{"q": strings.Repeat("w ", 40)}),             // over the parser's item limit
		webFilter(map[string]any{"sort": "deleted"}),                         // the trash's sort
		webFilter(map[string]any{"sort": "size"}),                            // no such sort
		webFilter(map[string]any{"order": "up"}),                             // no such order
		webFilter(map[string]any{"tag": []string{"not-a-uuid"}}),             // not an ID
		webFilter(map[string]any{"type": []string{strings.Repeat("a", 36)}}), // not an ID
		webFilter(map[string]any{"inbox": "yes"}),                            // not a boolean
		webFilter(map[string]any{"q": strings.Repeat("a ", 3000)}),           // over the parser's limits
		tooLarge,
	} {
		code("POST", "/api/saved-views", map[string]any{"name": "Bad", "filter": bad}, codeInvalidFilter)
		code("PATCH", path, map[string]any{"filter": bad}, codeInvalidFilter)
	}
	code("PATCH", path, map[string]any{"filter": nil}, codeInvalidFilter)

	// Within the limit, any keys of the app are kept as they are, and a
	// filter without the web's part is valid (a view made by the app).
	ok := map[string]any{"appKey": strings.Repeat("x", maxSavedViewFilter-20)}
	f.expect("POST", "/api/saved-views", map[string]any{"name": "Large", "filter": ok}, 201)
	appOnly := map[string]any{"inbox": true, "sort": "documentDate", "ascending": false, "futureKey": map[string]any{"x": 1.0}}
	got := f.expect("PATCH", path, map[string]any{"filter": appOnly}, 200)
	if !sameValue(t, got["filter"], appOnly) {
		t.Errorf("app filter: %v", got["filter"])
	}
	// The parameters the list accepts.
	good := webFilter(map[string]any{
		"q": "correspondent:Bank added:2026", "tag": []string{"6F9619FF-8B86-D011-B42D-00C04FC964FF"}, "correspondent": []string{}, "type": nil,
		"inbox": false, "favorite": true, "sort": "relevance", "order": "desc", "everyone": true,
	})
	f.expect("PATCH", path, map[string]any{"filter": good}, 200)
	if names := viewNames(t, f); !reflect.DeepEqual(names, []string{"View", "Large"}) {
		t.Errorf("refused changes changed the views: %v", names)
	}
}

// Saved views are personal: another user's view answers 404, also to an
// admin, and an admin lists only their own.
func TestSavedViewsScope(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", viewPassword, false)
	f.addUser("root", viewPassword, true)
	a := f.as()
	a.loginOK("alice", viewPassword)
	root := f.as()
	root.loginOK("root", viewPassword)

	view := a.expect("POST", "/api/saved-views", map[string]any{"name": "Alice's"}, 201)
	path := "/api/saved-views/" + view["id"].(string)
	root.expect("POST", "/api/saved-views", map[string]any{"name": "Root's"}, 201)

	if names := viewNames(t, root); !reflect.DeepEqual(names, []string{"Root's"}) {
		t.Errorf("admin lists %v", names)
	}
	if names := viewNames(t, a); !reflect.DeepEqual(names, []string{"Alice's"}) {
		t.Errorf("alice lists %v", names)
	}
	root.expect("PATCH", path, map[string]any{"name": "Taken"}, 404)
	root.expect("DELETE", path, nil, 404)
	if got, err := f.st.GetSavedView(context.Background(), store.Scope{All: true}, view["id"].(string)); err != nil || got.Name != "Alice's" {
		t.Errorf("alice's view after the admin's attempts: %+v, %v", got, err)
	}
}

// API keys: read lists, write changes; the route table's test checks the
// refusals for every scope.
func TestSavedViewsWithAPIKeys(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("kim", keyPassword, false)
	f.loginOK("kim", keyPassword)
	read := f.createKey("read", []string{"read"}, "")
	write := f.createKey("write", []string{"write"}, "")

	resp, out := f.keyRequest(write, "POST", "/api/saved-views", []byte(`{"name":"By key"}`), "application/json", nil)
	if resp.StatusCode != 201 {
		t.Fatalf("create with a write key: %d %v", resp.StatusCode, out)
	}
	id := out["id"].(string)
	if resp, out := f.keyRequest(read, "GET", "/api/saved-views", nil, "", nil); resp.StatusCode != 200 || len(out["savedViews"].([]any)) != 1 {
		t.Errorf("list with a read key: %d %v", resp.StatusCode, out)
	}
	if resp, _ := f.keyRequest(read, "PATCH", "/api/saved-views/"+id, []byte(`{"name":"x"}`), "application/json", nil); resp.StatusCode != 403 {
		t.Errorf("patch with a read key: %d", resp.StatusCode)
	}
	if resp, _ := f.keyRequest(write, "DELETE", "/api/saved-views/"+id, nil, "", nil); resp.StatusCode != 204 {
		t.Errorf("delete with a write key: %d", resp.StatusCode)
	}
	last := auditLog(t, f.st)
	if e := last[len(last)-1]; e.Action != "saved_view.delete" || e.Actor != store.ActorAPIKey {
		t.Errorf("last entry %s by %s", e.Action, e.Actor)
	}
}

// Each change writes its entry: the view, its name, the fields sent and a
// rename's old name, never the filter. Without the audit log nothing
// changes.
func TestSavedViewsAudit(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", viewPassword, false)
	f.loginOK("alice", viewPassword)
	n := len(auditLog(t, f.st))

	secret := webFilter(map[string]any{"q": "secretword"})
	view := f.expect("POST", "/api/saved-views", map[string]any{"name": "Old", "filter": secret}, 201)
	id := view["id"].(string)
	f.expect("PATCH", "/api/saved-views/"+id, map[string]any{"name": "New", "filter": secret}, 200)
	f.expect("PATCH", "/api/saved-views/"+id, map[string]any{"sortIndex": 3}, 200)
	f.expect("DELETE", "/api/saved-views/"+id, nil, 204)

	entries := newEntries(t, f.st, n)
	want := []struct{ action, name, details string }{
		{"saved_view.create", "Old", "map[name:Old]"},
		{"saved_view.update", "New", "map[fields:[name filter] name:New oldName:Old]"},
		{"saved_view.update", "New", "map[fields:[sortIndex] name:New]"},
		{"saved_view.delete", "New", "map[name:New]"},
	}
	if len(entries) != len(want) {
		t.Fatalf("%d entries, want %d", len(entries), len(want))
	}
	for i, w := range want {
		e := entries[i]
		d := details(t, e)
		if e.Action != w.action || e.TargetType != store.TargetSavedView || e.TargetID != id || e.TargetName != w.name ||
			fmt.Sprint(d) != w.details || e.UserName != "alice" {
			t.Errorf("entry %d: %s %s %s %s %v by %s, want %+v", i, e.Action, e.TargetType, e.TargetID, e.TargetName, d, e.UserName, w)
		}
		if strings.Contains(string(e.Details), "secretword") {
			t.Errorf("entry %d holds the filter: %s", i, e.Details)
		}
	}

	other := f.expect("POST", "/api/saved-views", map[string]any{"name": "Kept"}, 201)
	breakAudit(t, f.dbURL)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/saved-views", map[string]any{"name": "Lost"}},
		{"PATCH", "/api/saved-views/" + other["id"].(string), map[string]any{"name": "Renamed"}},
		{"DELETE", "/api/saved-views/" + other["id"].(string), nil},
	} {
		if out := f.expect(c.method, c.path, c.body, 503); out["error"] != errAuditUnavailable {
			t.Errorf("%s %s: %v", c.method, c.path, out)
		}
	}
	if names := viewNames(t, f); !reflect.DeepEqual(names, []string{"Kept"}) {
		t.Errorf("views without entries: %v", names)
	}
}
