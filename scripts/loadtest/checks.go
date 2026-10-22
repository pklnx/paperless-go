//go:build loadtest

package main

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// deadlockStat is pg_stat_database.deadlocks of the database.
func deadlockStat(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var n int64
	err := pool.QueryRow(ctx, `SELECT deadlocks FROM pg_stat_database WHERE datname = current_database()`).Scan(&n)
	return n, err
}

// pgLog runs the -pg-log-cmd and returns its output ("" without one).
func pgLog(ctx context.Context, cmd string) (string, error) {
	if cmd == "" {
		return "", nil
	}
	out, err := exec.CommandContext(ctx, "sh", "-c", cmd).CombinedOutput() //nolint:gosec // the operator's own command
	if err != nil {
		return "", fmt.Errorf("-pg-log-cmd: %w: %s", err, out)
	}
	return string(out), nil
}

// logCounts is what the PostgreSQL log says about a run.
type logCounts struct {
	Deadlocks int `json:"deadlocks"` // "deadlock detected" (40P01)
	LockWaits int `json:"lockWaits"` // log_lock_waits: "still waiting for"
	// Pairs names the sqlc queries of the processes in each deadlock
	// ("A + B"); Victims the query that was cancelled.
	Pairs   map[string]int `json:"pairs,omitempty"`
	Victims map[string]int `json:"victims,omitempty"`
}

var (
	queryName   = regexp.MustCompile(`-- name: (\w+)`)
	processLine = regexp.MustCompile(`Process \d+: (.*)$`)
)

// countLog counts the deadlocks and lock waits in a PostgreSQL log.
func countLog(log string) logCounts {
	c := logCounts{Pairs: map[string]int{}, Victims: map[string]int{}}
	lines := strings.Split(log, "\n")
	nameOf := func(s string) string {
		if m := queryName.FindStringSubmatch(s); m != nil {
			return m[1]
		}
		s = strings.TrimSpace(s)
		if len(s) > 60 {
			s = s[:60]
		}
		return s
	}
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if strings.Contains(l, "still waiting for") {
			c.LockWaits++
		}
		if !strings.Contains(l, "deadlock detected") {
			continue
		}
		c.Deadlocks++
		var procs []string
		for j := i + 1; j < len(lines) && j < i+400; j++ {
			if m := processLine.FindStringSubmatch(lines[j]); m != nil && !strings.Contains(m[1], "waits for") {
				procs = append(procs, nameOf(m[1]))
			}
			if _, after, ok := strings.Cut(lines[j], "STATEMENT:"); ok {
				c.Victims[nameOf(after)]++
				break
			}
			if strings.Contains(lines[j], "deadlock detected") {
				break
			}
		}
		slices.Sort(procs)
		procs = slices.Compact(procs)
		if len(procs) > 0 {
			c.Pairs[strings.Join(procs, " + ")]++
		}
	}
	return c
}

// logDelta counts what after added to before (the log of the same server).
func logDelta(before, after string) logCounts {
	if strings.HasPrefix(after, before) {
		return countLog(after[len(before):])
	}
	b, a := countLog(before), countLog(after)
	return logCounts{Deadlocks: a.Deadlocks - b.Deadlocks, LockWaits: a.LockWaits - b.LockWaits}
}

// consistency are review B's checks after the run: each counts rows that
// must not exist.
var consistency = []struct{ name, sql string }{
	{"search_links differs from documents_search_links_vector", `
SELECT count(*) FROM documents
WHERE search_links IS DISTINCT FROM documents_search_links_vector(id, correspondent_id, document_type_id)`},
	{"document_tags owner differs from the document's or the tag's", `
SELECT count(*) FROM document_tags dt
JOIN documents d ON d.id = dt.document_id
JOIN tags t ON t.id = dt.tag_id
WHERE dt.owner_id <> d.owner_id OR dt.owner_id <> t.owner_id`},
	{"documents with another owner's correspondent or type", `
SELECT (SELECT count(*) FROM documents d JOIN correspondents c ON c.id = d.correspondent_id WHERE c.owner_id <> d.owner_id)
     + (SELECT count(*) FROM documents d JOIN document_types t ON t.id = d.document_type_id WHERE t.owner_id <> d.owner_id)`},
	{"documents linking a missing correspondent, type or tag", `
SELECT (SELECT count(*) FROM documents d WHERE d.correspondent_id IS NOT NULL
          AND NOT EXISTS (SELECT 1 FROM correspondents c WHERE c.id = d.correspondent_id))
     + (SELECT count(*) FROM documents d WHERE d.document_type_id IS NOT NULL
          AND NOT EXISTS (SELECT 1 FROM document_types t WHERE t.id = d.document_type_id))
     + (SELECT count(*) FROM document_tags dt WHERE NOT EXISTS (SELECT 1 FROM tags t WHERE t.id = dt.tag_id))`},
	{"sub-tags with a missing parent", `
SELECT count(*) FROM tags t
WHERE t.parent_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM tags p WHERE p.id = t.parent_id AND p.owner_id = t.owner_id)`},
}

type countCheck struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

func runConsistency(ctx context.Context, pool *pgxpool.Pool) ([]countCheck, error) {
	var out []countCheck
	for _, c := range consistency {
		var n int64
		if err := pool.QueryRow(ctx, c.sql).Scan(&n); err != nil {
			return nil, fmt.Errorf("%s: %w", c.name, err)
		}
		out = append(out, countCheck{Name: c.name, Count: n})
	}
	return out, nil
}

// auditCheck compares the audit log's rows with what the client saw.
type auditCheck struct {
	Name   string `json:"name"`
	Audit  int64  `json:"audit"`
	Client int64  `json:"client"`
}

// runAudit compares the audit entries of the test users since start with
// the client's successful answers.
func runAudit(ctx context.Context, pool *pgxpool.Pool, since time.Time, users []int64, ops map[string]*opStats) ([]auditCheck, error) {
	status := func(code string, names ...string) int64 {
		var n int64
		for _, name := range names {
			if o := ops[name]; o != nil {
				n += int64(o.Status[code])
			}
		}
		return n
	}
	var changed, updated int64
	for _, name := range []string{"bulk_ids", "bulk_match", "bulk_loop"} {
		if o := ops[name]; o != nil {
			changed += int64(o.Changed)
			updated += o.Updated
		}
	}
	rows, err := pool.Query(ctx, `SELECT action, count(*) FROM audit_log WHERE at >= $1 AND user_id = ANY($2) GROUP BY action`, since, users)
	if err != nil {
		return nil, err
	}
	actions := map[string]int64{}
	for rows.Next() {
		var a string
		var n int64
		if err := rows.Scan(&a, &n); err != nil {
			rows.Close()
			return nil, err
		}
		actions[a] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var countSum, withoutRun, badRuns int64
	err = pool.QueryRow(ctx, `
SELECT
  (SELECT coalesce(sum((details->>'count')::bigint), 0) FROM audit_log
     WHERE action = 'document.bulk_update' AND at >= $1 AND user_id = ANY($2)),
  (SELECT count(*) FROM audit_log
     WHERE action = 'document.update' AND NOT details ? 'bulkRun' AND at >= $1 AND user_id = ANY($2)),
  (WITH b AS (
     SELECT details->>'bulkRun' AS run, (details->>'count')::bigint AS c, jsonb_array_length(details->'ids') AS n
     FROM audit_log WHERE action = 'document.bulk_update' AND at >= $1 AND user_id = ANY($2)
   ), d AS (
     SELECT details->>'bulkRun' AS run, count(*) AS k
     FROM audit_log WHERE action = 'document.update' AND details ? 'bulkRun' AND at >= $1 AND user_id = ANY($2)
     GROUP BY 1
   )
   SELECT count(*) FROM b LEFT JOIN d USING (run) WHERE b.c <> b.n OR b.c <> coalesce(d.k, 0))`,
		since, users).Scan(&countSum, &withoutRun, &badRuns)
	if err != nil {
		return nil, err
	}
	return []auditCheck{
		{"document.bulk_update rows vs bulk answers with updated > 0", actions["document.bulk_update"], changed},
		{"sum of bulk_update count vs sum of updated", countSum, updated},
		{"bulk runs whose count, ids and per-document entries differ (want 0)", badRuns, 0},
		{"document.update without bulkRun vs PATCH 200", withoutRun, status("200", "patch")},
		{"tag.delete vs 204", actions["tag.delete"], status("204", "tag_delete", "tag_delete_parent", "tag_delete_child")},
		{"correspondent.delete vs 204", actions["correspondent.delete"], status("204", "correspondent_delete")},
		{"document_type.delete vs 204", actions["document_type.delete"], status("204", "document_type_delete")},
		{"tag.update vs rename + move 200", actions["tag.update"], status("200", "tag_rename", "tag_move")},
		{"correspondent.update vs rename 200", actions["correspondent.update"], status("200", "correspondent_rename")},
		{"document_type.update vs rename 200", actions["document_type.update"], status("200", "document_type_rename")},
		{"tag.create vs 201", actions["tag.create"], status("201", "tag_create")},
		{"correspondent.create vs 201", actions["correspondent.create"], status("201", "correspondent_create")},
		{"document_type.create vs 201", actions["document_type.create"], status("201", "document_type_create")},
		{"document.create vs upload 201", actions["document.create"],
			status("201", "upload_baseline_alice", "upload_baseline_bob", "upload_bulk_alice", "upload_bulk_bob")},
	}, nil
}

// serverErrors counts the ERROR lines of a managed server's log.
func serverErrors(log string) (int, []string) {
	var n int
	var first []string
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, "level=ERROR") {
			n++
			if len(first) < 5 {
				first = append(first, l)
			}
		}
	}
	return n, first
}
