package store_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/search"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// The store search (#6, part 2): store.SearchDocuments with the parser's
// queries. The index itself is tested in search_index_test.go.

func parseQuery(t *testing.T, s string, loc *time.Location) search.Query {
	t.Helper()
	q, err := search.Parse(s, loc)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return q
}

func runSearch(t *testing.T, st *store.Store, sc store.Scope, q search.Query) *store.DocumentPage {
	t.Helper()
	page, err := st.SearchDocuments(context.Background(), sc, q)
	if err != nil {
		t.Fatalf("search %q: %v", q.String(), err)
	}
	return page
}

func hitTitles(page *store.DocumentPage) []string {
	titles := []string{}
	for _, h := range page.Hits {
		titles = append(titles, h.Title)
	}
	return titles
}

// found returns the titles a query finds in sc, sorted.
func found(t *testing.T, st *store.Store, sc store.Scope, s string) []string {
	t.Helper()
	titles := hitTitles(runSearch(t, st, sc, parseQuery(t, s, nil)))
	slices.Sort(titles)
	return titles
}

func wantFound(t *testing.T, st *store.Store, sc store.Scope, s string, want ...string) {
	t.Helper()
	wantTitles(t, s, found(t, st, sc, s), want...)
}

// TestSearchDocumentsFindsInflectedWordInRecognizedText is the issue's
// "done when", through the parser and the store search: a word of the
// recognized text is found, also in another inflected form.
func TestSearchDocumentsFindsInflectedWordInRecognizedText(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	doc := mustDoc(t, st, alice, newDoc("scan"))
	now := time.Now()
	if err := st.CompleteJob(ctx, claim(t, st, now), now, store.ProcessResult{
		PageCount: 1, Text: "Anbei erhalten Sie die Rechnungen für die Versicherungen im März.", TextSource: "Tesseract",
	}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"Rechnung", "rechnungen", "Versicherung", "Marz", "März", `"Rechnung für"`} {
		wantFound(t, st, alice, q, doc.Title)
	}
	wantFound(t, st, alice, "Mahnung")
}

func TestSearchDocumentsLanguage(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	var version int
	if err := connect(t, url).QueryRow(context.Background(), `SELECT current_setting('server_version_num')::int`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	foldsUE := version >= 180000 // PostgreSQL 18's German stemmer folds ue to u
	textDoc(t, st, alice, "rechnungen", "Alle Rechnungen und Versicherungen des Jahres, mit Gebühren.")
	textDoc(t, st, alice, "strasse", "Hauptstraße 5, Berlin. Die Häuser am See.")
	textDoc(t, st, alice, "english", "Please find the invoices of our meetings attached.")
	textDoc(t, st, alice, "numbers", "Rechnungsnummer RE-2024-00123, IBAN DE89370400440532013000.")
	textDoc(t, st, alice, "apart", "Die Stadtwerke senden morgen eine Rechnung.")

	for _, c := range []struct {
		q    string
		want []string
	}{
		{"Rechnung", []string{"apart", "numbers", "rechnungen"}}, // also as the prefix of Rechnungsnummer
		{"Versicherung", []string{"rechnungen"}},
		{"Gebuhren", []string{"rechnungen"}},
		{"Hauptstrasse", []string{"strasse"}},
		{"Haus", []string{"strasse"}},
		{"invoice", []string{"english"}},
		{"meeting", []string{"english"}},
		{"RE-2024-00123", []string{"numbers"}},
		{"DE89370400440532013000", []string{"numbers"}},
		{"Rechn", []string{"apart", "numbers", "rechnungen"}},
		{"Re", []string{"numbers"}}, // the word RE of RE-2024-00123
		{"Ha", nil},                 // two letters are no prefix of Hauptstraße
		{`"Stadtwerke senden"`, []string{"apart"}},
		{`"Stadtwerke Rechnung"`, nil},
		{"Stadtwerke Rechnung", []string{"apart"}},
		{"Rechnung -Stadtwerke", []string{"numbers", "rechnungen"}},
		{"Hauptstrasse OR invoices", []string{"english", "strasse"}},
		{"Hauptstrasse OR invoices Berlin", []string{"strasse"}},
	} {
		wantFound(t, st, alice, c.q, c.want...)
	}
	if foldsUE {
		wantFound(t, st, alice, "Gebuehren", "rechnungen")
	} else {
		wantFound(t, st, alice, "Gebuehren")
	}
}

// Free text also matches the other fields, and every word may match a
// different one.
func TestSearchDocumentsFields(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	telekom := mustCorrespondent(t, st, alice, "Deutsche Telekom")
	invoice, err := st.CreateDocumentType(ctx, alice, store.DocumentTypeInput{Name: "Rechnung"})
	if err != nil {
		t.Fatal(err)
	}
	auto := mustTag(t, st, alice, "Auto", nil)
	workshop := mustTag(t, st, alice, "Werkstatt", auto)
	kfz := mustTag(t, st, alice, "Kfz-Versicherung", nil)

	in := newDoc("linked")
	in.CorrespondentID, in.DocumentTypeID = &telekom.ID, &invoice.ID
	in.TagIDs = []string{workshop.ID}
	mustDoc(t, st, alice, in)
	in = newDoc("insured")
	in.TagIDs = []string{kfz.ID}
	mustDoc(t, st, alice, in)
	in = newDoc("numbered")
	in.ArchiveSerialNumber = ptr(int32(123))
	in.OriginalFileName = "Scan_Vodafone-2024.pdf"
	in.Notes = "Kündigung zum Jahresende"
	mustDoc(t, st, alice, in)
	textDoc(t, st, alice, "text", "Ihre Rechnung von der Telekom.")

	for _, c := range []struct {
		q    string
		want []string
	}{
		{"Telekom", []string{"linked", "text"}},
		{"Telekom Rechnung", []string{"linked", "text"}}, // correspondent and type, or the text
		{"Telekom Werkstatt", []string{"linked"}},        // correspondent and tag
		{"Auto", []string{"linked"}},                     // a parent tag through its child
		{"versich", []string{"insured"}},                 // a name substring
		{"123", []string{"numbered"}},
		{"ASN123", []string{"numbered"}},
		{"vodafone", []string{"numbered"}},
		{"Kündigung", []string{"numbered"}},
		{"Jahresende -Kündigung", nil},
	} {
		wantFound(t, st, alice, c.q, c.want...)
	}
}

func TestSearchDocumentsFilters(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	conn := connect(t, url)
	steuer := mustTag(t, st, alice, "Steuer", nil)
	belege := mustTag(t, st, alice, "Belege", steuer)
	auto := mustTag(t, st, alice, "Auto", nil)
	kfz := mustTag(t, st, alice, "Kfz Versicherung", nil)
	telekom := mustCorrespondent(t, st, alice, "Telekom")
	bank := mustCorrespondent(t, st, alice, "Bank")
	invoice, err := st.CreateDocumentType(ctx, alice, store.DocumentTypeInput{Name: "Rechnung"})
	if err != nil {
		t.Fatal(err)
	}

	day := func(h, m int) time.Time { return time.Date(2024, 3, 15, h, m, 0, 0, time.UTC) }
	type spec struct {
		title string
		tags  []string
		corr  *string
		typ   *string
		asn   int32
		date  time.Time
		added time.Time
		inbox bool
		fav   bool
		text  string
	}
	for _, s := range []spec{
		{title: "a", tags: []string{belege.ID}, corr: &telekom.ID, typ: &invoice.ID, asn: 123,
			date: day(0, 0).Add(-30 * time.Minute), added: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), inbox: true, text: "Rechnung"},
		{title: "b", tags: []string{auto.ID}, corr: &bank.ID, asn: 124,
			date: day(0, 0), added: time.Date(2026, 9, 30, 23, 59, 0, 0, time.UTC), fav: true, text: "Rechnung"},
		{title: "c", tags: []string{steuer.ID, auto.ID, kfz.ID}, corr: &telekom.ID,
			date: day(23, 59), added: time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC), inbox: true, fav: true},
		{title: "d", date: day(24, 0), added: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		in := newDoc(s.title)
		in.TagIDs, in.CorrespondentID, in.DocumentTypeID = s.tags, s.corr, s.typ
		if s.asn != 0 {
			in.ArchiveSerialNumber = &s.asn
		}
		in.DocumentDate, in.IsInbox, in.IsFavorite, in.ExtractedText = s.date, s.inbox, s.fav, s.text
		d := mustDoc(t, st, alice, in)
		if _, err := conn.Exec(ctx, `UPDATE documents SET created_at = $1 WHERE id = $2`, s.added, d.ID); err != nil {
			t.Fatal(err)
		}
	}

	for _, c := range []struct {
		q    string
		want []string
	}{
		{"", []string{"a", "b", "c", "d"}},
		{"tag:Steuer", []string{"a", "c"}}, // with its descendants
		{"tag:steuer", []string{"a", "c"}},
		{"tag:Belege", []string{"a"}},
		{"tag:Steuer,Auto", []string{"a", "b", "c"}},
		{"tag:Steuer tag:Auto", []string{"c"}},
		{"tag:Belege tag:Auto", nil},
		{"-tag:Steuer", []string{"b", "d"}},
		{"-tag:Steuer,Auto", []string{"d"}},
		{`tag:"Kfz Versicherung"`, []string{"c"}},
		{"tag:Bel", nil}, // a whole name
		{"tag:Unbekannt", nil},
		{"tag:Unbekannt,Auto", []string{"b", "c"}},
		{"-tag:Unbekannt", []string{"a", "b", "c", "d"}},
		{"correspondent:Telekom", []string{"a", "c"}},
		{"correspondent:telekom,Bank", []string{"a", "b", "c"}},
		{"correspondent:Telekom correspondent:Bank", nil},
		{"-correspondent:Telekom", []string{"b", "d"}},
		{"correspondent:Tele", nil},
		{"type:Rechnung", []string{"a"}},
		{"-type:Rechnung", []string{"b", "c", "d"}},
		{"type:Vertrag", nil},
		{"asn:123", []string{"a"}},
		{"asn:123,124", []string{"a", "b"}},
		{"asn:123 asn:124", nil},
		{"-asn:123", []string{"b", "c", "d"}},
		{"asn:999", nil},
		{"is:inbox", []string{"a", "c"}},
		{"-is:inbox", []string{"b", "d"}},
		{"is:favorite", []string{"b", "c"}},
		{"is:inbox is:favorite", []string{"c"}},
		{"is:inbox -is:inbox", nil},
		// Days in UTC: a is 2024-03-14 23:30, b 2024-03-15 00:00, c 23:59, d 2024-03-16 00:00.
		{"date:2024-03-15", []string{"b", "c"}},
		{"created:2024-03-15", []string{"b", "c"}},
		{"date:2024-03", []string{"a", "b", "c", "d"}},
		{"date:2024-03-14..2024-03-15", []string{"a", "b", "c"}},
		{"date:..2024-03-14", []string{"a"}},
		{"date:2024-03-16..", []string{"d"}},
		{"date:>2024-03-15", []string{"d"}},
		{"date:>=2024-03-15", []string{"b", "c", "d"}},
		{"date:<2024-03-15", []string{"a"}},
		{"date:<=2024-03-15", []string{"a", "b", "c"}},
		{"-date:2024-03-15", []string{"a", "d"}},
		{"-date:<2024-03-15", []string{"b", "c", "d"}},
		{"-date:>=2024-03-16", []string{"a", "b", "c"}},
		{"date:2024-03-15 -date:2024-03-15", nil},
		{"date:2023", nil},
		{"date:2024-03-16..2024-03-14", nil},
		{"added:2026-10", []string{"a", "c"}},
		{"added:2026-09-30", []string{"b"}},
		{"added:<2026", []string{"d"}},
		{"-added:2026-10", []string{"b", "d"}},
		// Filters and text together.
		{"Rechnung tag:Steuer", []string{"a"}},
		{"Rechnung -is:inbox", []string{"b"}},
	} {
		wantFound(t, st, alice, c.q, c.want...)
	}

	// Day boundaries in another time zone: in Berlin (UTC+1 in March)
	// 2024-03-15 runs from 03-14 23:00 to 03-15 23:00 UTC.
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	q := parseQuery(t, "date:2024-03-15", berlin)
	wantTitles(t, "Berlin", sortedTitles(runSearch(t, st, alice, q)), "a", "b")

	// The ID parameters: any of them, a tag with its descendants, joined
	// with the rest by AND.
	for _, c := range []struct {
		name string
		q    search.Query
		want []string
	}{
		{"tag ID with descendants", search.Query{TagIDs: []string{steuer.ID}}, []string{"a", "c"}},
		{"tag IDs", search.Query{TagIDs: []string{belege.ID, auto.ID}}, []string{"a", "b", "c"}},
		{"tag ID and q", withText(t, "tag:Auto", search.Query{TagIDs: []string{steuer.ID}}), []string{"c"}},
		{"correspondent IDs", search.Query{CorrespondentIDs: []string{bank.ID, telekom.ID}}, []string{"a", "b", "c"}},
		{"correspondent ID and q", withText(t, "correspondent:Telekom", search.Query{CorrespondentIDs: []string{bank.ID}}), nil},
		{"type ID and text", withText(t, "Rechnung", search.Query{TypeIDs: []string{invoice.ID}}), []string{"a"}},
		{"unknown tag ID", search.Query{TagIDs: []string{"00000000-0000-0000-0000-000000000000"}}, nil},
	} {
		wantTitles(t, c.name, sortedTitles(runSearch(t, st, alice, c.q)), c.want...)
	}
	for _, bad := range []search.Query{{TagIDs: []string{"x"}}, {CorrespondentIDs: []string{""}}, {TypeIDs: []string{"1"}}} {
		if _, err := st.SearchDocuments(ctx, alice, bad); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("malformed ID %+v: %v", bad, err)
		}
	}
}

func withText(t *testing.T, s string, q search.Query) search.Query {
	t.Helper()
	p := parseQuery(t, s, nil)
	q.Text, q.Filters = p.Text, p.Filters
	return q
}

func sortedTitles(page *store.DocumentPage) []string {
	titles := hitTitles(page)
	slices.Sort(titles)
	return titles
}

// A user finds nothing of another user's, not by names nor by IDs; an
// admin finds both, with names resolved per owner.
func TestSearchDocumentsScope(t *testing.T) {
	st := storetest.New(t)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	admin := store.Scope{UserID: storetest.User(t, st, "admin", true).ID, All: true}
	for _, sc := range []store.Scope{alice, bob} {
		name := "alice"
		if sc == bob {
			name = "bob"
		}
		tag := mustTag(t, st, sc, "Steuer", nil)
		corr := mustCorrespondent(t, st, sc, "Bank")
		in := newDoc(name)
		in.ExtractedText = "Rechnung"
		in.TagIDs, in.CorrespondentID = []string{tag.ID}, &corr.ID
		mustDoc(t, st, sc, in)
	}
	secret := mustTag(t, st, bob, "Geheimprojekt", nil)
	bobCorr := mustCorrespondent(t, st, bob, "Schweizer Bank")
	in := newDoc("bob secret")
	in.TagIDs, in.CorrespondentID = []string{secret.ID}, &bobCorr.ID
	mustDoc(t, st, bob, in)

	wantFound(t, st, alice, "Rechnung", "alice")
	wantFound(t, st, alice, "tag:Steuer", "alice")
	wantFound(t, st, alice, "correspondent:Bank", "alice")
	wantFound(t, st, alice, "Geheimprojekt")
	wantFound(t, st, alice, "tag:Geheimprojekt")
	wantFound(t, st, alice, `correspondent:"Schweizer Bank"`)
	wantFound(t, st, alice, "Schweizer")
	wantFound(t, st, alice, secret.ID)
	wantFound(t, st, alice, `"`+secret.ID+`"`)
	wantTitles(t, "bob's tag ID", sortedTitles(runSearch(t, st, alice, search.Query{TagIDs: []string{secret.ID}})))
	wantTitles(t, "bob's correspondent ID", sortedTitles(runSearch(t, st, alice, search.Query{CorrespondentIDs: []string{bobCorr.ID}})))
	wantFound(t, st, alice, "", "alice")

	wantFound(t, st, admin, "Rechnung", "alice", "bob")
	wantFound(t, st, admin, "tag:Steuer", "alice", "bob")
	wantFound(t, st, admin, "Steuer", "alice", "bob")
	wantFound(t, st, admin, "Geheimprojekt", "bob secret")
	wantFound(t, st, admin, "", "alice", "bob", "bob secret")
}

// Odd input gives a result or a parse error, never a database error.
func TestSearchDocumentsOddInput(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	mustTag(t, st, alice, `100% \_Steuer_`, nil)
	textDoc(t, st, alice, "doc", `It's 100% done: a\b, x_y, (a|b) & !c <-> d:*`)
	odd := []string{
		`'`, `''`, `\`, `\\`, `&|!():*<->`, `%`, `_`, `%_%`, `'; DROP TABLE documents; --`,
		`"'; DROP TABLE documents; --"`, `it's`, `a\b`, `d:*`, `!c`, `<->`, `the and or`, `der die das`,
		`... ,,, ;;;`, `"..."`, `tag:'`, `tag:\`, `tag:%`, `correspondent:_`, `type:"%_"`, `-"&|!"`,
		`a OR "'" OR \`, `100%`, `\_Steuer_`, `x_y`, `tag:"100% \_Steuer_"`, "  ", "😀", "ß", "İ",
	}
	r := rand.New(rand.NewPCG(53, 6))
	alphabet := []rune(`ab ' " \ - : , . & | ! ( ) * < > % _ ä ß 😀 OR tag: is:inbox date:2024 asn:1`)
	for range 300 {
		var b strings.Builder
		for range r.IntN(40) {
			b.WriteRune(alphabet[r.IntN(len(alphabet))])
		}
		odd = append(odd, b.String())
	}
	for _, s := range odd {
		q, err := search.Parse(s, nil)
		if err != nil {
			t.Errorf("parse %q: %v", s, err)
			continue
		}
		for _, sort := range []search.Sort{"", search.SortAdded, search.SortDate} {
			q.Sort = sort
			if _, err := st.SearchDocuments(ctx, alice, q); err != nil {
				t.Errorf("search %q (sort %q): %v", s, sort, err)
			}
		}
	}
	wantFound(t, st, alice, `tag:"100% \_Steuer_"`)
	wantFound(t, st, alice, `100%`, "doc")
	// Text the parser would refuse never reaches the database.
	for _, bad := range []string{"a\x00b", "\xff"} {
		_, err := st.SearchDocuments(ctx, alice, search.Query{Text: []search.Text{{Value: bad}}})
		if !errors.Is(err, store.ErrInvalid) {
			t.Errorf("text %q: %v", bad, err)
		}
		_, err = st.SearchDocuments(ctx, alice, search.Query{Filters: []search.Filter{{Field: search.FieldTag, Values: []string{bad}}}})
		if !errors.Is(err, store.ErrInvalid) {
			t.Errorf("tag %q: %v", bad, err)
		}
	}
}

// Only stop words or punctuation leave no full-text condition: the newest
// documents, without snippets, also when relevance was asked for.
func TestSearchDocumentsNothingLeft(t *testing.T) {
	st := storetest.New(t)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	textDoc(t, st, alice, "doc", "Etwas Text.")
	for _, s := range []string{"...", "!!! ???"} {
		for _, sort := range []search.Sort{"", search.SortRelevance} {
			q := parseQuery(t, s, nil)
			q.Sort = sort
			page := runSearch(t, st, alice, q)
			if len(page.Hits) != 1 || page.Hits[0].Snippet != "" {
				t.Errorf("%q sort %q: %+v", s, sort, page.Hits)
			}
		}
	}
}

func TestSearchDocumentsPlainList(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	admin := store.Scope{UserID: storetest.User(t, st, "admin", true).ID, All: true}
	conn := connect(t, url)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, c := range []string{"a1", "a2", "a3"} {
		in := newDoc(c)
		in.ExtractedText = "text of " + c
		d := mustDoc(t, st, alice, in)
		if _, err := conn.Exec(ctx, `UPDATE documents SET created_at = $1 WHERE id = $2`, base.Add(time.Duration(i)*time.Hour), d.ID); err != nil {
			t.Fatal(err)
		}
	}
	mustDoc(t, st, bob, newDoc("b1"))

	page := runSearch(t, st, alice, search.Query{Limit: 2})
	if got := hitTitles(page); !slices.Equal(got, []string{"a3", "a2"}) || page.NextCursor == "" {
		t.Fatalf("first page: %v, cursor %q", got, page.NextCursor)
	}
	for _, h := range page.Hits {
		if h.OwnerID != alice.UserID || h.ExtractedText != "" || h.Snippet != "" || h.TagIDs == nil {
			t.Fatalf("hit: %+v", h)
		}
	}
	next := runSearch(t, st, alice, search.Query{Limit: 2, Cursor: page.NextCursor})
	if got := hitTitles(next); !slices.Equal(got, []string{"a1"}) || next.NextCursor != "" {
		t.Fatalf("last page: %v, cursor %q", got, next.NextCursor)
	}
	if got := hitTitles(runSearch(t, st, alice, search.Query{Ascending: true})); !slices.Equal(got, []string{"a1", "a2", "a3"}) {
		t.Fatalf("ascending: %v", got)
	}
	if got := hitTitles(runSearch(t, st, bob, search.Query{})); !slices.Equal(got, []string{"b1"}) {
		t.Fatalf("bob: %v", got)
	}
	if got := runSearch(t, st, admin, search.Query{}); len(got.Hits) != 4 {
		t.Fatalf("admin: %v", hitTitles(got))
	}
	// The limit: 0 is the default, more than the maximum is the maximum.
	for i := range search.MaxLimit {
		mustDoc(t, st, alice, newDoc(fmt.Sprintf("more %d", i)))
	}
	if got := runSearch(t, st, alice, search.Query{}); len(got.Hits) != search.DefaultLimit || got.NextCursor == "" {
		t.Fatalf("default limit: %d", len(got.Hits))
	}
	if got := runSearch(t, st, alice, search.Query{Limit: 1000}); len(got.Hits) != search.MaxLimit {
		t.Fatalf("limit 1000: %d", len(got.Hits))
	}
}

// Keyset paging gives every match exactly once and in order, for each sort,
// also when many rows share a key.
func TestSearchDocumentsPaging(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	conn := connect(t, url)
	const n = 23
	for i := range n {
		// Three texts give three ranks; the dates repeat in threes.
		text := strings.Repeat("Rechnung ", 1+i%3) + strings.Repeat("Wort ", 10)
		in := newDoc(fmt.Sprintf("doc %02d", i))
		in.ExtractedText = text
		in.DocumentDate = time.Date(2024, 1, 1+i%4, 12, 0, 0, 0, time.UTC)
		d := mustDoc(t, st, alice, in)
		if _, err := conn.Exec(ctx, `UPDATE documents SET created_at = $1 WHERE id = $2`,
			time.Date(2026, 1, 1, 0, 0, 0, 123456789*(i%3), time.UTC), d.ID); err != nil {
			t.Fatal(err)
		}
	}
	type row struct {
		id    string
		rank  float64
		added time.Time
		date  time.Time
	}
	rowsByID := map[string]row{}
	rows, err := conn.Query(ctx, `
		SELECT id::text, ts_rank_cd('{0.1,0.2,0.4,1.0}', search_text || search_links,
		                            document_search_tsquery($1, '{Rechnung}', '{f}', '{f}', '{0}'), 1)::float8,
		       created_at, document_date
		FROM documents`, alice.UserID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.rank, &r.added, &r.date); err != nil {
			t.Fatal(err)
		}
		rowsByID[r.id] = r
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}

	for _, c := range []struct {
		sort search.Sort
		asc  bool
		less func(a, b row) bool // a comes before b
	}{
		{search.SortRelevance, false, func(a, b row) bool { return a.rank > b.rank || a.rank == b.rank && a.id < b.id }},
		{search.SortAdded, false, func(a, b row) bool { return a.added.After(b.added) || a.added.Equal(b.added) && a.id < b.id }},
		{search.SortAdded, true, func(a, b row) bool { return a.added.Before(b.added) || a.added.Equal(b.added) && a.id > b.id }},
		{search.SortDate, false, func(a, b row) bool { return a.date.After(b.date) || a.date.Equal(b.date) && a.id < b.id }},
		{search.SortDate, true, func(a, b row) bool { return a.date.Before(b.date) || a.date.Equal(b.date) && a.id > b.id }},
	} {
		t.Run(fmt.Sprintf("%s asc=%v", c.sort, c.asc), func(t *testing.T) {
			q := parseQuery(t, "Rechnung", nil)
			q.Sort, q.Ascending, q.Limit = c.sort, c.asc, 4
			var got []string
			for pages := 0; ; pages++ {
				if pages > n {
					t.Fatal("no last page")
				}
				page := runSearch(t, st, alice, q)
				for _, h := range page.Hits {
					got = append(got, h.ID)
				}
				if page.NextCursor == "" {
					break
				}
				if len(page.Hits) != 4 {
					t.Fatalf("a short page before the last: %d", len(page.Hits))
				}
				q.Cursor = page.NextCursor
			}
			want := make([]string, 0, n)
			for id := range rowsByID {
				want = append(want, id)
			}
			slices.SortFunc(want, func(a, b string) int {
				if c.less(rowsByID[a], rowsByID[b]) {
					return -1
				}
				return 1
			})
			if !slices.Equal(got, want) {
				t.Fatalf("pages give\n%v\nwant\n%v", got, want)
			}
		})
	}

	// Cursors belong to their sort.
	q := parseQuery(t, "Rechnung", nil)
	q.Limit = 2
	relevance := runSearch(t, st, alice, q).NextCursor
	q.Sort = search.SortAdded
	added := runSearch(t, st, alice, q).NextCursor
	for _, c := range []struct {
		sort   search.Sort
		asc    bool
		cursor string
	}{
		{search.SortAdded, false, relevance},
		{search.SortRelevance, false, added},
		{search.SortDate, false, added},
		{search.SortAdded, true, added},
		{search.SortAdded, false, "not base64!"},
		{search.SortAdded, false, base64.RawURLEncoding.EncodeToString([]byte(`{"s":"added","i":"x","t":1}`))},
		{search.SortAdded, false, base64.RawURLEncoding.EncodeToString([]byte(`{"s":"added","i":"00000000-0000-0000-0000-000000000000"}`))},
		{search.SortRelevance, false, base64.RawURLEncoding.EncodeToString([]byte(`{"s":"relevance","i":"00000000-0000-0000-0000-000000000000","t":1}`))},
		{search.SortAdded, false, base64.RawURLEncoding.EncodeToString([]byte(`[]`))},
		// Times PostgreSQL cannot hold, and math.MinInt64, which pgx's
		// conversion would wrap around.
		{search.SortAdded, false, rawCursor(`{"s":"added","i":"00000000-0000-0000-0000-000000000000","t":-210866803200000001}`)},
		{search.SortDate, false, rawCursor(`{"s":"date","i":"00000000-0000-0000-0000-000000000000","t":-1152921504606846976}`)},
		{search.SortAdded, true, rawCursor(`{"s":"added","a":true,"i":"00000000-0000-0000-0000-000000000000","t":-9223372036854775808}`)},
		{search.SortAdded, false, rawCursor(`{"s":"added","i":"00000000-0000-0000-0000-000000000000","t":9223372036854775808}`)},
		// A rank that is not a finite number: NaN and +Inf.
		{search.SortRelevance, false, rawCursor(`{"s":"relevance","i":"00000000-0000-0000-0000-000000000000","r":2143289344}`)},
		{search.SortRelevance, false, rawCursor(`{"s":"relevance","i":"00000000-0000-0000-0000-000000000000","r":2139095040}`)},
	} {
		q := parseQuery(t, "Rechnung", nil)
		q.Sort, q.Ascending, q.Cursor = c.sort, c.asc, c.cursor
		if _, err := st.SearchDocuments(ctx, alice, q); !errors.Is(err, store.ErrInvalidCursor) {
			t.Errorf("cursor %q with sort %s asc=%v: %v", c.cursor, c.sort, c.asc, err)
		}
	}
	// The ends of PostgreSQL's range are valid cursors: newest first from
	// the latest time finds everything, oldest first from the earliest too.
	for _, c := range []struct {
		sort  search.Sort
		asc   bool
		at    int64
		total int
	}{
		{search.SortAdded, false, math.MaxInt64, 1},
		{search.SortDate, false, math.MaxInt64, 1},
		{search.SortAdded, true, -210866803200000000, 1},
		{search.SortDate, true, -210866803200000000, 1},
	} {
		q := parseQuery(t, "Rechnung", nil)
		q.Sort, q.Ascending, q.Limit = c.sort, c.asc, 1
		q.Cursor = rawCursor(fmt.Sprintf(`{"s":%q,"a":%v,"i":"00000000-0000-0000-0000-000000000000","t":%d}`, c.sort, c.asc, c.at))
		page, err := st.SearchDocuments(ctx, alice, q)
		if err != nil || len(page.Hits) != c.total {
			t.Errorf("cursor at %d, sort %s asc=%v: %v, %v", c.at, c.sort, c.asc, page, err)
		}
	}
	// A query built by hand is held to the parser's limits.
	tooMany := search.Query{}
	for range search.MaxItems + 1 {
		tooMany.Text = append(tooMany.Text, search.Text{Value: "Rechnung"})
	}
	tooLong := search.Query{Text: []search.Text{{Value: strings.Repeat("x", search.MaxBytes+1)}}}
	for _, bad := range []search.Query{tooMany, tooLong} {
		if _, err := st.SearchDocuments(ctx, alice, bad); !errors.Is(err, search.ErrTooLong) {
			t.Errorf("query over the limits: %v", err)
		}
	}
	for _, bad := range []search.Query{{Sort: search.SortRelevance, Ascending: true}, {Sort: "title"}} {
		if _, err := st.SearchDocuments(ctx, alice, bad); !errors.Is(err, store.ErrInvalidSort) {
			t.Errorf("sort %+v: %v", bad, err)
		}
	}
}

// Relevance is the default with free text; a title hit ranks above a hit in
// the text.
func TestSearchDocumentsRelevance(t *testing.T) {
	st := storetest.New(t)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	textDoc(t, st, alice, "Brief", "Anbei die Rechnung für den Monat, und noch viel mehr Text dazu.")
	textDoc(t, st, alice, "Rechnung März", "")
	textDoc(t, st, alice, "Notiz", "Nichts davon.")
	if got := hitTitles(runSearch(t, st, alice, parseQuery(t, "Rechnung", nil))); !slices.Equal(got, []string{"Rechnung März", "Brief"}) {
		t.Fatalf("relevance: %v", got)
	}
	q := parseQuery(t, "Rechnung", nil)
	q.Sort = search.SortAdded
	if got := hitTitles(runSearch(t, st, alice, q)); !slices.Equal(got, []string{"Rechnung März", "Brief"}) {
		t.Fatalf("added: %v", got)
	}
	// Ascending without a sort means oldest added first.
	q = parseQuery(t, "Rechnung", nil)
	q.Ascending = true
	if got := hitTitles(runSearch(t, st, alice, q)); !slices.Equal(got, []string{"Brief", "Rechnung März"}) {
		t.Fatalf("ascending: %v", got)
	}
	// Only negated words rank nothing: newest added first, and the cursor
	// is one of that sort.
	q = parseQuery(t, "-Notiz", nil)
	q.Limit = 1
	page := runSearch(t, st, alice, q)
	if got := hitTitles(page); !slices.Equal(got, []string{"Rechnung März"}) || page.NextCursor == "" {
		t.Fatalf("negation only: %v, cursor %q", got, page.NextCursor)
	}
	q.Sort, q.Cursor = search.SortAdded, page.NextCursor
	if got := hitTitles(runSearch(t, st, alice, q)); !slices.Equal(got, []string{"Brief"}) {
		t.Fatalf("negation only, next page: %v", got)
	}
	q.Sort = search.SortRelevance
	if _, err := st.SearchDocuments(context.Background(), alice, q); !errors.Is(err, store.ErrInvalidCursor) {
		t.Fatalf("negation-only cursor with relevance: %v", err)
	}
}

// Snippets mark the matches, in German, English or accent-free form;
// without free text there is none.
func TestSearchDocumentsSnippets(t *testing.T) {
	st := storetest.New(t)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	corr := mustCorrespondent(t, st, alice, "Telekom")
	textDoc(t, st, alice, "german", "Sehr geehrte Damen und Herren, anbei die Rechnungen für März. Mit freundlichen Grüßen")
	textDoc(t, st, alice, "english", "We wish you happy holidays.")
	textDoc(t, st, alice, "accent", "Wir trafen uns im Café am Markt.")
	textDoc(t, st, alice, "markup", "<script>alert(1)</script> Kündigung")
	in := newDoc("linked")
	in.CorrespondentID = &corr.ID
	in.ExtractedText = "Ein Text ohne den Namen."
	mustDoc(t, st, alice, in)
	snippet := func(q string) string {
		t.Helper()
		page := runSearch(t, st, alice, parseQuery(t, q, nil))
		if len(page.Hits) != 1 {
			t.Fatalf("%q: %v", q, hitTitles(page))
		}
		return page.Hits[0].Snippet
	}
	mark := func(w string) string { return store.SnippetStart + w + store.SnippetStop }
	for _, c := range []struct{ q, want string }{
		{"Rechnung", mark("Rechnungen")},
		{`"Rechnungen für"`, mark("Rechnungen")},
		{"happiness", mark("happy")}, // english stems only
		{"cafe", mark("Café")},       // paperless_simple only
		// ts_headline leaves out what its parser takes for HTML tags; the
		// rest is plain text, which the UI never treats as HTML.
		{"Kündigung", mark("Kündigung")},
	} {
		if got := snippet(c.q); !strings.Contains(got, c.want) {
			t.Errorf("%q: snippet %q, want it to contain %q", c.q, got, c.want)
		}
	}
	if got := snippet("Telekom"); strings.Contains(got, store.SnippetStart) || !strings.Contains(got, "ohne den Namen") {
		t.Errorf("a match by name: snippet %q", got)
	}
	for _, h := range runSearch(t, st, alice, search.Query{}).Hits {
		if h.Snippet != "" {
			t.Errorf("snippet without free text: %q", h.Snippet)
		}
	}
	if got := runSearch(t, st, alice, parseQuery(t, "-Rechnung", nil)).Hits; len(got) != 4 {
		t.Errorf("exclusion only: %d hits", len(got))
	}
}

// TestSearchFilterCopies keeps the copies of the filter block and of the
// snippet in queries/search.sql equal.
func TestSearchFilterCopies(t *testing.T) {
	src, err := os.ReadFile("queries/search.sql")
	if err != nil {
		t.Fatal(err)
	}
	// The filter block is also in MatchDocumentIDs (the bulk change by
	// search), which has no snippet. SearchDocumentsDeletedDesc is the
	// trash's own order (#12).
	for _, marker := range []string{"filters", "snippet"} {
		names := `SearchDocuments`
		want := 6
		if marker == "filters" {
			names, want = `(SearchDocuments|MatchDocumentIDs)`, 7
		}
		queries := regexp.MustCompile(`(?m)^-- name: `+names).FindAllStringIndex(string(src), -1)
		var copies []string
		for _, part := range strings.Split(string(src), "-- "+marker+":begin")[1:] {
			body, _, ok := strings.Cut(part, "-- "+marker+":end")
			if !ok {
				t.Fatalf("%s:begin without %s:end", marker, marker)
			}
			var lines []string
			for _, l := range strings.Split(body, "\n") {
				lines = append(lines, strings.TrimSpace(l))
			}
			copies = append(copies, strings.Join(lines, "\n"))
		}
		if len(copies) != len(queries) || len(copies) != want {
			t.Fatalf("%d %s copies for %d queries, want %d", len(copies), marker, len(queries), want)
		}
		for i, c := range copies[1:] {
			if c != copies[0] {
				t.Errorf("%s copy %d differs from the first:\n%s\n---\n%s", marker, i+2, c, copies[0])
			}
		}
	}
}

// document_search_tsquery runs once per search, also when every search
// query walks the table, and the full-text condition of what the store
// runs uses documents_search_idx. A call in the search query itself would
// run once per row of a sequential scan (part 1's review measured 4.9 s
// instead of 22 ms for 20 000 documents).
func TestSearchTsqueryOncePerSearch(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	conn := connect(t, url)
	if _, err := conn.Exec(ctx, `
		INSERT INTO documents (owner_id, title, source, original_sha256, original_size, original_mime, extracted_text)
		SELECT $1, 'doc ' || i, 'file', encode(sha256(i::text::bytea), 'hex'), 1, 'application/pdf',
		       CASE WHEN i % 500 = 0 THEN 'Eine Zahlungserinnerung ' ELSE 'Ein Brief ' END || repeat('Wort' || (i % 97) || ' ', 30)
		FROM generate_series(1, 3000) i`, alice.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `
		CREATE SEQUENCE tsquery_calls;
		ALTER FUNCTION document_search_tsquery(bigint, text[], boolean[], boolean[], integer[])
		    RENAME TO document_search_tsquery_counted;
		CREATE FUNCTION document_search_tsquery(owner bigint, terms text[], is_phrase boolean[], negated boolean[], or_group integer[])
		RETURNS tsquery LANGUAGE plpgsql STABLE AS $$
		BEGIN
		    PERFORM nextval('tsquery_calls');
		    RETURN document_search_tsquery_counted(owner, terms, is_phrase, negated, or_group);
		END $$;
		ANALYZE documents;`); err != nil {
		t.Fatal(err)
	}
	calls := func() int64 {
		var n int64
		if err := conn.QueryRow(ctx, `SELECT CASE WHEN is_called THEN last_value ELSE 0 END FROM tsquery_calls`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// A second store whose sessions cannot use an index: every search
	// query is a sequential scan.
	if _, err := conn.Exec(ctx, `DO $$ BEGIN
		EXECUTE format('ALTER DATABASE %I SET enable_indexscan = off', current_database());
		EXECUTE format('ALTER DATABASE %I SET enable_bitmapscan = off', current_database());
		END $$`); err != nil {
		t.Fatal(err)
	}
	seq, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer seq.Close()
	var recorded []struct {
		sql  string
		args []any
	}
	store.RecordSearchSQL(seq, func(sql string, args []any) {
		recorded = append(recorded, struct {
			sql  string
			args []any
		}{sql, args})
	})
	for _, s := range []search.Sort{search.SortRelevance, search.SortAdded, search.SortDate} {
		for _, asc := range []bool{false, true} {
			if s == search.SortRelevance && asc {
				continue
			}
			before := calls()
			q := parseQuery(t, "Brief OR Zahlungserinnerung", nil)
			q.Sort, q.Ascending, q.Limit = s, asc, 10
			page := runSearch(t, seq, alice, q)
			if len(page.Hits) != 10 || page.NextCursor == "" {
				t.Fatalf("%s: %d hits", s, len(page.Hits))
			}
			q.Cursor = page.NextCursor
			runSearch(t, seq, alice, q)
			if got := calls() - before; got != 2 {
				t.Errorf("sort %s asc=%v: %d calls of document_search_tsquery for two pages, want 2", s, asc, got)
			}
		}
	}

	// What the store runs, explained on a session that may use indexes:
	// a selective term goes through documents_search_idx.
	recorded = nil
	page := runSearch(t, seq, alice, parseQuery(t, "Zahlungserinnerung", nil))
	if len(page.Hits) != 6 {
		t.Fatalf("selective term: %d hits", len(page.Hits))
	}
	q := parseQuery(t, "Zahlungserinnerung", nil)
	q.Sort = search.SortDate
	runSearch(t, seq, alice, q)
	plain := 0
	for _, r := range recorded {
		if strings.Contains(r.sql, "document_search_tsquery") {
			if !strings.Contains(r.sql, "name: SearchTsquery") {
				t.Errorf("a search query calls document_search_tsquery:\n%s", r.sql)
			}
			continue
		}
		plain++
		// A session that may use indexes, planned with the values as the
		// store's unnamed statements are.
		explain := connect(t, url)
		if _, err := explain.Exec(ctx, `SET enable_indexscan = on; SET enable_bitmapscan = on`); err != nil {
			t.Fatal(err)
		}
		rows, err := explain.Query(ctx, "EXPLAIN "+r.sql, append([]any{pgx.QueryExecModeCacheDescribe}, r.args...)...)
		if err != nil {
			t.Fatal(err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		plan := strings.Join(lines, "\n")
		if !strings.Contains(plan, "documents_search_idx") {
			t.Errorf("the full-text condition does not use the index:\n%s", plan)
		}
	}
	if plain != 2 {
		t.Fatalf("recorded %d search queries, want 2", plain)
	}
}

// A deadlock with SQL outside the store gives ErrRetry: the owner lock
// keeps store writers apart, not raw SQL. The test holds a lock on the
// document's tag row, lets PatchDocument (replacing the tags) lock the
// document and wait for it, then waits for the document itself:
// PostgreSQL aborts the store's transaction, whose deadlock check runs
// first.
func TestDeadlockWithSQLOutsideTheStoreIsRetry(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	tag := mustTag(t, st, alice, "Steuer", nil)
	in := newDoc("doc")
	in.TagIDs = []string{tag.ID}
	doc := mustDoc(t, st, alice, in)

	conn := connect(t, url)
	if _, err := conn.Exec(ctx, `SET deadlock_timeout = '1h'`); err != nil {
		t.Skipf("cannot raise deadlock_timeout (needs superuser): %v", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var pid int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM document_tags WHERE document_id = $1 FOR UPDATE`, doc.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := st.PatchDocument(ctx, alice, doc.ID, store.DocumentPatch{SetTags: true})
		done <- err
	}()
	// Wait until the store's transaction waits for the test's lock.
	watch := connect(t, url)
	for {
		var waiting bool
		if err := watch.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE $1 = ANY (pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("PatchDocument returned without waiting: %v", err)
		default:
		}
	}
	// Now wait for the document the store holds; the store's check finds
	// the cycle and aborts its transaction, which releases the row.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM documents WHERE id = $1 FOR UPDATE`, doc.ID); err != nil {
		t.Fatalf("the test's transaction: %v", err)
	}
	if err := <-done; !errors.Is(err, store.ErrRetry) {
		t.Fatalf("PatchDocument: %v, want ErrRetry", err)
	}
}

// rawCursor encodes a cursor's JSON as the store does.
func rawCursor(js string) string { return base64.RawURLEncoding.EncodeToString([]byte(js)) }

// Name resolution stays in the scope on its own (#64 review): a name or a
// tag ID of another user resolves to nothing, not only because the search
// query checks the owner again.
func TestSearchFilterIDsOwner(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	admin := store.Scope{UserID: storetest.User(t, st, "root", true).ID, All: true}
	type ids struct{ tag, child, corr, typ string }
	newMeta := func(sc store.Scope) ids {
		tag, err := st.CreateTag(ctx, sc, store.TagInput{Name: "Steuer"})
		if err != nil {
			t.Fatal(err)
		}
		child, err := st.CreateTag(ctx, sc, store.TagInput{Name: "Kfz", ParentID: &tag.ID})
		if err != nil {
			t.Fatal(err)
		}
		corr, err := st.CreateCorrespondent(ctx, sc, store.CorrespondentInput{Name: "Telekom"})
		if err != nil {
			t.Fatal(err)
		}
		typ, err := st.CreateDocumentType(ctx, sc, store.DocumentTypeInput{Name: "Rechnung"})
		if err != nil {
			t.Fatal(err)
		}
		return ids{tag.ID, child.ID, corr.ID, typ.ID}
	}
	a, b := newMeta(alice), newMeta(bob)
	sorted := func(s ...string) []string { slices.Sort(s); return s }

	q := parseQuery(t, "tag:steuer -tag:Kfz correspondent:TELEKOM type:Rechnung", nil)
	tags, notTags, corr, types, none, err := store.SearchFilterIDs(ctx, st, alice, q)
	if err != nil || none {
		t.Fatalf("alice: none=%v, %v", none, err)
	}
	if !slices.Equal(sorted(tags...), sorted(a.tag, a.child)) || !slices.Equal(notTags, []string{a.child}) ||
		!slices.Equal(corr, []string{a.corr}) || !slices.Equal(types, []string{a.typ}) {
		t.Errorf("alice resolves tags %v, not %v, correspondents %v, types %v; want only her own %+v (bob: %+v)", tags, notTags, corr, types, a, b)
	}

	// Bob's tag by ID: nothing of alice's matches it.
	byID := search.Query{TagIDs: []string{b.tag}}
	if _, _, _, _, none, err := store.SearchFilterIDs(ctx, st, alice, byID); err != nil || !none {
		t.Errorf("bob's tag ID for alice: none=%v, %v", none, err)
	}

	// An admin's names resolve in every owner's metadata.
	tags, _, corr, types, none, err = store.SearchFilterIDs(ctx, st, admin, q)
	if err != nil || none {
		t.Fatalf("admin: none=%v, %v", none, err)
	}
	if !slices.Equal(sorted(tags...), sorted(a.tag, a.child, b.tag, b.child)) || !slices.Equal(sorted(corr...), sorted(a.corr, b.corr)) ||
		!slices.Equal(sorted(types...), sorted(a.typ, b.typ)) {
		t.Errorf("admin resolves tags %v, correspondents %v, types %v", tags, corr, types)
	}
}

// A search query that runs longer than SearchTimeout fails with
// ErrSearchTooBroad, in every sort; a cancelled context stays the
// context's error. The test holds a lock on documents, so the search
// waits for it until its statement_timeout (or the cancel) ends it.
func TestSearchTimeout(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	textDoc(t, st, alice, "doc", "Stromrechnung")
	lock := connect(t, url)
	tx, err := lock.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE documents IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	st.SearchTimeout = 50 * time.Millisecond
	for _, sort := range []search.Sort{search.SortRelevance, search.SortAdded, search.SortDate} {
		for _, asc := range []bool{false, true} {
			if sort == search.SortRelevance && asc {
				continue
			}
			q := parseQuery(t, "rechnung", nil)
			q.Sort, q.Ascending = sort, asc
			if _, err := st.SearchDocuments(ctx, alice, q); !errors.Is(err, store.ErrSearchTooBroad) {
				t.Errorf("sort %s asc=%v: %v, want ErrSearchTooBroad", sort, asc, err)
			}
		}
	}

	// A cancel while the search waits is the context's error.
	st.SearchTimeout = time.Hour
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := st.SearchDocuments(cctx, alice, parseQuery(t, "rechnung", nil))
		done <- err
	}()
	watch := connect(t, url)
	for {
		var waiting bool
		if err := watch.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%SearchDocumentsRelevance%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
	}
	cancel()
	if err := <-done; errors.Is(err, store.ErrSearchTooBroad) || !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled search: %v, want context.Canceled", err)
	}

	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	st.SearchTimeout = store.DefaultSearchTimeout
	wantFound(t, st, alice, "rechnung", "doc")
}
