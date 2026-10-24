//go:build loadtest

// Command loadtest drives a local paperless-go server with concurrent
// writes and checks the database afterwards: the load test of review B of
// PR #103, as scenarios S1–S3 of the #102 plan. It never runs in CI and
// is not part of normal builds (build tag loadtest). See README.md.
//
// It only talks to a server and a PostgreSQL on loopback addresses unless
// -allow-non-loopback is given, and only to a database without users and
// documents: it creates its own test users and generated documents.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"time"
)

// Bulk flows: fingerprint sends a dry run before every bulk change by
// match and passes its fingerprint (#106); legacy sends the change alone,
// as servers before #106 expect.
const (
	flowFingerprint = "fingerprint"
	flowLegacy      = "legacy"
)

type options struct {
	Scenarios        []string      `json:"scenarios"`
	Duration         time.Duration `json:"-"`
	DurationText     string        `json:"duration"`
	Threads          int           `json:"threads"`
	Seed             int64         `json:"seed"`
	Uploads          int           `json:"uploads"`
	UploadSize       int           `json:"uploadSize"`
	UploadInterval   time.Duration `json:"-"`
	UploadEvery      string        `json:"uploadInterval"`
	BulkFlow         string        `json:"bulkFlow"`
	Bin              string        `json:"bin,omitempty"`
	PGAdminURL       string        `json:"-"`
	URL              string        `json:"url,omitempty"`
	DBURL            string        `json:"-"`
	PGLogCmd         string        `json:"pgLogCmd,omitempty"`
	PGImage          string        `json:"pgImage,omitempty"`
	Label            string        `json:"label,omitempty"`
	Out              string        `json:"-"`
	Baseline         string        `json:"baseline,omitempty"`
	Workdir          string        `json:"-"`
	Listen           string        `json:"listen,omitempty"`
	AllowNonLoopback bool          `json:"allowNonLoopback,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "loadtest:", err)
		os.Exit(2)
	}
}

func run() error {
	var o options
	var scenarios string
	flag.StringVar(&scenarios, "scenarios", "S1,S2,S3", "scenarios to run, comma-separated (S1, S2, S3)")
	flag.DurationVar(&o.Duration, "duration", 240*time.Second, "how long the S1/S2 mix runs")
	flag.IntVar(&o.Threads, "threads", 21, "concurrent client threads in S1/S2")
	flag.Int64Var(&o.Seed, "seed", 1, "seed of the request mix")
	flag.IntVar(&o.Uploads, "uploads", 50, "uploads per user and phase in S3")
	flag.IntVar(&o.UploadSize, "upload-size", 20<<10, "size of each uploaded PDF in bytes (S3)")
	flag.DurationVar(&o.UploadInterval, "upload-interval", 200*time.Millisecond, "S3: start an upload of each user this often (sequential per user)")
	flag.StringVar(&o.BulkFlow, "bulk-flow", flowFingerprint,
		"bulk change by match: fingerprint (dry run first, send its fingerprint; #106) or legacy (no dry run, no fingerprint)")
	flag.StringVar(&o.Bin, "bin", "", "managed mode: the paperless-go binary; each scenario gets a fresh database and its own serve")
	flag.StringVar(&o.PGAdminURL, "pg-admin-url", "", "managed mode: URL of the throwaway cluster's maintenance database (creates and drops loadtest_* databases)")
	flag.StringVar(&o.Listen, "listen", "127.0.0.1:18080", "managed mode: address serve listens on")
	flag.StringVar(&o.Workdir, "workdir", "", "managed mode: folder for data directories and server logs (default: a new temporary folder)")
	flag.StringVar(&o.URL, "url", "", "external mode: base URL of a running server (one scenario per run, on a fresh database)")
	flag.StringVar(&o.DBURL, "db", "", "external mode: URL of that server's database")
	flag.StringVar(&o.PGLogCmd, "pg-log-cmd", "", `command that prints the PostgreSQL log, e.g. "docker logs pgload" (counts "deadlock detected")`)
	flag.StringVar(&o.PGImage, "pg-image", "", "PostgreSQL image, recorded in the report")
	flag.StringVar(&o.Label, "label", "", "free text recorded in the report, e.g. the server's commit")
	flag.StringVar(&o.Out, "out", "", "write the JSON result to this file (stdout gets the human summary)")
	flag.StringVar(&o.Baseline, "baseline", "", "JSON result of an earlier run (the before numbers) for the S2 throughput check")
	flag.BoolVar(&o.AllowNonLoopback, "allow-non-loopback", false, "allow a server or database that is not on a loopback address (never point this at a real server)")
	flag.Parse()
	o.DurationText, o.UploadEvery = o.Duration.String(), o.UploadInterval.String()

	for _, s := range strings.Split(scenarios, ",") {
		s = strings.ToUpper(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		if !slices.Contains([]string{"S1", "S2", "S3"}, s) {
			return fmt.Errorf("unknown scenario %q", s)
		}
		o.Scenarios = append(o.Scenarios, s)
	}
	if err := o.validate(); err != nil {
		return err
	}

	var baseline *result
	if o.Baseline != "" {
		b, err := readResult(o.Baseline)
		if err != nil {
			return err
		}
		baseline = b
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if o.Bin != "" {
		if err := checkPortFree(o.Listen); err != nil {
			return err
		}
	}
	res := &result{Options: o, Host: hostInfo(), ServerPool: poolSize(&o), StartedAt: time.Now().UTC(), Scenarios: map[string]*scenarioResult{}}
	for _, name := range o.Scenarios {
		fmt.Fprintf(os.Stderr, "== %s\n", name)
		sr, err := runScenario(ctx, &o, name)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		res.Scenarios[name] = sr
		if res.Postgres == nil {
			res.Postgres = sr.postgres
		}
	}
	res.FinishedAt = time.Now().UTC()
	res.Checks = evaluate(res, baseline)

	printSummary(os.Stdout, res)
	if o.Out != "" {
		b, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(o.Out, append(b, '\n'), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (o *options) validate() error {
	if len(o.Scenarios) == 0 {
		return errors.New("no scenario")
	}
	if o.BulkFlow != flowFingerprint && o.BulkFlow != flowLegacy {
		return fmt.Errorf("-bulk-flow must be %s or %s", flowFingerprint, flowLegacy)
	}
	if o.Threads < 1 || o.Duration <= 0 || o.Uploads < 1 || o.UploadInterval <= 0 || o.UploadSize < 64 {
		return errors.New("-threads, -duration, -uploads, -upload-interval must be positive and -upload-size at least 64")
	}
	managed, external := o.Bin != "", o.URL != "" || o.DBURL != ""
	switch {
	case managed && external:
		return errors.New("give either -bin and -pg-admin-url (managed) or -url and -db (external)")
	case managed:
		if o.PGAdminURL == "" {
			return errors.New("-bin needs -pg-admin-url")
		}
		if err := checkLoopbackDB(o.PGAdminURL, o.AllowNonLoopback); err != nil {
			return err
		}
		if err := checkLoopbackURL("http://"+o.Listen, o.AllowNonLoopback); err != nil {
			return err
		}
	case external:
		if o.URL == "" || o.DBURL == "" {
			return errors.New("external mode needs both -url and -db")
		}
		if len(o.Scenarios) != 1 {
			return errors.New("external mode runs one scenario per fresh database: pass -scenarios with one name")
		}
		if err := checkLoopbackURL(o.URL, o.AllowNonLoopback); err != nil {
			return err
		}
		if err := checkLoopbackDB(o.DBURL, o.AllowNonLoopback); err != nil {
			return err
		}
	default:
		return errors.New("give -bin and -pg-admin-url (managed) or -url and -db (external); see README.md")
	}
	return nil
}
