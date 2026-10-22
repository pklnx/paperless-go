//go:build loadtest

package main

import (
	"net"
	"strings"
	"testing"
)

func op(status map[string]int, changed int, p99 float64) *opStats {
	n := 0
	for _, c := range status {
		n += c
	}
	return &opStats{Requests: n, Status: status, Changed: changed, P99ms: p99}
}

func opts(scenarios ...string) options {
	return options{Scenarios: scenarios, Threads: 21, DurationText: "4m0s", Seed: 1, BulkFlow: flowLegacy}
}

// goodMix is an S1/S2 result whose numbers measure the mix.
func goodMix(name string, perSecond float64) *scenarioResult {
	return &scenarioResult{Name: name, Totals: totals{PerSecond: perSecond}, Ops: map[string]*opStats{
		"bulk_ids":   op(map[string]int{"200": 90, "400 owner_mismatch": 10}, 90, 1000),
		"bulk_match": op(map[string]int{"200": 95, "409 match_changed": 5}, 95, 1200),
		"patch":      op(map[string]int{"200": 300, "404": 3}, 0, 900),
	}}
}

// goodUploads is an S3 result whose numbers measure the uploads.
func goodUploads() *scenarioResult {
	up := func(p99 float64) *opStats { return op(map[string]int{"201": 50}, 0, p99) }
	return &scenarioResult{Name: "S3", Ops: map[string]*opStats{
		"bulk_loop":             op(map[string]int{"200": 7}, 7, 2900),
		"upload_baseline_alice": up(36), "upload_baseline_bob": up(45),
		"upload_bulk_alice": up(67), "upload_bulk_bob": up(32),
	}}
}

func find(t *testing.T, checks []check, name string) check {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check %q in %+v", name, checks)
	return check{}
}

const (
	s3Criterion = "S3: alice's upload p99 ≤ baseline p99 + bulk p99"
	s2Criterion = "S2: throughput ≥ half of the baseline's"
)

func TestS3Measured(t *testing.T) {
	r := &result{Options: opts("S3"), Scenarios: map[string]*scenarioResult{"S3": goodUploads()}}
	checks := evaluate(r, nil)
	if c := find(t, checks, "S3: measured"); c.Status != "pass" {
		t.Errorf("measured: %+v", c)
	}
	if c := find(t, checks, s3Criterion); c.Status != "pass" {
		t.Errorf("criterion: %+v", c)
	}
}

// A #106 server refuses the legacy flow: the bulk loop never changes
// anything and its 1 ms answers must not make the criterion pass.
func TestS3RefusedBulkFlowFails(t *testing.T) {
	s := goodUploads()
	s.Ops["bulk_loop"] = op(map[string]int{"400 invalid_selection": 3199}, 0, 1)
	r := &result{Options: opts("S3"), Scenarios: map[string]*scenarioResult{"S3": s}}
	checks := evaluate(r, nil)
	for _, name := range []string{"S3: measured", s3Criterion} {
		if c := find(t, checks, name); c.Status != "fail" || !strings.Contains(c.Detail, "invalid_selection") {
			t.Errorf("%s: %+v", name, c)
		}
	}
	if c := find(t, checks, "S3: bulk flow"); c.Status != "info" || !strings.Contains(c.Detail, "bulk_loop") {
		t.Errorf("bulk flow: %+v", c)
	}
}

func TestS3WithoutSuccessfulBulkFails(t *testing.T) {
	s := goodUploads()
	s.Ops["bulk_loop"] = op(map[string]int{"200": 3}, 0, 5)
	r := &result{Options: opts("S3"), Scenarios: map[string]*scenarioResult{"S3": s}}
	if c := find(t, evaluate(r, nil), s3Criterion); c.Status != "fail" || !strings.Contains(c.Detail, "bulk_loop") {
		t.Errorf("criterion: %+v", c)
	}
}

func TestS3UploadNot201Fails(t *testing.T) {
	s := goodUploads()
	s.Ops["upload_bulk_alice"] = op(map[string]int{"201": 49, "503": 1}, 0, 10)
	r := &result{Options: opts("S3"), Scenarios: map[string]*scenarioResult{"S3": s}}
	if c := find(t, evaluate(r, nil), s3Criterion); c.Status != "fail" || !strings.Contains(c.Detail, "upload_bulk_alice") {
		t.Errorf("criterion: %+v", c)
	}
	delete(s.Ops, "upload_bulk_bob")
	if c := find(t, evaluate(r, nil), "S3: measured"); !strings.Contains(c.Detail, "upload_bulk_bob: no upload") {
		t.Errorf("measured: %+v", c)
	}
}

func TestS2AgainstBaseline(t *testing.T) {
	base := &result{Options: opts("S2"), ServerPool: serverPool{MaxConns: 4}, Scenarios: map[string]*scenarioResult{"S2": goodMix("S2", 14.9)}}
	for _, tc := range []struct {
		name      string
		perSecond float64
		edit      func(r *result)
		status    string
		detail    string
	}{
		{"half or more", 7.5, nil, "pass", "requests/s"},
		{"below half", 7.4, nil, "fail", "requests/s"},
		{"other threads", 20, func(r *result) { r.Options.Threads = 10 }, "fail", "threads 10 vs 21"},
		{"other duration", 20, func(r *result) { r.Options.DurationText = "20s" }, "fail", "duration"},
		{"other seed", 20, func(r *result) { r.Options.Seed = 2 }, "fail", "seed"},
		{"other flow", 20, func(r *result) { r.Options.BulkFlow = flowFingerprint }, "fail", "bulk flow"},
		{"other pool", 20, func(r *result) { r.ServerPool.MaxConns = 8 }, "fail", "server pool"},
		{"refused flow", 40, func(r *result) {
			r.Scenarios["S2"].Ops["bulk_match"] = op(map[string]int{"400 invalid_selection": 900}, 0, 1)
		}, "fail", "nothing measured"},
		{"unexpected 4xx", 40, func(r *result) {
			r.Scenarios["S2"].Ops["tag_create"] = op(map[string]int{"400": 20}, 0, 1)
		}, "fail", "unexpected 4xx"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &result{Options: opts("S2"), ServerPool: serverPool{MaxConns: 4},
				Scenarios: map[string]*scenarioResult{"S2": goodMix("S2", tc.perSecond)}}
			if tc.edit != nil {
				tc.edit(r)
			}
			c := find(t, evaluate(r, base), s2Criterion)
			if c.Status != tc.status || !strings.Contains(c.Detail, tc.detail) {
				t.Errorf("got %+v, want %s with %q", c, tc.status, tc.detail)
			}
		})
	}
}

func TestS2WithoutBaselineSkips(t *testing.T) {
	r := &result{Options: opts("S2"), Scenarios: map[string]*scenarioResult{"S2": goodMix("S2", 10)}}
	if c := find(t, evaluate(r, nil), s2Criterion); c.Status != "skip" {
		t.Errorf("got %+v", c)
	}
}

func TestS2BaselineThatMeasuredNothingFails(t *testing.T) {
	bad := goodMix("S2", 100)
	bad.Ops["bulk_match"] = op(map[string]int{"400 invalid_selection": 900}, 0, 1)
	base := &result{Options: opts("S2"), Scenarios: map[string]*scenarioResult{"S2": bad}}
	r := &result{Options: opts("S2"), Scenarios: map[string]*scenarioResult{"S2": goodMix("S2", 10)}}
	if c := find(t, evaluate(r, base), s2Criterion); c.Status != "fail" || !strings.Contains(c.Detail, "baseline") {
		t.Errorf("got %+v", c)
	}
}

func TestLoopbackLiteralsOnly(t *testing.T) {
	for host, want := range map[string]bool{
		"127.0.0.1": true, "[::1]": true, "::1": true, "127.1.2.3": true,
		"localhost": false, "example.com": false, "0.0.0.0": false, "192.168.1.5": false,
	} {
		if got := isLoopbackHost(host); got != want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestChildEnvDropsInheritedSettings(t *testing.T) {
	t.Setenv("PAPERLESS_GO_PUBLIC_URL", "https://docs.example.org")
	env := childEnv("PAPERLESS_GO_OCR_WORKERS=0")
	for _, kv := range env {
		if strings.HasPrefix(kv, "PAPERLESS_GO_") && kv != "PAPERLESS_GO_OCR_WORKERS=0" {
			t.Errorf("inherited %s", kv)
		}
	}
	if env[len(env)-1] != "PAPERLESS_GO_OCR_WORKERS=0" {
		t.Errorf("own setting missing: %v", env[len(env)-1])
	}
}

func TestCheckPortFree(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	if err := checkPortFree(l.Addr().String()); err == nil {
		t.Error("a port in use passed")
	}
}
