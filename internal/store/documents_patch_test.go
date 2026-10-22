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

func bulk(t *testing.T, st *store.Store, sc store.Scope, sel store.BulkSelection, c store.BulkChange) ([]string, error) {
	t.Helper()
	return st.BulkUpdateDocuments(context.Background(), sc, sel, c, "document.update")
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
	if m, err := st.CountBulkMatches(ctx, admin, *parse("rechnung")); err != nil || m.Count != 4 || m.Owners != 3 {
		t.Errorf("dry run: %+v, %v", m, err)
	}
	if m, err := st.CountBulkMatches(ctx, alice, *parse("nichtda")); err != nil || m.Count != 0 {
		t.Errorf("dry run without matches: %+v, %v", m, err)
	}

	// The limit: one match too many refuses the whole change.
	conn := connect(t, dbURL)
	insertDocs(t, conn, bob.UserID, store.MaxBulkMatches, "Massenbrief")
	if m, err := st.CountBulkMatches(ctx, bob, *parse("massenbrief")); err != nil || m.Count != store.MaxBulkMatches || m.Owners != 1 {
		t.Errorf("dry run at the limit: %+v, %v", m, err)
	}
	if got, err := bulk(t, st, bob, store.BulkSelection{Match: parse("massenbrief")}, store.BulkChange{IsInbox: ptr(false)}); err != nil || len(got) != store.MaxBulkMatches {
		t.Errorf("at the limit: %d, %v", len(got), err)
	}
	insertDocs(t, conn, bob.UserID, 1, "Massenbrief extra")
	_, err := bulk(t, st, bob, store.BulkSelection{Match: parse("massenbrief")}, store.BulkChange{IsInbox: ptr(true)})
	var tooMany *store.TooManyError
	if !errors.As(err, &tooMany) || tooMany.Count != store.MaxBulkMatches+1 || tooMany.Limit != store.MaxBulkMatches {
		t.Errorf("over the limit: %v", err)
	}
	if m, err := st.CountBulkMatches(ctx, bob, *parse("massenbrief")); err != nil || m.Count != store.MaxBulkMatches+1 || m.Owners != 0 {
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

// A bulk change that holds its documents while DeleteTag of a tag it
// removes waits for them deadlocks once it goes on; as the victim it is
// retried and succeeds. The bulk change's connection detects deadlocks
// after 10 ms, DeleteTag's only after an hour, so the bulk change is the
// victim; nothing waits for time.
func TestBulkUpdateRetriesDeadlockWithDeleteTag(t *testing.T) {
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
	bulkStore := openWith(t, dbURL, map[string]string{"options": "-c deadlock_timeout=10ms", "application_name": "bulk"})
	deleter := openWith(t, dbURL, map[string]string{"options": "-c deadlock_timeout=1h", "application_name": "deleter"})
	conn := connect(t, dbURL)

	var attempts atomic.Int32
	locked, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		held := store.WithAfterLock(ctx, func() {
			if attempts.Add(1) > 1 {
				return
			}
			close(locked)
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
		_, err := bulkStore.BulkUpdateDocuments(held, alice, store.BulkSelection{IDs: docs},
			store.BulkChange{RemoveTags: []string{tag.ID}, SetCorrespondent: true, CorrespondentID: &corr.ID}, "document.update")
		done <- err
	}()
	select {
	case <-locked:
	case err := <-done:
		t.Fatalf("bulk change finished before it locked: %v", err)
	}
	deleted := make(chan error, 1)
	go func() { deleted <- deleter.DeleteTag(ctx, alice, tag.ID) }()
	waitForLock(ctx, t, conn, "deleter", deleted)
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("bulk change: %v", err)
	}
	if err := <-deleted; err != nil {
		t.Fatalf("delete tag: %v", err)
	}
	if n := attempts.Load(); n != 2 {
		t.Errorf("%d attempts, want 2 (one deadlock, one retry)", n)
	}
	for _, id := range docs {
		if g := getDoc(t, st, id); len(g.TagIDs) != 0 || g.CorrespondentID == nil || *g.CorrespondentID != corr.ID {
			t.Errorf("after the retry: %+v", g)
		}
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
