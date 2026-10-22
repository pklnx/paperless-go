package web

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/pklnx/paperless-go/internal/store"
)

const metaPassword = "correct horse battery"

// metaRows returns the rows of a metadata list answer by name.
func metaRows(t *testing.T, out map[string]any, key string) map[string]map[string]any {
	t.Helper()
	rows, ok := out[key].([]any)
	if !ok {
		t.Fatalf("no %s in %v", key, out)
	}
	m := map[string]map[string]any{}
	for _, r := range rows {
		row := r.(map[string]any)
		m[row["name"].(string)] = row
	}
	return m
}

// Each kind round-trips through create, list, rename and delete.
func TestMetadataCreateRenameDelete(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.addUser("alice", metaPassword, false)
	a := f.as()
	a.loginOK("alice", metaPassword)
	owner := float64(alice.ID)

	for _, kind := range []struct{ path, key string }{
		{"/api/tags", "tags"}, {"/api/correspondents", "correspondents"}, {"/api/document-types", "documentTypes"},
	} {
		created := a.expect("POST", kind.path, map[string]any{"name": "  Stadtwerke  "}, 201)
		id, _ := created["id"].(string)
		if id == "" || created["name"] != "Stadtwerke" || created["ownerId"] != owner {
			t.Fatalf("%s: created %v", kind.path, created)
		}
		row := metaRows(t, a.expect("GET", kind.path, nil, 200), kind.key)["Stadtwerke"]
		if row == nil || row["id"] != id || row["documentCount"] != float64(0) || row["ownerId"] != owner {
			t.Errorf("%s: listed %v", kind.path, row)
		}
		renamed := a.expect("PATCH", kind.path+"/"+id, map[string]any{"name": "SWM"}, 200)
		if renamed["name"] != "SWM" || renamed["id"] != id {
			t.Errorf("%s: renamed %v", kind.path, renamed)
		}
		a.expect("DELETE", kind.path+"/"+id, nil, 204)
		a.expect("DELETE", kind.path+"/"+id, nil, 404)
		a.expect("PATCH", kind.path+"/"+id, map[string]any{"name": "x"}, 404)
		if rows := metaRows(t, a.expect("GET", kind.path, nil, 200), kind.key); len(rows) != 0 {
			t.Errorf("%s: after delete %v", kind.path, rows)
		}
		// Fields the web does not set are refused, not ignored.
		a.expect("POST", kind.path, map[string]any{"name": "X", "matchText": "x"}, 400)
	}

	// The tag fields.
	parent := a.expect("POST", "/api/tags", map[string]any{"name": "Parent"}, 201)
	if parent["color"] != store.DefaultTagColor || parent["symbol"] != store.DefaultTagSymbol || parent["parentId"] != nil {
		t.Errorf("defaults: %v", parent)
	}
	child := a.expect("POST", "/api/tags", map[string]any{"name": "Child", "parentId": parent["id"], "color": "#a1b2c3"}, 201)
	if child["parentId"] != parent["id"] || child["color"] != "#a1b2c3" {
		t.Errorf("child: %v", child)
	}
	id := child["id"].(string)
	// A patch with only a name keeps the colour and parent.
	got := a.expect("PATCH", "/api/tags/"+id, map[string]any{"name": "Kind"}, 200)
	if got["color"] != "#a1b2c3" || got["parentId"] != parent["id"] || got["symbol"] != store.DefaultTagSymbol {
		t.Errorf("name patch: %v", got)
	}
	got = a.expect("PATCH", "/api/tags/"+id, map[string]any{"parentId": nil, "color": "#000000"}, 200)
	if got["parentId"] != nil || got["color"] != "#000000" || got["name"] != "Kind" {
		t.Errorf("root and colour patch: %v", got)
	}
	a.expect("PATCH", "/api/tags/"+id, map[string]any{}, 400)
	a.expect("PATCH", "/api/tags/"+id, map[string]any{"symbol": "star"}, 400)
}

func TestMetadataNamesAndColors(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.addUser("alice", metaPassword, false)
	a := f.as()
	a.loginOK("alice", metaPassword)

	code := func(method, path string, body any, status int, want string) {
		t.Helper()
		out := a.expect(method, path, body, status)
		if out["code"] != want {
			t.Errorf("%s %s %v: code %v, want %s", method, path, body, out["code"], want)
		}
	}
	a.expect("POST", "/api/correspondents", map[string]any{"name": "Bank"}, 201)
	code("POST", "/api/correspondents", map[string]any{"name": "BANK"}, 409, "name_taken")
	other := a.expect("POST", "/api/correspondents", map[string]any{"name": "Insurance"}, 201)
	code("PATCH", "/api/correspondents/"+other["id"].(string), map[string]any{"name": "bank"}, 409, "name_taken")
	a.expect("POST", "/api/document-types", map[string]any{"name": "Bank"}, 201) // another kind: fine
	code("POST", "/api/document-types", map[string]any{"name": "bank"}, 409, "name_taken")

	p1 := a.expect("POST", "/api/tags", map[string]any{"name": "Home"}, 201)
	p2 := a.expect("POST", "/api/tags", map[string]any{"name": "Work"}, 201)
	a.expect("POST", "/api/tags", map[string]any{"name": "Bills", "parentId": p1["id"]}, 201)
	code("POST", "/api/tags", map[string]any{"name": "bills", "parentId": p1["id"]}, 409, "name_taken")
	a.expect("POST", "/api/tags", map[string]any{"name": "Bills", "parentId": p2["id"]}, 201)
	a.expect("POST", "/api/tags", map[string]any{"name": "Bills"}, 201)

	long := strings.Repeat("ä", 128)
	a.expect("POST", "/api/tags", map[string]any{"name": long}, 201)
	for _, name := range []any{"", "   ", long + "x", nil} {
		code("POST", "/api/tags", map[string]any{"name": name}, 400, "invalid_name")
		code("PATCH", "/api/tags/"+p1["id"].(string), map[string]any{"name": name}, 400, "invalid_name")
	}
	for _, color := range []any{"red", "#12345", "#1234567", "#GGGGGG", nil} {
		if color != nil {
			code("POST", "/api/tags", map[string]any{"name": "C", "color": color}, 400, "invalid_color")
		}
		code("PATCH", "/api/tags/"+p1["id"].(string), map[string]any{"color": color}, 400, "invalid_color")
	}

	// Only the fields sent are checked: a colour patch of an imported tag
	// whose name is longer than the API allows works.
	imported, err := f.st.CreateTag(context.Background(), store.Scope{UserID: alice.ID}, store.TagInput{Name: strings.Repeat("n", 300)})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.expect("PATCH", "/api/tags/"+imported.ID, map[string]any{"color": "#FF0000"}, 200); got["name"] != imported.Name || got["color"] != "#FF0000" {
		t.Errorf("colour patch of a long name: %v", got)
	}
}

// Names with control characters (Unicode Cc: NUL, newline, tab, DEL, C1)
// are refused with invalid_name on create and rename of every kind, also
// at the start or end where trimming would have removed them, and nothing
// is written. NUL used to reach PostgreSQL and answer 500.
func TestMetadataNameControlCharacters(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", metaPassword, false)
	a := f.as()
	a.loginOK("alice", metaPassword)

	kinds := []struct{ path, key string }{
		{"/api/tags", "tags"}, {"/api/correspondents", "correspondents"}, {"/api/document-types", "documentTypes"},
	}
	ids := map[string]string{}
	for _, k := range kinds {
		ids[k.path] = a.expect("POST", k.path, map[string]any{"name": "Keep"}, 201)["id"].(string)
	}
	n := len(auditLog(t, f.st))

	refuse := func(path string, names []string) {
		t.Helper()
		for _, name := range names {
			for _, req := range []struct{ method, path string }{{"POST", path}, {"PATCH", path + "/" + ids[path]}} {
				out := a.expect(req.method, req.path, map[string]any{"name": name}, 400)
				if out["code"] != "invalid_name" {
					t.Errorf("%s %s %q: code %v, want invalid_name", req.method, req.path, name, out["code"])
				}
			}
		}
	}
	refuse("/api/tags", []string{"a\x00b", "a\nb", "Tax\n", "\tTax", "a\rb", "a\u007fb", "a\u0085b", "\x00"})
	refuse("/api/correspondents", []string{"a\x00b", "a\nb", "Tax\n"})
	refuse("/api/document-types", []string{"a\x00b", "a\nb", "\tTax"})

	for _, k := range kinds {
		if rows := metaRows(t, a.expect("GET", k.path, nil, 200), k.key); len(rows) != 1 || rows["Keep"] == nil {
			t.Errorf("%s after refused names: %v", k.key, rows)
		}
	}
	if got := newEntries(t, f.st, n); len(got) != 0 {
		t.Errorf("refused names wrote audit entries: %+v", got)
	}

	// Other characters, inner spaces and outer spaces are still fine.
	for _, k := range kinds {
		if got := a.expect("POST", k.path, map[string]any{"name": "  Straße & Co ✓  "}, 201); got["name"] != "Straße & Co ✓" {
			t.Errorf("%s: created %v", k.path, got)
		}
		if got := a.expect("PATCH", k.path+"/"+ids[k.path], map[string]any{"name": "Neue Bank AG"}, 200); got["name"] != "Neue Bank AG" {
			t.Errorf("%s: renamed %v", k.path, got)
		}
	}
}

func TestMetadataTagErrors(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", metaPassword, false)
	f.addUser("bob", metaPassword, false)
	a := f.as()
	a.loginOK("alice", metaPassword)
	b := f.as()
	b.loginOK("bob", metaPassword)

	parent := a.expect("POST", "/api/tags", map[string]any{"name": "Parent"}, 201)
	child := a.expect("POST", "/api/tags", map[string]any{"name": "Taxes", "parentId": parent["id"]}, 201)
	a.expect("POST", "/api/tags", map[string]any{"name": "taxes"}, 201) // a root tag of the child's name
	pid, cid := parent["id"].(string), child["id"].(string)

	out := a.expect("PATCH", "/api/tags/"+pid, map[string]any{"parentId": cid}, 409)
	if out["code"] != "tag_cycle" {
		t.Errorf("cycle: %v", out)
	}
	out = a.expect("DELETE", "/api/tags/"+pid, nil, 409)
	if out["code"] != "tag_name_conflict" || out["childName"] != "Taxes" || out["childId"] != cid {
		t.Errorf("name conflict: %v", out)
	}
	tags := metaRows(t, a.expect("GET", "/api/tags", nil, 200), "tags")
	if tags["Parent"] == nil || tags["Taxes"]["parentId"] != pid {
		t.Errorf("a refused delete changed the tags: %v", tags)
	}

	bobs := b.expect("POST", "/api/tags", map[string]any{"name": "Bob's"}, 201)
	out = a.expect("PATCH", "/api/tags/"+pid, map[string]any{"parentId": bobs["id"]}, 400)
	if out["code"] != "owner_mismatch" {
		t.Errorf("parent of another owner: %v", out)
	}
	out = a.expect("POST", "/api/tags", map[string]any{"name": "X", "parentId": "not-a-uuid"}, 400)
	if out["code"] != "owner_mismatch" {
		t.Errorf("malformed parent: %v", out)
	}

	// A deleted parent's children become root tags.
	other := a.expect("POST", "/api/tags", map[string]any{"name": "Other"}, 201)
	sub := a.expect("POST", "/api/tags", map[string]any{"name": "Sub", "parentId": other["id"]}, 201)
	a.expect("DELETE", "/api/tags/"+other["id"].(string), nil, 204)
	if got := metaRows(t, a.expect("GET", "/api/tags", nil, 200), "tags")["Sub"]; got == nil || got["parentId"] != nil || got["id"] != sub["id"] {
		t.Errorf("child after its parent's delete: %v", got)
	}
}

// Another user's row is 404 for a non-admin; an admin changes it, the row
// keeps its owner, and the audit entry names the owner.
func TestMetadataOwners(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.addUser("alice", metaPassword, false)
	bob := f.addUser("bob", metaPassword, false)
	root := f.addUser("root", metaPassword, true)
	a := f.as()
	a.loginOK("alice", metaPassword)
	b := f.as()
	b.loginOK("bob", metaPassword)
	r := f.as()
	r.loginOK("root", metaPassword)

	tag := a.expect("POST", "/api/tags", map[string]any{"name": "Alice's"}, 201)
	corr := a.expect("POST", "/api/correspondents", map[string]any{"name": "Alice's"}, 201)
	tid, cid := tag["id"].(string), corr["id"].(string)
	b.expect("PATCH", "/api/tags/"+tid, map[string]any{"name": "Mine"}, 404)
	b.expect("DELETE", "/api/tags/"+tid, nil, 404)
	b.expect("DELETE", "/api/correspondents/"+cid, nil, 404)
	if rows := metaRows(t, b.expect("GET", "/api/tags", nil, 200), "tags"); len(rows) != 0 {
		t.Errorf("bob sees %v", rows)
	}

	n := len(auditLog(t, f.st))
	got := r.expect("PATCH", "/api/tags/"+tid, map[string]any{"name": "Renamed"}, 200)
	if got["ownerId"] != float64(alice.ID) || got["name"] != "Renamed" {
		t.Errorf("admin rename: %v", got)
	}
	entries := newEntries(t, f.st, n)
	if len(entries) != 1 || entries[0].Action != "tag.update" || entries[0].TargetID != tid || *entries[0].UserID != root.ID {
		t.Fatalf("admin rename entries: %+v", entries)
	}
	if d := details(t, entries[0]); d["ownerId"] != float64(alice.ID) || d["oldName"] != "Alice's" || d["name"] != "Renamed" {
		t.Errorf("admin rename details: %v", d)
	}
	r.expect("DELETE", "/api/correspondents/"+cid, nil, 204)

	// owner: an admin's own rows, or one user's.
	r.expect("POST", "/api/tags", map[string]any{"name": "Root's"}, 201)
	if rows := metaRows(t, r.expect("GET", "/api/tags", nil, 200), "tags"); len(rows) != 2 {
		t.Errorf("admin's full list: %v", rows)
	}
	if rows := metaRows(t, r.expect("GET", "/api/tags?owner=me", nil, 200), "tags"); len(rows) != 1 || rows["Root's"] == nil {
		t.Errorf("owner=me: %v", rows)
	}
	if rows := metaRows(t, r.expect("GET", fmt.Sprintf("/api/tags?owner=%d", alice.ID), nil, 200), "tags"); len(rows) != 1 || rows["Renamed"] == nil {
		t.Errorf("owner=alice: %v", rows)
	}
	if rows := metaRows(t, a.expect("GET", "/api/tags?owner=me", nil, 200), "tags"); len(rows) != 1 {
		t.Errorf("non-admin owner=me: %v", rows)
	}
	a.expect("GET", fmt.Sprintf("/api/tags?owner=%d", alice.ID), nil, 200)
	a.expect("GET", fmt.Sprintf("/api/tags?owner=%d", bob.ID), nil, 403)
	a.expect("GET", "/api/tags?owner=x", nil, 400)

	// ownerId: an admin creates for another user, nobody else may.
	n = len(auditLog(t, f.st))
	forBob := r.expect("POST", "/api/tags", map[string]any{"name": "For Bob", "ownerId": bob.ID}, 201)
	if forBob["ownerId"] != float64(bob.ID) {
		t.Errorf("created for bob: %v", forBob)
	}
	entries = newEntries(t, f.st, n)
	if len(entries) != 1 || entries[0].TargetID != forBob["id"] || details(t, entries[0])["ownerId"] != float64(bob.ID) {
		t.Errorf("create for bob entries: %+v", entries)
	}
	if rows := metaRows(t, b.expect("GET", "/api/tags", nil, 200), "tags"); rows["For Bob"] == nil {
		t.Errorf("bob's list: %v", rows)
	}
	r.expect("POST", "/api/correspondents", map[string]any{"name": "For Bob", "ownerId": bob.ID}, 201)
	r.expect("POST", "/api/document-types", map[string]any{"name": "For Bob", "ownerId": bob.ID}, 201)
	a.expect("POST", "/api/tags", map[string]any{"name": "Sneaky", "ownerId": bob.ID}, 403)
	a.expect("POST", "/api/tags", map[string]any{"name": "Own", "ownerId": alice.ID}, 201)
	if out := r.expect("POST", "/api/tags", map[string]any{"name": "Nobody", "ownerId": 999999}, 400); out["code"] != "unknown_owner" {
		t.Errorf("unknown owner: %v", out)
	}
}

func TestMetadataCounts(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.addUser("alice", metaPassword, false)
	bob := f.addUser("bob", metaPassword, false)
	f.addUser("root", metaPassword, true)
	a := f.as()
	a.loginOK("alice", metaPassword)
	r := f.as()
	r.loginOK("root", metaPassword)
	ctx := context.Background()
	as := store.Scope{UserID: alice.ID}

	parent := a.expect("POST", "/api/tags", map[string]any{"name": "Parent"}, 201)["id"].(string)
	child := a.expect("POST", "/api/tags", map[string]any{"name": "Child", "parentId": parent}, 201)["id"].(string)
	corr := a.expect("POST", "/api/correspondents", map[string]any{"name": "Bank"}, 201)["id"].(string)
	typ := a.expect("POST", "/api/document-types", map[string]any{"name": "Invoice"}, 201)["id"].(string)
	doc := func(sc store.Scope, content string, fav, inbox bool, tags []string, corr, typ *string) {
		t.Helper()
		sum := fmt.Sprintf("%064x", len(content)*7919+int(sc.UserID))
		if _, err := f.st.CreateDocument(ctx, sc, store.NewDocument{
			Title: content, Source: store.SourceFile, OriginalSHA256: sum, OriginalMIME: "application/pdf",
			IsFavorite: fav, IsInbox: inbox, TagIDs: tags, CorrespondentID: corr, DocumentTypeID: typ,
		}); err != nil {
			t.Fatal(err)
		}
	}
	doc(as, "a", true, true, []string{parent, child}, &corr, &typ)
	doc(as, "bb", false, false, []string{child}, &corr, nil)
	doc(as, "ccc", false, true, nil, nil, nil)
	doc(store.Scope{UserID: bob.ID}, "dddd", true, true, nil, nil, nil)

	tags := metaRows(t, a.expect("GET", "/api/tags", nil, 200), "tags")
	if p := tags["Parent"]; p["documentCount"] != float64(1) || p["subtreeCount"] != float64(2) {
		t.Errorf("parent counts: %v", p)
	}
	if c := tags["Child"]; c["documentCount"] != float64(2) || c["subtreeCount"] != float64(2) {
		t.Errorf("child counts: %v", c)
	}
	if c := metaRows(t, a.expect("GET", "/api/correspondents", nil, 200), "correspondents")["Bank"]; c["documentCount"] != float64(2) {
		t.Errorf("correspondent count: %v", c)
	}
	if c := metaRows(t, a.expect("GET", "/api/document-types", nil, 200), "documentTypes")["Invoice"]; c["documentCount"] != float64(1) {
		t.Errorf("document type count: %v", c)
	}
	counts := func(f *authFixture, path string, want [3]float64) {
		t.Helper()
		out := f.expect("GET", path, nil, 200)
		if out["inbox"] != want[0] || out["favorites"] != want[1] || out["total"] != want[2] {
			t.Errorf("%s: %v, want inbox, favorites, total %v", path, out, want)
		}
	}
	counts(a, "/api/documents/counts", [3]float64{2, 1, 3})
	counts(a, "/api/documents/counts?owner=me", [3]float64{2, 1, 3})
	counts(r, "/api/documents/counts", [3]float64{3, 2, 4})
	counts(r, "/api/documents/counts?owner=me", [3]float64{0, 0, 0})
	counts(r, fmt.Sprintf("/api/documents/counts?owner=%d", bob.ID), [3]float64{1, 1, 1})
}

// Each change writes one entry with the row and the names of the fields
// sent, never their values; without the audit log nothing changes and the
// answer is 503.
func TestMetadataAudit(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.addUser("alice", metaPassword, false)
	a := f.as()
	a.loginOK("alice", metaPassword)

	n := len(auditLog(t, f.st))
	tag := a.expect("POST", "/api/tags", map[string]any{"name": "Tax", "color": "#123456"}, 201)
	id := tag["id"].(string)
	a.expect("PATCH", "/api/tags/"+id, map[string]any{"name": "Steuer", "color": "#654321", "parentId": nil}, 200)
	a.expect("PATCH", "/api/tags/"+id, map[string]any{"color": "#ABCDEF"}, 200)
	a.expect("DELETE", "/api/tags/"+id, nil, 204)
	c := a.expect("POST", "/api/correspondents", map[string]any{"name": "Bank"}, 201)["id"].(string)
	a.expect("PATCH", "/api/correspondents/"+c, map[string]any{"name": "Sparkasse"}, 200)
	a.expect("DELETE", "/api/correspondents/"+c, nil, 204)
	d := a.expect("POST", "/api/document-types", map[string]any{"name": "Invoice"}, 201)["id"].(string)
	a.expect("PATCH", "/api/document-types/"+d, map[string]any{"name": "Rechnung"}, 200)
	a.expect("DELETE", "/api/document-types/"+d, nil, 204)
	// Refused changes write nothing.
	a.expect("PATCH", "/api/document-types/"+d, map[string]any{"name": "Gone"}, 404)
	a.expect("POST", "/api/tags", map[string]any{"name": ""}, 400)

	want := []struct {
		action, target, id, name string
		details                  map[string]any
	}{
		{"tag.create", store.TargetTag, id, "Tax", map[string]any{"name": "Tax"}},
		{"tag.update", store.TargetTag, id, "Steuer", map[string]any{"name": "Steuer", "oldName": "Tax", "fields": []any{"name", "color", "parentId"}}},
		{"tag.update", store.TargetTag, id, "Steuer", map[string]any{"name": "Steuer", "fields": []any{"color"}}},
		{"tag.delete", store.TargetTag, id, "Steuer", map[string]any{"name": "Steuer"}},
		{"correspondent.create", store.TargetCorrespondent, c, "Bank", map[string]any{"name": "Bank"}},
		{"correspondent.update", store.TargetCorrespondent, c, "Sparkasse", map[string]any{"name": "Sparkasse", "oldName": "Bank", "fields": []any{"name"}}},
		{"correspondent.delete", store.TargetCorrespondent, c, "Sparkasse", map[string]any{"name": "Sparkasse"}},
		{"document_type.create", store.TargetDocumentType, d, "Invoice", map[string]any{"name": "Invoice"}},
		{"document_type.update", store.TargetDocumentType, d, "Rechnung", map[string]any{"name": "Rechnung", "oldName": "Invoice", "fields": []any{"name"}}},
		{"document_type.delete", store.TargetDocumentType, d, "Rechnung", map[string]any{"name": "Rechnung"}},
	}
	got := newEntries(t, f.st, n)
	if len(got) != len(want) {
		t.Fatalf("%d entries, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		e := got[i]
		if e.Action != w.action || e.TargetType != w.target || e.TargetID != w.id || e.TargetName != w.name ||
			e.UserID == nil || *e.UserID != alice.ID || e.Actor != store.ActorSession {
			t.Errorf("entry %d: %+v", i, e)
		}
		dt := details(t, e)
		wantJSON, _ := json.Marshal(w.details)
		gotJSON, _ := json.Marshal(dt)
		if string(wantJSON) != string(gotJSON) {
			t.Errorf("entry %d (%s): details %s, want %s", i, w.action, gotJSON, wantJSON)
		}
		if strings.Contains(string(e.Details), "#") {
			t.Errorf("entry %d records a colour: %s", i, e.Details)
		}
	}

	// Without the audit log: 503, and nothing changes.
	keep := a.expect("POST", "/api/tags", map[string]any{"name": "Keep"}, 201)["id"].(string)
	keepC := a.expect("POST", "/api/correspondents", map[string]any{"name": "Keep"}, 201)["id"].(string)
	keepD := a.expect("POST", "/api/document-types", map[string]any{"name": "Keep"}, 201)["id"].(string)
	breakAudit(t, f.dbURL)
	for _, req := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/tags", map[string]any{"name": "New"}},
		{"PATCH", "/api/tags/" + keep, map[string]any{"name": "Changed"}},
		{"DELETE", "/api/tags/" + keep, nil},
		{"POST", "/api/correspondents", map[string]any{"name": "New"}},
		{"PATCH", "/api/correspondents/" + keepC, map[string]any{"name": "Changed"}},
		{"DELETE", "/api/correspondents/" + keepC, nil},
		{"POST", "/api/document-types", map[string]any{"name": "New"}},
		{"PATCH", "/api/document-types/" + keepD, map[string]any{"name": "Changed"}},
		{"DELETE", "/api/document-types/" + keepD, nil},
	} {
		a.expect(req.method, req.path, req.body, 503)
	}
	if tags := metaRows(t, a.expect("GET", "/api/tags", nil, 200), "tags"); len(tags) != 1 || tags["Keep"] == nil {
		t.Errorf("tags after refused changes: %v", tags)
	}
	for _, kind := range []struct{ path, key, id string }{
		{"/api/correspondents", "correspondents", keepC}, {"/api/document-types", "documentTypes", keepD},
	} {
		if rows := metaRows(t, a.expect("GET", kind.path, nil, 200), kind.key); len(rows) != 1 || rows["Keep"] == nil || rows["Keep"]["id"] != kind.id {
			t.Errorf("%s after refused changes: %v", kind.key, rows)
		}
	}
}

// A read key lists metadata but cannot change it; a write key can.
func TestMetadataAPIKeys(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("kim", keyPassword, false)
	f.loginOK("kim", keyPassword)
	read := f.createKey("r", []string{"read"}, "")
	write := f.createKey("w", []string{"read", "write"}, "")
	tag := f.expect("POST", "/api/tags", map[string]any{"name": "Tax"}, 201)["id"].(string)

	if s := f.keyStatus(read, "GET", "/api/tags"); s != 200 {
		t.Errorf("read key lists: %d", s)
	}
	body := []byte(`{"name":"X"}`)
	for _, req := range [][2]string{{"POST", "/api/tags"}, {"PATCH", "/api/tags/" + tag}, {"DELETE", "/api/tags/" + tag}, {"POST", "/api/correspondents"}} {
		if resp, _ := f.keyRequest(read, req[0], req[1], body, "application/json", nil); resp.StatusCode != 403 {
			t.Errorf("read key %s %s: %d", req[0], req[1], resp.StatusCode)
		}
	}
	if resp, out := f.keyRequest(write, "POST", "/api/correspondents", body, "application/json", nil); resp.StatusCode != 201 {
		t.Errorf("write key creates: %d %v", resp.StatusCode, out)
	}
	n := len(auditLog(t, f.st))
	if resp, _ := f.keyRequest(write, "DELETE", "/api/tags/"+tag, nil, "", nil); resp.StatusCode != 204 {
		t.Errorf("write key deletes: %d", resp.StatusCode)
	}
	if e := newEntries(t, f.st, n); len(e) != 1 || e[0].Actor != store.ActorAPIKey || e[0].APIKeyName != "w" {
		t.Errorf("key delete entry: %+v", e)
	}
}
