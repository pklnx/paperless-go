package web

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/pklnx/paperless-go/internal/store"
)

// Editing documents (#7, part 2): PATCH /api/documents/{id} and POST
// /api/documents/bulk. The store side is tested in
// internal/store/documents_patch_test.go.

func docPath(id string) string { return "/api/documents/" + id }

func (f *searchFixture) tag(sc store.Scope, name string) string {
	f.t.Helper()
	tag, err := f.st.CreateTag(context.Background(), sc, store.TagInput{Name: name})
	if err != nil {
		f.t.Fatal(err)
	}
	return tag.ID
}

func (f *searchFixture) doc(id string) *store.Document {
	f.t.Helper()
	d, err := f.st.GetDocument(context.Background(), store.Scope{All: true}, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return d
}

func TestPatchDocumentAPI(t *testing.T) {
	f := newSearchFixture(t)
	id := f.ids["Telekom"]
	steuer := f.tag(f.alice, "Steuer")
	bobTag := f.tag(f.bob, "Bob's")

	out := f.expect("PATCH", docPath(id), map[string]any{"title": "Telekom März", "tagIds": []string{steuer}, "archiveSerialNumber": 7}, 200)
	if out["title"] != "Telekom März" || fmt.Sprint(out["tagIds"]) != "["+steuer+"]" || out["archiveSerialNumber"] != float64(7) ||
		out["extractedText"] != "Ihre Rechnung für März über 39,95 Euro." || out["ownerId"] != float64(f.alice.UserID) || out["ownerName"] != nil {
		t.Errorf("patched: %v", out)
	}
	// null clears; other fields stay.
	out = f.expect("PATCH", docPath(id), map[string]any{"archiveSerialNumber": nil, "correspondentId": nil, "isFavorite": true}, 200)
	if out["archiveSerialNumber"] != nil || out["title"] != "Telekom März" || out["isFavorite"] != true || len(out["tagIds"].([]any)) != 1 {
		t.Errorf("cleared: %v", out)
	}

	// Long notes fit the body limit.
	f.expect("PATCH", docPath(id), map[string]any{"notes": strings.Repeat("ü", store.MaxNotesLength)}, 200)
	// A title over the limit is refused only when sent: a stored longer one
	// (an import) does not block a change of another field.
	sqlExec(t, f.dbURL, `UPDATE documents SET title = $1 WHERE id = $2`, strings.Repeat("x", 2000), f.ids["Vertrag"])
	f.expect("PATCH", docPath(f.ids["Vertrag"]), map[string]any{"isInbox": false}, 200)

	f.expect("PATCH", docPath(f.ids["Strom"]), map[string]any{"archiveSerialNumber": 8}, 200)
	// Notes keep line breaks and tabs.
	f.expect("PATCH", docPath(f.ids["Strom"]), map[string]any{"notes": "eins\r\n\tzwei"}, 200)
	for _, c := range []struct {
		body   map[string]any
		status int
		code   string
	}{
		{map[string]any{"title": strings.Repeat("x", store.MaxTitleLength+1)}, 400, codeInvalidTitle},
		{map[string]any{"notes": "a\x00b"}, 400, codeInvalidNotes},
		{map[string]any{"notes": "a\x1bb"}, 400, codeInvalidNotes},
		{map[string]any{"title": "a\x00b"}, 400, codeInvalidTitle},
		{map[string]any{"title": "a\nb"}, 400, codeInvalidTitle},
		{map[string]any{"title": nil}, 400, codeInvalidField},
		{map[string]any{"tagIds": nil}, 400, codeInvalidField},
		{map[string]any{"archiveSerialNumber": 0}, 400, codeInvalidASN},
		{map[string]any{}, 400, codeNothingToChange},
		{map[string]any{"tagIds": []string{bobTag}}, 400, codeOwnerMismatch},
		{map[string]any{"correspondentId": "not-a-uuid"}, 400, codeOwnerMismatch},
		{map[string]any{"archiveSerialNumber": 8}, 409, codeASNTaken},
	} {
		resp, out := f.do("PATCH", docPath(id), c.body)
		if resp.StatusCode != c.status || out["code"] != c.code {
			t.Errorf("%v: %d %v, want %d %s", c.body, resp.StatusCode, out, c.status, c.code)
		}
		if msg, _ := out["error"].(string); (c.code == codeInvalidTitle || c.code == codeInvalidNotes) && !strings.Contains(msg, "control characters") {
			t.Errorf("%v: message %q does not name control characters", c.body, msg)
		}
		if c.code == codeASNTaken && out["documentId"] != f.ids["Strom"] {
			t.Errorf("asn_taken names %v, want %s", out["documentId"], f.ids["Strom"])
		}
	}
	f.expect("PATCH", docPath(f.ids["Bob"]), map[string]any{"title": "mine"}, 404)
	f.expect("PATCH", docPath("x"), map[string]any{"title": "mine"}, 404)
	if d := f.doc(f.ids["Bob"]); d.Title != "Bob" {
		t.Errorf("bob's document changed: %+v", d)
	}
}

// One document.update per patch with the field names and no values; an
// admin's change of another user's document names the owner. Without the
// audit log nothing changes.
func TestPatchDocumentAudit(t *testing.T) {
	f := newSearchFixture(t)
	id := f.ids["Telekom"]
	n := len(auditLog(t, f.st))
	f.expect("PATCH", docPath(id), map[string]any{"title": "Geheim", "notes": "Geheime Notiz"}, 200)
	got := newEntries(t, f.st, n)
	if len(got) != 1 || got[0].Action != "document.update" || got[0].TargetType != store.TargetDocument ||
		got[0].TargetID != id || got[0].TargetName != "" || got[0].UserName != "alice" {
		t.Fatalf("entries %+v", got)
	}
	d := details(t, got[0])
	if fmt.Sprint(d) != "map[fields:[title notes]]" {
		t.Errorf("details %v", d)
	}

	f.loginOK("root", keyPassword)
	n = len(auditLog(t, f.st))
	out := f.expect("PATCH", docPath(f.ids["Bob"]), map[string]any{"isInbox": false}, 200)
	if out["ownerName"] != "bob" || out["ownerId"] != float64(f.bob.UserID) {
		t.Errorf("admin's answer: %v", out)
	}
	got = newEntries(t, f.st, n)
	if len(got) != 1 || got[0].TargetID != f.ids["Bob"] || fmt.Sprint(details(t, got[0])["ownerId"]) != fmt.Sprint(f.bob.UserID) {
		t.Errorf("admin's entry %+v", got)
	}
	// An admin cannot link another user's tag.
	resp, body := f.do("PATCH", docPath(f.ids["Bob"]), map[string]any{"tagIds": []string{f.tag(f.alice, "Alice's")}})
	if resp.StatusCode != 400 || body["code"] != codeOwnerMismatch {
		t.Errorf("admin, alice's tag on bob's document: %d %v", resp.StatusCode, body)
	}

	breakAudit(t, f.dbURL)
	f.expect("PATCH", docPath(id), map[string]any{"title": "Lost"}, 503)
	if d := f.doc(id); d.Title != "Geheim" {
		t.Errorf("changed without the audit log: %q", d.Title)
	}
	resp, _ = f.do("POST", "/api/documents/bulk", map[string]any{"ids": []string{id}, "favorite": true})
	if resp.StatusCode != 503 || f.doc(id).IsFavorite {
		t.Errorf("bulk without the audit log: %d", resp.StatusCode)
	}
}

func TestBulkDocumentsByIDs(t *testing.T) {
	f := newSearchFixture(t)
	steuer := f.tag(f.alice, "Steuer")
	all := []string{f.ids["Telekom"], f.ids["Strom"], f.ids["Vertrag"]}
	n := len(auditLog(t, f.st))
	out := f.expect("POST", "/api/documents/bulk", map[string]any{
		"ids": append(slices.Clone(all), all[0]), "addTags": []string{steuer}, "inbox": false, "favorite": true,
	}, 200)
	if out["updated"] != float64(3) {
		t.Errorf("answer %v", out)
	}
	for _, id := range all {
		if d := f.doc(id); !slices.Equal(d.TagIDs, []string{steuer}) || d.IsInbox || !d.IsFavorite {
			t.Errorf("after bulk: %+v", d)
		}
	}
	got := newEntries(t, f.st, n)
	if len(got) != 4 {
		t.Fatalf("%d entries, want a summary and 3 per document", len(got))
	}
	var run any
	perDoc := 0
	for _, e := range got {
		d := details(t, e)
		switch e.Action {
		case "document.bulk_update":
			run = d["bulkRun"]
			if d["count"] != float64(3) || d["selection"] != "ids" || fmt.Sprint(d["fields"]) != "[addTags inbox favorite]" ||
				len(d["ids"].([]any)) != 3 || e.TargetType != "" {
				t.Errorf("summary %+v %v", e, d)
			}
		case "document.update":
			perDoc++
			if e.TargetType != store.TargetDocument || !slices.Contains(all, e.TargetID) || d["ownerId"] != nil {
				t.Errorf("per-document %+v %v", e, d)
			}
		}
		if strings.Contains(string(e.Details), steuer) {
			t.Errorf("an entry holds the tag ID: %s", e.Details)
		}
	}
	for _, e := range got {
		if details(t, e)["bulkRun"] != run || run == nil {
			t.Errorf("bulkRun differs: %s", e.Details)
		}
	}
	if perDoc != 3 {
		t.Errorf("%d per-document entries", perDoc)
	}

	many := make([]string, store.MaxBulkIDs+1)
	for i := range many {
		many[i] = fmt.Sprintf("00000000-0000-0000-0000-%012d", i)
	}
	for _, c := range []struct {
		name   string
		body   map[string]any
		status int
		code   string
	}{
		{"empty list", map[string]any{"ids": []string{}, "inbox": true}, 400, codeEmptySelection},
		{"neither", map[string]any{"inbox": true}, 400, codeInvalidSelect},
		{"both", map[string]any{"ids": all, "match": map[string]any{"q": "x"}, "inbox": true}, 400, codeInvalidSelect},
		{"dry run by ids", map[string]any{"ids": all, "dryRun": true}, 400, codeInvalidSelect},
		{"501 ids", map[string]any{"ids": many, "inbox": true}, 400, codeTooMany},
		{"nothing to change", map[string]any{"ids": all}, 400, codeNothingToChange},
		{"add and remove", map[string]any{"ids": all, "addTags": []string{steuer}, "removeTags": []string{steuer}}, 400, codeTagAddRemove},
		{"add and remove, another case", map[string]any{"ids": all, "addTags": []string{steuer}, "removeTags": []string{strings.ToUpper(steuer)}}, 400, codeTagAddRemove},
		{"repeated tags", map[string]any{"ids": all, "addTags": []string{steuer, steuer, strings.ToUpper(steuer)}}, 200, ""},
		{"repeated tags to remove", map[string]any{"ids": all, "removeTags": []string{steuer, strings.ToUpper(steuer)}}, 200, ""},
		{"another owner's tag", map[string]any{"ids": all, "addTags": []string{f.tag(f.bob, "Bob's")}}, 400, codeOwnerMismatch},
		{"one foreign id", map[string]any{"ids": []string{all[0], f.ids["Bob"]}, "inbox": true}, 404, ""},
	} {
		resp, out := f.do("POST", "/api/documents/bulk", c.body)
		if resp.StatusCode != c.status || c.code != "" && out["code"] != c.code {
			t.Errorf("%s: %d %v, want %d %s", c.name, resp.StatusCode, out, c.status, c.code)
		}
	}
	if d := f.doc(all[0]); d.IsInbox || len(d.TagIDs) != 0 {
		t.Errorf("a refused bulk change changed a document: %+v", d)
	}

	// An admin across owners: inbox works, a tag does not.
	f.loginOK("root", keyPassword)
	mixed := []string{all[0], f.ids["Bob"]}
	resp, out := f.do("POST", "/api/documents/bulk", map[string]any{"ids": mixed, "addTags": []string{steuer}})
	if resp.StatusCode != 400 || out["code"] != codeSeveralOwners {
		t.Errorf("several owners: %d %v", resp.StatusCode, out)
	}
	n = len(auditLog(t, f.st))
	f.expect("POST", "/api/documents/bulk", map[string]any{"ids": mixed, "inbox": true}, 200)
	for _, e := range newEntries(t, f.st, n) {
		if d := details(t, e); e.Action == "document.update" && (d["ownerId"] == nil) {
			t.Errorf("admin's per-document entry without the owner: %v", d)
		}
	}
}

func TestBulkDocumentsByMatch(t *testing.T) {
	f := newSearchFixture(t)
	root, err := f.st.GetUserByName(context.Background(), "root")
	if err != nil {
		t.Fatal(err)
	}
	rootDoc := searchDoc(t, f.st, store.Scope{UserID: root.ID}, "Root", "Eine Rechnung von root.")
	bulk := func(body map[string]any, want int) map[string]any {
		t.Helper()
		return f.expect("POST", "/api/documents/bulk", body, want)
	}
	favorites := func() []string {
		t.Helper()
		var out []string
		for title, id := range f.ids {
			if f.doc(id).IsFavorite {
				out = append(out, title)
			}
		}
		if f.doc(rootDoc).IsFavorite {
			out = append(out, "Root")
		}
		slices.Sort(out)
		sqlExec(t, f.dbURL, `UPDATE documents SET is_favorite = false`)
		return out
	}

	// Alice: the search and her own documents only, with filters.
	if out := bulk(map[string]any{"match": map[string]any{"q": "rechnung"}, "dryRun": true}, 200); out["matched"] != float64(2) ||
		out["owners"] != float64(1) || out["limit"] != float64(store.MaxBulkMatches) {
		t.Errorf("dry run: %v", out)
	}
	if got := favorites(); len(got) != 0 {
		t.Errorf("the dry run changed %v", got)
	}
	n := len(auditLog(t, f.st))
	if out := bulk(map[string]any{"match": map[string]any{"q": "rechnung"}, "favorite": true}, 200); out["updated"] != float64(2) {
		t.Errorf("by match: %v", out)
	}
	if got := favorites(); !slices.Equal(got, []string{"Strom", "Telekom"}) {
		t.Errorf("alice's match changed %v", got)
	}
	got := newEntries(t, f.st, n)
	if len(got) != 3 || got[0].Action != "document.bulk_update" || fmt.Sprint(details(t, got[0])["match"]) != "map[q:rechnung]" ||
		details(t, got[0])["selection"] != "match" {
		t.Errorf("entries %+v", got)
	}
	bulk(map[string]any{"match": map[string]any{"q": "rechnung", "inbox": false}, "favorite": true}, 200)
	if got := favorites(); !slices.Equal(got, []string{"Strom"}) {
		t.Errorf("with inbox=false: %v", got)
	}
	bulk(map[string]any{"match": map[string]any{"tag": []string{f.tag(f.alice, "Leer")}}, "favorite": true}, 200)
	// An empty match changes nothing and writes nothing.
	n = len(auditLog(t, f.st))
	if out := bulk(map[string]any{"match": map[string]any{"q": "nirgends"}, "favorite": true}, 200); out["updated"] != float64(0) {
		t.Errorf("empty match: %v", out)
	}
	if got := newEntries(t, f.st, n); len(got) != 0 || len(favorites()) != 0 {
		t.Errorf("empty match logged %+v", got)
	}
	// A non-admin cannot name another owner.
	bulk(map[string]any{"match": map[string]any{"owner": fmt.Sprint(f.bob.UserID)}, "favorite": true}, 403)
	bulk(map[string]any{"match": map[string]any{"q": "rechnung", "inbox": "yes"}, "favorite": true}, 400)
	bulk(map[string]any{"match": map[string]any{"tz": "Nowhere/Land"}, "favorite": true}, 400)

	// The admin: everyone's by default, own with owner=me, one user's by ID.
	f.loginOK("root", keyPassword)
	if out := bulk(map[string]any{"match": map[string]any{"q": "rechnung"}, "dryRun": true}, 200); out["matched"] != float64(4) || out["owners"] != float64(3) {
		t.Errorf("admin dry run: %v", out)
	}
	bulk(map[string]any{"match": map[string]any{"q": "rechnung"}, "favorite": true}, 200)
	if got := favorites(); !slices.Equal(got, []string{"Bob", "Root", "Strom", "Telekom"}) {
		t.Errorf("admin, everyone's: %v", got)
	}
	bulk(map[string]any{"match": map[string]any{"q": "rechnung", "owner": "me"}, "favorite": true}, 200)
	if got := favorites(); !slices.Equal(got, []string{"Root"}) {
		t.Errorf("admin, own: %v", got)
	}
	bulk(map[string]any{"match": map[string]any{"q": "rechnung", "owner": fmt.Sprint(f.bob.UserID)}, "favorite": true}, 200)
	if got := favorites(); !slices.Equal(got, []string{"Bob"}) {
		t.Errorf("admin, bob's: %v", got)
	}
	resp, out := f.do("POST", "/api/documents/bulk", map[string]any{"match": map[string]any{"q": "rechnung"}, "addTags": []string{f.tag(f.alice, "X")}})
	if resp.StatusCode != 400 || out["code"] != codeSeveralOwners {
		t.Errorf("admin match with a tag: %d %v", resp.StatusCode, out)
	}

	// Over the limit: refused with the count.
	sqlExec(t, f.dbURL, `
		INSERT INTO documents (owner_id, title, source, original_sha256, original_size, original_mime)
		SELECT $1, 'Massenbrief', 'file', encode(sha256(('mass-' || g)::bytea), 'hex'), 1, 'application/pdf'
		FROM generate_series(1, $2) AS g`, f.bob.UserID, store.MaxBulkMatches+1)
	resp, out = f.do("POST", "/api/documents/bulk", map[string]any{"match": map[string]any{"q": "massenbrief"}, "favorite": true})
	if resp.StatusCode != 400 || out["code"] != codeTooMany || out["matched"] != float64(store.MaxBulkMatches+1) || out["limit"] != float64(store.MaxBulkMatches) {
		t.Errorf("over the limit: %d %v", resp.StatusCode, out)
	}
	if out := bulk(map[string]any{"match": map[string]any{"q": "massenbrief"}, "dryRun": true}, 200); out["matched"] != float64(store.MaxBulkMatches+1) {
		t.Errorf("dry run over the limit: %v", out)
	}
}

// A read key can neither patch nor change in bulk.
func TestDocumentEditNeedsWrite(t *testing.T) {
	f := newSearchFixture(t)
	key := f.createKey("reader", []string{"read"}, "")
	id := f.ids["Telekom"]
	if resp, _ := f.keyRequest(key, "PATCH", docPath(id), []byte(`{"title":"x"}`), "application/json", nil); resp.StatusCode != 403 {
		t.Errorf("patch with a read key: %d", resp.StatusCode)
	}
	if resp, _ := f.keyRequest(key, "POST", "/api/documents/bulk", []byte(`{"ids":["`+id+`"],"inbox":false}`), "application/json", nil); resp.StatusCode != 403 {
		t.Errorf("bulk with a read key: %d", resp.StatusCode)
	}
	writer := f.createKey("writer", []string{"read", "write"}, "")
	if resp, out := f.keyRequest(writer, "PATCH", docPath(id), []byte(`{"title":"by key"}`), "application/json", nil); resp.StatusCode != 200 || out["title"] != "by key" {
		t.Errorf("patch with a write key: %d %v", resp.StatusCode, out)
	}
}

// The list's favorite and owner parameters, and the owner fields.
func TestListDocumentsFavoriteAndOwner(t *testing.T) {
	f := newSearchFixture(t)
	sqlExec(t, f.dbURL, `UPDATE documents SET is_favorite = true WHERE id = $1`, f.ids["Vertrag"])
	f.search([]string{"Vertrag"}, "favorite", "true")
	f.search([]string{"Strom", "Telekom"}, "favorite", "false")
	f.search([]string{"Vertrag", "Strom", "Telekom"}, "owner", "me")
	f.expect("GET", searchPath("favorite", "maybe"), nil, 400)
	f.expect("GET", searchPath("owner", fmt.Sprint(f.bob.UserID)), nil, 403)
	for _, d := range listed(t, f.expect("GET", "/api/documents", nil, 200)) {
		if d["ownerId"] != float64(f.alice.UserID) || d["ownerName"] != nil {
			t.Errorf("alice's list: %v", d)
		}
	}

	f.loginOK("root", keyPassword)
	f.search([]string{"Bob", "Vertrag", "Strom", "Telekom"})
	f.search([]string{}, "owner", "me")
	f.search([]string{"Bob"}, "owner", fmt.Sprint(f.bob.UserID))
	for _, d := range listed(t, f.expect("GET", "/api/documents", nil, 200)) {
		want := "alice"
		if d["id"] == f.ids["Bob"] {
			want = "bob"
		}
		if d["ownerName"] != want {
			t.Errorf("admin's list: %v", d)
		}
	}
	if out := f.expect("GET", docPath(f.ids["Bob"]), nil, 200); out["ownerName"] != "bob" {
		t.Errorf("admin's document: %v", out)
	}
}

// Every per-document entry of an admin's bulk change records the owner of
// another user's document (ownerId), by ids, by match with owner empty
// and by match with owner=<id>; the admin's own documents get none, also
// with owner=me.
func TestBulkDocumentsAdminOwnerID(t *testing.T) {
	f := newSearchFixture(t)
	root, err := f.st.GetUserByName(context.Background(), "root")
	if err != nil {
		t.Fatal(err)
	}
	rootDoc := searchDoc(t, f.st, store.Scope{UserID: root.ID}, "Root", "Eine Rechnung von root.")
	f.loginOK("root", keyPassword)
	for _, c := range []struct {
		name string
		body map[string]any
		want int // per-document entries
	}{
		{"ids", map[string]any{"ids": []string{f.ids["Bob"], f.ids["Telekom"], rootDoc}}, 3},
		{"match, owner empty", map[string]any{"match": map[string]any{"q": "rechnung"}}, 4},
		{"match, owner=bob", map[string]any{"match": map[string]any{"q": "rechnung", "owner": fmt.Sprint(f.bob.UserID)}}, 1},
		{"match, owner=alice", map[string]any{"match": map[string]any{"q": "rechnung", "owner": fmt.Sprint(f.alice.UserID)}}, 2},
		{"match, owner=me", map[string]any{"match": map[string]any{"q": "rechnung", "owner": "me"}}, 1},
		{"match, owner=root's id", map[string]any{"match": map[string]any{"q": "rechnung", "owner": fmt.Sprint(root.ID)}}, 1},
	} {
		c.body["favorite"] = true
		n := len(auditLog(t, f.st))
		f.expect("POST", "/api/documents/bulk", c.body, 200)
		perDoc := 0
		for _, e := range newEntries(t, f.st, n) {
			if e.Action != "document.update" {
				continue
			}
			perDoc++
			want := ""
			if owner := f.doc(e.TargetID).OwnerID; owner != root.ID {
				want = fmt.Sprint(owner)
			}
			got, ok := details(t, e)["ownerId"]
			if want == "" && ok || want != "" && fmt.Sprint(got) != want {
				t.Errorf("%s: entry of %s has ownerId %v, want %q", c.name, e.TargetID, got, want)
			}
		}
		if perDoc != c.want {
			t.Errorf("%s: %d per-document entries, want %d", c.name, perDoc, c.want)
		}
	}
}
