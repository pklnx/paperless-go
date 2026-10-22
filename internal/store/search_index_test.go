package store_test

import (
	"context"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// The search index (#6, part 1): the trigger-maintained vectors and
// document_search_tsquery. Part 2's store search builds on them; these tests
// call the SQL function directly with the bound arrays the parser will pass.

// searchVersion is the migration that adds the search index.
const searchVersion = 11

// item is one free-text item of a search, as the parser will pass it.
type item struct {
	term   string
	phrase bool
	neg    bool
	group  int // items with the same non-zero group are joined by OR
}

// words makes one AND item per word.
func words(ws ...string) []item {
	items := make([]item, len(ws))
	for i, w := range ws {
		items[i] = item{term: w}
	}
	return items
}

// searchIndex runs document_search_tsquery for owner (nil: all owners)
// against every document, regardless of owner, so tests see whether a
// document's own vector can match. It returns the matching titles, sorted.
func searchIndex(t *testing.T, conn *pgx.Conn, owner *int64, items []item) []string {
	t.Helper()
	titles, err := trySearchIndex(conn, owner, items)
	if err != nil {
		t.Fatalf("search %+v: %v", items, err)
	}
	return titles
}

func trySearchIndex(conn *pgx.Conn, owner *int64, items []item) ([]string, error) {
	terms := make([]string, len(items))
	phrase := make([]bool, len(items))
	neg := make([]bool, len(items))
	groups := make([]int32, len(items))
	for i, it := range items {
		terms[i], phrase[i], neg[i] = it.term, it.phrase, it.neg
		groups[i] = int32(it.group)
		if it.group == 0 {
			groups[i] = int32(-1 - i)
		}
	}
	rows, err := conn.Query(context.Background(), `
		SELECT d.title FROM documents d
		WHERE (d.search_text || d.search_links) @@ document_search_tsquery($1, $2, $3, $4, $5)
		ORDER BY d.title`, owner, terms, phrase, neg, groups)
	if err != nil {
		return nil, err
	}
	titles, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if titles == nil {
		titles = []string{}
	}
	return titles, err
}

// textDoc creates a document of sc with title and extracted text.
func textDoc(t *testing.T, st *store.Store, sc store.Scope, title, text string) *store.Document {
	t.Helper()
	in := newDoc(title)
	in.OriginalFileName = "scan.pdf"
	in.ExtractedText = text
	return mustDoc(t, st, sc, in)
}

// xmin returns the transaction ID that wrote the document's current row
// version: it changes whenever the row is updated.
func xmin(t *testing.T, conn *pgx.Conn, id string) string {
	t.Helper()
	var v string
	if err := conn.QueryRow(context.Background(), `SELECT xmin::text FROM documents WHERE id = $1`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func wantTitles(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("%s: got %q, want %q", what, got, want)
	}
}

// TestSearchFindsInflectedWordInRecognizedText is the issue's "done when":
// a word of the recognized text is found, also in another inflected form.
func TestSearchFindsInflectedWordInRecognizedText(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	doc := mustDoc(t, st, alice, newDoc("scan"))
	now := time.Now()
	j := claim(t, st, now)
	if err := st.CompleteJob(ctx, j, now, store.ProcessResult{
		PageCount: 1, Text: "Anbei erhalten Sie die Rechnungen für die Versicherungen im März.", TextSource: "Tesseract",
	}); err != nil {
		t.Fatal(err)
	}
	conn := connect(t, url)
	for _, q := range []string{"Rechnung", "rechnungen", "Versicherung", "Marz", "März"} {
		wantTitles(t, q, searchIndex(t, conn, &alice.UserID, words(q)), doc.Title)
	}
	wantTitles(t, "a word not in the text", searchIndex(t, conn, &alice.UserID, words("Mahnung")))
}

func TestSearchStemmingAndFolding(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	conn := connect(t, url)
	for _, c := range []struct {
		text, query string
		found       bool
	}{
		{"Bitte zahlen Sie die Rechnung.", "Rechnungen", true},
		{"Alle Rechnungen des Jahres.", "Rechnung", true},
		{"Ihre Versicherungen im Überblick.", "Versicherung", true},
		{"Die Versicherung zahlt.", "Versicherungen", true},
		{"Gebühren für das Konto.", "Gebuhren", true},
		{"Gebuhren ohne Umlaut.", "Gebühren", true},
		{"Gebühren für das Konto.", "Gebuehren", false},
		{"Hauptstraße 5, Berlin.", "Hauptstrasse", true},
		{"Hauptstrasse 5, Berlin.", "Hauptstraße", true},
		{"Die Häuser am See.", "Haus", true},
		{"Ein Haus am See.", "Häuser", true},
		{"Please find the invoices attached.", "invoice", true},
		{"The invoice is due.", "invoices", true},
		{"Minutes of the meetings.", "meeting", true},
		{"We met at the meeting.", "meetings", true},
		{"Café am Markt.", "cafe", true},
	} {
		t.Run(c.text+"/"+c.query, func(t *testing.T) {
			_, err := conn.Exec(context.Background(), `DELETE FROM documents`)
			if err != nil {
				t.Fatal(err)
			}
			textDoc(t, st, alice, "doc", c.text)
			got := searchIndex(t, conn, &alice.UserID, words(c.query))
			if (len(got) == 1) != c.found {
				t.Errorf("%q in %q: found %v, want %v", c.query, c.text, got, c.found)
			}
		})
	}
}

func TestSearchExactWordsPrefixesPhrases(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	conn := connect(t, url)
	textDoc(t, st, alice, "invoice", "Rechnungsnummer RE-2024-00123, IBAN DE89370400440532013000. Die Stadtwerke senden eine Rechnung.")
	textDoc(t, st, alice, "letter", "Sehr geehrte Damen und Herren, anbei der Vertrag.")
	me := &alice.UserID

	wantTitles(t, "invoice number", searchIndex(t, conn, me, words("RE-2024-00123")), "invoice")
	wantTitles(t, "IBAN", searchIndex(t, conn, me, words("DE89370400440532013000")), "invoice")
	wantTitles(t, "IBAN prefix", searchIndex(t, conn, me, words("DE8937")), "invoice")
	wantTitles(t, "prefix", searchIndex(t, conn, me, words("Rechn")), "invoice")
	wantTitles(t, "prefix of another word", searchIndex(t, conn, me, words("Vertr")), "letter")
	wantTitles(t, "two letters are no prefix", searchIndex(t, conn, me, words("Ve")))
	wantTitles(t, "three letters are", searchIndex(t, conn, me, words("Ver")), "letter")
	wantTitles(t, "phrase", searchIndex(t, conn, me, []item{{term: "Stadtwerke senden", phrase: true}}), "invoice")
	wantTitles(t, "phrase with inflection", searchIndex(t, conn, me, []item{{term: "Stadtwerk senden", phrase: true}}), "invoice")
	wantTitles(t, "words apart are no phrase", searchIndex(t, conn, me, []item{{term: "Stadtwerke Rechnung", phrase: true}}))
	wantTitles(t, "the same words as words", searchIndex(t, conn, me, words("Stadtwerke", "Rechnung")), "invoice")
	wantTitles(t, "exclusion", searchIndex(t, conn, me, []item{{term: "Rechnung", neg: true}}), "letter")
	wantTitles(t, "OR", searchIndex(t, conn, me, []item{{term: "Vertrag", group: 1}, {term: "Stadtwerke", group: 1}}), "invoice", "letter")
	wantTitles(t, "OR and AND", searchIndex(t, conn, me, []item{
		{term: "Vertrag", group: 1}, {term: "Stadtwerke", group: 1}, {term: "Herren"},
	}), "letter")
	wantTitles(t, "no items", searchIndex(t, conn, me, nil))
}

// A phrase relies on the german copy of the text, which comes first and
// keeps real positions up to position 16 383; behind it every position is
// 16 383, so a phrase there is not found, while its words still are.
func TestSearchPhrasePositions(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	conn := connect(t, url)
	filler := strings.Repeat("Füllwort ", 17000)
	textDoc(t, st, alice, "early", "Am Anfang steht die Kündigung des Vertrags. "+filler)
	textDoc(t, st, alice, "late", filler+"Am Ende steht die Kündigung des Vertrags.")
	me := &alice.UserID

	// Early: the simple and english copies of this text are past 16 383,
	// so only the german copy can find the phrase.
	wantTitles(t, "phrase in a long text", searchIndex(t, conn, me, []item{{term: "Kündigung des Vertrags", phrase: true}}), "early")
	wantTitles(t, "words in a long text", searchIndex(t, conn, me, words("Kündigung", "Vertrags")), "early", "late")
}

func TestSearchOtherFields(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	conn := connect(t, url)
	me := &alice.UserID

	telekom := mustCorrespondent(t, st, alice, "Telekom Deutschland")
	vw := mustCorrespondent(t, st, alice, "VW")
	cafe := mustCorrespondent(t, st, alice, "Café Müller")
	contract, err := st.CreateDocumentType(ctx, alice, store.DocumentTypeInput{Name: "Vertrag"})
	if err != nil {
		t.Fatal(err)
	}
	auto := mustTag(t, st, alice, "Auto", nil)
	garage := mustTag(t, st, alice, "Werkstatt", auto)
	insurance := mustTag(t, st, alice, "Kfz-Versicherung", nil)

	in := newDoc("by correspondent")
	in.OriginalFileName = "scan.pdf"
	in.CorrespondentID = &telekom.ID
	in.ExtractedText = "Ihre Rechnung für Oktober."
	mustDoc(t, st, alice, in)

	in = newDoc("by type")
	in.OriginalFileName = "scan.pdf"
	in.DocumentTypeID = &contract.ID
	in.TagIDs = []string{insurance.ID}
	mustDoc(t, st, alice, in)

	in = newDoc("by child tag")
	in.OriginalFileName = "scan.pdf"
	in.TagIDs = []string{garage.ID}
	in.CorrespondentID = &vw.ID
	mustDoc(t, st, alice, in)

	in = newDoc("by asn")
	in.OriginalFileName = "scan.pdf"
	in.ArchiveSerialNumber = ptr[int32](123)
	in.CorrespondentID = &cafe.ID
	mustDoc(t, st, alice, in)

	in = newDoc("by file name")
	in.OriginalFileName = "Rechnung_Telekom-2024.pdf"
	mustDoc(t, st, alice, in)

	in = newDoc("by notes")
	in.OriginalFileName = "scan.pdf"
	in.Notes = "Original liegt im Ordner Steuern."
	mustDoc(t, st, alice, in)

	for _, c := range []struct {
		query []string
		want  []string
	}{
		{[]string{"telekom"}, []string{"by correspondent", "by file name"}},
		{[]string{"Telekom", "Rechnung"}, []string{"by correspondent", "by file name"}},
		{[]string{"Telekom", "Oktober"}, []string{"by correspondent"}},
		{[]string{"deutschland"}, []string{"by correspondent"}},
		{[]string{"vertrag"}, []string{"by type"}},
		{[]string{"versich"}, []string{"by type"}},
		{[]string{"Auto"}, []string{"by child tag"}},
		{[]string{"werkstatt"}, []string{"by child tag"}},
		{[]string{"vw"}, []string{"by child tag"}},
		{[]string{"cafe"}, []string{"by asn"}},
		{[]string{"muller"}, []string{"by asn"}},
		{[]string{"123"}, []string{"by asn"}},
		{[]string{"ASN123"}, []string{"by asn"}},
		{[]string{"2024"}, []string{"by file name"}},
		{[]string{"Steuer"}, []string{"by notes"}},
		{[]string{"Ordner"}, []string{"by notes"}},
		// Two letters match a whole name only, not a part of one.
		{[]string{"au"}, nil},
		{[]string{"kf"}, nil},
	} {
		wantTitles(t, strings.Join(c.query, " "), searchIndex(t, conn, me, words(c.query...)), c.want...)
	}
}

// TestSearchNameMatchCap: at most 1000 link IDs per item, the same ones on
// every call.
func TestSearchNameMatchCap(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := storetest.User(t, st, "alice", false)
	conn := connect(t, url)
	if _, err := conn.Exec(ctx, `INSERT INTO tags (owner_id, name) SELECT $1, 'Sammlung ' || g FROM generate_series(1, 1100) g`, alice.ID); err != nil {
		t.Fatal(err)
	}
	links := func() (int, string) {
		var n int
		var q string
		if err := conn.QueryRow(ctx, `
			WITH q AS (SELECT document_search_tsquery($1, '{sammlung}', '{f}', '{f}', '{1}')::text AS q)
			SELECT (length(q) - length(replace(q, ''' t', ''))) / 3, q FROM q`, alice.ID).Scan(&n, &q); err != nil {
			t.Fatal(err)
		}
		return n, q
	}
	n, first := links()
	if n != 1000 {
		t.Errorf("link IDs in the query: %d, want 1000", n)
	}
	if _, again := links(); again != first {
		t.Error("the capped IDs differ between calls")
	}
}

func TestSearchOddInput(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	mustTag(t, st, alice, "100%_sicher", nil)
	textDoc(t, st, alice, "doc", `O'Brien schreibt C:\Pfad und a&b|c!d (x):*y <-> z`)
	conn := connect(t, url)
	odd := []string{
		"'", "''", `\`, `\\`, `&|!():*<->`, "%", "_", "'; DROP TABLE documents; --",
		"O'Brien", `C:\Pfad`, "a&b", "(x):*y", "<->", "der die das", "the and of", "...", "-", "!!",
		"'a':*", `\'`, "x' | 'y",
	}
	for _, term := range odd {
		for _, phrase := range []bool{false, true} {
			for _, neg := range []bool{false, true} {
				if _, err := trySearchIndex(conn, &alice.UserID, []item{{term: term, phrase: phrase, neg: neg}}); err != nil {
					t.Errorf("%q (phrase %v, negated %v): %v", term, phrase, neg, err)
				}
			}
		}
	}
	wantTitles(t, "apostrophe", searchIndex(t, conn, &alice.UserID, words("O'Brien")), "doc")
	wantTitles(t, "LIKE wildcards are literal", searchIndex(t, conn, &alice.UserID, words("0%_s")))
	wantTitles(t, "stop words only", searchIndex(t, conn, &alice.UserID, words("der die das")))

	r := rand.New(rand.NewPCG(52, 6))
	alphabet := []rune(`ab 'ä\%_&|!():*<->"-ß.,;`)
	for range 300 {
		b := make([]rune, 1+r.IntN(12))
		for i := range b {
			b[i] = alphabet[r.IntN(len(alphabet))]
		}
		it := item{term: string(b), phrase: r.IntN(2) == 0, neg: r.IntN(3) == 0, group: r.IntN(2)}
		if _, err := trySearchIndex(conn, &alice.UserID, []item{it, {term: "doc"}}); err != nil {
			t.Errorf("%+v: %v", it, err)
		}
	}

	if _, err := conn.Exec(context.Background(), `SELECT document_search_tsquery(NULL, '{a,b}', '{f}', '{f}', '{1}')`); err == nil {
		t.Error("arrays of different lengths accepted")
	}
}

// accentedWords returns n random words of accented letters: their german,
// english and paperless_simple lexemes all differ, so the vector grows
// fastest. It is deterministic.
func accentedWords(n int, seed uint64) string {
	letters := []rune("àáâãäåèéêëìíîïòóôõöùúûüýÿ")
	r := rand.New(rand.NewPCG(seed, 2))
	var b strings.Builder
	for range n {
		for range 8 {
			b.WriteRune(letters[r.IntN(len(letters))])
		}
		b.WriteByte(' ')
	}
	return b.String()
}

func TestSearchVectorCap(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	conn := connect(t, url)
	me := &alice.UserID
	// Created first, so its job is the first to be claimed.
	scan := mustDoc(t, st, alice, newDoc("Verarbeitet"))

	// 16 MiB, the worker's limit: only the first 300 000 characters count.
	sentence := "Dies ist ein langer Bericht über nichts. "
	long := "Anfangswort " + strings.Repeat(sentence, (16<<20)/len(sentence)-1) + "Schlusswort"
	textDoc(t, st, alice, "Großer Bericht", long)

	// About 360 000 characters whose vector overflows 1 MB: the text is
	// indexed with 100 000 characters.
	random := "Zufallsanfang " + accentedWords(40000, 1) + "Zufallsende"
	textDoc(t, st, alice, "Zufallstext", random)

	// Huge notes too: only the title and 100 000 characters of the other
	// fields are indexed.
	in := newDoc("Notizenflut")
	in.OriginalFileName = "scan.pdf"
	in.Notes = "Notizanfang " + accentedWords(40000, 2)
	in.ExtractedText = random
	mustDoc(t, st, alice, in)

	// Everything huge: the vector is empty, and the document still saves.
	in = newDoc("everything")
	in.Title = accentedWords(40000, 3)
	in.Notes = accentedWords(40000, 4)
	in.OriginalFileName = accentedWords(40000, 5)
	in.ExtractedText = random
	empty := mustDoc(t, st, alice, in)

	wantTitles(t, "title", searchIndex(t, conn, me, words("Bericht")), "Großer Bericht")
	wantTitles(t, "start of 16 MiB", searchIndex(t, conn, me, words("Anfangswort")), "Großer Bericht")
	wantTitles(t, "end of 16 MiB", searchIndex(t, conn, me, words("Schlusswort")))
	wantTitles(t, "overflowing title", searchIndex(t, conn, me, words("Zufallstext")), "Zufallstext")
	wantTitles(t, "start of overflowing text", searchIndex(t, conn, me, words("Zufallsanfang")), "Zufallstext")
	wantTitles(t, "end of overflowing text", searchIndex(t, conn, me, words("Zufallsende")))
	wantTitles(t, "huge notes", searchIndex(t, conn, me, words("Notizenflut", "Notizanfang")), "Notizenflut")
	var size int
	if err := conn.QueryRow(ctx, `SELECT length(search_text) FROM documents WHERE id = $1`, empty.ID).Scan(&size); err != nil || size != 0 {
		t.Errorf("vector of a document with only huge fields: %d lexemes, %v", size, err)
	}

	// The worker's result transaction stores such text.
	j := claim(t, st, time.Now())
	if j.DocumentID != scan.ID {
		t.Fatalf("claimed the job of %s, want %s", j.DocumentID, scan.ID)
	}
	if err := st.CompleteJob(ctx, j, time.Now(), store.ProcessResult{PageCount: 1, Text: random, TextSource: "Tesseract"}); err != nil {
		t.Fatalf("complete with overflowing text: %v", err)
	}
	wantTitles(t, "processed", searchIndex(t, conn, me, words("Verarbeitet", "Zufallsanfang")), "Verarbeitet")
}

func TestSearchLinkPropagation(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	conn := connect(t, url)
	me := &alice.UserID
	find := func(w string) []string { return searchIndex(t, conn, me, words(w)) }

	steuer := mustTag(t, st, alice, "Steuer", nil)
	auto := mustTag(t, st, alice, "Auto", nil)
	telekom := mustCorrespondent(t, st, alice, "Telekom")
	vodafone := mustCorrespondent(t, st, alice, "Vodafone")
	invoice, err := st.CreateDocumentType(ctx, alice, store.DocumentTypeInput{Name: "Rechnung"})
	if err != nil {
		t.Fatal(err)
	}
	contract, err := st.CreateDocumentType(ctx, alice, store.DocumentTypeInput{Name: "Vertrag"})
	if err != nil {
		t.Fatal(err)
	}
	doc := textDoc(t, st, alice, "doc", "")
	other := textDoc(t, st, alice, "other", "")

	// Tags.
	if err := st.SetDocumentTags(ctx, alice, doc.ID, []string{steuer.ID}); err != nil {
		t.Fatal(err)
	}
	wantTitles(t, "tag added", find("Steuer"), "doc")
	if err := st.SetDocumentTags(ctx, alice, doc.ID, []string{auto.ID}); err != nil {
		t.Fatal(err)
	}
	wantTitles(t, "tag removed", find("Steuer"))
	wantTitles(t, "tag replaced", find("Auto"), "doc")

	// Correspondent and type.
	update := store.DocumentUpdate{Title: "doc", CorrespondentID: &telekom.ID, DocumentTypeID: &invoice.ID}
	if _, err := st.UpdateDocument(ctx, alice, doc.ID, update); err != nil {
		t.Fatal(err)
	}
	wantTitles(t, "correspondent set", find("Telekom"), "doc")
	wantTitles(t, "type set", find("Rechnung"), "doc")
	wantTitles(t, "tags kept", find("Auto"), "doc")
	update.CorrespondentID, update.DocumentTypeID = &vodafone.ID, &contract.ID
	if _, err := st.UpdateDocument(ctx, alice, doc.ID, update); err != nil {
		t.Fatal(err)
	}
	wantTitles(t, "old correspondent", find("Telekom"))
	wantTitles(t, "new correspondent", find("Vodafone"), "doc")
	wantTitles(t, "old type", find("Rechnung"))
	wantTitles(t, "new type", find("Vertrag"), "doc")

	// Renames and moves rewrite no document.
	before := xmin(t, conn, doc.ID)
	if _, err := st.UpdateCorrespondent(ctx, alice, vodafone.ID, store.CorrespondentInput{Name: "Vodafone Kabel"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateDocumentType(ctx, alice, contract.ID, store.DocumentTypeInput{Name: "Kontrakt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateTag(ctx, alice, auto.ID, store.TagInput{Name: "Fahrzeug"}); err != nil {
		t.Fatal(err)
	}
	wantTitles(t, "renamed correspondent", find("Kabel"), "doc")
	wantTitles(t, "renamed type, new name", find("Kontrakt"), "doc")
	wantTitles(t, "renamed type, old name", find("Vertrag"))
	wantTitles(t, "renamed tag, new name", find("Fahrzeug"), "doc")
	wantTitles(t, "renamed tag, old name", find("Auto"))
	if _, err := st.UpdateTag(ctx, alice, auto.ID, store.TagInput{Name: "Fahrzeug", ParentID: &steuer.ID}); err != nil {
		t.Fatal(err)
	}
	wantTitles(t, "moved tag, new parent", find("Steuer"), "doc")
	if after := xmin(t, conn, doc.ID); after != before {
		t.Errorf("renames and a move rewrote the document row (xmin %s → %s)", before, after)
	}

	// Deletes: each removes its lexeme from the document.
	links := func(when string, id string, want int) {
		t.Helper()
		var n int
		if err := conn.QueryRow(ctx, `SELECT length(search_links) FROM documents WHERE id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Errorf("%s: %d link lexemes, want %d", when, n, want)
		}
	}
	links("before the deletes", doc.ID, 3)
	if err := st.DeleteCorrespondent(ctx, alice, vodafone.ID); err != nil {
		t.Fatal(err)
	}
	links("deleted correspondent", doc.ID, 2)
	if err := st.DeleteDocumentType(ctx, alice, contract.ID); err != nil {
		t.Fatal(err)
	}
	links("deleted type", doc.ID, 1)
	if err := st.SetDocumentTags(ctx, alice, other.ID, []string{auto.ID}); err != nil {
		t.Fatal(err)
	}
	wantTitles(t, "two documents", find("Fahrzeug"), "doc", "other")
	if err := st.DeleteTag(ctx, alice, auto.ID); err != nil {
		t.Fatal(err)
	}
	links("deleted tag", doc.ID, 0)
	links("deleted tag, other document", other.ID, 0)
}

// One statement that changes the tags of many documents updates each
// document once.
func TestSearchTagStatementUpdatesEachDocumentOnce(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	conn := connect(t, url)
	a, b := mustTag(t, st, alice, "Alpha", nil), mustTag(t, st, alice, "Beta", nil)
	var ids []string
	for _, title := range []string{"one", "two", "three"} {
		ids = append(ids, textDoc(t, st, alice, title, "").ID)
	}
	// A test-only trigger counts the row updates of documents.
	if _, err := conn.Exec(ctx, `
		CREATE TABLE document_updates (id uuid NOT NULL);
		CREATE FUNCTION count_document_update() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN INSERT INTO document_updates VALUES (NEW.id); RETURN NULL; END $$;
		CREATE TRIGGER count_document_update AFTER UPDATE ON documents
			FOR EACH ROW EXECUTE FUNCTION count_document_update();`); err != nil {
		t.Fatal(err)
	}
	updates := func(sql string, args ...any) int64 {
		t.Helper()
		if _, err := conn.Exec(ctx, `TRUNCATE document_updates`); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
		var n, distinct int64
		if err := conn.QueryRow(ctx, `SELECT count(*), count(DISTINCT id) FROM document_updates`).Scan(&n, &distinct); err != nil {
			t.Fatal(err)
		}
		if distinct != 3 {
			t.Errorf("%d documents updated, want 3", distinct)
		}
		return n
	}
	if n := updates(`INSERT INTO document_tags (owner_id, document_id, tag_id)
		SELECT $1, d, t FROM unnest($2::uuid[]) d, unnest($3::uuid[]) t`, alice.UserID, ids, []string{a.ID, b.ID}); n != 3 {
		t.Errorf("insert of 6 links: %d document updates, want 3", n)
	}
	wantTitles(t, "both tags", searchIndex(t, conn, &alice.UserID, words("Alpha", "Beta")), "one", "two", "three")
	if n := updates(`DELETE FROM document_tags`); n != 3 {
		t.Errorf("delete of 6 links: %d document updates, want 3", n)
	}
	wantTitles(t, "no tags", searchIndex(t, conn, &alice.UserID, words("Alpha")))
}

func TestSearchScope(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	conn := connect(t, url)

	aliceTag := mustTag(t, st, alice, "Gemeinsam", nil)
	bobTag := mustTag(t, st, bob, "Gemeinsam", nil)
	bobSecret := mustTag(t, st, bob, "Bobsgeheimnis", nil)
	bobCorr := mustCorrespondent(t, st, bob, "Bobs Bank")
	in := newDoc("alice doc")
	in.TagIDs = []string{aliceTag.ID}
	mustDoc(t, st, alice, in)
	in = newDoc("bob doc")
	in.TagIDs = []string{bobTag.ID, bobSecret.ID}
	in.CorrespondentID = &bobCorr.ID
	mustDoc(t, st, bob, in)

	// The query runs against every document; only the tsquery's names are
	// scoped, so this shows that alice's query cannot match bob's links.
	me := &alice.UserID
	wantTitles(t, "a shared tag name", searchIndex(t, conn, me, words("Gemeinsam")), "alice doc")
	wantTitles(t, "bob's tag name", searchIndex(t, conn, me, words("Bobsgeheimnis")))
	wantTitles(t, "bob's correspondent name", searchIndex(t, conn, me, words("Bank")))
	for _, id := range []string{bobTag.ID, bobSecret.ID, bobCorr.ID, "t" + bobTag.ID, " t" + bobTag.ID} {
		wantTitles(t, "bob's link ID as text", searchIndex(t, conn, me, words(id)))
		wantTitles(t, "bob's link ID as a phrase", searchIndex(t, conn, me, []item{{term: id, phrase: true}}))
	}
	wantTitles(t, "admin, names per owner", searchIndex(t, conn, nil, words("Gemeinsam")), "alice doc", "bob doc")
	wantTitles(t, "admin, bob's names", searchIndex(t, conn, nil, words("Bobsgeheimnis")), "bob doc")
}

func TestSearchMigration(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := storetest.User(t, st, "alice", false)
	if err := st.MigrateDownTo(ctx, searchVersion-1); err != nil {
		t.Fatal(err)
	}
	conn := connect(t, url)
	if _, err := conn.Exec(ctx, "SELECT search_text FROM documents"); err == nil {
		t.Fatal("search_text still there after rolling back its migration")
	}
	var tag, doc string
	if err := conn.QueryRow(ctx, `INSERT INTO tags (owner_id, name) VALUES ($1, 'Altbestand') RETURNING id`, alice.ID).Scan(&tag); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `INSERT INTO documents (owner_id, title, extracted_text, archive_serial_number, source, original_sha256, original_size, original_mime, archive_sha256, archive_size)
		VALUES ($1, 'Alte Rechnung', 'Die Versicherungen zahlen.', 77, 'file', $2, 1, 'application/pdf', $2, 1) RETURNING id`, alice.ID, hashOf("old")).Scan(&doc); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO document_tags (owner_id, document_id, tag_id) VALUES ($1, $2, $3)`, alice.ID, doc, tag); err != nil {
		t.Fatal(err)
	}
	check := func(when string) {
		t.Helper()
		for _, q := range [][]string{{"Rechnungen"}, {"Versicherung"}, {"asn77"}, {"altbest"}, {"Rechnung", "Altbestand"}} {
			wantTitles(t, when+": "+strings.Join(q, " "), searchIndex(t, conn, &alice.ID, words(q...)), "Alte Rechnung")
		}
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	check("backfill")

	if err := st.MigrateDownTo(ctx, searchVersion-1); err != nil {
		t.Fatalf("down again: %v", err)
	}
	for _, sql := range []string{
		`SELECT 'paperless_simple'::regconfig`,
		`SELECT document_search_tsquery(NULL, '{}', '{}', '{}', '{}')`,
	} {
		if _, err := conn.Exec(ctx, sql); err == nil {
			t.Errorf("%s works after rolling back", sql)
		}
	}
	var ext int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_extension WHERE extname = 'unaccent'`).Scan(&ext); err != nil || ext != 0 {
		t.Errorf("unaccent left installed: %d, %v", ext, err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	check("up again")
}

// An update that assigns the indexed fields without changing them (as
// SetDocumentProcessed does with the text on every run) does not parse
// them again; a real change does.
func TestSearchTextSkipsUnchangedFields(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	conn := connect(t, url)
	doc := textDoc(t, st, alice, "Vertrag", "Kündigung zum Jahresende.")
	// Clearing the vector by hand marks it: only a new parse fills it.
	length := func(sql string) int {
		t.Helper()
		if _, err := conn.Exec(ctx, sql, doc.ID); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := conn.QueryRow(ctx, `SELECT length(search_text) FROM documents WHERE id = $1`, doc.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := length(`UPDATE documents SET search_text = '' WHERE id = $1`); n != 0 {
		t.Fatalf("cleared vector has %d lexemes", n)
	}
	if n := length(`UPDATE documents SET title = title, extracted_text = extracted_text, notes = notes WHERE id = $1`); n != 0 {
		t.Errorf("unchanged fields parsed again: %d lexemes", n)
	}
	if n := length(`UPDATE documents SET extracted_text = extracted_text || ' Ende.' WHERE id = $1`); n == 0 {
		t.Error("changed text not parsed")
	}
}
