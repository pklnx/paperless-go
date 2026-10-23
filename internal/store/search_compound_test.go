package store_test

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/search"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// Compound words (#56, part 2): document_search_tsquery ORs into a word
// the lexemes of the scope's search vocabulary that contain it
// (00021_search_infix.sql).

// infixVersion is the migration that adds the expansion.
const infixVersion = 21

// tsqueryOf returns document_search_tsquery's text for one item in the
// scope of owner (nil: all owners).
func tsqueryOf(t *testing.T, conn *pgx.Conn, owner *int64, it item) string {
	t.Helper()
	var q string
	if err := conn.QueryRow(context.Background(),
		`SELECT coalesce(document_search_tsquery($1, ARRAY[$2::text], ARRAY[$3::boolean], ARRAY[$4::boolean], '{1}')::text, '')`,
		owner, it.term, it.phrase, it.neg).Scan(&q); err != nil {
		t.Fatalf("tsquery of %+v: %v", it, err)
	}
	return q
}

// quotedLexemes returns the quoted lexemes of a tsquery's text, without
// prefix markers, sorted and deduplicated.
func quotedLexemes(q string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`'((?:[^']|'')*)'`).FindAllStringSubmatch(q, -1) {
		out = append(out, strings.ReplaceAll(m[1], "''", "'"))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func TestSearchCompoundWords(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	conn := connect(t, url)
	strom := mustTag(t, st, alice, "Strom", nil)

	in := newDoc("text")
	in.OriginalFileName = "scan.pdf"
	in.ExtractedText = "Die Stromrechnung für Juli."
	in.TagIDs = []string{strom.ID}
	mustDoc(t, st, alice, in)
	textDoc(t, st, alice, "Wasserrechnung", "")
	in = newDoc("notes")
	in.OriginalFileName = "scan.pdf"
	in.Notes = "Heizungsrechnung bezahlt"
	mustDoc(t, st, alice, in)
	in = newDoc("file")
	in.OriginalFileName = "Muellrechnung_2023.pdf"
	mustDoc(t, st, alice, in)
	textDoc(t, st, alice, "plain", "Eine Rechnung.")
	textDoc(t, st, alice, "prefix", "Rechnungsbetrag 12 Euro")
	textDoc(t, st, alice, "false hit", "Die Berechnung stimmt.") // accepted (#56, decision 1)
	textDoc(t, st, alice, "lease", "Der Mietvertrag")
	textDoc(t, st, alice, "statement", "Kontoauszug")
	all := []string{"text", "Wasserrechnung", "notes", "file", "plain", "prefix", "false hit", "lease", "statement"}
	rechnung := []string{"text", "Wasserrechnung", "notes", "file", "plain", "prefix", "false hit"}
	without := func(of []string, drop ...string) []string {
		return slices.DeleteFunc(slices.Clone(of), func(s string) bool { return slices.Contains(drop, s) })
	}

	// In the text, the title, the notes and the file name.
	wantFound(t, st, alice, "rechnung", rechnung...)
	wantFound(t, st, alice, "Rechnung", rechnung...)
	// The german stem: Rechnungen → rechnung finds the compounds; the
	// stem's own prefixes are not added (Rechnungsbetrag), as before.
	wantFound(t, st, alice, "Rechnungen", without(rechnung, "prefix")...)
	wantFound(t, st, alice, "stromrechnung", "text")
	wantFound(t, st, alice, "Rechn", rechnung...) // a prefix and, from 4 characters, inside words
	wantFound(t, st, alice, "-rechnung", "lease", "statement")
	wantFound(t, st, alice, "rechnung OR vertrag", without(all, "statement")...)
	wantFound(t, st, alice, "rechnung tag:Strom", "text")
	wantFound(t, st, alice, "rechnung -Juli", without(rechnung, "text")...)
	wantFound(t, st, alice, "vertrag -rechnung", "lease")

	// Phrases keep exact words; a word under 4 characters, or one that
	// makes several lexemes, does not expand.
	wantFound(t, st, alice, `"Strom Rechnung"`)
	wantFound(t, st, alice, `"Rechnung für Juli"`)
	wantFound(t, st, alice, `"Stromrechnung für Juli"`, "text")
	wantFound(t, st, alice, "chn")
	wantFound(t, st, alice, "chnu", rechnung...)
	me := &alice.UserID
	if q := tsqueryOf(t, conn, me, item{term: "Strom-Rechnung"}); strings.Contains(q, "wasserrechnung") {
		t.Errorf("Strom-Rechnung expanded: %s", q)
	}
	if q := tsqueryOf(t, conn, me, item{term: "Stromrechnung Wasser", phrase: true}); strings.Contains(q, "wasserrechnung") {
		t.Errorf("a phrase expanded: %s", q)
	}

	// The bulk change by search (#7) matches what the search finds.
	for _, s := range []string{"rechnung", "-rechnung", "rechnung OR vertrag"} {
		q := parseQuery(t, s, nil)
		m, err := st.CountBulkMatches(ctx, alice, store.BulkSelection{Match: &q})
		if err != nil {
			t.Fatal(err)
		}
		if n := len(found(t, st, alice, s)); m.Count != n {
			t.Errorf("%q: %d bulk matches, %d hits", s, m.Count, n)
		}
	}

	// A compound counts like the word itself: in the title it ranks above
	// the word in the text.
	q := parseQuery(t, "wasser", nil)
	if got := hitTitles(runSearch(t, st, alice, q)); !slices.Equal(got, []string{"Wasserrechnung"}) {
		t.Fatalf("prefix: %v", got)
	}
	got := hitTitles(runSearch(t, st, alice, parseQuery(t, "rechnung", nil)))
	if len(got) == 0 || got[0] != "Wasserrechnung" {
		t.Errorf("relevance: %v, want the compound in the title first", got)
	}
}

// LIKE wildcards in a word are literal. A file path is one lexeme that may
// hold an underscore.
func TestSearchCompoundWildcards(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	conn := connect(t, url)
	textDoc(t, st, alice, "underscore", "Abgelegt unter pfad/abc_def heute")
	textDoc(t, st, alice, "letter", "Abgelegt unter pfad/abcxdef heute")
	me := &alice.UserID
	wantTitles(t, "underscore", searchIndex(t, conn, me, words("/abc_def")), "underscore")
	wantTitles(t, "backslash", searchIndex(t, conn, me, words(`/abc\def`)))
}

// Umlauts (must-do 8 of the plan review): the unaccented paperless_simple
// lexeme carries the compound, and its snippet is marked.
func TestSearchCompoundUmlauts(t *testing.T) {
	st := storetest.New(t)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	textDoc(t, st, alice, "bakery", "Brötchen von der Großbäckerei Müller, frisch.")
	textDoc(t, st, alice, "other", "Brötchen vom Markt.")
	for _, q := range []string{"bäckerei", "Backerei", "BÄCKEREI"} {
		wantFound(t, st, alice, q, "bakery")
		page := runSearch(t, st, alice, parseQuery(t, q, nil))
		if want := store.SnippetStart + "Großbäckerei" + store.SnippetStop; len(page.Hits) != 1 || !strings.Contains(page.Hits[0].Snippet, want) {
			t.Errorf("%q: hits %v, want one with %q in the snippet", q, page.Hits, want)
		}
	}
	wantFound(t, st, alice, "-bäckerei", "other")
}

// Numbers (must-do 8): a number of 4 or more digits is found inside longer
// tokens too, a date, an invoice number or a larger number, like a word.
func TestSearchCompoundNumbers(t *testing.T) {
	st := storetest.New(t)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	textDoc(t, st, alice, "year", "Steuerjahr 2023")
	textDoc(t, st, alice, "compact date", "Beleg 20230115") // a prefix, as before
	textDoc(t, st, alice, "date", "Zahlbar bis 15.01.2023")
	textDoc(t, st, alice, "invoice number", "Rechnungsnummer RE2023-001")
	textDoc(t, st, alice, "amount", "Betrag 12023 Euro")
	textDoc(t, st, alice, "other", "Betrag 2024 Euro")
	wantFound(t, st, alice, "2023", "year", "compact date", "date", "invoice number", "amount")
	wantFound(t, st, alice, "202", "year", "compact date", "other") // 3 digits: prefix only
}

// The cap (decision 3): at most 100 compounds per word, the most frequent
// first, then by lexeme; X and -X use the same set.
func TestSearchCompoundCap(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	conn := connect(t, url)
	// 100 compounds in one document each and zzzrechnung in two: it comes
	// first, then the first 99 of the others by lexeme.
	var compounds []string
	for i := range 100 {
		compounds = append(compounds, fmt.Sprintf("x%c%crechnung", 'a'+i/26, 'a'+i%26))
	}
	for _, c := range compounds {
		textDoc(t, st, alice, c, c)
	}
	textDoc(t, st, alice, "zzz 1", "zzzrechnung")
	textDoc(t, st, alice, "zzz 2", "zzzrechnung")
	textDoc(t, st, alice, "unrelated", "Vertrag")

	me := &alice.UserID
	want := append([]string{"rechnung"}, compounds[:99]...)
	want = append(want, "zzzrechnung")
	slices.Sort(want)
	pos, neg := tsqueryOf(t, conn, me, item{term: "rechnung"}), tsqueryOf(t, conn, me, item{term: "rechnung", neg: true})
	if got := quotedLexemes(pos); !slices.Equal(got, want) {
		t.Errorf("lexemes of rechnung: %q, want %q", got, want)
	}
	if got := quotedLexemes(neg); !slices.Equal(got, want) {
		t.Errorf("lexemes of -rechnung: %q, want %q", got, want)
	}
	if got := strings.Count(pos, "|") + 1; got != 4+100 {
		t.Errorf("rechnung: %d alternatives, want 3 words, 1 prefix and 100 compounds: %s", got, pos)
	}

	// More than a page: the SQL function against every document.
	hits := searchIndex(t, conn, me, words("rechnung"))
	if len(hits) != 101 || slices.Contains(hits, compounds[99]) || !slices.Contains(hits, "zzz 1") {
		t.Errorf("rechnung: %d hits %q", len(hits), hits)
	}
	// -X excludes exactly what X finds.
	var all int
	if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM documents`).Scan(&all); err != nil {
		t.Fatal(err)
	}
	rest := searchIndex(t, conn, me, []item{{term: "rechnung", neg: true}})
	if len(rest)+len(hits) != all || slices.ContainsFunc(rest, func(s string) bool { return slices.Contains(hits, s) }) {
		t.Errorf("-rechnung: %q", rest)
	}
	if !slices.Equal(rest, []string{"unrelated", compounds[99]}) {
		t.Errorf("-rechnung: %q", rest)
	}
}

// Scope (privacy): the expansion uses only the vocabulary the search may
// see. A user's word never expands into another user's compounds; an admin
// with "Everyone's documents" expands over all owners, with "My
// documents" over their own.
func TestSearchCompoundScope(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	adminID := storetest.User(t, st, "admin", true).ID
	everyone := store.Scope{UserID: adminID, All: true}
	mine := store.Scope{UserID: adminID}
	conn := connect(t, url)
	textDoc(t, st, alice, "alice", "Alicesrechnung")
	textDoc(t, st, bob, "bob", "Bobsrechnung")
	textDoc(t, st, mine, "admin", "Adminsrechnung")

	wantFound(t, st, alice, "rechnung", "alice")
	wantFound(t, st, bob, "rechnung", "bob")
	wantFound(t, st, mine, "rechnung", "admin")
	wantFound(t, st, everyone, "rechnung", "admin", "alice", "bob")
	wantFound(t, st, everyone, "-rechnung")

	for _, c := range []struct {
		name  string
		owner *int64
		want  []string
	}{
		{"alice", &alice.UserID, []string{"alicesrechnung"}},
		{"bob", &bob.UserID, []string{"bobsrechnung"}},
		{"admin, my documents", &mine.UserID, []string{"adminsrechnung"}},
		{"admin, everyone", nil, []string{"adminsrechnung", "alicesrechnung", "bobsrechnung"}},
	} {
		got := slices.DeleteFunc(quotedLexemes(tsqueryOf(t, conn, c.owner, item{term: "rechnung"})),
			func(s string) bool { return s == "rechnung" })
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: compounds %q, want %q", c.name, got, c.want)
		}
	}
}

// Snippets mark a compound that only the expansion matched.
func TestSearchCompoundSnippets(t *testing.T) {
	st := storetest.New(t)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	textDoc(t, st, alice, "power", "Anbei die Stromrechnung für den Monat Juli.")
	page := runSearch(t, st, alice, parseQuery(t, "rechnung", nil))
	if want := store.SnippetStart + "Stromrechnung" + store.SnippetStop; len(page.Hits) != 1 || !strings.Contains(page.Hits[0].Snippet, want) {
		t.Fatalf("snippet %v, want it to contain %q", page.Hits, want)
	}
	q := parseQuery(t, "rechnung", nil)
	q.Sort = search.SortDate
	if page := runSearch(t, st, alice, q); len(page.Hits) != 1 || !strings.Contains(page.Hits[0].Snippet, "Stromrechnung"+store.SnippetStop) {
		t.Fatalf("sort=date: %v", page.Hits)
	}
}

// The lookup runs through EXECUTE … USING, so it is planned with its
// values: EXPLAIN of the function's own statement with values uses the
// trigram index.
func TestSearchCompoundLookupUsesTrigramIndex(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := storetest.User(t, st, "alice", false)
	bob := storetest.User(t, st, "bob", false)
	conn := connect(t, url)
	// A vocabulary of 100 000 rows over two owners, written directly: the
	// test needs its size, not its documents.
	for _, sql := range []string{`
		INSERT INTO search_vocabulary (owner_id, lexeme, n)
		SELECT o, translate(lpad(to_hex(i), 6, '0'), '0123456789abcdef', 'bcdfghjklmnpqrst'), 1 + i % 7
		FROM generate_series(1, 50000) i, unnest(ARRAY[$1::bigint, $2::bigint]) o`,
		`INSERT INTO search_vocabulary (owner_id, lexeme, n) VALUES ($1, 'stromrechnung', 3), ($2, 'gasrechnung', 1)`,
	} {
		if _, err := conn.Exec(ctx, sql, alice.ID, bob.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Exec(ctx, `ANALYZE search_vocabulary`); err != nil {
		t.Fatal(err)
	}
	var src string
	if err := conn.QueryRow(ctx, `SELECT prosrc FROM pg_proc WHERE proname = 'document_search_tsquery'`).Scan(&src); err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)\$lookup\$(.*?)\$lookup\$`).FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("no $lookup$ statement in document_search_tsquery")
	}
	for _, owner := range []*int64{&alice.ID, nil} {
		rows, err := conn.Query(ctx, "EXPLAIN "+m[1], pgx.QueryExecModeCacheDescribe, owner,
			[]string{"%rechnung%", "%rechn%"}, []string{"rechnung%", "rechn%"})
		if err != nil {
			t.Fatal(err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		if plan := strings.Join(lines, "\n"); !strings.Contains(plan, "search_vocabulary_trgm_idx") {
			t.Errorf("owner %v: the lookup does not use the trigram index:\n%s", owner, plan)
		}
	}
	if q := tsqueryOf(t, conn, &alice.ID, item{term: "rechnung"}); !strings.Contains(q, "'stromrechnung'") {
		t.Errorf("tsquery %s", q)
	}
}

// Down restores 00011's function: no expansion; up again expands.
func TestSearchCompoundMigration(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	conn := connect(t, url)
	textDoc(t, st, alice, "power", "Stromrechnung")
	textDoc(t, st, alice, "plain", "Rechnung")
	me := &alice.UserID
	wantTitles(t, "up", searchIndex(t, conn, me, words("rechnung")), "plain", "power")
	if err := st.MigrateDownTo(ctx, infixVersion-1); err != nil {
		t.Fatal(err)
	}
	wantTitles(t, "down", searchIndex(t, conn, me, words("rechnung")), "plain")
	wantTitles(t, "down, prefix", searchIndex(t, conn, me, words("strom")), "power")
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	wantTitles(t, "up again", searchIndex(t, conn, me, words("rechnung")), "plain", "power")
}

// Trash (#12): a trashed document's words stay in the vocabulary until it
// is purged, but a live search, compound or not, never returns it; the
// trash's own search finds it by its compound.
func TestSearchCompoundTrash(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	textDoc(t, st, alice, "live", "Gasrechnung")
	trashed := textDoc(t, st, alice, "trashed", "Stromrechnung")
	textDoc(t, st, alice, "other", "Vertrag")
	if err := st.TrashDocument(ctx, alice, trashed.ID); err != nil {
		t.Fatal(err)
	}
	wantFound(t, st, alice, "rechnung", "live")
	wantFound(t, st, alice, "stromrechnung")
	wantFound(t, st, alice, "-rechnung", "other")
	wantFound(t, st, alice, "rechnung OR vertrag", "live", "other")
	for _, s := range []string{"rechnung", "-gasrechnung"} {
		q := parseQuery(t, s, nil)
		m, err := st.CountBulkMatches(ctx, alice, store.BulkSelection{Match: &q})
		if err != nil {
			t.Fatal(err)
		}
		if m.Count != 1 {
			t.Errorf("%q: %d bulk matches, want 1 live document", s, m.Count)
		}
	}
	q := parseQuery(t, "rechnung", nil)
	q.Trashed = true
	if got := hitTitles(runSearch(t, st, alice, q)); !slices.Equal(got, []string{"trashed"}) {
		t.Errorf("trash search: %v", got)
	}
	if _, err := st.RestoreDocument(ctx, alice, trashed.ID); err != nil {
		t.Fatal(err)
	}
	wantFound(t, st, alice, "rechnung", "live", "trashed")
}

// tsqueryOfItems returns document_search_tsquery's text for AND items in
// the scope of owner.
func tsqueryOfItems(t *testing.T, conn *pgx.Conn, owner *int64, terms ...string) string {
	t.Helper()
	n := len(terms)
	groups := make([]int32, n)
	for i := range groups {
		groups[i] = int32(-1 - i)
	}
	var q string
	if err := conn.QueryRow(context.Background(),
		`SELECT coalesce(document_search_tsquery($1, $2, $3, $4, $5)::text, '')`,
		owner, terms, make([]bool, n), make([]bool, n), groups).Scan(&q); err != nil {
		t.Fatalf("tsquery of %q: %v", terms, err)
	}
	return q
}

// The cap per search (#140): at most 250 compounds per search, shared out
// fairly. A word that finds fewer than an equal share keeps all of its
// own, the others split the rest, each at most 100, the most frequent
// first; the result is the same on every call.
func TestSearchCompoundCapPerSearch(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := storetest.User(t, st, "alice", false)
	conn := connect(t, url)
	// Vocabulary rows written directly: 120 compounds each of five roots
	// with falling counts, and 10 of mahnung.
	roots := []string{"rechnung", "vertrag", "kosten", "beitrag", "auftrag"}
	for _, root := range roots {
		if _, err := conn.Exec(ctx, `INSERT INTO search_vocabulary (owner_id, lexeme, n)
			SELECT $1, 'x' || lpad(i::text, 3, '0') || $2, 1000 - i FROM generate_series(0, 119) i`, alice.ID, root); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Exec(ctx, `INSERT INTO search_vocabulary (owner_id, lexeme, n)
		SELECT $1, 'x' || lpad(i::text, 3, '0') || 'mahnung', 1000 - i FROM generate_series(0, 9) i`, alice.ID); err != nil {
		t.Fatal(err)
	}
	// per returns, for each root, which compounds of it the tsquery holds.
	per := func(q string) map[string][]string {
		out := map[string][]string{}
		for _, l := range quotedLexemes(q) {
			if !strings.HasPrefix(l, "x") {
				continue
			}
			for _, root := range append(slices.Clone(roots), "mahnung") {
				if strings.HasSuffix(l, root) {
					out[root] = append(out[root], l)
				}
			}
		}
		return out
	}
	first := func(root string, k int) []string {
		var want []string
		for i := range k {
			want = append(want, fmt.Sprintf("x%03d%s", i, root))
		}
		return want
	}
	for _, c := range []struct {
		terms []string
		want  map[string]int
	}{
		{[]string{"rechnung"}, map[string]int{"rechnung": 100}},
		{[]string{"rechnung", "vertrag"}, map[string]int{"rechnung": 100, "vertrag": 100}},
		{[]string{"rechnung", "vertrag", "kosten"}, map[string]int{"rechnung": 83, "vertrag": 83, "kosten": 84}},
		{[]string{"mahnung", "rechnung", "vertrag"}, map[string]int{"mahnung": 10, "rechnung": 100, "vertrag": 100}},
		{roots, map[string]int{"rechnung": 50, "vertrag": 50, "kosten": 50, "beitrag": 50, "auftrag": 50}},
		// A word without compounds and a phrase take no share.
		{[]string{"rechnung", "nichts", `vertrag kosten`, "kosten"}, map[string]int{"rechnung": 100, "kosten": 100}},
	} {
		terms := c.terms
		phrase := slices.Index(terms, `vertrag kosten`)
		var q string
		if phrase < 0 {
			q = tsqueryOfItems(t, conn, &alice.ID, terms...)
		} else {
			n := len(terms)
			isPhrase := make([]bool, n)
			isPhrase[phrase] = true
			groups := make([]int32, n)
			for i := range groups {
				groups[i] = int32(-1 - i)
			}
			if err := conn.QueryRow(ctx, `SELECT coalesce(document_search_tsquery($1, $2, $3, $4, $5)::text, '')`,
				&alice.ID, terms, isPhrase, make([]bool, n), groups).Scan(&q); err != nil {
				t.Fatal(err)
			}
		}
		got := per(q)
		total := 0
		for root, k := range c.want {
			if !slices.Equal(got[root], first(root, k)) {
				t.Errorf("%q: %s: %d compounds %q, want the first %d", terms, root, len(got[root]), got[root], k)
			}
			total += k
		}
		if len(got) != len(c.want) {
			t.Errorf("%q: compounds of %v, want %v", terms, got, c.want)
		}
		if total > 250 {
			t.Fatalf("test case over 250: %d", total)
		}
		if again := tsqueryOfItems(t, conn, &alice.ID, terms...); phrase < 0 && again != q {
			t.Errorf("%q: another call gave another tsquery", terms)
		}
	}
	// 32 words of which every one finds many: 7 or 8 each, 250 in all.
	var many []string
	for i := range 32 {
		root := fmt.Sprintf("qk%c%czt", 'a'+i/26, 'a'+i%26) // no stemmer changes it
		many = append(many, root)
		if _, err := conn.Exec(ctx, `INSERT INTO search_vocabulary (owner_id, lexeme, n)
			SELECT $1, 'x' || lpad(i::text, 3, '0') || $2, 1000 - i FROM generate_series(0, 19) i`, alice.ID, root); err != nil {
			t.Fatal(err)
		}
	}
	q := tsqueryOfItems(t, conn, &alice.ID, many...)
	n := 0
	for _, l := range quotedLexemes(q) {
		if strings.HasPrefix(l, "x") {
			n++
		}
	}
	if n != 250 {
		t.Errorf("32 words: %d compounds, want 250", n)
	}
}
