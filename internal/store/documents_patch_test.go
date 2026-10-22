package store_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/search"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

func mustDocType(t *testing.T, st *store.Store, sc store.Scope, name string) *store.DocumentType {
	t.Helper()
	d, err := st.CreateDocumentType(context.Background(), sc, store.DocumentTypeInput{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func getDoc(t *testing.T, st *store.Store, id string) *store.Document {
	t.Helper()
	d, err := st.GetDocument(context.Background(), store.Scope{All: true}, id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func sortedTags(d *store.Document) []string {
	out := slices.Clone(d.TagIDs)
	slices.Sort(out)
	return out
}

func ids(tags ...*store.Tag) []string {
	out := make([]string, len(tags))
	for i, t := range tags {
		out[i] = t.ID
	}
	slices.Sort(out)
	return out
}

// A patch of one field changes only that field; the others, the text and
// the archive stay. null clears ASN, correspondent and type; tags are
// replaced; modified_at moves.
func TestPatchDocumentSetsOnlyWhatIsSent(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	corr := mustCorrespondent(t, st, alice, "Bank")
	typ := mustDocType(t, st, alice, "Invoice")
	a, b, c := mustTag(t, st, alice, "A", nil), mustTag(t, st, alice, "B", nil), mustTag(t, st, alice, "C", nil)
	in := newDoc("patch me")
	in.Notes, in.ExtractedText, in.TextSource = "old notes", "the text", "PDF-Text"
	in.ArchiveSHA256, in.ArchiveSize = ptr(hashOf("archive")), ptr(int64(7))
	in.ArchiveSerialNumber, in.CorrespondentID, in.DocumentTypeID = ptr(int32(12)), &corr.ID, &typ.ID
	in.TagIDs = []string{a.ID, b.ID}
	doc := mustDoc(t, st, alice, in)

	got, err := st.PatchDocument(ctx, alice, doc.ID, store.DocumentPatch{Title: ptr("New title")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "New title" || got.Notes != "old notes" || got.ExtractedText != "the text" || got.TextSource != "PDF-Text" ||
		got.ArchiveSHA256 == nil || *got.ArchiveSHA256 != hashOf("archive") || *got.ArchiveSerialNumber != 12 ||
		*got.CorrespondentID != corr.ID || *got.DocumentTypeID != typ.ID || !got.IsInbox || got.IsFavorite ||
		!slices.Equal(sortedTags(got), ids(a, b)) || !got.DocumentDate.Equal(doc.DocumentDate) {
		t.Errorf("title patch changed more: %+v", got)
	}
	if !got.ModifiedAt.After(doc.ModifiedAt) {
		t.Errorf("modified_at %v not after %v", got.ModifiedAt, doc.ModifiedAt)
	}

	date := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	got, err = st.PatchDocument(ctx, alice, doc.ID, store.DocumentPatch{
		DocumentDate: &date, Notes: ptr(""), IsFavorite: ptr(true), IsInbox: ptr(false),
		SetASN: true, SetCorrespondent: true, SetDocumentType: true, SetTags: true, TagIDs: []string{c.ID, c.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "New title" || !got.DocumentDate.Equal(date) || got.Notes != "" || !got.IsFavorite || got.IsInbox ||
		got.ArchiveSerialNumber != nil || got.CorrespondentID != nil || got.DocumentTypeID != nil ||
		!slices.Equal(got.TagIDs, []string{c.ID}) || got.ExtractedText != "the text" {
		t.Errorf("second patch: %+v", got)
	}
	// An empty tag list removes all tags; links set again.
	got, err = st.PatchDocument(ctx, alice, doc.ID, store.DocumentPatch{
		SetTags: true, TagIDs: []string{}, SetCorrespondent: true, CorrespondentID: &corr.ID, SetASN: true, ArchiveSerialNumber: ptr(int32(3)),
	})
	if err != nil || len(got.TagIDs) != 0 || got.CorrespondentID == nil || *got.ArchiveSerialNumber != 3 {
		t.Errorf("third patch: %+v, %v", got, err)
	}
}

// After a change of the title or the tags, a search finds the document by
// the new values and no longer by the old ones.
func TestPatchDocumentSearchFollows(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	oldTag, newTag := mustTag(t, st, alice, "Alt", nil), mustTag(t, st, alice, "Neu", nil)
	in := newDoc("Stromrechnung")
	in.OriginalFileName = "scan.pdf"
	in.TagIDs = []string{oldTag.ID}
	doc := mustDoc(t, st, alice, in)
	found := func(q string) bool {
		t.Helper()
		parsed, err := search.Parse(q, nil)
		if err != nil {
			t.Fatal(err)
		}
		return len(runSearch(t, st, alice, parsed).Hits) == 1
	}
	if !found("stromrechnung") || !found("tag:Alt") {
		t.Fatal("not found before the patch")
	}
	if _, err := st.PatchDocument(ctx, alice, doc.ID, store.DocumentPatch{
		Title: ptr("Mietvertrag"), SetTags: true, TagIDs: []string{newTag.ID},
	}); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]bool{"stromrechnung": false, "tag:Alt": false, "mietvertrag": true, "tag:Neu": true} {
		if found(q) != want {
			t.Errorf("after the patch, %q found: %v, want %v", q, !want, want)
		}
	}
}

func TestPatchDocumentErrors(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	admin := store.Scope{UserID: storetest.User(t, st, "root", true).ID, All: true}
	aliceTag := mustTag(t, st, alice, "Alice's", nil)
	bobTag := mustTag(t, st, bob, "Bob's", nil)
	bobCorr := mustCorrespondent(t, st, bob, "Bob's bank")
	in := newDoc("alice's")
	in.ArchiveSerialNumber = ptr(int32(5))
	holder := mustDoc(t, st, alice, in)
	doc := mustDoc(t, st, alice, newDoc("another"))
	bobDoc := mustDoc(t, st, bob, newDoc("bob's"))

	cases := []struct {
		name string
		sc   store.Scope
		id   string
		p    store.DocumentPatch
		want error
	}{
		{"another owner's tag", alice, doc.ID, store.DocumentPatch{SetTags: true, TagIDs: []string{bobTag.ID}}, store.ErrOwnerMismatch},
		{"admin: alice's tag on bob's document", admin, bobDoc.ID, store.DocumentPatch{SetTags: true, TagIDs: []string{aliceTag.ID}}, store.ErrOwnerMismatch},
		{"another owner's correspondent", alice, doc.ID, store.DocumentPatch{SetCorrespondent: true, CorrespondentID: &bobCorr.ID}, store.ErrOwnerMismatch},
		{"unknown type", alice, doc.ID, store.DocumentPatch{SetDocumentType: true, DocumentTypeID: ptr("00000000-0000-0000-0000-000000000009")}, store.ErrOwnerMismatch},
		{"malformed tag", alice, doc.ID, store.DocumentPatch{SetTags: true, TagIDs: []string{"x"}}, store.ErrOwnerMismatch},
		{"taken ASN", alice, doc.ID, store.DocumentPatch{SetASN: true, ArchiveSerialNumber: ptr(int32(5))}, store.ErrASNTaken},
		{"zero ASN", alice, doc.ID, store.DocumentPatch{SetASN: true, ArchiveSerialNumber: ptr(int32(0))}, store.ErrInvalid},
		{"title too long", alice, doc.ID, store.DocumentPatch{Title: ptr(strings.Repeat("ä", store.MaxTitleLength+1))}, store.ErrInvalid},
		{"notes too long", alice, doc.ID, store.DocumentPatch{Notes: ptr(strings.Repeat("x", store.MaxNotesLength+1))}, store.ErrInvalid},
		{"NUL in notes", alice, doc.ID, store.DocumentPatch{Notes: ptr("a\x00b")}, store.ErrInvalid},
		{"NUL in title", alice, doc.ID, store.DocumentPatch{Title: ptr("a\x00b")}, store.ErrInvalid},
		{"line break in title", alice, doc.ID, store.DocumentPatch{Title: ptr("a\nb")}, store.ErrInvalid},
		{"tab in title", alice, doc.ID, store.DocumentPatch{Title: ptr("a\tb")}, store.ErrInvalid},
		{"C1 control in title", alice, doc.ID, store.DocumentPatch{Title: ptr("a\u0085b")}, store.ErrInvalid},
		{"escape in notes", alice, doc.ID, store.DocumentPatch{Notes: ptr("a\x1bb")}, store.ErrInvalid},
		{"nothing to change", alice, doc.ID, store.DocumentPatch{}, store.ErrInvalid},
		{"another user's document", bob, doc.ID, store.DocumentPatch{Title: ptr("mine")}, store.ErrNotFound},
		{"malformed id", alice, "x", store.DocumentPatch{Title: ptr("mine")}, store.ErrNotFound},
	}
	for _, c := range cases {
		if _, err := st.PatchDocument(ctx, c.sc, c.id, c.p); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	_, err := st.PatchDocument(ctx, alice, doc.ID, store.DocumentPatch{Title: ptr("x"), SetASN: true, ArchiveSerialNumber: ptr(int32(5))})
	var taken *store.ASNTakenError
	if !errors.As(err, &taken) || taken.DocumentID != holder.ID {
		t.Errorf("taken ASN: %v, want the holder %s", err, holder.ID)
	}
	if got := getDoc(t, st, doc.ID); got.Title != "another" || len(got.TagIDs) != 0 || got.ArchiveSerialNumber != nil {
		t.Errorf("a refused patch changed the document: %+v", got)
	}
	// Titles at the limit, and empty ones, are fine; so is the same ASN
	// for another owner.
	if _, err := st.PatchDocument(ctx, alice, doc.ID, store.DocumentPatch{Title: ptr(strings.Repeat("ä", store.MaxTitleLength))}); err != nil {
		t.Errorf("title at the limit: %v", err)
	}
	if _, err := st.PatchDocument(ctx, alice, doc.ID, store.DocumentPatch{Title: ptr("")}); err != nil {
		t.Errorf("empty title: %v", err)
	}
	// Notes keep their lines and tabs.
	if _, err := st.PatchDocument(ctx, alice, doc.ID, store.DocumentPatch{Notes: ptr("line\r\n\tindented\nend")}); err != nil {
		t.Errorf("notes with line breaks and tabs: %v", err)
	}
	if _, err := st.PatchDocument(ctx, bob, bobDoc.ID, store.DocumentPatch{SetASN: true, ArchiveSerialNumber: ptr(int32(5))}); err != nil {
		t.Errorf("same ASN for another owner: %v", err)
	}
	// An admin edits bob's document with bob's tag; the owner stays.
	got, err := st.PatchDocument(ctx, admin, bobDoc.ID, store.DocumentPatch{SetTags: true, TagIDs: []string{bobTag.ID}})
	if err != nil || got.OwnerID != bob.UserID || !slices.Equal(got.TagIDs, []string{bobTag.ID}) {
		t.Errorf("admin patch: %+v, %v", got, err)
	}
}

// Processing writes only its own columns: a patch, then a finished
// processing run, keeps title, notes and tags.
func TestProcessingKeepsEdits(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	tag := mustTag(t, st, alice, "Steuer", nil)
	doc := mustDoc(t, st, alice, newDoc("scan-001")) // queued for processing
	if _, err := st.PatchDocument(ctx, alice, doc.ID, store.DocumentPatch{
		Title: ptr("Steuerbescheid"), Notes: ptr("checked"), SetTags: true, TagIDs: []string{tag.ID},
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	j := claim(t, st, now)
	if err := st.CompleteJob(ctx, j, now, store.ProcessResult{
		PageCount: 2, Text: "Bescheid", TextSource: "Tesseract", ArchiveSHA256: hashOf("archive"), ArchiveSize: 9,
	}); err != nil {
		t.Fatal(err)
	}
	got := getDoc(t, st, doc.ID)
	if got.Title != "Steuerbescheid" || got.Notes != "checked" || !slices.Equal(got.TagIDs, []string{tag.ID}) ||
		got.ExtractedText != "Bescheid" || got.PageCount != 2 {
		t.Errorf("after processing: %+v", got)
	}
}

// bulk runs a bulk change as the UI does: by search, a dry run first
// whose fingerprint the change sends (unless sel has one).
func bulk(t *testing.T, st *store.Store, sc store.Scope, sel store.BulkSelection, c store.BulkChange) ([]string, error) {
	t.Helper()
	if sel.Match != nil && sel.Fingerprint == "" {
		sel = confirmed(t, st, sc, sel)
	}
	return st.BulkUpdateDocuments(context.Background(), sc, sel, c, "document.update")
}

// confirmed is sel with the fingerprint of its dry run, the set a user
// confirmed.
func confirmed(t *testing.T, st *store.Store, sc store.Scope, sel store.BulkSelection) store.BulkSelection {
	t.Helper()
	m, err := st.CountBulkMatches(context.Background(), sc, sel)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if m.Fingerprint == "" {
		t.Fatalf("dry run without fingerprint: %+v", m)
	}
	sel.Fingerprint = m.Fingerprint
	return sel
}

func TestBulkUpdateByIDs(t *testing.T) {
	st := storetest.New(t)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	admin := store.Scope{UserID: storetest.User(t, st, "root", true).ID, All: true}
	a, b := mustTag(t, st, alice, "A", nil), mustTag(t, st, alice, "B", nil)
	corr := mustCorrespondent(t, st, alice, "Bank")
	typ := mustDocType(t, st, alice, "Invoice")
	bobTag := mustTag(t, st, bob, "Bob", nil)
	var docs []*store.Document
	for i := range 3 {
		in := newDoc(fmt.Sprint("doc ", i))
		if i == 0 {
			in.TagIDs = []string{a.ID}
		}
		docs = append(docs, mustDoc(t, st, alice, in))
	}
	all := []string{docs[0].ID, docs[1].ID, docs[2].ID}
	bobDoc := mustDoc(t, st, bob, newDoc("bob's"))

	// Adding a tag one of them has: no error, no duplicate; repeated IDs
	// count once.
	got, err := bulk(t, st, alice, store.BulkSelection{IDs: append(slices.Clone(all), docs[0].ID)},
		store.BulkChange{AddTags: []string{a.ID, b.ID}, SetCorrespondent: true, CorrespondentID: &corr.ID,
			SetDocumentType: true, DocumentTypeID: &typ.ID, IsInbox: ptr(false), IsFavorite: ptr(true)})
	if err != nil || len(got) != 3 {
		t.Fatalf("bulk add: %v, %v", got, err)
	}
	for i, d := range docs {
		g := getDoc(t, st, d.ID)
		if !slices.Equal(sortedTags(g), ids(a, b)) || *g.CorrespondentID != corr.ID || *g.DocumentTypeID != typ.ID ||
			g.IsInbox || !g.IsFavorite || !g.ModifiedAt.After(d.ModifiedAt) {
			t.Errorf("doc %d after the bulk change: %+v", i, g)
		}
	}
	// Removing a tag, also from documents without it; clearing the links.
	if _, err := bulk(t, st, alice, store.BulkSelection{IDs: all[:2]}, store.BulkChange{RemoveTags: []string{a.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := bulk(t, st, alice, store.BulkSelection{IDs: all[:2]}, store.BulkChange{RemoveTags: []string{a.ID}, SetCorrespondent: true, SetDocumentType: true}); err != nil {
		t.Errorf("removing a missing tag: %v", err)
	}
	if g := getDoc(t, st, docs[0].ID); !slices.Equal(g.TagIDs, []string{b.ID}) || g.CorrespondentID != nil || g.DocumentTypeID != nil {
		t.Errorf("after remove and clear: %+v", g)
	}
	if g := getDoc(t, st, docs[2].ID); !slices.Equal(sortedTags(g), ids(a, b)) || g.CorrespondentID == nil {
		t.Errorf("an unselected document changed: %+v", g)
	}

	many := make([]string, store.MaxBulkIDs+1)
	for i := range many {
		many[i] = fmt.Sprintf("00000000-0000-0000-0000-%012d", i)
	}
	before := getDoc(t, st, docs[1].ID)
	for _, c := range []struct {
		name string
		sc   store.Scope
		sel  store.BulkSelection
		ch   store.BulkChange
		want error
	}{
		{"501 IDs", alice, store.BulkSelection{IDs: many}, store.BulkChange{IsInbox: ptr(true)}, store.ErrTooMany},
		{"empty list", alice, store.BulkSelection{IDs: []string{}}, store.BulkChange{IsInbox: ptr(true)}, store.ErrInvalid},
		{"no selection", alice, store.BulkSelection{}, store.BulkChange{IsInbox: ptr(true)}, store.ErrInvalid},
		{"nothing to change", alice, store.BulkSelection{IDs: all}, store.BulkChange{}, store.ErrInvalid},
		{"add and remove", alice, store.BulkSelection{IDs: all}, store.BulkChange{AddTags: []string{a.ID}, RemoveTags: []string{a.ID}}, store.ErrInvalid},
		{"one foreign ID", alice, store.BulkSelection{IDs: []string{docs[1].ID, bobDoc.ID}}, store.BulkChange{IsInbox: ptr(true)}, store.ErrNotFound},
		{"malformed ID", alice, store.BulkSelection{IDs: []string{docs[1].ID, "x"}}, store.BulkChange{IsInbox: ptr(true)}, store.ErrNotFound},
		{"another owner's tag", alice, store.BulkSelection{IDs: all}, store.BulkChange{AddTags: []string{bobTag.ID}}, store.ErrOwnerMismatch},
		{"several owners, tags", admin, store.BulkSelection{IDs: []string{docs[1].ID, bobDoc.ID}}, store.BulkChange{AddTags: []string{a.ID}}, store.ErrSeveralOwners},
		{"several owners, correspondent", admin, store.BulkSelection{IDs: []string{docs[1].ID, bobDoc.ID}}, store.BulkChange{SetCorrespondent: true, CorrespondentID: &corr.ID}, store.ErrSeveralOwners},
		{"admin: alice's tag on bob's", admin, store.BulkSelection{IDs: []string{bobDoc.ID}}, store.BulkChange{AddTags: []string{a.ID}}, store.ErrOwnerMismatch},
	} {
		if _, err := bulk(t, st, c.sc, c.sel, c.ch); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	if g := getDoc(t, st, docs[1].ID); !g.ModifiedAt.Equal(before.ModifiedAt) || g.IsInbox {
		t.Errorf("a refused bulk change changed a document: %+v", g)
	}
	// Removing another owner's tag is a no-op: no document has it.
	if _, err := bulk(t, st, alice, store.BulkSelection{IDs: all}, store.BulkChange{RemoveTags: []string{bobTag.ID}}); err != nil {
		t.Errorf("removing another owner's tag: %v", err)
	}
	// Inbox and favourite work across owners, and so does clearing.
	if got, err := bulk(t, st, admin, store.BulkSelection{IDs: []string{docs[1].ID, bobDoc.ID}},
		store.BulkChange{IsInbox: ptr(true), SetCorrespondent: true}); err != nil || len(got) != 2 {
		t.Errorf("admin inbox across owners: %v, %v", got, err)
	}
	if g := getDoc(t, st, bobDoc.ID); !g.IsInbox || g.OwnerID != bob.UserID {
		t.Errorf("bob's document: %+v", g)
	}
}

// insertDocs adds n documents of owner directly, faster than one
// CreateDocument each.
func insertDocs(t *testing.T, conn *pgx.Conn, owner int64, n int, title string) {
	t.Helper()
	if _, err := conn.Exec(context.Background(), `
		INSERT INTO documents (owner_id, title, source, original_sha256, original_size, original_mime, is_inbox)
		SELECT $1, $3, 'file', encode(sha256(($4 || '-' || $3 || '-' || g)::bytea), 'hex'), 1, 'application/pdf', true
		FROM generate_series(1, $2) AS g`, owner, n, title, fmt.Sprint(owner)); err != nil {
		t.Fatal(err)
	}
}

// A bulk change by search resolves the query on the server with the
// search's filters and the scope's visibility.
func TestBulkUpdateByMatch(t *testing.T) {
	st, dbURL := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	root := storetest.User(t, st, "root", true).ID
	admin := store.Scope{UserID: root, All: true}
	adminOwn := store.Scope{UserID: root}
	steuer := mustTag(t, st, alice, "Steuer", nil)
	in := newDoc("Rechnung Strom")
	in.TagIDs = []string{steuer.ID}
	strom := mustDoc(t, st, alice, in)
	gas := mustDoc(t, st, alice, newDoc("Rechnung Gas"))
	vertrag := mustDoc(t, st, alice, newDoc("Vertrag"))
	bobs := mustDoc(t, st, bob, newDoc("Rechnung Bob"))
	roots := mustDoc(t, st, adminOwn, newDoc("Rechnung Root"))

	parse := func(s string) *search.Query {
		q, err := search.Parse(s, nil)
		if err != nil {
			t.Fatal(err)
		}
		return &q
	}
	favorites := func(sc store.Scope, q *search.Query) []string {
		t.Helper()
		changed, err := bulk(t, st, sc, store.BulkSelection{Match: q}, store.BulkChange{IsFavorite: ptr(true)})
		if err != nil {
			t.Fatal(err)
		}
		// Reset for the next case.
		if len(changed) > 0 {
			if _, err := bulk(t, st, store.Scope{UserID: root, All: true}, store.BulkSelection{IDs: changed}, store.BulkChange{IsFavorite: ptr(false)}); err != nil {
				t.Fatal(err)
			}
		}
		slices.Sort(changed)
		return changed
	}
	sorted := func(docs ...*store.Document) []string {
		out := []string{}
		for _, d := range docs {
			out = append(out, d.ID)
		}
		slices.Sort(out)
		return out
	}
	for _, c := range []struct {
		name string
		sc   store.Scope
		q    *search.Query
		want []string
	}{
		{"text, alice", alice, parse("rechnung"), sorted(strom, gas)},
		{"text and tag filter", alice, parse("rechnung tag:Steuer"), sorted(strom)},
		{"tag ID", alice, &search.Query{TagIDs: []string{steuer.ID}}, sorted(strom)},
		{"inbox filter", alice, &search.Query{Filters: []search.Filter{{Field: search.FieldIs, Values: []string{search.IsInbox}}}}, sorted(strom, gas, vertrag)},
		{"bob sees only his", bob, parse("rechnung"), sorted(bobs)},
		{"admin, everyone's", admin, parse("rechnung"), sorted(strom, gas, bobs, roots)},
		{"admin, own only", adminOwn, parse("rechnung"), sorted(roots)},
		{"empty match", alice, parse("nichtda"), []string{}},
		{"unknown tag name", alice, parse("tag:Unbekannt"), []string{}},
	} {
		if got := favorites(c.sc, c.q); !slices.Equal(got, c.want) {
			t.Errorf("%s: changed %v, want %v", c.name, got, c.want)
		}
	}
	// Several owners in the match: tags refused, inbox fine.
	if _, err := bulk(t, st, admin, store.BulkSelection{Match: parse("rechnung")}, store.BulkChange{AddTags: []string{steuer.ID}}); !errors.Is(err, store.ErrSeveralOwners) {
		t.Errorf("several owners: %v", err)
	}
	// The dry run counts what the change would cover.
	if m, err := st.CountBulkMatches(ctx, admin, store.BulkSelection{Match: parse("rechnung")}); err != nil || m.Count != 4 || m.Owners != 3 {
		t.Errorf("dry run: %+v, %v", m, err)
	}
	if m, err := st.CountBulkMatches(ctx, alice, store.BulkSelection{Match: parse("nichtda")}); err != nil || m.Count != 0 {
		t.Errorf("dry run without matches: %+v, %v", m, err)
	}

	// The limit: one match too many refuses the whole change.
	conn := connect(t, dbURL)
	insertDocs(t, conn, bob.UserID, store.MaxBulkMatches, "Massenbrief")
	atLimit, err := st.CountBulkMatches(ctx, bob, store.BulkSelection{Match: parse("massenbrief")})
	if err != nil || atLimit.Count != store.MaxBulkMatches || atLimit.Owners != 1 || atLimit.Fingerprint == "" {
		t.Errorf("dry run at the limit: %+v, %v", atLimit, err)
	}
	if got, err := bulk(t, st, bob, store.BulkSelection{Match: parse("massenbrief")}, store.BulkChange{IsInbox: ptr(false)}); err != nil || len(got) != store.MaxBulkMatches {
		t.Errorf("at the limit: %d, %v", len(got), err)
	}
	insertDocs(t, conn, bob.UserID, 1, "Massenbrief extra")
	// Above the limit with the (now stale) fingerprint of the dry run at
	// the limit: the limit is checked first.
	_, err = bulk(t, st, bob, store.BulkSelection{Match: parse("massenbrief"), Fingerprint: atLimit.Fingerprint}, store.BulkChange{IsInbox: ptr(true)})
	var tooMany *store.TooManyError
	if !errors.As(err, &tooMany) || tooMany.Count != store.MaxBulkMatches+1 || tooMany.Limit != store.MaxBulkMatches {
		t.Errorf("over the limit: %v", err)
	}
	if m, err := st.CountBulkMatches(ctx, bob, store.BulkSelection{Match: parse("massenbrief")}); err != nil || m.Count != store.MaxBulkMatches+1 || m.Owners != 0 || m.Fingerprint != "" {
		t.Errorf("dry run over the limit: %+v, %v", m, err)
	}
	var inbox int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM documents WHERE owner_id = $1 AND is_inbox`, bob.UserID).Scan(&inbox); err != nil || inbox != 2 {
		t.Errorf("the refused change changed documents: %d in the inbox, %v", inbox, err)
	}
}

// One summary entry per bulk change and one entry per document, with field
// names and no values; the owner's ID for another user's document.
func TestBulkUpdateAudit(t *testing.T) {
	st, dbURL := storetest.NewWithURL(t)
	conn := connect(t, dbURL)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	root := storetest.User(t, st, "root", true)
	admin := store.Scope{UserID: root.ID, All: true}
	corr := mustCorrespondent(t, st, alice, "Secret bank")
	d1 := mustDoc(t, st, alice, newDoc("Secret one"))
	d2 := mustDoc(t, st, alice, newDoc("Secret two"))
	before := len(auditRows(t, conn))

	e := &store.AuditEntry{Actor: store.ActorSession, Action: "document.bulk_update"}
	e.SetUser(root.ID, root.Name)
	e.Detail("fields", []string{"correspondent"})
	e.Detail("bulkRun", "run-1")
	ctx := store.WithAudit(context.Background(), e)
	if _, err := st.BulkUpdateDocuments(ctx, admin, store.BulkSelection{IDs: []string{d2.ID, d1.ID}},
		store.BulkChange{SetCorrespondent: true, CorrespondentID: &corr.ID}, "document.update"); err != nil {
		t.Fatal(err)
	}
	rows := auditRows(t, conn)[before:]
	if len(rows) != 3 {
		t.Fatalf("%d entries, want 3: %+v", len(rows), rows)
	}
	sum := rows[0]
	for _, r := range rows {
		if r.Action == "document.bulk_update" {
			sum = r
		}
	}
	if sum.Action != "document.bulk_update" || sum.TargetType != nil || fmt.Sprint(sum.Details["count"]) != "2" ||
		len(sum.Details["ids"].([]any)) != 2 {
		t.Errorf("summary: %+v", sum)
	}
	targets := []string{}
	for _, r := range rows {
		if r.Action != "document.update" {
			continue
		}
		if *r.TargetType != store.TargetDocument || r.TargetName != nil || *r.UserName != "root" ||
			fmt.Sprint(r.Details["fields"]) != "[correspondent]" || r.Details["bulkRun"] != "run-1" ||
			fmt.Sprint(r.Details["ownerId"]) != fmt.Sprint(alice.UserID) {
			t.Errorf("per-document entry: %+v", r)
		}
		targets = append(targets, *r.TargetID)
	}
	slices.Sort(targets)
	if want := []string{d1.ID, d2.ID}; !slices.Equal(targets, slices.Sorted(slices.Values(want))) {
		t.Errorf("targets %v", targets)
	}
	for _, r := range rows {
		if s := fmt.Sprint(r.Details); strings.Contains(s, "Secret") || strings.Contains(s, corr.ID) {
			t.Errorf("an entry holds a value: %v", s)
		}
	}
}

// openWith opens another store on the test database with extra connection
// parameters (a deadlock timeout, an application name).
func openWith(t *testing.T, dbURL string, params map[string]string) *store.Store {
	t.Helper()
	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
	s, err := store.Open(context.Background(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// waitForLock returns once a statement of application app waits for a
// lock, or fails when done delivers first or ctx ends.
func waitForLock(ctx context.Context, t *testing.T, conn *pgx.Conn, app string, done <-chan error) {
	t.Helper()
	for {
		var waiting bool
		err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND application_name = $1 AND wait_event_type = 'Lock')`, app).Scan(&waiting)
		if err == nil && waiting {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("%s finished without waiting for a lock: %v", app, err)
		case <-ctx.Done():
			t.Fatalf("%s never waited for a lock", app)
		default:
		}
	}
}

// Two bulk changes over overlapping sets lock in ID order, so the second
// waits for the first and both succeed.
func TestBulkUpdatesOverlapping(t *testing.T) {
	st, dbURL := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	a, b := mustTag(t, st, alice, "A", nil), mustTag(t, st, alice, "B", nil)
	var docs []string
	for i := range 4 {
		docs = append(docs, mustDoc(t, st, alice, newDoc(fmt.Sprint("doc ", i))).ID)
	}
	second := openWith(t, dbURL, map[string]string{"application_name": "second"})
	conn := connect(t, dbURL)

	locked, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		held := store.WithAfterLock(ctx, func() {
			close(locked)
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
		_, err := st.BulkUpdateDocuments(held, alice, store.BulkSelection{IDs: docs[:3]}, store.BulkChange{AddTags: []string{a.ID}}, "document.update")
		first <- err
	}()
	select {
	case <-locked:
	case err := <-first:
		t.Fatalf("first bulk change finished before it locked: %v", err)
	}
	other := make(chan error, 1)
	go func() {
		// The reverse order: the store sorts.
		rev := []string{docs[3], docs[2], docs[1]}
		_, err := second.BulkUpdateDocuments(ctx, alice, store.BulkSelection{IDs: rev}, store.BulkChange{AddTags: []string{b.ID}}, "document.update")
		other <- err
	}()
	waitForLock(ctx, t, conn, "second", other)
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := <-other; err != nil {
		t.Fatalf("second: %v", err)
	}
	for i, id := range docs {
		g := getDoc(t, st, id)
		want := map[int][]string{0: ids(a), 1: ids(a, b), 2: ids(a, b), 3: ids(b)}[i]
		if !slices.Equal(sortedTags(g), want) {
			t.Errorf("doc %d tags %v, want %v", i, g.TagIDs, want)
		}
	}
}

// heldChange is a change held at its first WithAfterLock call until
// release is closed; attempts counts the calls, one per attempt that got
// its locks.
type heldChange struct {
	attempts atomic.Int32
	release  chan struct{}
	done     chan error
}

// holdAfterLock runs change in the background with a WithAfterLock
// context and returns once its first attempt holds its locks.
func holdAfterLock(ctx context.Context, t *testing.T, name string, change func(context.Context) error) *heldChange {
	t.Helper()
	h := &heldChange{release: make(chan struct{}), done: make(chan error, 1)}
	locked := make(chan struct{})
	go func() {
		held := store.WithAfterLock(ctx, func() {
			if h.attempts.Add(1) > 1 {
				return
			}
			close(locked)
			select { // a failing test cancels ctx, so the pool can close
			case <-h.release:
			case <-ctx.Done():
			}
		})
		h.done <- change(held)
	}()
	select {
	case <-locked:
	case err := <-h.done:
		t.Fatalf("%s finished before it locked: %v", name, err)
	case <-ctx.Done():
		t.Fatalf("%s never locked", name)
	}
	return h
}

// countAfterLock runs change in the background with a WithAfterLock
// context that counts its attempts.
func countAfterLock(ctx context.Context, change func(context.Context) error) (*atomic.Int32, chan error) {
	var attempts atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- change(store.WithAfterLock(ctx, func() { attempts.Add(1) }))
	}()
	return &attempts, done
}

// lockOrderStores opens two stores for a lock-order test: winner detects
// deadlocks only after an hour, loser after 10 ms, so a deadlock always
// aborts loser's transaction and shows as a second attempt there; nothing
// waits for time.
func lockOrderStores(t *testing.T, dbURL, winner, loser string) (w, l *store.Store) {
	t.Helper()
	w = openWith(t, dbURL, map[string]string{"options": "-c deadlock_timeout=1h", "application_name": winner})
	l = openWith(t, dbURL, map[string]string{"options": "-c deadlock_timeout=10ms", "application_name": loser})
	return w, l
}

// A bulk change that holds its documents while DeleteTag of a tag it
// removes waits for them, then goes on, does not deadlock: the delete
// locks the tag's documents before it deletes its document_tags rows, so
// it waits for the bulk change and then finds the rows gone. Before #99
// the delete's cascade took those rows first and its trigger then waited
// for the documents while the bulk change waited for the rows: a
// deadlock, and the bulk change (10 ms) had a second attempt.
func TestBulkUpdateNoDeadlockWithDeleteTag(t *testing.T) {
	st, dbURL := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	tag := mustTag(t, st, alice, "Doomed", nil)
	corr := mustCorrespondent(t, st, alice, "Bank")
	var docs []string
	for i := range 3 {
		in := newDoc(fmt.Sprint("doc ", i))
		in.TagIDs = []string{tag.ID}
		docs = append(docs, mustDoc(t, st, alice, in).ID)
	}
	deleter, bulkStore := lockOrderStores(t, dbURL, "deleter", "bulk")
	conn := connect(t, dbURL)

	h := holdAfterLock(ctx, t, "bulk change", func(ctx context.Context) error {
		_, err := bulkStore.BulkUpdateDocuments(ctx, alice, store.BulkSelection{IDs: docs},
			store.BulkChange{RemoveTags: []string{tag.ID}, SetCorrespondent: true, CorrespondentID: &corr.ID}, "document.update")
		return err
	})
	deleted := make(chan error, 1)
	go func() { deleted <- deleter.DeleteTag(ctx, alice, tag.ID) }()
	waitForLock(ctx, t, conn, "deleter", deleted)
	close(h.release)
	if err := <-h.done; err != nil {
		t.Fatalf("bulk change: %v", err)
	}
	if err := <-deleted; err != nil {
		t.Fatalf("delete tag: %v", err)
	}
	if n := h.attempts.Load(); n != 1 {
		t.Errorf("%d attempts of the bulk change, want 1 (no deadlock)", n)
	}
	for _, id := range docs {
		if g := getDoc(t, st, id); len(g.TagIDs) != 0 || g.CorrespondentID == nil || *g.CorrespondentID != corr.ID {
			t.Errorf("after both: %+v", g)
		}
	}
	if _, err := st.GetTag(ctx, alice, tag.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("tag after the delete: %v", err)
	}
}

// A bulk change adding a tag and DeleteTag of that tag take their locks in
// one order: the tag first (the bulk change's key share before its
// documents, the delete's row lock before the tag's documents).
//   - Bulk first: the delete waits for the bulk change's key share, then
//     removes the tag from every document; one attempt each. Before #99
//     the bulk change's foreign-key check waited for the delete's row lock
//     while holding the documents the delete's trigger waited for: a
//     deadlock, and a retry with owner_mismatch.
//   - Delete first: the bulk change waits for the delete, finds the tag
//     gone (its key share skips the row) and fails with ErrOwnerMismatch,
//     changing nothing.
func TestBulkAddTagNoDeadlockWithDeleteTag(t *testing.T) {
	for _, bulkFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "bulk first", false: "delete first"}[bulkFirst], func(t *testing.T) {
			st, dbURL := storetest.NewWithURL(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
			tag := mustTag(t, st, alice, "Doomed", nil)
			var docs []string
			for i := range 4 {
				in := newDoc(fmt.Sprint("doc ", i))
				if i%2 == 0 {
					in.TagIDs = []string{tag.ID}
				}
				docs = append(docs, mustDoc(t, st, alice, in).ID)
			}
			deleter, bulkStore := lockOrderStores(t, dbURL, "deleter", "bulk")
			conn := connect(t, dbURL)
			change := func(ctx context.Context) error {
				_, err := bulkStore.BulkUpdateDocuments(ctx, alice, store.BulkSelection{IDs: docs},
					store.BulkChange{AddTags: []string{tag.ID}, IsFavorite: ptr(true)}, "document.update")
				return err
			}
			del := func(ctx context.Context) error { return deleter.DeleteTag(ctx, alice, tag.ID) }

			var bulkErr, delErr error
			var bulkAttempts, delAttempts int32
			if bulkFirst {
				h := holdAfterLock(ctx, t, "bulk change", change)
				n, deleted := countAfterLock(ctx, del)
				waitForLock(ctx, t, conn, "deleter", deleted)
				close(h.release)
				bulkErr, delErr = <-h.done, <-deleted
				bulkAttempts, delAttempts = h.attempts.Load(), n.Load()
			} else {
				h := holdAfterLock(ctx, t, "delete", del)
				n, changed := countAfterLock(ctx, change)
				waitForLock(ctx, t, conn, "bulk", changed)
				close(h.release)
				delErr, bulkErr = <-h.done, <-changed
				delAttempts, bulkAttempts = h.attempts.Load(), n.Load()
			}
			if delErr != nil || delAttempts != 1 {
				t.Errorf("delete: %v after %d attempts, want success after 1", delErr, delAttempts)
			}
			if bulkFirst {
				if bulkErr != nil || bulkAttempts != 1 {
					t.Errorf("bulk change: %v after %d attempts, want success after 1", bulkErr, bulkAttempts)
				}
			} else if !errors.Is(bulkErr, store.ErrOwnerMismatch) {
				t.Errorf("bulk change after the delete: %v, want ErrOwnerMismatch", bulkErr)
			}
			for _, id := range docs {
				if g := getDoc(t, st, id); len(g.TagIDs) != 0 || g.IsFavorite != bulkFirst {
					t.Errorf("after both: %+v", g)
				}
			}
		})
	}
}

// A bulk change setting a correspondent or document type and the delete
// of that row take their locks in one order. d1 already has the row, d2
// not. Bulk first: the delete waits for the bulk change's key share, then
// clears the row on both; before #99 the delete's SET NULL waited for d1
// while the bulk change's foreign-key check for d2 waited for the
// delete's row lock: a deadlock. Delete first: the bulk change waits,
// then fails with ErrOwnerMismatch and changes nothing.
func TestBulkSetLinkNoDeadlockWithDelete(t *testing.T) {
	for _, kind := range []string{"correspondent", "documentType"} {
		for _, bulkFirst := range []bool{true, false} {
			t.Run(kind+map[bool]string{true: ", bulk first", false: ", delete first"}[bulkFirst], func(t *testing.T) {
				st, dbURL := storetest.NewWithURL(t)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
				var id string
				link := func(in *store.NewDocument) { in.CorrespondentID = &id }
				c := store.BulkChange{SetCorrespondent: true, CorrespondentID: &id}
				linked := func(d *store.Document) bool { return d.CorrespondentID != nil }
				if kind == "correspondent" {
					id = mustCorrespondent(t, st, alice, "Bank").ID
				} else {
					id = mustDocType(t, st, alice, "Invoice").ID
					link = func(in *store.NewDocument) { in.DocumentTypeID = &id }
					c = store.BulkChange{SetDocumentType: true, DocumentTypeID: &id}
					linked = func(d *store.Document) bool { return d.DocumentTypeID != nil }
				}
				c.IsFavorite = ptr(true)
				in1 := newDoc("d1")
				link(&in1)
				docs := []string{mustDoc(t, st, alice, in1).ID, mustDoc(t, st, alice, newDoc("d2")).ID}
				deleter, bulkStore := lockOrderStores(t, dbURL, "deleter", "bulk")
				conn := connect(t, dbURL)
				change := func(ctx context.Context) error {
					_, err := bulkStore.BulkUpdateDocuments(ctx, alice, store.BulkSelection{IDs: docs}, c, "document.update")
					return err
				}
				del := func(ctx context.Context) error {
					if kind == "correspondent" {
						return deleter.DeleteCorrespondent(ctx, alice, id)
					}
					return deleter.DeleteDocumentType(ctx, alice, id)
				}

				var bulkErr, delErr error
				var bulkAttempts, delAttempts int32
				if bulkFirst {
					h := holdAfterLock(ctx, t, "bulk change", change)
					n, deleted := countAfterLock(ctx, del)
					waitForLock(ctx, t, conn, "deleter", deleted)
					close(h.release)
					bulkErr, delErr = <-h.done, <-deleted
					bulkAttempts, delAttempts = h.attempts.Load(), n.Load()
				} else {
					h := holdAfterLock(ctx, t, "delete", del)
					n, changed := countAfterLock(ctx, change)
					waitForLock(ctx, t, conn, "bulk", changed)
					close(h.release)
					delErr, bulkErr = <-h.done, <-changed
					delAttempts, bulkAttempts = h.attempts.Load(), n.Load()
				}
				if delErr != nil || delAttempts != 1 {
					t.Errorf("delete: %v after %d attempts, want success after 1", delErr, delAttempts)
				}
				if bulkFirst {
					if bulkErr != nil || bulkAttempts != 1 {
						t.Errorf("bulk change: %v after %d attempts, want success after 1", bulkErr, bulkAttempts)
					}
				} else if !errors.Is(bulkErr, store.ErrOwnerMismatch) {
					t.Errorf("bulk change after the delete: %v, want ErrOwnerMismatch", bulkErr)
				}
				for _, d := range docs {
					if g := getDoc(t, st, d); linked(g) || g.IsFavorite != bulkFirst {
						t.Errorf("after both: %+v", g)
					}
				}
			})
		}
	}
}

// A patch linking a document to a tag or correspondent takes its key share
// before the document's lock, so the row's delete waits for it, then
// removes the link: one attempt each, the patch succeeds.
//   - tagIds with T, the document already has T: before #99 the delete's
//     cascade took the document's document_tags row, its trigger waited
//     for the document, and the patch's ClearDocumentTags waited for the
//     row: a deadlock, then owner_mismatch on the retry.
//   - correspondentId C, the document already has C: the key is unchanged,
//     so the patch has no foreign-key check and there was no deadlock
//     before #99 either; the row checks the order and the end state.
//   - correspondentId C, the document has none: before #99 the delete did
//     not touch the document and finished without waiting, and the patch
//     then failed with owner_mismatch.
func TestPatchDocumentNoDeadlockWithDelete(t *testing.T) {
	for _, c := range []struct {
		name   string
		tag    bool // else a correspondent
		linked bool // the document has the row before the patch
	}{
		{"tagIds, document has the tag", true, true},
		{"correspondentId, document has it", false, true},
		{"correspondentId, document has none", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, dbURL := storetest.NewWithURL(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
			var id string
			var p store.DocumentPatch
			in := newDoc("doc")
			if c.tag {
				id = mustTag(t, st, alice, "T", nil).ID
				p = store.DocumentPatch{SetTags: true, TagIDs: []string{id}, Title: ptr("patched")}
				if c.linked {
					in.TagIDs = []string{id}
				}
			} else {
				id = mustCorrespondent(t, st, alice, "C").ID
				p = store.DocumentPatch{SetCorrespondent: true, CorrespondentID: &id, Title: ptr("patched")}
				if c.linked {
					in.CorrespondentID = &id
				}
			}
			doc := mustDoc(t, st, alice, in)
			deleter, patcher := lockOrderStores(t, dbURL, "deleter", "patcher")
			conn := connect(t, dbURL)

			h := holdAfterLock(ctx, t, "patch", func(ctx context.Context) error {
				_, err := patcher.PatchDocument(ctx, alice, doc.ID, p)
				return err
			})
			n, deleted := countAfterLock(ctx, func(ctx context.Context) error {
				if c.tag {
					return deleter.DeleteTag(ctx, alice, id)
				}
				return deleter.DeleteCorrespondent(ctx, alice, id)
			})
			waitForLock(ctx, t, conn, "deleter", deleted)
			close(h.release)
			if err := <-h.done; err != nil || h.attempts.Load() != 1 {
				t.Errorf("patch: %v after %d attempts, want success after 1", err, h.attempts.Load())
			}
			if err := <-deleted; err != nil || n.Load() != 1 {
				t.Errorf("delete: %v after %d attempts, want success after 1", err, n.Load())
			}
			if g := getDoc(t, st, doc.ID); g.Title != "patched" || len(g.TagIDs) != 0 || g.CorrespondentID != nil {
				t.Errorf("after both: %+v", g)
			}
		})
	}
}

// The recheck after the lock runs over the locked documents only: 5 001
// documents that start matching between the match and the lock, all with
// IDs below the locked ones, neither push the locked ones out of the
// recheck (before #99 it returned the first 5 001 matches in id order, so
// all 20 fell out and the change reported nothing, without an error) nor
// join the change.
func TestBulkUpdateByMatchRecheckKeepsLockedMatches(t *testing.T) {
	st, dbURL := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn := connect(t, dbURL)
	inserter := connect(t, dbURL)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	insertDocs(t, conn, alice.UserID, 20, "old")
	const low = "00000000-0000-4000-8000-000000005001" // the highest inserted ID
	var above bool
	if err := conn.QueryRow(ctx, `SELECT bool_and(id > $1::uuid) FROM documents`, low).Scan(&above); err != nil || !above {
		t.Fatalf("the documents' IDs are not all above the inserted ones: %v, %v", above, err)
	}
	inbox := &search.Query{Filters: []search.Filter{{Field: search.FieldIs, Values: []string{search.IsInbox}}}}
	before := len(auditRows(t, conn))

	var attempts atomic.Int32
	var insertErr error
	e := &store.AuditEntry{Actor: store.ActorCLI, Action: "document.bulk_update"}
	held := store.WithBeforeLock(store.WithAudit(ctx, e), func() {
		if attempts.Add(1) > 1 {
			return
		}
		// Committed before the lock, so the recheck sees them.
		_, insertErr = inserter.Exec(ctx, `
			INSERT INTO documents (id, owner_id, title, source, original_sha256, original_size, original_mime, is_inbox)
			SELECT ('00000000-0000-4000-8000-' || lpad(g::text, 12, '0'))::uuid, $1, 'new', 'file',
			       encode(sha256(('new-' || g)::bytea), 'hex'), 1, 'application/pdf', true
			FROM generate_series(1, 5001) AS g`, alice.UserID)
	})
	// The dry run's fingerprint names the 20; the change covers exactly
	// those.
	sel := confirmed(t, st, alice, store.BulkSelection{Match: inbox})
	changed, err := st.BulkUpdateDocuments(held, alice, sel, store.BulkChange{IsFavorite: ptr(true)}, "document.update")
	if insertErr != nil {
		t.Fatal(insertErr)
	}
	if err != nil || len(changed) != 20 || attempts.Load() != 1 {
		t.Fatalf("changed %d documents: %v after %d attempts, want 20", len(changed), err, attempts.Load())
	}
	var oldFav, newFav int
	if err := conn.QueryRow(ctx, `SELECT count(*) FILTER (WHERE title = 'old' AND is_favorite),
		count(*) FILTER (WHERE title = 'new' AND is_favorite) FROM documents`).Scan(&oldFav, &newFav); err != nil {
		t.Fatal(err)
	}
	if oldFav != 20 || newFav != 0 {
		t.Errorf("favourites: %d of the matched, %d of the new; want 20 and 0", oldFav, newFav)
	}
	summaries := 0
	for _, r := range auditRows(t, conn)[before:] {
		if r.Action == "document.bulk_update" {
			summaries++
			if fmt.Sprint(r.Details["count"]) != "20" {
				t.Errorf("summary: %+v", r.Details)
			}
		}
	}
	if summaries != 1 {
		t.Errorf("%d summaries, want 1", summaries)
	}
}

// A deadlock on every attempt gives ErrRetry after three attempts, and
// nothing changes.
func TestBulkUpdateDeadlockEveryAttempt(t *testing.T) {
	st, dbURL := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	doc := mustDoc(t, st, alice, newDoc("doc"))
	conn := connect(t, dbURL)
	if _, err := conn.Exec(ctx, `
		CREATE FUNCTION test_deadlock() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'test deadlock' USING ERRCODE = 'deadlock_detected'; END $$;
		CREATE TRIGGER test_deadlock BEFORE UPDATE ON documents FOR EACH ROW EXECUTE FUNCTION test_deadlock();`); err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int32
	held := store.WithAfterLock(ctx, func() { attempts.Add(1) })
	_, err := st.BulkUpdateDocuments(held, alice, store.BulkSelection{IDs: []string{doc.ID}}, store.BulkChange{IsFavorite: ptr(true)}, "document.update")
	if !errors.Is(err, store.ErrRetry) || attempts.Load() != 3 {
		t.Errorf("%v after %d attempts, want ErrRetry after 3", err, attempts.Load())
	}
	attempts.Store(0)
	if _, err := st.PatchDocument(held, alice, doc.ID, store.DocumentPatch{Title: ptr("x")}); !errors.Is(err, store.ErrRetry) || attempts.Load() != 3 {
		t.Errorf("patch: %v after %d attempts, want ErrRetry after 3", err, attempts.Load())
	}
	if _, err := conn.Exec(ctx, `DROP TRIGGER test_deadlock ON documents`); err != nil {
		t.Fatal(err)
	}
	if g := getDoc(t, st, doc.ID); g.IsFavorite || g.Title != "doc" {
		t.Errorf("changed: %+v", g)
	}
}

// Every per-document entry of an admin's bulk change of another user's
// documents has ownerId = that user, by IDs, by a match over everyone's
// and by a match narrowed to the owner (Owner, the list's owner=<id>);
// the admin's own documents get none, also with Owner = the admin.
func TestBulkUpdateAuditOwnerID(t *testing.T) {
	st, dbURL := storetest.NewWithURL(t)
	conn := connect(t, dbURL)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	root := storetest.User(t, st, "root", true)
	admin := store.Scope{UserID: root.ID, All: true}
	var aliceDocs, rootDocs []string
	for i := range 2 {
		aliceDocs = append(aliceDocs, mustDoc(t, st, alice, newDoc(fmt.Sprint("Rechnung alice ", i))).ID)
		rootDocs = append(rootDocs, mustDoc(t, st, store.Scope{UserID: root.ID}, newDoc(fmt.Sprint("Rechnung root ", i))).ID)
	}
	parse := func(s string) *search.Query {
		q, err := search.Parse(s, nil)
		if err != nil {
			t.Fatal(err)
		}
		return &q
	}
	run := 0
	for _, c := range []struct {
		name    string
		sel     store.BulkSelection
		targets []string
		ownerID string // "" for none
	}{
		{"ids, alice's", store.BulkSelection{IDs: aliceDocs}, aliceDocs, fmt.Sprint(alice.UserID)},
		{"match, alice's (owner empty)", store.BulkSelection{Match: parse("alice")}, aliceDocs, fmt.Sprint(alice.UserID)},
		{"match, owner=alice", store.BulkSelection{Match: parse("rechnung"), Owner: &alice.UserID}, aliceDocs, fmt.Sprint(alice.UserID)},
		{"ids, own", store.BulkSelection{IDs: rootDocs}, rootDocs, ""},
		{"match, own (owner empty)", store.BulkSelection{Match: parse("root")}, rootDocs, ""},
		{"match, owner=me", store.BulkSelection{Match: parse("rechnung"), Owner: &root.ID}, rootDocs, ""},
	} {
		before := len(auditRows(t, conn))
		run++
		e := &store.AuditEntry{Actor: store.ActorSession, Action: "document.bulk_update"}
		e.SetUser(root.ID, root.Name)
		e.Detail("fields", []string{"favorite"})
		e.Detail("bulkRun", fmt.Sprint("run-", run))
		ctx := store.WithAudit(context.Background(), e)
		sel := c.sel
		if sel.Match != nil {
			sel = confirmed(t, st, admin, sel)
		}
		if _, err := st.BulkUpdateDocuments(ctx, admin, sel, store.BulkChange{IsFavorite: ptr(run%2 == 1)}, "document.update"); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var targets []string
		for _, r := range auditRows(t, conn)[before:] {
			if r.Action != "document.update" {
				continue
			}
			targets = append(targets, *r.TargetID)
			got, ok := r.Details["ownerId"]
			if c.ownerID == "" && ok || c.ownerID != "" && fmt.Sprint(got) != c.ownerID {
				t.Errorf("%s: entry of %s has ownerId %v, want %q", c.name, *r.TargetID, got, c.ownerID)
			}
		}
		slices.Sort(targets)
		if !slices.Equal(targets, slices.Sorted(slices.Values(c.targets))) {
			t.Errorf("%s: entries for %v, want %v", c.name, targets, c.targets)
		}
	}
	// A non-admin naming another owner matches nothing.
	if got, err := bulk(t, st, alice, store.BulkSelection{Match: parse("rechnung"), Owner: &root.ID}, store.BulkChange{IsInbox: ptr(false)}); err != nil || len(got) != 0 {
		t.Errorf("alice with owner=root: %v, %v", got, err)
	}
	empty, err := st.CountBulkMatches(context.Background(), alice, store.BulkSelection{Match: parse("nichtda")})
	if err != nil || empty.Count != 0 || empty.Fingerprint == "" {
		t.Fatalf("alice's dry run without matches: %+v, %v", empty, err)
	}
	// Outside the scope the dry run names the empty set too.
	if m, err := st.CountBulkMatches(context.Background(), alice, store.BulkSelection{Match: parse("rechnung"), Owner: &root.ID}); err != nil || m.Count != 0 || m.Fingerprint != empty.Fingerprint {
		t.Errorf("alice's dry run with owner=root: %+v, %v", m, err)
	}
	if m, err := st.CountBulkMatches(context.Background(), admin, store.BulkSelection{Match: parse("rechnung"), Owner: &alice.UserID}); err != nil || m.Count != 2 || m.Owners != 1 {
		t.Errorf("admin's dry run with owner=alice: %+v, %v", m, err)
	}
}

// Repeated tags to add or remove are one tag, also in another case; the
// same tag added and removed is refused, also in another case.
func TestBulkUpdateRepeatedTags(t *testing.T) {
	st := storetest.New(t)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	a, b := mustTag(t, st, alice, "A", nil), mustTag(t, st, alice, "B", nil)
	in := newDoc("doc")
	in.TagIDs = []string{b.ID}
	doc := mustDoc(t, st, alice, in)
	sel := store.BulkSelection{IDs: []string{doc.ID}}
	if _, err := bulk(t, st, alice, sel, store.BulkChange{AddTags: []string{a.ID, a.ID, strings.ToUpper(a.ID)},
		RemoveTags: []string{b.ID, strings.ToUpper(b.ID), b.ID}}); err != nil {
		t.Fatalf("repeated tags: %v", err)
	}
	if g := getDoc(t, st, doc.ID); !slices.Equal(sortedTags(g), ids(a)) {
		t.Errorf("tags %v, want %v", g.TagIDs, ids(a))
	}
	var ve *store.ValidationError
	if _, err := bulk(t, st, alice, sel, store.BulkChange{AddTags: []string{b.ID}, RemoveTags: []string{strings.ToUpper(b.ID)}}); !errors.As(err, &ve) || ve.Field != "tags" {
		t.Errorf("add and remove in another case: %v", err)
	}
}

// A document that a concurrent change takes out of the match after the
// match ran but before the bulk change locked it is not changed: the bulk
// change runs the search again with the documents locked, and the set
// left is no longer the confirmed one, so nothing changes (409). Without
// the recheck the locked set would still match the fingerprint and the
// document would be changed although it no longer matches (#99).
func TestBulkUpdateByMatchRecheckedAfterLock(t *testing.T) {
	st, dbURL := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := connect(t, dbURL)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	moved := mustDoc(t, st, alice, newDoc("moved"))
	stays := mustDoc(t, st, alice, newDoc("stays"))
	inbox := &search.Query{Filters: []search.Filter{{Field: search.FieldIs, Values: []string{search.IsInbox}}}}
	sel := confirmed(t, st, alice, store.BulkSelection{Match: inbox})
	before := len(auditRows(t, conn))

	var attempts atomic.Int32
	matched, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	var changed []string
	go func() {
		e := &store.AuditEntry{Actor: store.ActorCLI, Action: "document.bulk_update"}
		held := store.WithBeforeLock(store.WithAudit(ctx, e), func() {
			if attempts.Add(1) > 1 {
				return
			}
			close(matched)
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
		var err error
		changed, err = st.BulkUpdateDocuments(held, alice, sel, store.BulkChange{IsFavorite: ptr(true)}, "document.update")
		done <- err
	}()
	select {
	case <-matched:
	case err := <-done:
		t.Fatalf("bulk change finished before it matched: %v", err)
	}
	// Both matched; now one leaves the inbox.
	if _, err := st.PatchDocument(ctx, alice, moved.ID, store.DocumentPatch{IsInbox: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	close(release)
	var mc *store.MatchChangedError
	if err := <-done; !errors.As(err, &mc) || !errors.Is(err, store.ErrMatchChanged) || mc.Count != 1 || changed != nil {
		t.Fatalf("bulk change: %v, changed %v; want ErrMatchChanged with count 1", err, changed)
	}
	if g := getDoc(t, st, moved.ID); g.IsFavorite {
		t.Errorf("the document no longer matching was changed: %+v", g)
	}
	if g := getDoc(t, st, stays.ID); g.IsFavorite {
		t.Errorf("the refused change changed the matching document: %+v", g)
	}
	for _, r := range auditRows(t, conn)[before:] {
		if r.Action == "document.bulk_update" || r.Actor == store.ActorCLI {
			t.Errorf("the refused change wrote an audit entry: %+v", r)
		}
	}
}

// A bulk change by search changes only exactly the set its dry run
// counted (the fingerprint): one more match since the count, one less, or
// the same count with another document refuse the whole change with
// ErrMatchChanged, and nothing changes or is logged (#106).
func TestBulkUpdateByMatchConfirmedSet(t *testing.T) {
	st, dbURL := storetest.NewWithURL(t)
	ctx := context.Background()
	conn := connect(t, dbURL)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	insertDocs(t, conn, alice.UserID, 230, "Rechnung")
	inbox := store.BulkSelection{Match: &search.Query{Filters: []search.Filter{{Field: search.FieldIs, Values: []string{search.IsInbox}}}}}
	unchanged := func(name string, before int) {
		t.Helper()
		var favs int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM documents WHERE is_favorite`).Scan(&favs); err != nil || favs != 0 {
			t.Errorf("%s: %d documents changed, %v", name, favs, err)
		}
		if n := len(auditRows(t, conn)); n != before {
			t.Errorf("%s: %d audit entries written", name, n-before)
		}
	}
	wantChanged := func(name string, err error, count int) {
		t.Helper()
		var mc *store.MatchChangedError
		if !errors.As(err, &mc) || !errors.Is(err, store.ErrMatchChanged) || mc.Count != count {
			t.Errorf("%s: %v, want ErrMatchChanged with count %d", name, err, count)
		}
	}
	fav := store.BulkChange{IsFavorite: ptr(true)}
	// logged is bulk with an audit entry, so a refused change could write one.
	logged := func(t *testing.T, st *store.Store, sc store.Scope, sel store.BulkSelection, c store.BulkChange) ([]string, error) {
		t.Helper()
		if sel.Fingerprint == "" {
			sel = confirmed(t, st, sc, sel)
		}
		e := &store.AuditEntry{Actor: store.ActorCLI, Action: "document.bulk_update"}
		return st.BulkUpdateDocuments(store.WithAudit(ctx, e), sc, sel, c, "document.update")
	}

	// The reported case: 230 confirmed, one imported before the confirm.
	sel := confirmed(t, st, alice, inbox)
	extra := mustDoc(t, st, alice, newDoc("Neu"))
	before := len(auditRows(t, conn))
	_, err := logged(t, st, alice, sel, fav)
	wantChanged("one more", err, 231)
	unchanged("one more", before)

	// Same count, another set: one leaves the inbox (231 → 230), and the
	// fingerprint of the first 230 still does not fit.
	var first string
	if err := conn.QueryRow(ctx, `SELECT id::text FROM documents WHERE title = 'Rechnung' ORDER BY id LIMIT 1`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `UPDATE documents SET is_inbox = false WHERE id = $1`, first); err != nil {
		t.Fatal(err)
	}
	if m, err := st.CountBulkMatches(ctx, alice, inbox); err != nil || m.Count != 230 || m.Fingerprint == sel.Fingerprint {
		t.Fatalf("dry run of the other set: %+v, %v", m, err)
	}
	before = len(auditRows(t, conn))
	_, err = logged(t, st, alice, sel, fav)
	wantChanged("same count, other set", err, 230)
	unchanged("same count, other set", before)

	// One left the match since the count.
	sel = confirmed(t, st, alice, inbox)
	if _, err := conn.Exec(ctx, `UPDATE documents SET is_inbox = false WHERE id = $1`, extra.ID); err != nil {
		t.Fatal(err)
	}
	_, err = logged(t, st, alice, sel, fav)
	wantChanged("one less", err, 229)
	unchanged("one less", before)

	// Nothing matches any more: 409 with 0, not an empty success.
	gone := confirmed(t, st, alice, inbox)
	if _, err := conn.Exec(ctx, `UPDATE documents SET is_inbox = false`); err != nil {
		t.Fatal(err)
	}
	_, err = logged(t, st, alice, gone, fav)
	wantChanged("now empty", err, 0)
	unchanged("now empty", before)

	// An empty confirmation that is still empty changes nothing, no entry.
	if got, err := logged(t, st, alice, inbox, fav); err != nil || len(got) != 0 {
		t.Errorf("still empty: %v, %v", got, err)
	}
	unchanged("still empty", before)

	// Missing or misplaced fingerprints are refused.
	var ve *store.ValidationError
	if _, err := st.BulkUpdateDocuments(ctx, alice, inbox, fav, "document.update"); !errors.As(err, &ve) || ve.Field != "selection" {
		t.Errorf("match without fingerprint: %v", err)
	}
	if _, err := st.BulkUpdateDocuments(ctx, alice, store.BulkSelection{IDs: []string{extra.ID}, Fingerprint: gone.Fingerprint}, fav, "document.update"); !errors.As(err, &ve) || ve.Field != "selection" {
		t.Errorf("ids with fingerprint: %v", err)
	}
	if _, err := st.CountBulkMatches(ctx, alice, gone); !errors.As(err, &ve) || ve.Field != "selection" {
		t.Errorf("dry run with fingerprint: %v", err)
	}

	// The unchanged set goes through, with its entries.
	if _, err := conn.Exec(ctx, `UPDATE documents SET is_inbox = true WHERE title = 'Rechnung'`); err != nil {
		t.Fatal(err)
	}
	sel = confirmed(t, st, alice, inbox)
	got, err := logged(t, st, alice, sel, fav)
	if err != nil || len(got) != 230 {
		t.Fatalf("unchanged set: %d, %v", len(got), err)
	}
	if n := len(auditRows(t, conn)) - before; n != 231 {
		t.Errorf("%d audit entries, want the summary and 230", n)
	}
}

// finishesWithoutLockWait returns done's result, or fails once a statement
// of application app waits for a lock first.
func finishesWithoutLockWait(ctx context.Context, t *testing.T, conn *pgx.Conn, app string, done <-chan error) error {
	t.Helper()
	for {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			t.Fatalf("%s neither finished nor waited", app)
		default:
		}
		var waiting bool
		err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND application_name = $1 AND wait_event_type = 'Lock')`, app).Scan(&waiting)
		if err == nil && waiting {
			t.Fatalf("%s waits for the document's lock", app)
		}
	}
}

// A patch or a bulk change holds the document with FOR NO KEY UPDATE: a
// row referencing the document (here a job) can be written meanwhile; its
// foreign-key check (FOR KEY SHARE) waited for FOR UPDATE.
func TestDocumentLocksLetReferencesIn(t *testing.T) {
	st, dbURL := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	doc := mustDoc(t, st, alice, newDoc("doc"))
	watch := connect(t, dbURL)
	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("application_name", "referrer")
	u.RawQuery = q.Encode()
	referrer := connect(t, u.String())

	for _, c := range []struct {
		name   string
		change func(ctx context.Context) error
	}{
		{"patch", func(ctx context.Context) error {
			_, err := st.PatchDocument(ctx, alice, doc.ID, store.DocumentPatch{Title: ptr("patched")})
			return err
		}},
		{"bulk", func(ctx context.Context) error {
			_, err := st.BulkUpdateDocuments(ctx, alice, store.BulkSelection{IDs: []string{doc.ID}}, store.BulkChange{IsFavorite: ptr(true)}, "document.update")
			return err
		}},
	} {
		locked, release := make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		go func() {
			held := store.WithAfterLock(ctx, func() {
				close(locked)
				select {
				case <-release:
				case <-ctx.Done():
				}
			})
			done <- c.change(held)
		}()
		select {
		case <-locked:
		case err := <-done:
			t.Fatalf("%s finished before it locked: %v", c.name, err)
		}
		inserted := make(chan error, 1)
		go func() {
			_, err := referrer.Exec(ctx, `INSERT INTO jobs (document_id, state, finished_at) VALUES ($1, 'done', now())`, doc.ID)
			inserted <- err
		}()
		if err := finishesWithoutLockWait(ctx, t, watch, "referrer", inserted); err != nil {
			t.Fatalf("%s: insert a job: %v", c.name, err)
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
}
