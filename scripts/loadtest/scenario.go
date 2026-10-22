//go:build loadtest

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

var descriptions = map[string]string{
	"S1": "review B's mix, alice (3 000 documents) and bob (1 000); two of three threads are alice's",
	"S2": "review B's mix, every thread as alice (bob's 1 000 documents stay idle)",
	"S3": "uploads by alice and bob: a baseline, then the same while alice's bulk changes by match over her 5 000 documents loop",
}

type pgInfo struct {
	Version  string            `json:"version"`
	Settings map[string]string `json:"settings"`
}

type scenarioResult struct {
	Name           string              `json:"name"`
	Description    string              `json:"description"`
	Users          []*testUser         `json:"users"`
	Threads        int                 `json:"threads,omitempty"`
	ElapsedSeconds float64             `json:"elapsedSeconds"`
	Totals         totals              `json:"totals"`
	Ops            map[string]*opStats `json:"ops"`
	DeadlockStat   int64               `json:"deadlocksPgStat"`
	Log            *logCounts          `json:"postgresLog,omitempty"`
	ServerErrors   *int                `json:"serverLogErrors,omitempty"`
	ServerSample   []string            `json:"serverLogErrorSample,omitempty"`
	Consistency    []countCheck        `json:"consistency"`
	Audit          []auditCheck        `json:"audit"`
	postgres       *pgInfo
}

// totals sum up every recorded request except the dry runs (reported on
// their own: in the fingerprint flow each change by match sends one).
type totals struct {
	Requests         int            `json:"requests"`
	S2xx             int            `json:"2xx"`
	S4xx             int            `json:"4xx"`
	S500             int            `json:"500"`
	S503             int            `json:"503"`
	OtherStatus      int            `json:"otherStatus"`
	TransportErrors  int            `json:"transportErrors"`
	Refusals         map[string]int `json:"4xxByCode"`
	By503            map[string]int `json:"503ByOp,omitempty"`
	PerSecond        float64        `json:"requestsPerSecond"`
	OKPerSecond      float64        `json:"2xxPerSecond"`
	BulkChanges      int            `json:"successfulBulkChanges"`
	DocumentsChanged int64          `json:"documentsChanged"`
	DryRuns          int            `json:"dryRuns"`
}

func sumTotals(ops map[string]*opStats, elapsed time.Duration) totals {
	t := totals{Refusals: map[string]int{}, By503: map[string]int{}}
	for name, o := range ops {
		if name == "bulk_dry_run" {
			t.DryRuns += o.Requests
			continue
		}
		t.Requests += o.Requests
		t.BulkChanges += o.Changed
		t.DocumentsChanged += o.Updated
		for k, n := range o.Status {
			code, _, _ := strings.Cut(k, " ")
			switch {
			case k == "error":
				t.TransportErrors += n
			case code == "500":
				t.S500 += n
			case code == "503":
				t.S503 += n
				t.By503[name] += n
			case strings.HasPrefix(code, "2"):
				t.S2xx += n
			case strings.HasPrefix(code, "4"):
				t.S4xx += n
				t.Refusals[k] += n
			default:
				t.OtherStatus += n
			}
		}
	}
	if s := elapsed.Seconds(); s > 0 {
		t.PerSecond = round2(float64(t.Requests) / s)
		t.OKPerSecond = round2(float64(t.S2xx) / s)
	}
	return t
}

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

func runScenario(ctx context.Context, o *options, name string) (*scenarioResult, error) {
	e, err := openEnv(ctx, o, name)
	if err != nil {
		return nil, err
	}
	defer e.close()
	if err := requireEmpty(ctx, e.pool); err != nil {
		return nil, err
	}
	sr := &scenarioResult{Name: name, Description: descriptions[name]}
	if sr.postgres, err = postgresInfo(ctx, e); err != nil {
		return nil, err
	}

	aliceDocs, archived := 3000, false
	if name == "S3" {
		aliceDocs, archived = 5000, true
	}
	alice, err := seedUser(ctx, e.pool, "alice", aliceDocs, archived)
	if err != nil {
		return nil, err
	}
	bob, err := seedUser(ctx, e.pool, "bob", 1000, archived)
	if err != nil {
		return nil, err
	}
	sr.Users = []*testUser{alice, bob}
	if _, err := e.pool.Exec(ctx, "ANALYZE"); err != nil {
		return nil, err
	}

	logBefore, err := pgLog(ctx, o.PGLogCmd)
	if err != nil {
		return nil, err
	}
	dlBefore, err := deadlockStat(ctx, e.pool)
	if err != nil {
		return nil, err
	}
	var since time.Time
	mark := func() error { return e.pool.QueryRow(ctx, "SELECT now()").Scan(&since) }

	st := newStats()
	start := time.Now()
	var elapsed time.Duration
	switch name {
	case "S1":
		sr.Threads = o.Threads
		elapsed, err = runMix(ctx, e, o, sr.Users, func(i int) *testUser {
			if i%3 == 2 {
				return bob
			}
			return alice
		}, st, mark)
	case "S2":
		sr.Threads = o.Threads
		elapsed, err = runMix(ctx, e, o, []*testUser{alice}, func(int) *testUser { return alice }, st, mark)
	case "S3":
		err = runUploads(ctx, e, o, alice, bob, st, mark)
		elapsed = time.Since(start)
	}
	if err != nil {
		return nil, err
	}
	sr.ElapsedSeconds = round2(elapsed.Seconds())
	fmt.Fprintf(os.Stderr, "   load done after %.0f s; checking\n", elapsed.Seconds())

	// Stopping the server ends its backends, which flushes their
	// statistics; in external mode the counter may lag (README.md).
	e.stopServer()
	if sr.DeadlockStat, err = deadlockStat(ctx, e.pool); err != nil {
		return nil, err
	}
	sr.DeadlockStat -= dlBefore
	if o.PGLogCmd != "" {
		logAfter, err := pgLog(ctx, o.PGLogCmd)
		if err != nil {
			return nil, err
		}
		d := logDelta(logBefore, logAfter)
		sr.Log = &d
	}
	sr.Ops = st.finish()
	sr.Totals = sumTotals(sr.Ops, elapsed)
	if sr.Consistency, err = runConsistency(ctx, e.pool); err != nil {
		return nil, err
	}
	if sr.Audit, err = runAudit(ctx, e.pool, since, []int64{alice.ID, bob.ID}, sr.Ops); err != nil {
		return nil, err
	}
	if e.serverLog != "" {
		b, err := os.ReadFile(e.serverLog)
		if err != nil {
			return nil, err
		}
		n, sample := serverErrors(string(b))
		sr.ServerErrors, sr.ServerSample = &n, sample
	}
	return sr, nil
}

func postgresInfo(ctx context.Context, e *env) (*pgInfo, error) {
	p := &pgInfo{Settings: map[string]string{}}
	if err := e.pool.QueryRow(ctx, "SELECT version()").Scan(&p.Version); err != nil {
		return nil, err
	}
	for _, s := range []string{"log_lock_waits", "deadlock_timeout", "max_connections", "shared_buffers", "default_transaction_isolation"} {
		var v string
		if err := e.pool.QueryRow(ctx, "SELECT current_setting($1)", s).Scan(&v); err != nil {
			return nil, err
		}
		p.Settings[s] = v
	}
	return p, nil
}
