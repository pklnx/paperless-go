package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

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

// rawJSON sends body as it is, as the browser of f.
func rawJSON(t *testing.T, f *authFixture, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", f.srv.URL)
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
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
		// What the web cannot show exactly: several IDs of a kind, and
		// "not in the inbox" or "not a favourite".
		webFilter(map[string]any{"tag": []string{"6F9619FF-8B86-D011-B42D-00C04FC964FF", "7F9619FF-8B86-D011-B42D-00C04FC964FF"}}),
		webFilter(map[string]any{"correspondent": []string{"6F9619FF-8B86-D011-B42D-00C04FC964FF", "7F9619FF-8B86-D011-B42D-00C04FC964FF"}}),
		webFilter(map[string]any{"type": []string{"6F9619FF-8B86-D011-B42D-00C04FC964FF", "7F9619FF-8B86-D011-B42D-00C04FC964FF"}}),
		webFilter(map[string]any{"inbox": false}),
		webFilter(map[string]any{"favorite": false}),
		tooLarge,
	} {
		code("POST", "/api/saved-views", map[string]any{"name": "Bad", "filter": bad}, codeInvalidFilter)
		code("PATCH", path, map[string]any{"filter": bad}, codeInvalidFilter)
	}
	code("PATCH", path, map[string]any{"filter": nil}, codeInvalidFilter)

	// Keys are matched by their exact names: encoding/json would match "Q"
	// to the q field case-insensitively, and a repeated key would win
	// over the checked one.
	for _, raw := range []string{
		`{"paperless-go": {"v": 1, "Sort": "deleted", "sort": "date"}}`,
		`{"paperless-go": {"v": 1, "Order": "up", "order": "asc"}}`,
		`{"paperless-go": {"v": 1, "Q": "(((", "q": "ok"}}`,
		`{"paperless-go": {"V": 1}}`,
		`{"paperless-go": {"v": 1, "Tag": ["not-a-uuid"]}}`,
		`{"paperless-go": {"v": 1, "order": "up", "order": "asc"}}`,
		`{"paperless-go": {"v": 1, "sort": "deleted", "sort": "date"}}`,
	} {
		for _, c := range []struct{ method, path, body string }{
			{"POST", "/api/saved-views", `{"name": "Bad", "filter": ` + raw + `}`},
			{"PATCH", path, `{"filter": ` + raw + `}`},
		} {
			status, out := rawJSON(t, f, c.method, c.path, c.body)
			if status != 400 || out["code"] != codeInvalidFilter {
				t.Errorf("%s %s: %d %v, want 400 invalid_filter", c.method, raw, status, out)
			}
		}
	}

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
		"inbox": nil, "favorite": true, "sort": "relevance", "order": "desc", "everyone": true,
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

// sortIndex stays within ±(2^53 − 1), which the web holds exactly; the
// reviewer's repro: a create at the largest bigint used to make every
// later create without an index fail with 500.
func TestSavedViewsSortIndexRange(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", viewPassword, false)
	f.loginOK("alice", viewPassword)
	view := f.expect("POST", "/api/saved-views", map[string]any{"name": "View"}, 201)
	path := "/api/saved-views/" + view["id"].(string)

	for _, n := range []string{"9223372036854775807", "9007199254740992", "-9007199254740992", "-9223372036854775808", "9223372036854775808", "1.5"} {
		if status, out := rawJSON(t, f, "POST", "/api/saved-views", `{"name": "x", "sortIndex": `+n+`}`); status != 400 {
			t.Errorf("POST sortIndex %s: %d %v", n, status, out)
		}
		if status, out := rawJSON(t, f, "PATCH", path, `{"sortIndex": `+n+`}`); status != 400 {
			t.Errorf("PATCH sortIndex %s: %d %v", n, status, out)
		}
	}
	for _, n := range []string{"9007199254740991", "-9007199254740991"} {
		if status, out := rawJSON(t, f, "PATCH", path, `{"sortIndex": `+n+`}`); status != 200 || fmt.Sprint(int64(out["sortIndex"].(float64))) != n {
			t.Errorf("PATCH sortIndex %s: %d %v", n, status, out)
		}
	}
	if status, out := rawJSON(t, f, "POST", "/api/saved-views", `{"name": "Top", "sortIndex": 9007199254740991}`); status != 201 {
		t.Fatalf("POST at the maximum: %d %v", status, out)
	}
	// After the maximum, new views still go last.
	for range 2 {
		next := f.expect("POST", "/api/saved-views", map[string]any{"name": "Next"}, 201)
		if next["sortIndex"] != float64(store.MaxSavedViewSortIndex) {
			t.Errorf("next sortIndex = %v", next["sortIndex"])
		}
	}
}

// Filter text that jsonb cannot hold answers 400 invalid_filter, not 500:
// a lone surrogate escape and bytes that are not UTF-8.
func TestSavedViewsFilterBadText(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", viewPassword, false)
	f.loginOK("alice", viewPassword)
	view := f.expect("POST", "/api/saved-views", map[string]any{"name": "View"}, 201)
	path := "/api/saved-views/" + view["id"].(string)
	for _, bad := range []string{`{"appKey": "\ud800"}`, `{"\udc00": 1}`, "{\"appKey\": \"\xff\xfe\"}", `{"paperless-go": {"v": 1, "q": "\ud800"}}`} {
		for _, c := range []struct{ method, path, body string }{
			{"POST", "/api/saved-views", `{"name": "Bad", "filter": ` + bad + `}`},
			{"PATCH", path, `{"filter": ` + bad + `}`},
		} {
			if status, out := rawJSON(t, f, c.method, c.path, c.body); status != 400 || out["code"] != codeInvalidFilter {
				t.Errorf("%s %q: %d %v, want 400 invalid_filter", c.method, bad, status, out)
			}
		}
	}
	if names := viewNames(t, f); len(names) != 1 {
		t.Errorf("views after refusals: %v", names)
	}
}

// PATCH with paperlessGo replaces only the web's part of the filter and
// keeps the app's keys as stored at the write, also when another client
// changes them while the request waits for the row (#143). The full
// replace of filter stays as it was.
func TestSavedViewsPatchPaperlessGo(t *testing.T) {
	f := newAuthFixtureDB(t, Options{}, map[string]string{"application_name": "web-views"})
	f.addUser("alice", viewPassword, false)
	f.loginOK("alice", viewPassword)
	n := len(auditLog(t, f.st))
	filter := webFilter(map[string]any{"q": "old"})
	filter["app"] = 1.0
	view := f.expect("POST", "/api/saved-views", map[string]any{"name": "View", "filter": filter}, 201)
	id := view["id"].(string)
	path := "/api/saved-views/" + id

	got := f.expect("PATCH", path, map[string]any{"paperlessGo": map[string]any{"v": 1, "q": "new", "inbox": true}}, 200)
	if want := map[string]any{"app": 1, savedViewKey: map[string]any{"v": 1, "q": "new", "inbox": true}}; !sameValue(t, got["filter"], want) {
		t.Errorf("filter %v, want %v", got["filter"], want)
	}
	entries := newEntries(t, f.st, n)
	if d := fmt.Sprint(details(t, entries[len(entries)-1])); d != "map[fields:[paperlessGo] name:View]" {
		t.Errorf("audit details %s", d)
	}

	code := func(body string) {
		t.Helper()
		status, out := rawJSON(t, f, "PATCH", path, body)
		if status != 400 || out["code"] != codeInvalidFilter {
			t.Errorf("%s: %d %v, want 400 invalid_filter", body, status, out)
		}
	}
	for _, bad := range []string{
		`{"paperlessGo": null}`,
		`{"paperlessGo": []}`,
		`{"paperlessGo": "x"}`,
		`{"paperlessGo": {"q": "x"}}`,
		`{"paperlessGo": {"v": 2}}`,
		`{"paperlessGo": {"v": 1, "sort": "deleted"}}`,
		`{"paperlessGo": {"v": 1, "Q": "((("}}`,
		`{"paperlessGo": {"v": 1, "q": "\ud800"}}`,
		`{"paperlessGo": {"v": 1, "q": "` + strings.Repeat("x", maxSavedViewFilter) + `"}}`,
		`{"paperlessGo": {"v": 1}, "filter": {}}`,
	} {
		code(bad)
	}

	// Another client holds the row while it changes the app's key; the
	// web's update waits for it and merges into what it wrote.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	holder, err := pgx.Connect(ctx, f.dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close(context.Background()) }()
	watch, err := pgx.Connect(ctx, f.dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = watch.Close(context.Background()) }()
	tx, err := holder.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `UPDATE saved_views SET filter = jsonb_set(filter, '{app}', '2') WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		status, _, body, err := f.call(ctx, "PATCH", path, map[string]any{"paperlessGo": map[string]any{"v": 1, "q": "newer"}})
		if err != nil || status != 200 {
			t.Errorf("waiting patch: %d %s %v", status, body, err)
		}
		done <- status
	}()
	for {
		var waiting bool
		if err := watch.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND application_name = 'web-views' AND wait_event_type = 'Lock')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case status := <-done:
			t.Fatalf("the patch finished while the row was held: %d", status)
		case <-ctx.Done():
			t.Fatal("the patch never waited for the row")
		default:
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	<-done
	got = f.expect("GET", "/api/saved-views", nil, 200)
	row := got["savedViews"].([]any)[0].(map[string]any)
	if want := map[string]any{"app": 2, savedViewKey: map[string]any{"v": 1, "q": "newer"}}; !sameValue(t, row["filter"], want) {
		t.Errorf("after the concurrent change: %v, want %v", row["filter"], want)
	}

	// filter still replaces the whole object.
	got = f.expect("PATCH", path, map[string]any{"filter": webFilter(nil)}, 200)
	if !sameValue(t, got["filter"], webFilter(nil)) {
		t.Errorf("filter replace: %v", got["filter"])
	}
}

// "q": null is the same as no search, as the web reads it (#143).
func TestSavedViewsNullSearch(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", viewPassword, false)
	f.loginOK("alice", viewPassword)
	filter := webFilter(map[string]any{"q": nil, "sort": nil, "order": nil, "everyone": nil})
	view := f.expect("POST", "/api/saved-views", map[string]any{"name": "Null", "filter": filter}, 201)
	f.expect("PATCH", "/api/saved-views/"+view["id"].(string), map[string]any{"paperlessGo": filter[savedViewKey]}, 200)
}
