//go:build searchbench

package store_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/search"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// TestSearchBench measures the search on generated documents (#6, part 2;
// #56, parts 1 and 2):
//
//	TEST_DATABASE_URL=... go test -tags searchbench -run TestSearchBench -v -timeout 4h ./internal/store/
//
// SEARCHBENCH_DOCS sets alice's number of documents (default 100 000), with
// about 5 000 characters of synthetic German and English text each.
// "rechnung" is in half of them, "zahlungserinnerung" in 0.8 %; the
// compounds stromrechnung (5 %), gasrechnung (2 %), berechnung (2 %) and
// rechnungsbetrag (5 %) are in documents without "rechnung", and no other
// word of the generator contains "rechnung", so the compound shares hold
// for the expansion of part 2 (berechnung is its false hit). 30 % of the
// words are a long tail of letter tokens (benchTail), which gives each
// owner well over 1.5 million distinct lexemes, closer to an OCR archive.
// bob has half as many documents with twice the share of tail words, so
// his vocabulary is about as large: the trigram index is not per owner.
//
// SEARCHBENCH_BEFORE=<version> runs the same generator on a database
// migrated only to that version, and at the end times migrating up: 17 is
// part 1's "before" (no vocabulary; the migration times the backfill), 20
// part 2's (the vocabulary and the trash, without the expansion of 00021).
// SEARCHBENCH_RECHECK=1 re-measures #115's failed cases with and without
// the expansion on the same data (#140). SEARCHBENCH_FILLFACTOR sets the
// fillfactor of search_vocabulary before the generation. "Before" and
// "after" are two runs on the same host and server, with the same seed.
//
// It logs the timings of each case, EXPLAIN (ANALYZE, BUFFERS) of the
// queries the store ran, hit counts of the compound cases, the
// per-document write paths (with the vocabulary also once more after
// dropping its foreign key to users) and the sizes. It is not run in CI
// and asserts nothing.
func TestSearchBench(t *testing.T) {
	n := 100000
	if v := os.Getenv("SEARCHBENCH_DOCS"); v != "" {
		var err error
		if n, err = strconv.Atoi(v); err != nil {
			t.Fatal(err)
		}
	}
	var beforeVersion int64
	if v := os.Getenv("SEARCHBENCH_BEFORE"); v != "" {
		beforeVersion = int64(atoi(t, v))
	}
	before := beforeVersion != 0
	// hasVocabulary: the database has search_vocabulary (00018).
	hasVocabulary := !before || beforeVersion >= vocabularyVersion
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	// The benchmark measures how long a search takes, so no time limit.
	st.SearchTimeout = 0
	if before {
		if err := st.MigrateDownTo(ctx, beforeVersion); err != nil {
			t.Fatal(err)
		}
	}
	conn := connect(t, url)
	var version string
	if err := conn.QueryRow(ctx, `SHOW server_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	mode := "after (all migrations)"
	if before {
		mode = fmt.Sprintf("before (migrated to %05d)", beforeVersion)
	}
	t.Logf("PostgreSQL %s, %d documents of alice, %d of bob, %s, %d CPUs", version, n, n/2, mode, runtime.NumCPU())
	if ff := os.Getenv("SEARCHBENCH_FILLFACTOR"); ff != "" && hasVocabulary {
		if _, err := conn.Exec(ctx, "ALTER TABLE search_vocabulary SET (fillfactor = "+strconv.Itoa(atoi(t, ff))+")"); err != nil {
			t.Fatal(err)
		}
		t.Logf("search_vocabulary fillfactor %s", ff)
	}

	steuer := mustTag(t, st, alice, "Steuer", nil)
	if _, err := conn.Exec(ctx, `SELECT setseed(0.53)`); err != nil {
		t.Fatal(err)
	}
	generate := func(who string, owner int64, count int, tail float64) {
		start := time.Now()
		// 100 documents per statement and transaction. One transaction
		// that adds thousands of documents is slow with the vocabulary
		// trigger: each document's upsert rewrites the rows of the
		// common lexemes, whose versions cannot be pruned before the
		// commit, so every lookup walks a longer chain. Import writes
		// one document per transaction.
		const batch = 100
		for lo := 1; lo <= count; lo += batch {
			hi := min(lo+batch-1, count)
			if _, err := conn.Exec(ctx, `
				INSERT INTO documents (owner_id, title, source, original_sha256, original_size, original_mime,
				                       extracted_text, created_at, document_date, is_inbox)
				SELECT $1, 'Dokument ' || i, 'file', encode(sha256(i::text::bytea), 'hex'), 1, 'application/pdf',
				       CASE WHEN i % 2 = 0 THEN 'Rechnung ' ELSE '' END
				       || CASE WHEN i % 125 = 0 THEN 'Zahlungserinnerung ' ELSE '' END
				       || CASE WHEN i % 20 = 1 THEN 'Stromrechnung ' ELSE '' END
				       || CASE WHEN i % 20 = 9 THEN 'Rechnungsbetrag ' ELSE '' END
				       || CASE WHEN i % 50 = 3 THEN 'Gasrechnung ' ELSE '' END
				       || CASE WHEN i % 50 = 7 THEN 'Berechnung ' ELSE '' END
				       || (SELECT string_agg(CASE WHEN random() >= $5 THEN ($4::text[])[1 + floor(random() * cardinality($4::text[]))::int]
				                                  ELSE translate(lpad(to_hex(floor(random() * $6)::int), 6, '0'), '0123456789abcdef', $7) END, ' ')
				           FROM generate_series(1, 550 + i % 200) g WHERE i > 0),
				       timestamptz '2026-01-01' - i * interval '7 minutes',
				       timestamptz '2026-01-01' - (i % 3000) * interval '1 day', i % 10 = 0
				FROM generate_series($2::int, $3::int) i`, owner, lo, hi, benchVocab, tail, benchTailSize, benchTailLetters); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("generated %d documents of %s in %v", count, who, time.Since(start).Round(time.Second))
	}
	start := time.Now()
	generate("alice", alice.UserID, n, 0.3)
	generate("bob", bob.UserID, n/2, 0.6)
	if _, err := conn.Exec(ctx, `
		INSERT INTO document_tags (owner_id, document_id, tag_id)
		SELECT owner_id, id, $1 FROM documents WHERE owner_id = $2 AND title LIKE '%0'`, steuer.ID, alice.UserID); err != nil {
		t.Fatal(err)
	}
	analyze := func() {
		for _, table := range []string{"documents", "document_tags", "search_vocabulary"} {
			if table == "search_vocabulary" && !hasVocabulary {
				continue
			}
			if _, err := conn.Exec(ctx, "VACUUM ANALYZE "+table); err != nil {
				t.Fatal(err)
			}
		}
	}
	analyze()
	var avg float64
	if err := conn.QueryRow(ctx, `SELECT avg(char_length(extracted_text)) FROM documents`).Scan(&avg); err != nil {
		t.Fatal(err)
	}
	t.Logf("generated in %v: %.0f characters of text on average", time.Since(start).Round(time.Second), avg)
	sizes := func(when string) {
		var docs, docsIdx int64
		if err := conn.QueryRow(ctx, `SELECT pg_total_relation_size('documents'), pg_relation_size('documents_search_idx')`).Scan(&docs, &docsIdx); err != nil {
			t.Fatal(err)
		}
		line := fmt.Sprintf("sizes %s: documents %s in total, documents_search_idx %s", when, mb(docs), mb(docsIdx))
		var exists bool
		if err := conn.QueryRow(ctx, `SELECT to_regclass('search_vocabulary') IS NOT NULL`).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			var rowsA, rowsB, heap, pkey, trgm, total int64
			if err := conn.QueryRow(ctx, `SELECT count(*) FILTER (WHERE owner_id = $1), count(*) FILTER (WHERE owner_id = $2),
				pg_table_size('search_vocabulary'), pg_relation_size('search_vocabulary_pkey'),
				pg_relation_size('search_vocabulary_trgm_idx'), pg_total_relation_size('search_vocabulary')
				FROM search_vocabulary`, alice.UserID, bob.UserID).Scan(&rowsA, &rowsB, &heap, &pkey, &trgm, &total); err != nil {
				t.Fatal(err)
			}
			line += fmt.Sprintf("; search_vocabulary %d rows of alice, %d of bob, table %s, pkey %s, trgm %s, %s in total (%.1f %% of documents)",
				rowsA, rowsB, mb(heap), mb(pkey), mb(trgm), mb(total), 100*float64(total)/float64(docs))
		}
		t.Log(line)
	}
	sizes("after the generation")

	var last struct {
		sql  string
		args []any
	}
	store.RecordSearchSQL(st, func(sql string, args []any) {
		if !strings.Contains(sql, "name: SearchTsquery") {
			last.sql, last.args = sql, args
		}
	})
	explain := func(name string) {
		// The store runs the relevance query with sequential scans off.
		seqscan := "on"
		if strings.Contains(last.sql, "name: SearchDocumentsRelevance") {
			seqscan = "off"
		}
		if _, err := conn.Exec(ctx, "SET enable_seqscan = "+seqscan); err != nil {
			t.Fatal(err)
		}
		rows, err := conn.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+last.sql, append([]any{pgx.QueryExecModeCacheDescribe}, last.args...)...)
		if err != nil {
			t.Fatal(err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("EXPLAIN (ANALYZE, BUFFERS) %s:\n%s", name, strings.Join(lines, "\n"))
	}
	stat := func(name string, runs int, fn func()) {
		fn() // warm the cache
		times := make([]time.Duration, runs)
		for i := range runs {
			start := time.Now()
			fn()
			times[i] = time.Since(start)
		}
		slices.Sort(times)
		t.Logf("%-55s p50 %8v  p95 %8v  max %8v  (%d runs)", name,
			times[runs/2].Round(time.Microsecond), times[runs*95/100].Round(time.Microsecond),
			times[runs-1].Round(time.Microsecond), runs)
	}
	measure := func(name string, runs int, fn func()) {
		stat(name, runs, fn)
		explain(name)
	}
	search1 := func(q search.Query) func() {
		return func() {
			if _, err := st.SearchDocuments(ctx, alice, q); err != nil {
				t.Fatal(err)
			}
		}
	}
	query := func(s string, sort search.Sort) search.Query {
		q, err := search.Parse(s, nil)
		if err != nil {
			t.Fatal(err)
		}
		q.Sort, q.Limit = sort, 25
		return q
	}

	measure("selective (0.8 %), relevance, 25 snippets", 40, search1(query("Zahlungserinnerung", "")))
	measure("selective (0.8 %), sort=added, 25 snippets", 40, search1(query("Zahlungserinnerung", search.SortAdded)))
	measure("selective prefix zahlungserinn, relevance", 40, search1(query("zahlungserinn", "")))
	measure("broad (50 %), sort=added", 20, search1(query("Rechnung", search.SortAdded)))
	measure("broad (50 %), sort=date", 20, search1(query("Rechnung", search.SortDate)))
	measure("broad (50 %), relevance", 10, search1(query("Rechnung", "")))
	measure("broad AND selective, relevance", 20, search1(query("Rechnung Zahlungserinnerung", "")))
	// Only negated items (#54): every hit ranks 0, so the default is
	// newest added; relevance on request.
	measure("negation only -rechnung (50 %), default sort", 20, search1(query("-Rechnung", "")))
	measure("negation only -zahlungserinnerung (99 %), default sort", 20, search1(query("-Zahlungserinnerung", "")))
	measure("negation only -zahlungserinnerung, sort=relevance", 10, search1(query("-Zahlungserinnerung", search.SortRelevance)))
	measure("tag:Steuer (10 %), sort=added", 20, search1(query("tag:Steuer", "")))

	// Compound words (#56, part 2). Before 00021 these find only exact
	// words and prefixes; the hit counts show the difference.
	measure("selective infix erinnerung (0.8 %), relevance", 40, search1(query("erinnerung", "")))
	measure("selective infix erinnerung, sort=added", 40, search1(query("erinnerung", search.SortAdded)))
	measure("broad infix rechnung, relevance", 10, search1(query("rechnung", "")))
	measure("broad infix rechnung, sort=added", 20, search1(query("rechnung", search.SortAdded)))
	measure("compound stromrechnung (5 %), relevance", 40, search1(query("stromrechnung", "")))
	// 32 items of 4 characters, the parser's maximum: 16 of the common
	// words' parts and 16 of the tail's, each matching at least 100
	// vocabulary lexemes of alice.
	worst := strings.Fields("rech nung zahl trag kund steu wohn miet beit glie bitt tage ment tung ungs erha " +
		"bcdf ghjk lmnp qrst dfgh jklm npqr stbc cdfg hjkl mnpq rstb fghj klmn pqrs tbcd")
	measure("32 items of 4 characters, AND, relevance", 10, search1(query(strings.Join(worst, " "), "")))
	measure("32 items of 4 characters, OR, sort=added", 10, search1(query(strings.Join(worst, " OR "), search.SortAdded)))
	// Ranking every match against thousands of alternatives is slow (#115:
	// 4 min 55 s before the expansion, over an hour with it on 100 000
	// documents), so it runs only with SEARCHBENCH_OR_RELEVANCE=1: one
	// cold run, no EXPLAIN (it would run the query again).
	if os.Getenv("SEARCHBENCH_OR_RELEVANCE") == "1" {
		start = time.Now()
		search1(query(strings.Join(worst, " OR "), ""))()
		t.Logf("%-55s %v (1 run)", "32 items of 4 characters, OR, relevance", time.Since(start).Round(time.Millisecond))
	}
	tsquery := func(owner *int64, terms ...string) string {
		n := len(terms)
		groups := make([]int32, n)
		for i := range groups {
			groups[i] = int32(i)
		}
		var q string
		if err := conn.QueryRow(ctx, `SELECT coalesce(document_search_tsquery($1, $2, $3, $4, $5)::text, '')`,
			owner, terms, make([]bool, n), make([]bool, n), groups).Scan(&q); err != nil {
			t.Fatal(err)
		}
		return q
	}
	for _, term := range []string{"rechnung", "erinnerung", "rech", "bcdf"} {
		stat("document_search_tsquery, one item "+term, 100, func() { tsquery(&alice.UserID, term) })
	}
	stat("document_search_tsquery, 32 items of 4 characters", 20, func() { tsquery(&alice.UserID, worst...) })
	stat("document_search_tsquery, admin over all owners, rechnung", 100, func() { tsquery(nil, "rechnung") })
	var src string
	if err := conn.QueryRow(ctx, `SELECT prosrc FROM pg_proc WHERE proname = 'document_search_tsquery'`).Scan(&src); err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`(?s)\$lookup\$(.*?)\$lookup\$`).FindStringSubmatch(src); m != nil {
		for _, c := range []struct {
			name  string
			owner *int64
			p     []string
		}{
			{"rechnung", &alice.UserID, []string{"rechnung"}},
			{"erinnerung", &alice.UserID, []string{"erinnerung"}},
			{"rechnungen (with its stem)", &alice.UserID, []string{"rechnungen", "rechnung"}},
			{"rech", &alice.UserID, []string{"rech"}},
			{"bcdf", &alice.UserID, []string{"bcdf"}},
			{"rech, admin over all owners", nil, []string{"rech"}},
		} {
			var like, prefix []string
			for _, p := range c.p {
				like, prefix = append(like, "%"+p+"%"), append(prefix, p+"%")
			}
			stat("vocabulary lookup alone, "+c.name, 100, func() {
				var found []string
				if err := conn.QueryRow(ctx, m[1], pgx.QueryExecModeCacheDescribe, c.owner, like, prefix).Scan(&found); err != nil {
					t.Fatal(err)
				}
			})
			rows, err := conn.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+m[1], pgx.QueryExecModeCacheDescribe, c.owner, like, prefix)
			if err != nil {
				t.Fatal(err)
			}
			lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("EXPLAIN (ANALYZE, BUFFERS) vocabulary lookup, %s:\n%s", c.name, strings.Join(lines, "\n"))
		}
	}
	hits := func(q string) int {
		var n int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM documents d WHERE d.owner_id = $1 AND (d.search_text || d.search_links) @@ $2::tsquery`,
			alice.UserID, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, term := range []string{"rechnung", "erinnerung", "stromrechnung", "rechnungen"} {
		q := tsquery(&alice.UserID, term)
		t.Logf("hits of %s: %d (%d alternatives)", term, hits(q), strings.Count(q, "|")+1)
	}
	// berechnung is the expansion's false hit: documents rechnung finds
	// that hold no word starting with rechnung and no other compound.
	if q := tsquery(&alice.UserID, "rechnung"); q != "" {
		var n int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM documents d WHERE d.owner_id = $1
			AND (d.search_text || d.search_links) @@ $2::tsquery
			AND NOT d.search_text @@ 'rechnung:* | stromrechnung | gasrechnung'::tsquery`, alice.UserID, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		t.Logf("hits of rechnung through berechnung only: %d", n)
	}

	// SEARCHBENCH_RECHECK=1 (#140): the cases that failed in #115, on the
	// same data with the expansion, without it (migrated down to 00020)
	// and with it again, so the order and the cache show. The OR ranking
	// runs once per round, without the time limit; at the end it runs once
	// more under the default limit.
	if os.Getenv("SEARCHBENCH_RECHECK") == "1" && !before {
		and, or := strings.Join(worst, " "), strings.Join(worst, " OR ")
		recheck := func(label string) {
			q := tsquery(&alice.UserID, worst...)
			t.Logf("%s: 32 items of 4 characters make %d alternatives", label, strings.Count(q, "|")+strings.Count(q, "&")+1)
			stat(label+": 32 items, AND, relevance", 10, search1(query(and, "")))
			stat(label+": 32 items, OR, sort=added", 10, search1(query(or, search.SortAdded)))
			start := time.Now()
			search1(query(or, ""))()
			t.Logf("%-55s %v (1 run)", label+": 32 items, OR, relevance", time.Since(start).Round(time.Millisecond))
			stat(label+": broad infix rechnung, relevance", 10, search1(query("rechnung", "")))
			stat(label+": selective infix erinnerung, relevance", 40, search1(query("erinnerung", "")))
			stat(label+": selective infix erinnerung, sort=added", 40, search1(query("erinnerung", search.SortAdded)))
		}
		recheck("after")
		if err := st.MigrateDownTo(ctx, infixVersion-1); err != nil {
			t.Fatal(err)
		}
		recheck("before")
		if _, err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		recheck("after again")
		st.SearchTimeout = store.DefaultSearchTimeout
		start := time.Now()
		_, err := st.SearchDocuments(ctx, alice, query(or, ""))
		t.Logf("32 items, OR, relevance under the default limit: %v after %v", err, time.Since(start).Round(time.Millisecond))
		st.SearchTimeout = 0
	}
	measure("plain list, first page", 40, search1(search.Query{Limit: 25}))

	var deepID string
	var deepAt time.Time
	if err := conn.QueryRow(ctx, `SELECT id::text, created_at FROM documents ORDER BY created_at DESC, id OFFSET $1 LIMIT 1`,
		n*9/10).Scan(&deepID, &deepAt); err != nil {
		t.Fatal(err)
	}
	cursor := base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, `{"s":"added","t":%d,"i":%q}`, deepAt.UnixMicro(), deepID))
	measure("plain list, page at 90 %", 40, search1(search.Query{Limit: 25, Cursor: cursor}))
	measure("plain list, sort=date, first page", 40, search1(search.Query{Limit: 25, Sort: search.SortDate}))

	start = time.Now()
	if _, err := st.UpdateTag(ctx, alice, steuer.ID, store.TagInput{Name: "Steuern"}); err != nil {
		t.Fatal(err)
	}
	t.Logf("rename a tag of 10 %% of the documents: %v", time.Since(start).Round(time.Microsecond))

	benchWrites(ctx, t, st, conn, alice, n, 0)
	analyze()
	sizes("after the writes")
	if hasVocabulary {
		// The foreign key of search_vocabulary to users costs one check
		// per new row (#120's review): the same writes again, with it as a
		// control and then without it.
		t.Log("write paths again, with the foreign key of search_vocabulary:")
		benchWrites(ctx, t, st, conn, alice, n, 1)
		if _, err := conn.Exec(ctx, `ALTER TABLE search_vocabulary DROP CONSTRAINT search_vocabulary_owner_id_fkey`); err != nil {
			t.Fatal(err)
		}
		t.Log("write paths again, without the foreign key of search_vocabulary:")
		benchWrites(ctx, t, st, conn, alice, n, 2)
	}

	if before {
		start := time.Now()
		if _, err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		t.Logf("migrate up from %05d (from 00017 on, with the backfill of the vocabulary and its indexes): %v",
			beforeVersion, time.Since(start).Round(time.Millisecond))
		hasVocabulary = true
		analyze()
		sizes("after the migration")
	}
}

// benchVocab are the generator's common words.
var benchVocab = strings.Fields(`Versicherung Vertrag Kündigung Zahlung Betrag Konto Überweisung Gebühren
	Steuer Finanzamt Bescheid Einkommen Abschlag Strom Wasser Miete Nebenkosten Vermieter Wohnung Straße
	Häuser Termin Arzt Krankenkasse Beitrag Mitglied Kunde Kundennummer Auftrag Lieferung Bestellung Datum
	sehr geehrte Damen Herren freundlichen Grüßen anbei erhalten Sie bitte innerhalb Tagen fällig zahlbar
	invoice insurance contract payment amount account transfer fees tax office notice income statement
	electricity water rent tenant apartment street houses appointment doctor member customer order delivery
	please find attached within days due payable meetings reports studies results the and of to in for with`)

// The long tail: a word of six letters from benchTailLetters (consonants,
// which no stemmer changes) for a number below benchTailSize, drawn
// uniformly.
const (
	benchTailSize    = 2000000
	benchTailLetters = "bcdfghjklmnpqrst"
)

// benchText is a text like the generator's, from Go: words common words
// with a share tail of tail words.
func benchText(r *rand.Rand, words int, tail float64) string {
	var b strings.Builder
	for i := range words {
		if i > 0 {
			b.WriteByte(' ')
		}
		if r.Float64() >= tail {
			b.WriteString(benchVocab[r.IntN(len(benchVocab))])
			continue
		}
		k := r.IntN(benchTailSize)
		var w [6]byte
		for j := 5; j >= 0; j-- {
			w[j] = benchTailLetters[k%16]
			k /= 16
		}
		b.Write(w[:])
	}
	return b.String()
}

// benchWrites measures the per-document write paths on alice's documents
// (the n generated ones are "Dokument 1" to "Dokument n"): text updates
// in SQL, deletes, imports with text, uploads and their OCR results
// through the store, and a retitle of 1 000 documents in one statement.
//
// round (0, 1, …) picks other documents and names, so the writes can be
// measured again on the same data.
func benchWrites(ctx context.Context, t *testing.T, st *store.Store, conn *pgx.Conn, alice store.Scope, n, round int) {
	r := rand.New(rand.NewPCG(53, 56+uint64(round)))
	timings := func(name string, runs int, fn func(i int)) {
		times := make([]time.Duration, runs)
		for i := range runs {
			start := time.Now()
			fn(i)
			times[i] = time.Since(start)
		}
		slices.Sort(times)
		t.Logf("%-55s p50 %8v  p95 %8v  max %8v  (%d runs)", name,
			times[runs/2].Round(time.Microsecond), times[runs*95/100].Round(time.Microsecond),
			times[runs-1].Round(time.Microsecond), runs)
	}
	// docID returns the generated document k, or the next one that an
	// earlier round has not deleted or retitled and this round has not
	// used yet.
	used := map[string]bool{}
	docID := func(k int) string {
		for ; ; k = k%n + 1 {
			var id string
			err := conn.QueryRow(ctx, `SELECT id::text FROM documents WHERE owner_id = $1 AND title = $2`,
				alice.UserID, fmt.Sprint("Dokument ", k)).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) || err == nil && used[id] {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			used[id] = true
			return id
		}
	}
	// Distinct documents for each step, spread over the range.
	pick := func(step, i int) int { return 1 + (step*7919+round*104729+i*487)%n }

	ids := make([]string, 200)
	texts := make([]string, 200)
	for i := range ids {
		ids[i], texts[i] = docID(pick(1, i)), benchText(r, 650, 0.3)
	}
	timings("text update of one document (SQL)", 200, func(i int) {
		if _, err := conn.Exec(ctx, `UPDATE documents SET extracted_text = $2 WHERE id = $1`, ids[i], texts[i]); err != nil {
			t.Fatal(err)
		}
	})
	for i := range ids {
		ids[i] = docID(pick(2, i))
	}
	timings("DeleteDocument", 200, func(i int) {
		if err := st.DeleteDocument(ctx, alice, ids[i]); err != nil {
			t.Fatal(err)
		}
	})
	for i := range texts {
		texts[i] = benchText(r, 650, 0.3)
	}
	timings("CreateDocument with text (import)", 200, func(i int) {
		in := newDoc(fmt.Sprint("import ", round, " ", i))
		in.ExtractedText = texts[i]
		in.ArchiveSHA256, in.ArchiveSize = ptr(hashOf(fmt.Sprint("import archive ", round, " ", i))), ptr(int64(1))
		if _, err := st.CreateDocument(ctx, alice, in); err != nil {
			t.Fatal(err)
		}
	})
	timings("CreateDocument without text (upload)", 200, func(i int) {
		if _, err := st.CreateDocument(ctx, alice, newDoc(fmt.Sprint("upload ", round, " ", i))); err != nil {
			t.Fatal(err)
		}
	})
	jobs := make([]*store.Job, 200)
	for i := range jobs {
		jobs[i] = claim(t, st, time.Now())
		texts[i] = benchText(r, 650, 0.3)
	}
	timings("CompleteJob with new text (OCR result)", 200, func(i int) {
		if err := st.CompleteJob(ctx, jobs[i], time.Now(), store.ProcessResult{PageCount: 1, Text: texts[i],
			TextSource: store.TextSourceTesseract, ArchiveSHA256: hashOf(fmt.Sprint("ocr ", round, " ", i)), ArchiveSize: 1}); err != nil {
			t.Fatal(err)
		}
	})
	titles := make([]string, 1000)
	for i := range titles {
		titles[i] = fmt.Sprint("Dokument ", pick(3, i))
	}
	start := time.Now()
	tag, err := conn.Exec(ctx, `UPDATE documents SET title = title || ' neu' WHERE owner_id = $1 AND title = ANY ($2)`, alice.UserID, titles)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("retitle %d documents in one statement: %v", tag.RowsAffected(), time.Since(start).Round(time.Millisecond))
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	v, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// mb formats a size in bytes as MB.
func mb(b int64) string { return fmt.Sprintf("%.1f MB", float64(b)/1e6) }
