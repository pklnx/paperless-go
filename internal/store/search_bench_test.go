//go:build searchbench

package store_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
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

// TestSearchBench measures the search on generated documents (#6, part 2):
//
//	TEST_DATABASE_URL=... go test -tags searchbench -run TestSearchBench -v -timeout 2h ./internal/store/
//
// SEARCHBENCH_DOCS sets the number of documents (default 100 000), all of
// one owner, with about 5 000 characters of synthetic German and English
// text each. "rechnung" is in half of them, "zahlungserinnerung" in 0.8 %.
// It logs the timings of each case and EXPLAIN (ANALYZE, BUFFERS) of the
// queries the store ran. It is not run in CI and asserts nothing.
func TestSearchBench(t *testing.T) {
	n := 100000
	if v := os.Getenv("SEARCHBENCH_DOCS"); v != "" {
		var err error
		if n, err = strconv.Atoi(v); err != nil {
			t.Fatal(err)
		}
	}
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	conn := connect(t, url)
	var version string
	if err := conn.QueryRow(ctx, `SHOW server_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("PostgreSQL %s, %d documents", version, n)

	steuer := mustTag(t, st, alice, "Steuer", nil)
	vocab := strings.Fields(`Versicherung Vertrag Kündigung Zahlung Betrag Konto Überweisung Gebühren
		Steuer Finanzamt Bescheid Einkommen Abrechnung Strom Wasser Miete Nebenkosten Vermieter Wohnung Straße
		Häuser Termin Arzt Krankenkasse Beitrag Mitglied Kunde Kundennummer Auftrag Lieferung Bestellung Datum
		sehr geehrte Damen Herren freundlichen Grüßen anbei erhalten Sie bitte innerhalb Tagen fällig zahlbar
		invoice insurance contract payment amount account transfer fees tax office notice income statement
		electricity water rent tenant apartment street houses appointment doctor member customer order delivery
		please find attached within days due payable meetings reports studies results the and of to in for with`)
	if _, err := conn.Exec(ctx, `SELECT setseed(0.53)`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	const batch = 10000
	for lo := 1; lo <= n; lo += batch {
		hi := min(lo+batch-1, n)
		if _, err := conn.Exec(ctx, `
			INSERT INTO documents (owner_id, title, source, original_sha256, original_size, original_mime,
			                       extracted_text, created_at, document_date, is_inbox)
			SELECT $1, 'Dokument ' || i, 'file', encode(sha256(i::text::bytea), 'hex'), 1, 'application/pdf',
			       CASE WHEN i % 2 = 0 THEN 'Rechnung ' ELSE '' END
			       || CASE WHEN i % 125 = 0 THEN 'Zahlungserinnerung ' ELSE '' END
			       || (SELECT string_agg(CASE WHEN random() < 0.7 THEN ($4::text[])[1 + floor(random() * cardinality($4::text[]))::int]
			                                  ELSE 'wort' || floor(random() * 30000)::int END, ' ')
			           FROM generate_series(1, 550 + i % 200) g WHERE i > 0),
			       timestamptz '2026-01-01' - i * interval '7 minutes',
			       timestamptz '2026-01-01' - (i % 3000) * interval '1 day', i % 10 = 0
			FROM generate_series($2::int, $3::int) i`, alice.UserID, lo, hi, vocab); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO document_tags (owner_id, document_id, tag_id)
		SELECT owner_id, id, $1 FROM documents WHERE title LIKE '%0'`, steuer.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"documents", "document_tags"} {
		if _, err := conn.Exec(ctx, "VACUUM ANALYZE "+table); err != nil {
			t.Fatal(err)
		}
	}
	var avg float64
	var size string
	if err := conn.QueryRow(ctx, `SELECT avg(char_length(extracted_text)), pg_size_pretty(pg_total_relation_size('documents'))
		FROM documents`).Scan(&avg, &size); err != nil {
		t.Fatal(err)
	}
	t.Logf("generated in %v: %.0f characters of text on average, documents table %s", time.Since(start).Round(time.Second), avg, size)

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
}
