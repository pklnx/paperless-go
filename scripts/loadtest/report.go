//go:build loadtest

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"
)

// result is the script's JSON output.
type result struct {
	Options    options                    `json:"options"`
	Host       host                       `json:"host"`
	Postgres   *pgInfo                    `json:"postgres"`
	ServerPool serverPool                 `json:"serverPool"`
	StartedAt  time.Time                  `json:"startedAt"`
	FinishedAt time.Time                  `json:"finishedAt"`
	Scenarios  map[string]*scenarioResult `json:"scenarios"`
	Checks     []check                    `json:"checks"`
}

func readResult(path string) (*result, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the operator's file
	if err != nil {
		return nil, err
	}
	var r result
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("baseline %s: %w", path, err)
	}
	return &r, nil
}

// check is one pass/fail line. Status is pass, fail, skip (missing input)
// or info (reported, no criterion).
type check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

func passFail(ok bool) string {
	if ok {
		return "pass"
	}
	return "fail"
}

// expected4xx are the refusals that are part of the mix (README.md).
var expected4xx = map[string]bool{
	"400 owner_mismatch": true, "404": true, "409 tag_cycle": true,
	"409 tag_name_conflict": true, "409 name_taken": true, "409 match_changed": true,
}

// maxUnexpected4xx is the share of unexpected 4xx answers above which a
// scenario counts as not measured.
const maxUnexpected4xx = 0.01

// notMeasured says why a scenario's numbers do not measure what it is
// meant to (empty when they do): a refused bulk flow, bulk changes or
// uploads that never succeeded, or more than a small share of unexpected
// refusals. Its timing criteria then fail instead of passing vacuously.
func notMeasured(name string, s *scenarioResult) []string {
	var why []string
	var all, unexpected int
	for _, op := range slices.Sorted(maps.Keys(s.Ops)) {
		o := s.Ops[op]
		all += o.Requests
		for k, n := range o.Status {
			if strings.HasPrefix(k, "4") && !expected4xx[k] {
				unexpected += n
			}
		}
		if n := o.Status["400 invalid_selection"]; n > 0 && strings.HasPrefix(op, "bulk_") {
			why = append(why, fmt.Sprintf("%s: %d × 400 invalid_selection (the bulk flow does not fit the server)", op, n))
		}
	}
	if all > 0 && float64(unexpected) > maxUnexpected4xx*float64(all) {
		why = append(why, fmt.Sprintf("%d of %d answers are unexpected 4xx", unexpected, all))
	}
	changed := func(op string) int {
		if o := s.Ops[op]; o != nil {
			return o.Changed
		}
		return 0
	}
	switch name {
	case "S1", "S2":
		for _, op := range []string{"bulk_ids", "bulk_match", "patch"} {
			if o := s.Ops[op]; o == nil || o.Status["200"] == 0 {
				why = append(why, op+": no successful request")
			}
		}
		if changed("bulk_match") == 0 {
			why = append(why, "bulk_match: no bulk change by match changed a document")
		}
	case "S3":
		if o := s.Ops["bulk_loop"]; o == nil || o.Status["200"] == 0 || changed("bulk_loop") == 0 {
			why = append(why, "bulk_loop: no successful bulk change")
		}
		for _, op := range []string{"upload_baseline_alice", "upload_baseline_bob", "upload_bulk_alice", "upload_bulk_bob"} {
			o := s.Ops[op]
			if o == nil || o.Requests == 0 {
				why = append(why, op+": no upload")
				continue
			}
			if o.Status["201"] != o.Requests {
				why = append(why, fmt.Sprintf("%s: %d of %d answers are not 201 (%s)", op, o.Requests-o.Status["201"], o.Requests, joinCounts(o.Status)))
			}
		}
	}
	return why
}

// comparable lists the parameters in which baseline differs from r, so
// that their S2 throughput cannot be compared.
func comparable(r, baseline *result) []string {
	a, b := r.Options, baseline.Options
	var diff []string
	if a.Threads != b.Threads {
		diff = append(diff, fmt.Sprintf("threads %d vs %d", a.Threads, b.Threads))
	}
	if a.DurationText != b.DurationText {
		diff = append(diff, fmt.Sprintf("duration %s vs %s", a.DurationText, b.DurationText))
	}
	if a.Seed != b.Seed {
		diff = append(diff, fmt.Sprintf("seed %d vs %d", a.Seed, b.Seed))
	}
	if a.BulkFlow != b.BulkFlow {
		diff = append(diff, fmt.Sprintf("bulk flow %s vs %s", a.BulkFlow, b.BulkFlow))
	}
	if p, q := r.ServerPool.MaxConns, baseline.ServerPool.MaxConns; p != q {
		diff = append(diff, fmt.Sprintf("server pool %d vs %d connections", p, q))
	}
	return diff
}

// evaluate prints the #102 plan's acceptance criteria as checks.
func evaluate(r *result, baseline *result) []check {
	var out []check
	invalid := map[string][]string{}
	for _, name := range r.Options.Scenarios {
		s := r.Scenarios[name]
		why := notMeasured(name, s)
		invalid[name] = why
		out = append(out, check{name + ": measured", passFail(len(why) == 0), strings.Join(why, "; ")})
		logDl := -1
		if s.Log != nil {
			logDl = s.Log.Deadlocks
		}
		detail := fmt.Sprintf("pg_stat_database %d", s.DeadlockStat)
		if logDl >= 0 {
			detail += fmt.Sprintf(", postgres log %d", logDl)
		} else {
			detail += ", postgres log not read (-pg-log-cmd)"
		}
		out = append(out, check{name + ": no deadlocks", passFail(s.DeadlockStat == 0 && logDl <= 0), detail})
		out = append(out, check{name + ": no 500 and no 503", passFail(s.Totals.S500 == 0 && s.Totals.S503 == 0),
			fmt.Sprintf("500: %d, 503: %d, transport errors: %d", s.Totals.S500, s.Totals.S503, s.Totals.TransportErrors)})
		var bad []string
		for _, c := range s.Consistency {
			if c.Count != 0 {
				bad = append(bad, fmt.Sprintf("%s: %d", c.Name, c.Count))
			}
		}
		out = append(out, check{name + ": consistency checks 0", passFail(len(bad) == 0), strings.Join(bad, "; ")})
		bad = nil
		for _, a := range s.Audit {
			if a.Audit != a.Client {
				bad = append(bad, fmt.Sprintf("%s: %d ≠ %d", a.Name, a.Audit, a.Client))
			}
		}
		out = append(out, check{name + ": audit log matches the answers", passFail(len(bad) == 0), strings.Join(bad, "; ")})
		for _, op := range []string{"bulk_match", "bulk_loop"} {
			if o := s.Ops[op]; o != nil && r.Options.BulkFlow == flowLegacy && o.Status["400 invalid_selection"] > 0 {
				out = append(out, check{name + ": bulk flow", "info",
					op + ": the server refused changes by match without a fingerprint (#106): run with -bulk-flow fingerprint"})
			}
		}
	}

	if s := r.Scenarios["S2"]; s != nil {
		c := check{Name: "S2: throughput ≥ half of the baseline's", Status: "skip", Detail: "no -baseline with S2"}
		var bs *scenarioResult
		if baseline != nil {
			bs = baseline.Scenarios["S2"]
		}
		switch {
		case len(invalid["S2"]) > 0:
			c.Status, c.Detail = "fail", "nothing measured: "+strings.Join(invalid["S2"], "; ")
		case bs == nil:
		case len(notMeasured("S2", bs)) > 0:
			c.Status, c.Detail = "fail", "the baseline's S2 measured nothing: "+strings.Join(notMeasured("S2", bs), "; ")
		case len(comparable(r, baseline)) > 0:
			c.Status, c.Detail = "fail", "baseline not comparable: "+strings.Join(comparable(r, baseline), "; ")
		default:
			b := bs.Totals.PerSecond
			c.Status = passFail(s.Totals.PerSecond >= b/2)
			c.Detail = fmt.Sprintf("%.2f requests/s vs baseline %.2f requests/s (2xx/s %.2f vs %.2f)",
				s.Totals.PerSecond, b, s.Totals.OKPerSecond, bs.Totals.OKPerSecond)
		}
		out = append(out, c)
	}
	if s := r.Scenarios["S3"]; s != nil {
		p99 := func(op string) float64 {
			if o := s.Ops[op]; o != nil {
				return o.P99ms
			}
			return 0
		}
		withBulk, base, bulk := p99("upload_bulk_alice"), p99("upload_baseline_alice"), p99("bulk_loop")
		c := check{"S3: alice's upload p99 ≤ baseline p99 + bulk p99", passFail(withBulk <= base+bulk),
			fmt.Sprintf("%.0f ms ≤ %.0f ms + %.0f ms", withBulk, base, bulk)}
		if len(invalid["S3"]) > 0 {
			c.Status, c.Detail = "fail", "nothing measured: "+strings.Join(invalid["S3"], "; ")
		}
		out = append(out, c)
		out = append(out, check{"S3: bob's uploads vs his baseline", "info",
			fmt.Sprintf("p99 %.0f ms with the bulk loop, %.0f ms without", p99("upload_bulk_bob"), p99("upload_baseline_bob"))})
	}
	return out
}

func printSummary(w io.Writer, r *result) {
	o := r.Options
	fmt.Fprintf(w, "paperless-go load test  %s\n", r.StartedAt.Format(time.RFC3339))
	fmt.Fprintf(w, "params: scenarios %s, %d threads, %s, seed %d, bulk flow %s, %d uploads × %d B every %s",
		strings.Join(o.Scenarios, ","), o.Threads, o.DurationText, o.Seed, o.BulkFlow, o.Uploads, o.UploadSize, o.UploadEvery)
	if o.Label != "" {
		fmt.Fprintf(w, ", label %q", o.Label)
	}
	fmt.Fprintln(w)
	h := r.Host
	fmt.Fprintf(w, "host: %s, %d CPUs, %.1f GiB, kernel %s, %s, load at start %s\n",
		h.CPU, h.CPUs, float64(h.MemoryKiB)/(1<<20), h.Kernel, h.GoVersion, h.LoadAvg)
	if r.Postgres != nil {
		fmt.Fprintf(w, "postgres: %s %s; %v\n", o.PGImage, firstWords(r.Postgres.Version, 2), r.Postgres.Settings)
	}
	fmt.Fprintf(w, "server database pool: %d connections (%s)\n", r.ServerPool.MaxConns, r.ServerPool.Source)

	for _, name := range o.Scenarios {
		s := r.Scenarios[name]
		t := s.Totals
		fmt.Fprintf(w, "\n== %s: %s (%.0f s)\n", name, s.Description, s.ElapsedSeconds)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintf(tw, "Requests\t%d (%.2f/s)\n", t.Requests, t.PerSecond)
		fmt.Fprintf(tw, "2xx\t%d\n", t.S2xx)
		fmt.Fprintf(tw, "4xx\t%d\t%s\n", t.S4xx, joinCounts(t.Refusals))
		fmt.Fprintf(tw, "500\t%d\n", t.S500)
		fmt.Fprintf(tw, "503\t%d\t%s\n", t.S503, joinCounts(t.By503))
		if t.TransportErrors > 0 || t.OtherStatus > 0 {
			fmt.Fprintf(tw, "transport errors / other\t%d / %d\n", t.TransportErrors, t.OtherStatus)
		}
		fmt.Fprintf(tw, "Dry runs (not in requests)\t%d\n", t.DryRuns)
		dl := fmt.Sprintf("pg_stat_database %d", s.DeadlockStat)
		if s.Log != nil {
			dl += fmt.Sprintf(", log %d (lock waits logged: %d)", s.Log.Deadlocks, s.Log.LockWaits)
		}
		fmt.Fprintf(tw, "Deadlocks (40P01)\t%s\n", dl)
		if s.Log != nil && len(s.Log.Pairs) > 0 {
			fmt.Fprintf(tw, "  by queries\t%s\n", joinCounts(s.Log.Pairs))
		}
		fmt.Fprintf(tw, "Successful bulk changes / documents changed\t%d / %d\n", t.BulkChanges, t.DocumentsChanged)
		if s.ServerErrors != nil {
			fmt.Fprintf(tw, "Server log ERROR lines\t%d\n", *s.ServerErrors)
		}
		_ = tw.Flush()

		fmt.Fprintln(w, "\nop                      requests  p50 ms   p99 ms   max ms  answers")
		for _, op := range slices.Sorted(maps.Keys(s.Ops)) {
			x := s.Ops[op]
			fmt.Fprintf(w, "%-22s  %8d  %7.0f  %7.0f  %7.0f  %s\n", op, x.Requests, x.P50ms, x.P99ms, x.MaxMs, joinCounts(x.Status))
		}
		fmt.Fprintln(w, "\nconsistency (rows that must not exist):")
		for _, c := range s.Consistency {
			fmt.Fprintf(w, "  %-62s %d\n", c.Name, c.Count)
		}
		fmt.Fprintln(w, "audit log vs client:")
		for _, a := range s.Audit {
			eq := "="
			if a.Audit != a.Client {
				eq = "≠"
			}
			fmt.Fprintf(w, "  %-66s %d %s %d\n", a.Name, a.Audit, eq, a.Client)
		}
	}
	fmt.Fprintln(w, "\nchecks:")
	for _, c := range r.Checks {
		fmt.Fprintf(w, "  %-4s  %s", strings.ToUpper(c.Status), c.Name)
		if c.Detail != "" {
			fmt.Fprintf(w, " (%s)", c.Detail)
		}
		fmt.Fprintln(w)
	}
}

func joinCounts(m map[string]int) string {
	keys := slices.Sorted(maps.Keys(m))
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

func firstWords(s string, n int) string {
	f := strings.Fields(s)
	return strings.Join(f[:min(n, len(f))], " ")
}
