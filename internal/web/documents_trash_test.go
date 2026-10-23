package web

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pklnx/paperless-go/internal/store"
)

// trashFixture is a search fixture whose server counts sweeper wakes.
func newTrashFixture(t *testing.T) (*searchFixture, *atomic.Int32) {
	t.Helper()
	var wakes atomic.Int32
	f := &searchFixture{authFixture: newAuthFixtureOpts(t, Options{SweepWake: func() { wakes.Add(1) }}), ids: map[string]string{}}
	f.alice = store.Scope{UserID: f.addUser("alice", keyPassword, false).ID}
	f.bob = store.Scope{UserID: f.addUser("bob", keyPassword, false).ID}
	f.addUser("root", keyPassword, true)
	for _, d := range []struct {
		sc          store.Scope
		title, text string
	}{
		{f.alice, "Telekom", "Ihre Rechnung für März."},
		{f.alice, "Strom", "Die Rechnung des Stromversorgers."},
		{f.alice, "Vertrag", "Ein Vertrag über die Wohnung."},
		{f.bob, "Bob", "Bobs eigene Rechnung."},
	} {
		f.ids[d.title] = searchDoc(t, f.st, d.sc, d.title, d.text)
	}
	f.loginOK("alice", keyPassword)
	return f, &wakes
}

func (f *searchFixture) counts() map[string]any {
	f.t.Helper()
	return f.expect("GET", "/api/documents/counts", nil, 200)
}

func (f *searchFixture) wantCode(method, path string, body any, status int, code string) {
	f.t.Helper()
	resp, out := f.do(method, path, body)
	if resp.StatusCode != status || out["code"] != code {
		f.t.Errorf("%s %s: %d %v, want %d %s", method, path, resp.StatusCode, out, status, code)
	}
}

// Delete, restore and purge one document through the API, with the trash
// list, the counts, the conflicts and the audit entries.
func TestTrashAPI(t *testing.T) {
	f, wakes := newTrashFixture(t)
	id := f.ids["Telekom"]
	n := len(auditLog(t, f.st))
	f.expect("DELETE", docPath(id), nil, 204)
	got := f.expect("GET", docPath(id), nil, 200)
	if got["deletedAt"] == nil || got["purgeAt"] == nil {
		t.Errorf("trashed document: %v", got)
	}
	f.search([]string{"Vertrag", "Strom"})
	f.search([]string{"Strom"}, "q", "rechnung")
	f.search([]string{"Telekom"}, "trashed", "true")
	f.search([]string{"Telekom"}, "trashed", "true", "sort", "deleted")
	f.search([]string{"Telekom"}, "trashed", "true", "q", "rechnung")
	f.search(nil, "trashed", "true", "q", "vertrag")
	for _, bad := range [][]string{{"sort", "deleted"}, {"trashed", "true", "sort", "deleted", "order", "asc"}, {"trashed", "yes"}} {
		f.expect("GET", searchPath(bad...), nil, 400)
	}
	if c := f.counts(); c["total"] != float64(2) || c["trash"] != float64(1) {
		t.Errorf("counts: %v", c)
	}
	// The file stays readable for the preview.
	up := f.uploadOK("scan.pdf", pdf("scan"))
	f.expect("DELETE", docPath(up), nil, 204)
	if resp, body := f.download(docPath(up) + "/file"); resp.StatusCode != 200 || string(body) != string(pdf("scan")) {
		t.Errorf("file of a trashed document: %d", resp.StatusCode)
	}
	f.expect("POST", docPath(up)+"/restore", nil, 200)
	f.wantCode("DELETE", docPath(id), nil, 409, codeInTrash)
	f.wantCode("PATCH", docPath(id), map[string]any{"title": "x"}, 409, codeInTrash)
	f.wantCode("POST", docPath(id)+"/reprocess", nil, 409, codeInTrash)
	// Another user's document is not found.
	f.expect("DELETE", docPath(f.ids["Bob"]), nil, 404)
	f.expect("POST", docPath(f.ids["Bob"])+"/restore", nil, 404)
	f.expect("POST", docPath(f.ids["Bob"])+"/purge", nil, 404)

	back := f.expect("POST", docPath(id)+"/restore", nil, 200)
	if back["deletedAt"] != nil || back["purgeAt"] != nil || back["title"] != "Telekom" || back["extractedText"] == nil {
		t.Errorf("restored: %v", back)
	}
	f.wantCode("POST", docPath(id)+"/restore", nil, 409, codeNotInTrash)
	f.wantCode("POST", docPath(id)+"/purge", nil, 409, codeNotInTrash)
	if wakes.Load() != 0 {
		t.Errorf("woken %d times before a purge", wakes.Load())
	}

	f.expect("DELETE", docPath(id), nil, 204)
	f.expect("POST", docPath(id)+"/purge", nil, 204)
	if wakes.Load() != 1 {
		t.Errorf("woken %d times after a purge, want 1", wakes.Load())
	}
	f.expect("GET", docPath(id), nil, 404)
	f.expect("POST", docPath(id)+"/restore", nil, 404)
	f.search(nil, "trashed", "true")
	if c := f.counts(); c["total"] != float64(3) || c["trash"] != float64(0) { // with the scan
		t.Errorf("counts after purge: %v", c)
	}

	var actions []string
	for _, e := range newEntries(t, f.st, n) {
		if e.TargetType == store.TargetDocument && e.TargetID != up {
			if e.TargetID != id || e.UserName != "alice" {
				t.Errorf("entry %+v", e)
			}
			actions = append(actions, e.Action)
		}
	}
	if fmt.Sprint(actions) != "[document.delete document.restore document.delete document.purge]" {
		t.Errorf("audit: %v", actions)
	}

	// An admin's change of another user's document names the owner.
	f.loginOK("root", keyPassword)
	n = len(auditLog(t, f.st))
	f.expect("DELETE", docPath(f.ids["Bob"]), nil, 204)
	if e := newEntries(t, f.st, n); len(e) != 1 || e[0].Action != "document.delete" ||
		fmt.Sprint(details(t, e[0])["ownerId"]) != fmt.Sprint(f.bob.UserID) {
		t.Errorf("admin's entry: %+v", e)
	}
	breakAudit(t, f.dbURL)
	f.expect("POST", docPath(f.ids["Bob"])+"/restore", nil, 503)
	if d := f.doc(f.ids["Bob"]); d.DeletedAt == nil {
		t.Error("restored without the audit log")
	}
}

// Deleting needs write: a read or upload key cannot.
func TestTrashNeedsWrite(t *testing.T) {
	f, _ := newTrashFixture(t)
	id := f.ids["Strom"]
	for _, scopes := range [][]string{{"read"}, {"upload"}, {"read", "upload"}} {
		key := f.createKey(fmt.Sprint(scopes), scopes, "")
		for _, path := range []string{docPath(id), docPath(id) + "/restore", docPath(id) + "/purge"} {
			method := "POST"
			if path == docPath(id) {
				method = "DELETE"
			}
			if s := f.keyStatus(key, method, path); s != 403 {
				t.Errorf("%v %s %s: %d", scopes, method, path, s)
			}
		}
	}
	key := f.createKey("writer", []string{"write"}, "")
	if s := f.keyStatus(key, "DELETE", docPath(id)); s != 204 {
		t.Errorf("write key: %d", s)
	}
}

// The bulk actions: by ids and by search, with the selection rules, the
// fingerprint bound to the action, an admin's empty trash and the audit.
func TestBulkTrashAPI(t *testing.T) {
	f, wakes := newTrashFixture(t)
	bulk := func(body map[string]any, status int) map[string]any {
		t.Helper()
		return f.expect("POST", "/api/documents/bulk", f.confirmed(body), status)
	}
	n := len(auditLog(t, f.st))
	if out := bulk(map[string]any{"ids": []string{f.ids["Telekom"], f.ids["Strom"]}, "action": "trash"}, 200); out["updated"] != float64(2) {
		t.Fatalf("bulk trash: %v", out)
	}
	entries := newEntries(t, f.st, n)
	var summary store.AuditRecord
	perDoc := 0
	for _, e := range entries {
		switch e.Action {
		case "document.bulk_delete":
			summary = e
		case "document.delete":
			perDoc++
			if details(t, e)["bulkRun"] == nil {
				t.Errorf("entry without bulkRun: %+v", e)
			}
		}
	}
	if d := details(t, summary); d["count"] != float64(2) || d["selection"] != "ids" || d["fields"] != nil || perDoc != 2 {
		t.Errorf("audit: %v, %d per document", d, perDoc)
	}
	f.search([]string{"Vertrag"})

	for _, c := range []struct {
		name   string
		body   map[string]any
		status int
		code   string
	}{
		{"unknown action", map[string]any{"ids": []string{f.ids["Vertrag"]}, "action": "shred"}, 400, codeInvalidAction},
		{"fields with an action", map[string]any{"ids": []string{f.ids["Vertrag"]}, "action": "trash", "favorite": true}, 400, codeInvalidField},
		{"update a trashed one", map[string]any{"ids": []string{f.ids["Vertrag"], f.ids["Strom"]}, "favorite": true}, 409, codeInTrash},
		{"restore a live one", map[string]any{"ids": []string{f.ids["Vertrag"], f.ids["Strom"]}, "action": "restore"}, 409, codeNotInTrash},
		{"purge a live one", map[string]any{"ids": []string{f.ids["Vertrag"]}, "action": "purge"}, 409, codeNotInTrash},
		{"restore without trashed", map[string]any{"match": map[string]any{"q": "rechnung"}, "action": "restore", "fingerprint": "v2.x"}, 400, codeInvalidSelect},
		{"trash with trashed", map[string]any{"match": map[string]any{"q": "rechnung", "trashed": true}, "action": "trash", "fingerprint": "v2.x"}, 400, codeInvalidSelect},
		{"purge without owner", map[string]any{"match": map[string]any{"trashed": true}, "action": "purge", "fingerprint": "v2.x"}, 400, codeInvalidSelect},
		{"dry run of a purge without owner", map[string]any{"match": map[string]any{"trashed": true}, "action": "purge", "dryRun": true}, 400, codeInvalidSelect},
	} {
		resp, out := f.do("POST", "/api/documents/bulk", c.body)
		if resp.StatusCode != c.status || out["code"] != c.code {
			t.Errorf("%s: %d %v, want %d %s", c.name, resp.StatusCode, out, c.status, c.code)
		}
	}

	// A dry run for restore does not confirm a purge of the same set.
	restoreDry := f.expect("POST", "/api/documents/bulk", map[string]any{
		"match": map[string]any{"trashed": true, "owner": "me"}, "action": "restore", "dryRun": true,
	}, 200)
	if restoreDry["matched"] != float64(2) {
		t.Fatalf("dry run: %v", restoreDry)
	}
	resp, out := f.do("POST", "/api/documents/bulk", map[string]any{
		"match": map[string]any{"trashed": true, "owner": "me"}, "action": "purge", "fingerprint": restoreDry["fingerprint"],
	})
	if resp.StatusCode != 409 || out["code"] != codeMatchChanged {
		t.Errorf("purge with a restore fingerprint: %d %v", resp.StatusCode, out)
	}
	if wakes.Load() != 0 {
		t.Errorf("woken %d times", wakes.Load())
	}
	// Restore one by search, then empty the trash of the rest.
	if out := bulk(map[string]any{"match": map[string]any{"q": "strom", "trashed": true}, "action": "restore"}, 200); out["updated"] != float64(1) {
		t.Fatalf("restore by search: %v", out)
	}
	n = len(auditLog(t, f.st))
	if out := bulk(map[string]any{"match": map[string]any{"trashed": true, "owner": "me"}, "action": "purge"}, 200); out["updated"] != float64(1) {
		t.Fatalf("empty trash: %v", out)
	}
	if wakes.Load() != 1 {
		t.Errorf("woken %d times after the purge, want 1", wakes.Load())
	}
	var actions []string
	for _, e := range newEntries(t, f.st, n) {
		actions = append(actions, e.Action)
	}
	if fmt.Sprint(actions) != "[document.bulk_purge document.purge]" {
		t.Errorf("purge audit: %v", actions)
	}
	f.search([]string{"Vertrag", "Strom"})
	f.search(nil, "trashed", "true")

	// An admin's empty trash touches only the admin's own trash, also
	// when they see everyone's documents; other users' trash goes by
	// explicit ids.
	f.loginOK("root", keyPassword)
	if out := bulk(map[string]any{"ids": []string{f.ids["Bob"], f.ids["Vertrag"]}, "action": "trash"}, 200); out["updated"] != float64(2) {
		t.Fatalf("admin trash: %v", out)
	}
	everyone := f.expect("POST", "/api/documents/bulk", map[string]any{"match": map[string]any{"trashed": true}, "action": "restore", "dryRun": true}, 200)
	if everyone["matched"] != float64(2) || everyone["owners"] != float64(2) {
		t.Errorf("admin's view of the trash: %v", everyone)
	}
	if out := bulk(map[string]any{"match": map[string]any{"trashed": true, "owner": "me"}, "action": "purge"}, 200); out["updated"] != float64(0) {
		t.Errorf("admin's empty trash: %v", out)
	}
	if d := f.doc(f.ids["Bob"]); d.DeletedAt == nil {
		t.Error("bob's document left the trash")
	}
	if out := bulk(map[string]any{"ids": []string{f.ids["Bob"], f.ids["Vertrag"]}, "action": "purge"}, 200); out["updated"] != float64(2) {
		t.Errorf("admin purge by ids: %v", out)
	}
}

// Re-uploading the file of a trashed document: a session and a read key
// get duplicate with trashed and nothing is restored; an upload-only key
// gets a plain duplicate and the document is restored, audited with via;
// a document due for purge answers 503 to every caller.
func TestUploadTrashedDuplicate(t *testing.T) {
	f, _ := newTrashFixture(t)
	reader := f.createKey("reader", []string{"read", "upload"}, "")
	phone := f.createKey("iPhone", []string{"upload"}, "")
	id := f.uploadOK("letter.pdf", pdf("letter"))
	f.expect("DELETE", docPath(id), nil, 204)

	resp, out := f.upload("/api/documents", "application/pdf", "again.pdf", pdf("letter"))
	existing, _ := out["existing"].(map[string]any)
	if resp.StatusCode != 200 || out["duplicate"] != true || out["trashed"] != true || existing["id"] != id {
		t.Errorf("session: %d %v", resp.StatusCode, out)
	}
	resp, out = f.keyUpload(reader, "again.pdf", pdf("letter"))
	if resp.StatusCode != 200 || out["trashed"] != true || out["existing"] == nil {
		t.Errorf("read key: %d %v", resp.StatusCode, out)
	}
	if d := f.doc(id); d.DeletedAt == nil {
		t.Fatal("restored by a caller that can restore it")
	}

	n := len(auditLog(t, f.st))
	resp, out = f.keyUpload(phone, "again.pdf", pdf("letter"))
	if resp.StatusCode != 200 || fmt.Sprint(out) != "map[duplicate:true]" {
		t.Errorf("upload key: %d %v", resp.StatusCode, out)
	}
	if d := f.doc(id); d.DeletedAt != nil {
		t.Error("not restored for the upload key")
	}
	e := newEntries(t, f.st, n)
	if len(e) != 1 || e[0].Action != "document.restore" || e[0].Actor != store.ActorAPIKey || e[0].APIKeyName != "iPhone" ||
		e[0].TargetID != id || details(t, e[0])["via"] != "upload" {
		t.Errorf("restore entry: %+v", e)
	}
	// A live duplicate stays a plain duplicate for the key.
	resp, out = f.keyUpload(phone, "again.pdf", pdf("letter"))
	if resp.StatusCode != 200 || fmt.Sprint(out) != "map[duplicate:true]" || len(newEntries(t, f.st, n)) != 1 {
		t.Errorf("live duplicate: %d %v", resp.StatusCode, out)
	}

	// Due for purge: neither duplicate nor new yet.
	f.expect("DELETE", docPath(id), nil, 204)
	f.expect("POST", docPath(id)+"/purge", nil, 204)
	n = len(auditLog(t, f.st))
	for name, up := range map[string]func() (*http.Response, map[string]any){
		"session": func() (*http.Response, map[string]any) {
			return f.upload("/api/documents", "application/pdf", "x.pdf", pdf("letter"))
		},
		"read key":   func() (*http.Response, map[string]any) { return f.keyUpload(reader, "x.pdf", pdf("letter")) },
		"upload key": func() (*http.Response, map[string]any) { return f.keyUpload(phone, "x.pdf", pdf("letter")) },
	} {
		resp, out := up()
		if resp.StatusCode != 503 || resp.Header.Get("Retry-After") != purgeRetryAfter || out["duplicate"] != nil {
			t.Errorf("%s, due for purge: %d %v %q", name, resp.StatusCode, out, resp.Header.Get("Retry-After"))
		}
	}
	if e := newEntries(t, f.st, n); len(e) != 0 {
		t.Errorf("entries: %+v", e)
	}
	if _, err := f.st.GetDocument(context.Background(), store.Scope{All: true}, id); err == nil {
		t.Error("a due document came back")
	}
}

// The ASN of a trashed document stays taken, and the conflict says so.
func TestASNTakenInTrash(t *testing.T) {
	f, _ := newTrashFixture(t)
	f.expect("PATCH", docPath(f.ids["Telekom"]), map[string]any{"archiveSerialNumber": 5}, 200)
	f.expect("DELETE", docPath(f.ids["Telekom"]), nil, 204)
	resp, out := f.do("PATCH", docPath(f.ids["Strom"]), map[string]any{"archiveSerialNumber": 5})
	if resp.StatusCode != 409 || out["code"] != codeASNTaken || out["documentId"] != f.ids["Telekom"] || out["trashed"] != true {
		t.Errorf("asn of a trashed document: %d %v", resp.StatusCode, out)
	}
}

// The automatic restore for an upload-only key meets a document that
// changed after the duplicate check. One that became due for purge in
// between (the store's clock passes the retention between the two
// decisions) answers 503 with Retry-After, never "duplicate", and
// restores nothing; one that someone else restored in between is a plain
// duplicate, not an error.
func TestUploadKeyRestoreRaces(t *testing.T) {
	f, _ := newTrashFixture(t)
	phone := f.createKey("iPhone", []string{"upload"}, "")
	id := f.uploadOK("letter.pdf", pdf("letter"))
	f.expect("DELETE", docPath(id), nil, 204)
	ctx := context.Background()

	// The first decision (the duplicate check) sees it trashed; from the
	// second on (the restore) the retention has passed.
	var calls atomic.Int32
	f.setStoreNow(func() time.Time {
		if calls.Add(1) == 1 {
			return time.Now()
		}
		return time.Now().Add(store.DefaultTrashRetention + time.Hour)
	})
	n := len(auditLog(t, f.st))
	resp, out := f.keyUpload(phone, "again.pdf", pdf("letter"))
	f.setStoreNow(nil)
	if resp.StatusCode != 503 || resp.Header.Get("Retry-After") != purgeRetryAfter || out["duplicate"] != nil {
		t.Errorf("due after the check: %d %v %q", resp.StatusCode, out, resp.Header.Get("Retry-After"))
	}
	if calls.Load() < 2 {
		t.Errorf("%d store clock calls, want the check and the restore", calls.Load())
	}
	if d := f.doc(id); d.DeletedAt == nil {
		t.Error("restored although it was due")
	}
	if e := newEntries(t, f.st, n); len(e) != 0 {
		t.Errorf("entries: %+v", e)
	}

	// Restored by someone else between the check and the key's restore.
	calls.Store(0)
	f.setStoreNow(func() time.Time {
		if calls.Add(1) == 2 {
			if _, err := f.st.RestoreDocument(ctx, f.alice, id); err != nil {
				t.Errorf("concurrent restore: %v", err)
			}
		}
		return time.Now()
	})
	resp, out = f.keyUpload(phone, "again.pdf", pdf("letter"))
	f.setStoreNow(nil)
	if resp.StatusCode != 200 || fmt.Sprint(out) != "map[duplicate:true]" {
		t.Errorf("restored meanwhile: %d %v", resp.StatusCode, out)
	}
	if d := f.doc(id); d.DeletedAt != nil {
		t.Error("not live after the concurrent restore")
	}
}
