package web

import (
	"testing"
)

// PATCH with expectedModifiedAt (#105): the modifiedAt exactly as GET
// answered it, microseconds included, goes through; any other value gives
// 409 document_changed with the current modifiedAt and changes nothing.
func TestPatchDocumentExpectedModifiedAt(t *testing.T) {
	f := newSearchFixture(t)
	id := f.ids["Telekom"]
	// A value whose microseconds are not a multiple of 1000: a client that
	// passed it through a millisecond Date would lose them.
	sqlExec(t, f.dbURL, `UPDATE documents SET modified_at = '2026-01-02 03:04:05.123456+00' WHERE id = $1`, id)
	got := f.expect("GET", docPath(id), nil, 200)
	stored, _ := got["modifiedAt"].(string)
	if stored != "2026-01-02T03:04:05.123456Z" {
		t.Fatalf("GET modifiedAt %q", stored)
	}

	// Truncated to milliseconds: another instant, so refused.
	resp, out := f.do("PATCH", docPath(id), map[string]any{"title": "lost", "expectedModifiedAt": "2026-01-02T03:04:05.123Z"})
	if resp.StatusCode != 409 || out["code"] != codeDocChanged || out["modifiedAt"] != stored {
		t.Errorf("stale precondition: %d %v", resp.StatusCode, out)
	}
	if d := f.doc(id); d.Title == "lost" {
		t.Error("a refused patch changed the title")
	}

	// Exactly the string GET answered: 200, and the answer's modifiedAt is
	// the next precondition.
	out = f.expect("PATCH", docPath(id), map[string]any{"title": "Telekom April", "expectedModifiedAt": stored}, 200)
	next, _ := out["modifiedAt"].(string)
	if out["title"] != "Telekom April" || next == "" || next == stored {
		t.Fatalf("patch with the stored value: %v", out)
	}
	// The old value no longer holds; the new one does, also in another zone.
	f.expect("PATCH", docPath(id), map[string]any{"isFavorite": true, "expectedModifiedAt": stored}, 409)
	out = f.expect("PATCH", docPath(id), map[string]any{"isFavorite": true, "expectedModifiedAt": next}, 200)
	if out["isFavorite"] != true {
		t.Errorf("patch with the answer's value: %v", out)
	}

	for _, c := range []struct {
		name   string
		body   map[string]any
		status int
		code   string
	}{
		{"null", map[string]any{"title": "x", "expectedModifiedAt": nil}, 400, codeInvalidField},
		{"only the precondition", map[string]any{"expectedModifiedAt": out["modifiedAt"]}, 400, codeNothingToChange},
		{"not a time", map[string]any{"title": "x", "expectedModifiedAt": "yesterday"}, 400, ""},
		{"a number", map[string]any{"title": "x", "expectedModifiedAt": 12}, 400, ""},
	} {
		resp, body := f.do("PATCH", docPath(id), c.body)
		if resp.StatusCode != c.status || (c.code != "" && body["code"] != c.code) {
			t.Errorf("%s: %d %v, want %d %s", c.name, resp.StatusCode, body, c.status, c.code)
		}
	}
	// Outside the scope it stays 404.
	f.expect("PATCH", docPath(f.ids["Bob"]), map[string]any{"title": "mine", "expectedModifiedAt": stored}, 404)
}
