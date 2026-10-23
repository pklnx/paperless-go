package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// namedQueries returns the sqlc queries of queries/*.sql: name → text from
// its "-- name:" line to the next one.
func namedQueries(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("queries", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no query files: %v", err)
	}
	name := regexp.MustCompile(`(?m)^-- name: (\w+)`)
	out := map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		starts := name.FindAllStringSubmatchIndex(src, -1)
		if len(starts) > 0 && writesOwnerTables(src[:starts[0][0]]) {
			t.Errorf("%s: a write of an owner table before the first named query", f)
		}
		for i, s := range starts {
			end := len(src)
			if i+1 < len(starts) {
				end = starts[i+1][0]
			}
			q := src[s[2]:s[3]]
			if _, dup := out[q]; dup {
				t.Errorf("query %s defined twice", q)
			}
			out[q] = src[s[0]:end]
		}
	}
	return out
}

// writerQueries is exactly the set of queries that write or row-lock an
// owner table: a query added without registering it, or registered
// without writing, fails here, so the run-time guard covers every query
// that can exist.
func TestWriterQueriesComplete(t *testing.T) {
	queries := namedQueries(t)
	var want []string
	for name, body := range queries {
		if writesOwnerTables(body) {
			want = append(want, name)
		}
	}
	slices.Sort(want)
	var got []string
	for name := range writerQueries {
		got = append(got, name)
		if _, ok := queries[name]; !ok {
			t.Errorf("writerQueries names %s, which is no query", name)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("writerQueries = %v,\nthe queries writing or row-locking an owner table are %v", got, want)
	}
	if len(want) < 20 {
		t.Errorf("only %d writer queries found: %v", len(want), want)
	}
}

// The classifier: writes and row locks of the five tables, ignoring
// comments; other tables, reads and the advisory lock are no writers.
func TestWritesOwnerTables(t *testing.T) {
	for sql, want := range map[string]bool{
		"UPDATE documents SET title = 'x'":                                                true,
		"update  public.document_tags set tag_id = $1":                                    true,
		"INSERT INTO tags (owner_id, name) VALUES (1, 'x')":                               true,
		"DELETE FROM correspondents WHERE id = $1":                                        true,
		"delete from ONLY document_types":                                                 true,
		"TRUNCATE documents":                                                              true,
		"WITH x AS (UPDATE documents SET title = '' RETURNING id) SELECT 1":               true,
		"SELECT id FROM documents WHERE id = $1 FOR NO KEY UPDATE":                        true,
		"SELECT 1 FROM jobs JOIN documents d ON d.id = jobs.document_id FOR UPDATE":       true,
		"SELECT id FROM tags FOR KEY SHARE":                                               true,
		`UPDATE "documents" SET title = 'x'`:                                              true,
		`DELETE FROM public."tags" WHERE id = $1`:                                         true,
		`insert into "public"."document_tags" (document_id, tag_id) values ($1, $2)`:      true,
		`SELECT id FROM "documents" WHERE id = $1 FOR UPDATE`:                             true,
		`SELECT 1 FROM "documents"`:                                                       false,
		`UPDATE "documents_x" SET a = 1`:                                                  false,
		"SELECT * FROM documents":                                                         false,
		"UPDATE jobs SET state = 'done'":                                                  false,
		"INSERT INTO jobs (document_id) SELECT id FROM documents":                         false,
		"SELECT id FROM jobs FOR UPDATE SKIP LOCKED":                                      false,
		"SELECT 1 FROM documents_search_links_vector($1)":                                 false,
		"SET LOCAL enable_seqscan = off":                                                  false,
		"-- UPDATE documents in a comment\nSELECT 1":                                      false,
		"SELECT pg_advisory_xact_lock(hashtextextended($1::text || $2::bigint::text, 0))": false,
	} {
		if got := writesOwnerTables(sql); got != want {
			t.Errorf("writesOwnerTables(%q) = %v, want %v", sql, got, want)
		}
	}
	// A named query is decided by writerQueries, other SQL by the
	// classifier (fail closed).
	if !isWriter("-- name: PatchDocument :execrows\nUPDATE documents SET title = $1") {
		t.Error("PatchDocument is no writer")
	}
	if isWriter("-- name: GetDocument :one\nSELECT 1") {
		t.Error("GetDocument is a writer")
	}
	if !isWriter("UPDATE documents SET title = 'raw'") {
		t.Error("unnamed UPDATE documents passes the guard")
	}
}

// Transactions and query sets are built only in the guarded constructors,
// so no store code reaches the raw pool or a raw transaction: Begin,
// BeginTx, pgx.BeginFunc, pgx.BeginTxFunc and WithTx only where listed,
// db.New only on the guard, the pool's Exec, Query and QueryRow only in
// collation.go (catalog reads and REINDEX, which write no owner table),
// connections of their own (pgx.Connect, pgx.ConnectConfig, Acquire) only
// for LISTEN, and the owner lock only through the owner gate (ownerTx).
func TestTransactionsOnlyThroughTheGuard(t *testing.T) {
	allowed := map[string][]string{
		"Begin":             {"audit.go:inTxWith"},
		"BeginTx":           {"locking.go:readTx"},
		"BeginFunc":         {"collation.go:reindex"},
		"BeginTxFunc":       nil,
		"WithTx":            nil,
		"db.New":            {"store.go:Open", "audit.go:inTxWith", "documents_edit.go:match", "search.go:SearchDocuments"},
		"pool.Exec":         {"collation.go:*"},
		"pool.Query":        {"collation.go:*"},
		"pool.QueryRow":     {"collation.go:*"},
		"pgx.Connect":       nil,
		"pgx.ConnectConfig": {"jobs.go:ListenJobs"},
		"Acquire":           nil,
		"lockOwners":        {"ownergate.go:ownerTx"},
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	found := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			where := f + ":" + fn.Name.Name
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				var key string
				switch m := sel.Sel.Name; {
				case m == "Begin" || m == "BeginTx" || m == "BeginFunc" || m == "BeginTxFunc" || m == "WithTx":
					key = m
				case m == "New" && isIdent(sel.X, "db"):
					key = "db.New"
				case (m == "Exec" || m == "Query" || m == "QueryRow") && isPool(sel.X):
					key = "pool." + m
				case (m == "Connect" || m == "ConnectConfig") && isIdent(sel.X, "pgx"):
					key = "pgx." + m
				case m == "Acquire" || m == "lockOwners":
					key = m
				default:
					return true
				}
				found++
				ok = false
				for _, a := range allowed[key] {
					if a == where || a == f+":*" {
						ok = true
					}
				}
				if !ok {
					t.Errorf("%s: %s outside the guarded constructors", fset.Position(call.Pos()), key)
				}
				return true
			})
		}
	}
	if found < 5 {
		t.Errorf("found only %d calls; the check is broken", found)
	}
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

// isPool reports whether e is x.pool for some x.
func isPool(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "pool"
}

func TestQueryName(t *testing.T) {
	for sql, want := range map[string]string{
		"-- name: LockDocument :one\nSELECT 1": "LockDocument",
		"-- name: X\n":                         "X",
		"SELECT 1":                             "",
		" -- name: X :one":                     "",
	} {
		if got := queryName(sql); got != want {
			t.Errorf("queryName(%q) = %q, want %q", sql, got, want)
		}
	}
}
