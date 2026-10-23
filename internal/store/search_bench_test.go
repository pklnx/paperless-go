//go:build searchbench

package store_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"math/rand/v2"
	"os"
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
// #56):
//
//	TEST_DATABASE_URL=... go test -tags searchbench -run TestSearchBench -v -timeout 4h ./internal/store/
//
// SEARCHBENCH_DOCS sets alice's number of documents (default 100 000), with
// about 5 000 characters of synthetic German and English text each.
// "rechnung" is in half of them, "zahlungserinnerung" in 0.8 %; the
// compounds stromrechnung (5 %), gasrechnung (2 %), berechnung (2 %) and
// rechnungsbetrag (5 %) are in documents without "rechnung". 30 % of the
// words are a long tail of letter tokens (benchTail), which gives each
// owner well over 1.5 million distinct lexemes, closer to an OCR archive.
// bob has half as many documents with twice the share of tail words, so
// his vocabulary is about as large: the trigram index is not per owner.
//
// SEARCHBENCH_BEFORE=1 runs the same generator on a database migrated only
// to 00017, without the search vocabulary, and at the end times migrating
// up to 00018 (its backfill); SEARCHBENCH_FILLFACTOR sets the fillfactor
// of search_vocabulary before the generation. "Before" and "after" are
// two runs on the same host and server, with the same seed.
//
// It logs the timings of each case, EXPLAIN (ANALYZE, BUFFERS) of the
// queries the store ran, the per-document write paths and the sizes. It is
// not run in CI and asserts nothing.
func TestSearchBench(t *testing.T) {
	n := 100000
	if v := os.Getenv("SEARCHBENCH_DOCS"); v != "" {
		var err error
		if n, err = strconv.Atoi(v); err != nil {
			t.Fatal(err)
		}
	}
	before := os.Getenv("SEARCHBENCH_BEFORE") == "1"
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	if before {
		if err := st.MigrateDownTo(ctx, vocabularyVersion-1); err != nil {
			t.Fatal(err)
		}
	}
	conn := connect(t, url)
	var version string
	if err := conn.QueryRow(ctx, `SHOW server_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	mode := "after (with the search vocabulary)"
	if before {
		mode = "before (migrated to 00017)"
	}
	t.Logf("PostgreSQL %s, %d documents of alice, %d of bob, %s, %d CPUs", version, n, n/2, mode, runtime.NumCPU())
	if ff := os.Getenv("SEARCHBENCH_FILLFACTOR"); ff != "" && !before {
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
			if before && table == "search_vocabulary" {
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
	measure := func(name string, runs int, fn func()) {
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

	benchWrites(ctx, t, st, conn, alice, n)
	analyze()
	sizes("after the writes")

	if before {
		start := time.Now()
		if _, err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		t.Logf("migrate up to 00018 (backfill of the vocabulary, its indexes): %v", time.Since(start).Round(time.Millisecond))
		analyze()
		sizes("after the migration")
	}
}

// benchVocab are the generator's common words.
var benchVocab = strings.Fields(`Versicherung Vertrag Kündigung Zahlung Betrag Konto Überweisung Gebühren
	Steuer Finanzamt Bescheid Einkommen Abrechnung Strom Wasser Miete Nebenkosten Vermieter Wohnung Straße
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
func benchWrites(ctx context.Context, t *testing.T, st *store.Store, conn *pgx.Conn, alice store.Scope, n int) {
	r := rand.New(rand.NewPCG(53, 56))
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
	docID := func(k int) string {
		var id string
		if err := conn.QueryRow(ctx, `SELECT id::text FROM documents WHERE owner_id = $1 AND title = $2`,
			alice.UserID, fmt.Sprint("Dokument ", k)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// Distinct documents for each step, spread over the range.
	pick := func(step, i int) int { return 1 + (step*7919+i*487)%n }

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
		in := newDoc(fmt.Sprint("import ", i))
		in.ExtractedText = texts[i]
		in.ArchiveSHA256, in.ArchiveSize = ptr(hashOf(fmt.Sprint("import archive ", i))), ptr(int64(1))
		if _, err := st.CreateDocument(ctx, alice, in); err != nil {
			t.Fatal(err)
		}
	})
	timings("CreateDocument without text (upload)", 200, func(i int) {
		if _, err := st.CreateDocument(ctx, alice, newDoc(fmt.Sprint("upload ", i))); err != nil {
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
			TextSource: store.TextSourceTesseract, ArchiveSHA256: hashOf(fmt.Sprint("ocr ", i)), ArchiveSize: 1}); err != nil {
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
