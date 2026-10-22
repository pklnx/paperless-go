package web

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/store"
)

// The search API (#6, part 3): GET /api/documents with q and the other
// search parameters. The store search itself is tested in
// internal/store/search_test.go.

// searchDoc creates a document of sc with text and returns its ID.
func searchDoc(t *testing.T, st *store.Store, sc store.Scope, title, text string) string {
	t.Helper()
	d, err := st.CreateDocument(context.Background(), sc, store.NewDocument{
		Title: title, Source: store.SourceFile, OriginalSHA256: fmt.Sprintf("%064x", time.Now().UnixNano()),
		OriginalMIME: "application/pdf", ExtractedText: text, TextSource: "PDF-Text",
	})
	if err != nil {
		t.Fatal(err)
	}
	return d.ID
}

// sqlExec runs a statement on the fixture's database, to set up columns
// the store does not set (created_at, document_date).
func sqlExec(t *testing.T, dbURL, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

func listed(t *testing.T, out map[string]any) []map[string]any {
	t.Helper()
	docs, ok := out["documents"].([]any)
	if !ok {
		t.Fatalf("no documents in %v", out)
	}
	var res []map[string]any
	for _, d := range docs {
		res = append(res, d.(map[string]any))
	}
	return res
}

func searchPath(params ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(params); i += 2 {
		v.Add(params[i], params[i+1])
	}
	return "/api/documents?" + v.Encode()
}

type searchFixture struct {
	*authFixture
	alice, bob store.Scope
	ids        map[string]string // title → ID
}

// newSearchFixture logs alice in. Her documents: "Telekom" and "Strom"
// mention Rechnung, "Vertrag" does not; "Strom" is dated 2025-12-31 23:30
// UTC (1 January in Berlin) and is out of the inbox. Bob has a Rechnung
// of his own.
func newSearchFixture(t *testing.T) *searchFixture {
	t.Helper()
	f := &searchFixture{authFixture: newAuthFixture(t), ids: map[string]string{}}
	f.alice = store.Scope{UserID: f.addUser("alice", keyPassword, false).ID}
	f.bob = store.Scope{UserID: f.addUser("bob", keyPassword, false).ID}
	f.addUser("root", keyPassword, true)
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i, d := range []struct {
		sc          store.Scope
		title, text string
	}{
		{f.alice, "Telekom", "Ihre Rechnung für März über 39,95 Euro."},
		{f.alice, "Strom", "Die Rechnungen des Stromversorgers kommen jährlich."},
		{f.alice, "Vertrag", "Ein Vertrag über die Wohnung."},
		{f.bob, "Bob", "Bobs eigene Rechnung."},
	} {
		id := searchDoc(t, f.st, d.sc, d.title, d.text)
		f.ids[d.title] = id
		sqlExec(t, f.dbURL, `UPDATE documents SET created_at = $1, is_inbox = true WHERE id = $2`, base.Add(time.Duration(i)*time.Hour), id)
	}
	sqlExec(t, f.dbURL, `UPDATE documents SET document_date = '2025-12-31T23:30:00Z', is_inbox = false WHERE id = $1`, f.ids["Strom"])
	f.loginOK("alice", keyPassword)
	return f
}

func (f *searchFixture) titles(out map[string]any) []string {
	f.t.Helper()
	byID := map[string]string{}
	for title, id := range f.ids {
		byID[id] = title
	}
	var got []string
	for _, d := range listed(f.t, out) {
		got = append(got, byID[d["id"].(string)])
	}
	return got
}

func (f *searchFixture) search(want []string, params ...string) map[string]any {
	f.t.Helper()
	out := f.expect("GET", searchPath(params...), nil, 200)
	if got := f.titles(out); !slices.Equal(got, want) {
		f.t.Errorf("%v: %v, want %v", params, got, want)
	}
	return out
}

func TestSearchAPI(t *testing.T) {
	f := newSearchFixture(t)

	// Relevance by default; every hit has a snippet with the markers
	// around the match, and bob's Rechnung is not there.
	out := f.expect("GET", searchPath("q", "rechnung"), nil, 200)
	got := f.titles(out)
	slices.Sort(got)
	if !slices.Equal(got, []string{"Strom", "Telekom"}) || out["nextCursor"] != nil {
		t.Fatalf("q=rechnung: %v, cursor %v", got, out["nextCursor"])
	}
	for _, d := range listed(t, out) {
		snippet, _ := d["snippet"].(string)
		if !strings.Contains(snippet, store.SnippetStart+"Rechnung") || !strings.Contains(snippet, store.SnippetStop) {
			t.Errorf("snippet %q", snippet)
		}
		if _, ok := d["extractedText"]; ok {
			t.Errorf("a hit carries the text: %v", d)
		}
	}

	// Paging: the cursor continues the same sort.
	first := f.search([]string{"Strom"}, "q", "rechnung", "sort", "added", "limit", "1")
	cursor, _ := first["nextCursor"].(string)
	if cursor == "" {
		t.Fatalf("no cursor: %v", first)
	}
	if next := f.search([]string{"Telekom"}, "q", "rechnung", "sort", "added", "limit", "1", "cursor", cursor); next["nextCursor"] != nil {
		t.Errorf("last page has a cursor: %v", next)
	}
	f.search([]string{"Telekom", "Strom"}, "q", "rechnung", "sort", "added", "order", "asc")
	f.search([]string{"Vertrag", "Telekom", "Strom"}, "sort", "date", "order", "desc")

	// Filters without text: no snippets.
	out = f.search([]string{"Vertrag", "Telekom"}, "inbox", "true")
	for _, d := range listed(t, out) {
		if _, ok := d["snippet"]; ok {
			t.Errorf("a snippet without text: %v", d)
		}
	}
	f.search([]string{"Strom"}, "inbox", "false")
	f.search([]string{"Telekom"}, "q", "rechnung", "inbox", "true")

	// Tag, correspondent and type IDs, alone and with q.
	ctx := context.Background()
	tag, err := f.st.CreateTag(ctx, f.alice, store.TagInput{Name: "Haushalt"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetDocumentTags(ctx, f.alice, f.ids["Strom"], []string{tag.ID}); err != nil {
		t.Fatal(err)
	}
	f.search([]string{"Strom"}, "tag", tag.ID)
	f.search([]string{"Strom"}, "tag", tag.ID, "q", "rechnung")
	f.search(nil, "tag", tag.ID, "q", "vertrag")
	f.search(nil, "correspondent", "00000000-0000-0000-0000-000000000001")
	f.search(nil, "type", "00000000-0000-0000-0000-000000000001")

	// Day boundaries: in UTC "Strom" is dated 2025-12-31, in Berlin
	// 2026-01-01.
	f.search(nil, "q", "date:2026-01-01")
	f.search([]string{"Strom"}, "q", "date:2026-01-01", "tz", "Europe/Berlin")
	f.search([]string{"Strom"}, "q", "date:2025-12-31", "tz", "UTC")

	// Another user finds only their own; an admin finds everyone's.
	b := f.as()
	b.loginOK("bob", keyPassword)
	if got := f.titles(b.expect("GET", searchPath("q", "rechnung"), nil, 200)); !slices.Equal(got, []string{"Bob"}) {
		t.Errorf("bob: %v", got)
	}
	b.expect("GET", searchPath("tag", tag.ID), nil, 200)
	if got := f.titles(b.expect("GET", searchPath("tag", tag.ID), nil, 200)); len(got) != 0 {
		t.Errorf("bob with alice's tag: %v", got)
	}
	r := f.as()
	r.loginOK("root", keyPassword)
	got = f.titles(r.expect("GET", searchPath("q", "rechnung"), nil, 200))
	slices.Sort(got)
	if !slices.Equal(got, []string{"Bob", "Strom", "Telekom"}) {
		t.Errorf("admin: %v", got)
	}

	// Without a session: 401.
	anon := f.as()
	anon.expect("GET", searchPath("q", "rechnung"), nil, 401)
}

// Each invalid parameter answers 400 with fixed wording, never a database
// message.
func TestSearchAPIInvalid(t *testing.T) {
	f := newSearchFixture(t)
	relevance := f.expect("GET", searchPath("q", "rechnung", "limit", "1"), nil, 200)["nextCursor"].(string)
	forged := func(js string) string { return base64.RawURLEncoding.EncodeToString([]byte(js)) }
	tags := make([]string, 0, 2*(maxFilterIDs+1))
	for i := range maxFilterIDs + 1 {
		tags = append(tags, "tag", fmt.Sprintf("00000000-0000-0000-0000-%012d", i))
	}
	words := strings.TrimSpace(strings.Repeat("a ", 32))
	for _, c := range []struct {
		path, want string
	}{
		{searchPath("q", strings.Repeat("a", 1001)), "query too long"},
		{searchPath("q", words+" b"), "query too long"},
		{searchPath("q", words, "inbox", "true"), "query too long"},
		{"/api/documents?q=%FF", "invalid query"},
		{"/api/documents?q=a%00b", "invalid query"},
		{searchPath("cursor", "not a cursor"), "invalid cursor"},
		{searchPath("cursor", strings.Repeat("A", maxCursorLen+1)), "invalid cursor"},
		{searchPath("q", "rechnung", "sort", "added", "cursor", relevance), "invalid cursor"},
		// A time PostgreSQL cannot hold (22008 in the database).
		{searchPath("cursor", forged(`{"s":"added","i":"00000000-0000-0000-0000-000000000000","t":-1152921504606846976}`)), "invalid cursor"},
		{searchPath("sort", "title"), "invalid sort"},
		{searchPath("q", "rechnung", "sort", "relevance", "order", "asc"), "invalid sort"},
		{searchPath("order", "up"), "invalid order"},
		{searchPath("tz", "Mars/Olympus"), "invalid time zone"},
		{searchPath("tz", "Local"), "invalid time zone"},
		{searchPath("tz", "../../etc/passwd"), "invalid time zone"},
		{searchPath("tag", "x"), "invalid tag id"},
		{searchPath("correspondent", "x"), "invalid correspondent id"},
		{searchPath("type", "x"), "invalid type id"},
		{searchPath(tags...), "too many tag ids"},
		{searchPath("inbox", "yes"), "inbox must be true or false"},
		{searchPath("limit", "0"), "limit must be a positive number"},
	} {
		if out := f.expect("GET", c.path, nil, 400); out["error"] != c.want {
			t.Errorf("%s: %v, want %q", c.path, out, c.want)
		}
	}
	// Odd input is a valid search.
	for _, q := range []string{`'; DROP TABLE documents; --`, `&|!():*<->`, "%_\\", `"unclosed`, "-", "OR"} {
		f.expect("GET", searchPath("q", q), nil, 200)
	}
}

// Without parameters the list answers as before the search: the newest
// documents, no snippet and no cursor while everything fits.
func TestListDocumentsUnchanged(t *testing.T) {
	f := newSearchFixture(t)
	out := f.expect("GET", "/api/documents", nil, 200)
	if got := f.titles(out); !slices.Equal(got, []string{"Vertrag", "Strom", "Telekom"}) {
		t.Fatalf("list: %v", got)
	}
	if _, ok := out["nextCursor"]; ok || out["maxUploadSize"] == nil {
		t.Errorf("answer: %v", out)
	}
	want := []string{
		"archiveSerialNumber", "archiveSize", "correspondentId", "createdAt", "documentDate", "documentTypeId", "hasArchive", "id",
		"isFavorite", "isInbox", "modifiedAt", "notes", "originalFileName", "originalMime", "originalSize", "pageCount",
		"processing", "processingNote", "source", "tagIds", "thumbnailUrl", "title",
	}
	for _, d := range listed(t, out) {
		var keys []string
		for k := range d {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if !slices.Equal(keys, want) {
			t.Errorf("fields %v, want %v", keys, want)
		}
	}
}

// The first page of a search writes document.search with the query and
// the filters; further pages, plain lists and refused searches write
// nothing.
func TestSearchAudit(t *testing.T) {
	f := newSearchFixture(t)
	tagID := "00000000-0000-0000-0000-000000000001"
	cursor := f.expect("GET", searchPath("q", "rechnung", "limit", "1"), nil, 200)["nextCursor"].(string)
	for _, c := range []struct {
		path    string
		details map[string]any // nil: no entry
	}{
		{searchPath("q", "rechnung tag:Steuer", "sort", "added", "tz", "Europe/Berlin", "limit", "5"), map[string]any{"q": "rechnung tag:Steuer"}},
		{searchPath("tag", tagID, "tag", tagID, "inbox", "false"), map[string]any{"tag": []any{tagID, tagID}, "inbox": "false"}},
		{searchPath("correspondent", tagID, "type", tagID), map[string]any{"correspondent": []any{tagID}, "type": []any{tagID}}},
		{"/api/documents", nil},
		{searchPath("limit", "5", "sort", "date", "order", "asc"), nil},
		{searchPath("q", "   "), nil},
		{searchPath("q", "rechnung", "limit", "1", "cursor", cursor), nil},
		{searchPath("q", "rechnung", "sort", "title"), nil},
	} {
		n := len(auditLog(t, f.st))
		resp, _ := f.do("GET", c.path, nil)
		got := newEntries(t, f.st, n)
		if c.details == nil {
			if len(got) != 0 {
				t.Errorf("%s (%d): logged %+v", c.path, resp.StatusCode, got)
			}
			continue
		}
		if resp.StatusCode != 200 || len(got) != 1 {
			t.Fatalf("%s: %d, %d entries", c.path, resp.StatusCode, len(got))
		}
		e := got[0]
		if e.Action != "document.search" || e.Actor != store.ActorSession || e.UserName != "alice" || e.TargetType != "" {
			t.Errorf("%s: entry %+v", c.path, e)
		}
		if d := details(t, e); fmt.Sprint(d) != fmt.Sprint(c.details) {
			t.Errorf("%s: details %v, want %v", c.path, d, c.details)
		}
	}

	// With an API key: the key is named.
	key := f.createKey("script", []string{"read"}, "")
	n := len(auditLog(t, f.st))
	if resp, out := f.keyRequest(key, "GET", searchPath("q", "vertrag"), nil, "", nil); resp.StatusCode != 200 || len(listed(t, out)) != 1 {
		t.Fatalf("search with a key: %d %v", resp.StatusCode, out)
	}
	if got := newEntries(t, f.st, n); len(got) != 1 || got[0].Action != "document.search" || got[0].Actor != store.ActorAPIKey || got[0].APIKeyName != "script" {
		t.Errorf("key search entries %+v", got)
	}

	// Without the audit log a search answers 503 and shows nothing; the
	// plain list still works.
	breakAudit(t, f.dbURL)
	if out := f.expect("GET", searchPath("q", "rechnung"), nil, 503); out["error"] != errAuditUnavailable || out["documents"] != nil {
		t.Errorf("search without the log: %v", out)
	}
	f.expect("GET", "/api/documents", nil, 200)
	f.expect("GET", searchPath("q", "rechnung", "cursor", cursor, "limit", "1"), nil, 200)
}
