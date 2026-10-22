//go:build loadtest

package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pklnx/paperless-go/internal/auth"
)

// checkLoopbackURL refuses a server URL whose host is not a loopback
// address, unless allowed.
func checkLoopbackURL(raw string, allow bool) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("server URL %q: want http(s)://host:port", raw)
	}
	if allow || isLoopbackHost(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("refusing server %q: not a loopback address (the load test writes and deletes data; -allow-non-loopback overrides)", u.Host)
}

// checkLoopbackDB refuses a database URL whose hosts are not loopback
// addresses or Unix sockets, unless allowed.
func checkLoopbackDB(raw string, allow bool) error {
	cfg, err := pgx.ParseConfig(raw)
	if err != nil {
		return fmt.Errorf("database URL: %w", err)
	}
	hosts := []string{cfg.Host}
	for _, f := range cfg.Fallbacks {
		hosts = append(hosts, f.Host)
	}
	for _, h := range hosts {
		if allow || strings.HasPrefix(h, "/") || isLoopbackHost(h) {
			continue
		}
		return fmt.Errorf("refusing database host %q: not a loopback address or socket (-allow-non-loopback overrides)", h)
	}
	return nil
}

// isLoopbackHost accepts loopback IP literals only: a name, "localhost"
// included, could resolve elsewhere.
func isLoopbackHost(h string) bool {
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

// serverPool is the database pool of the server under test, as far as the
// script knows it.
type serverPool struct {
	MaxConns int    `json:"maxConns"` // 0: unknown
	Source   string `json:"source"`
}

// poolSize is the server's pool: pool_max_conns of its database URL, or
// pgxpool's default (the store sets none). In external mode the server's
// URL is not known.
func poolSize(o *options) serverPool {
	if o.Bin == "" {
		return serverPool{Source: "unknown in external mode"}
	}
	if u, err := url.Parse(o.PGAdminURL); err == nil {
		if v := u.Query().Get("pool_max_conns"); v != "" {
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
				return serverPool{MaxConns: n, Source: "pool_max_conns of the database URL"}
			}
		}
	}
	return serverPool{MaxConns: max(4, runtime.NumCPU()), Source: "pgxpool default max(4, NumCPU)"}
}

// checkPortFree refuses a -listen address something already listens on:
// the script would otherwise log in to that server.
func checkPortFree(addr string) error {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("-listen %s is not free (%w): stop what listens there or pick another address", addr, err)
	}
	return l.Close()
}

// childEnv is the environment of the managed server: the script's own,
// without any inherited PAPERLESS_GO_ variable, plus extra.
func childEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "PAPERLESS_GO_") {
			env = append(env, kv)
		}
	}
	return append(env, extra...)
}

// env is the server and database one scenario runs against.
type env struct {
	baseURL   string
	pool      *pgxpool.Pool
	serverLog string // managed mode: the server's log file
	stop      func() // managed mode: stops the server; nil otherwise
}

// stopServer stops a managed server, so every backend has exited and
// flushed its statistics (pg_stat_database.deadlocks) before they are read.
func (e *env) stopServer() {
	if e.stop != nil {
		e.stop()
		e.stop = nil
	}
}

func (e *env) close() {
	e.stopServer()
	e.pool.Close()
}

func openEnv(ctx context.Context, o *options, scenario string) (*env, error) {
	if o.Bin == "" {
		pool, err := pgxpool.New(ctx, o.DBURL)
		if err != nil {
			return nil, err
		}
		return &env{baseURL: strings.TrimRight(o.URL, "/"), pool: pool}, nil
	}
	return managedEnv(ctx, o, scenario)
}

// managedEnv creates a fresh database loadtest_<scenario> on the
// throwaway cluster, migrates it with the binary and starts serve on it.
func managedEnv(ctx context.Context, o *options, scenario string) (*env, error) {
	u, err := url.Parse(o.PGAdminURL)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return nil, errors.New("-pg-admin-url must be a postgres:// URL")
	}
	dbName := "loadtest_" + strings.ToLower(scenario)
	admin, err := pgx.Connect(ctx, o.PGAdminURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = admin.Close(context.Background()) }()
	// The name is ours (loadtest_ + a validated scenario), never a user's.
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+dbName+" WITH (FORCE)"); err != nil {
		return nil, err
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		return nil, err
	}
	u.Path = "/" + dbName
	dbURL := u.String()

	work := o.Workdir
	if work == "" {
		if work, err = os.MkdirTemp("", "paperless-go-loadtest-"); err != nil {
			return nil, err
		}
		o.Workdir = work
	}
	dir := filepath.Join(work, strings.ToLower(scenario))
	_ = os.RemoveAll(dir)
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	// A throwaway key for a throwaway server: it lives only in the child's
	// environment.
	environ := childEnv(
		"PAPERLESS_GO_DATABASE_URL="+dbURL,
		"PAPERLESS_GO_DATA_DIR="+dataDir,
		"PAPERLESS_GO_SECRET_KEY="+base64.StdEncoding.EncodeToString(key),
		"PAPERLESS_GO_OCR_WORKERS=0",
		"PAPERLESS_GO_CONSUME_DIR=",
		"PAPERLESS_GO_ALLOWED_HOSTS=localhost,127.0.0.1,::1",
		"PAPERLESS_GO_LOG_LEVEL=info",
	)
	migrate := exec.CommandContext(ctx, o.Bin, "migrate", "up") //nolint:gosec // the operator's binary
	migrate.Env = environ
	if out, err := migrate.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("migrate up: %w: %s", err, out)
	}

	if err := checkPortFree(o.Listen); err != nil {
		return nil, err
	}
	logPath := filepath.Join(dir, "server.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // our own work folder
	if err != nil {
		return nil, err
	}
	serve := exec.Command(o.Bin, "serve", "--listen", o.Listen) //nolint:gosec // the operator's binary
	serve.Env = environ
	serve.Stdout, serve.Stderr = logFile, logFile
	if err := serve.Start(); err != nil {
		_ = logFile.Close()
		return nil, err
	}
	exited := make(chan error, 1)
	go func() { exited <- serve.Wait(); _ = logFile.Close() }()
	stop := func() {
		_ = serve.Process.Signal(os.Interrupt)
		select {
		case <-exited:
		case <-time.After(60 * time.Second):
			_ = serve.Process.Kill()
			<-exited
		}
	}
	base := "http://" + o.Listen
	if err := waitHealthy(ctx, base, exited); err != nil {
		stop()
		return nil, fmt.Errorf("%w (server log: %s)", err, logPath)
	}
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		stop()
		return nil, err
	}
	return &env{baseURL: base, pool: pool, serverLog: logPath, stop: stop}, nil
}

// waitHealthy waits until GET /healthz answers 200, the server exits, or
// 30 s pass.
func waitHealthy(ctx context.Context, base string, exited <-chan error) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case err := <-exited:
			return fmt.Errorf("serve exited: %w", err)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return errors.New("serve did not become healthy within 30 s")
		}
	}
}

// testUser is a user the script created.
type testUser struct {
	Name     string   `json:"name"`
	ID       int64    `json:"id"`
	Docs     int      `json:"documents"`
	password string   // generated per run, never printed
	docIDs   []string // the seeded documents, in a fixed order
}

// requireEmpty refuses a database that already has users or documents:
// the script only runs on a fresh database it fills itself.
func requireEmpty(ctx context.Context, pool *pgxpool.Pool) error {
	var n int64
	err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM users) + (SELECT count(*) FROM documents)`).Scan(&n)
	if err != nil {
		return fmt.Errorf("check the database (is it migrated?): %w", err)
	}
	if n > 0 {
		return errors.New("refusing a database that already has users or documents: the load test needs a fresh, throwaway database")
	}
	return nil
}

// seedUser creates a non-admin user with a random password and n generated
// documents. With allArchived the documents are out of the inbox (S3
// matches them by inbox=false, so new uploads never join the match);
// otherwise every second document is in the inbox.
func seedUser(ctx context.Context, pool *pgxpool.Pool, name string, n int, allArchived bool) (*testUser, error) {
	pw := make([]byte, 24)
	_, _ = rand.Read(pw)
	u := &testUser{Name: name, Docs: n, password: base64.RawURLEncoding.EncodeToString(pw)}
	hash, err := auth.NewHasher(auth.DefaultParams).Hash(ctx, u.password)
	if err != nil {
		return nil, err
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (name, password_hash) VALUES ($1, $2) RETURNING id`, name, hash).Scan(&u.ID); err != nil {
		return nil, err
	}
	_, err = pool.Exec(ctx, `
INSERT INTO documents (owner_id, title, document_date, extracted_text, is_inbox, source,
                       original_file_name, original_sha256, original_size, original_mime, page_count)
SELECT $1::bigint,
       (ARRAY['Invoice', 'Letter', 'Contract', 'Receipt'])[1 + i % 4] || ' ' || i,
       timestamptz '2026-01-01 00:00:00+00' - make_interval(days => i % 3650),
       'generated load-test document ' || i || CASE WHEN i % 4 = 0 THEN ' invoice total' ELSE ' text' END,
       CASE WHEN $3::boolean THEN false ELSE i % 2 = 0 END,
       'file', 'load-' || i || '.pdf',
       encode(sha256(convert_to('loadtest:' || $1::text || ':' || i, 'UTF8')), 'hex'),
       20480, 'application/pdf', 1
FROM generate_series(1, $2::int) AS i`, u.ID, n, allArchived)
	if err != nil {
		return nil, fmt.Errorf("seed documents: %w", err)
	}
	rows, err := pool.Query(ctx, `SELECT id::text FROM documents WHERE owner_id = $1 ORDER BY original_file_name`, u.ID)
	if err != nil {
		return nil, err
	}
	u.docIDs, err = pgx.CollectRows(rows, pgx.RowTo[string])
	return u, err
}

// hostInfo describes the machine the script runs on, for the report.
type host struct {
	CPU       string `json:"cpu"`
	CPUs      int    `json:"cpus"`
	MemoryKiB int64  `json:"memoryKiB"`
	Kernel    string `json:"kernel"`
	GoVersion string `json:"goVersion"`
	LoadAvg   string `json:"loadAvgAtStart"`
}

func hostInfo() host {
	h := host{CPUs: runtime.NumCPU(), GoVersion: runtime.Version()}
	h.CPU = procField("/proc/cpuinfo", "model name")
	if v := procField("/proc/meminfo", "MemTotal"); v != "" {
		_, _ = fmt.Sscanf(v, "%d", &h.MemoryKiB)
	}
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		h.Kernel = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		h.LoadAvg = strings.TrimSpace(string(b))
	}
	return h
}

// procField returns the value of the first "key: value" line of a /proc
// file.
func procField(path, key string) string {
	f, err := os.Open(path) //nolint:gosec // fixed /proc paths
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
