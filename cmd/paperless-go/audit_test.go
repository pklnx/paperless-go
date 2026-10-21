package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/config"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

func auditLog(t *testing.T, st *store.Store) []store.AuditRecord {
	t.Helper()
	var out []store.AuditRecord
	// The database clock sets at; a day around now covers any skew.
	f := store.AuditFilter{Since: time.Now().Add(-24 * time.Hour), Until: time.Now().Add(24 * time.Hour)}
	if err := st.ExportAudit(context.Background(), f, func(r store.AuditRecord) error {
		out = append(out, r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// detachCurrentPartition makes every audit insert fail: the current month's
// partition is detached and keeps its name, so it cannot be created again.
func detachCurrentPartition(t *testing.T, url string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, `DO $$ BEGIN
		EXECUTE format('ALTER TABLE audit_log DETACH PARTITION %I',
			'audit_log_' || to_char(now() AT TIME ZONE 'UTC', 'YYYY_MM'));
	END $$`); err != nil {
		t.Fatal(err)
	}
}

// TestCLIAudit runs every command that changes users and checks that each
// writes exactly one entry with the operator and the command.
func TestCLIAudit(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	t.Setenv(config.EnvOperator, "patrick")
	storetest.User(t, st, "anna", true)
	pw := "correct horse battery\n"
	for _, c := range []struct {
		stdin   string
		args    []string
		action  string
		command string
		details map[string]any
	}{
		{pw, []string{"add", "carol", "--password-stdin"}, "user.add", "user add", map[string]any{"admin": false}},
		{"", []string{"lock", "carol"}, "user.lock", "user lock", nil},
		{"", []string{"unlock", "carol"}, "user.unlock", "user unlock", nil},
		{"", []string{"promote", "carol"}, "user.promote", "user promote", nil},
		{"", []string{"demote", "carol"}, "user.demote", "user demote", nil},
		{"", []string{"admin", "carol"}, "user.promote", "user promote", nil},
		{"", []string{"unadmin", "carol"}, "user.demote", "user demote", nil},
		{pw, []string{"set-password", "carol", "--password-stdin"}, "user.password_set", "user set-password", nil},
		{"", []string{"reset-password", "carol"}, "user.password_reset", "user reset-password", nil},
		{"", []string{"remove", "carol"}, "user.remove", "user remove", nil},
		// Reading commands are not logged.
		{"", []string{"list"}, "", "", nil},
	} {
		name := strings.Join(c.args, " ")
		n := len(auditLog(t, st))
		out, err := runUser(t, url, c.stdin, c.args...)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := auditLog(t, st)[n:]
		if c.action == "" {
			if len(got) != 0 {
				t.Errorf("%s: logged %+v", name, got)
			}
			continue
		}
		if len(got) != 1 {
			t.Fatalf("%s: %d entries: %+v", name, len(got), got)
		}
		e := got[0]
		if e.Action != c.action || e.Actor != store.ActorCLI || e.UserID != nil || e.ClientIP != "" || e.UserAgent != "" {
			t.Errorf("%s: %+v", name, e)
		}
		if e.TargetType != store.TargetUser || e.TargetName != "carol" || e.TargetID == "" {
			t.Errorf("%s: target %s:%s (%s)", name, e.TargetType, e.TargetName, e.TargetID)
		}
		var d map[string]any
		if err := json.Unmarshal(e.Details, &d); err != nil {
			t.Fatal(err)
		}
		if d["operator"] != "patrick" || d["os_user"] == nil || d["os_user"] == "" || d["command"] != "paperless-go "+c.command {
			t.Errorf("%s: who %v", name, d)
		}
		for k, v := range c.details {
			if fmt.Sprint(d[k]) != fmt.Sprint(v) {
				t.Errorf("%s: %s = %v, want %v", name, k, d[k], v)
			}
		}
		if strings.Contains(string(e.Details), "horse") {
			t.Errorf("%s: a password was recorded: %s", name, e.Details)
		}
		if c.action == "user.password_reset" {
			lines := strings.Split(strings.TrimSpace(out), "\n")
			if generated := lines[len(lines)-1]; strings.Contains(string(e.Details), generated) {
				t.Errorf("generated password recorded: %s", e.Details)
			}
		}
	}
	// A refused change writes nothing.
	n := len(auditLog(t, st))
	if _, err := runUser(t, url, "", "lock", "anna"); err == nil || !strings.Contains(err.Error(), "last admin") {
		t.Fatalf("lock the last admin: %v", err)
	}
	if got := auditLog(t, st)[n:]; len(got) != 0 {
		t.Errorf("refused change logged: %+v", got)
	}
}

// Without the operator variable, the entry has the OS user only.
func TestCLIAuditWithoutOperator(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	t.Setenv(config.EnvOperator, "")
	if _, err := runUser(t, url, "correct horse battery\n", "add", "anna", "--admin", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	got := auditLog(t, st)
	if len(got) != 1 || strings.Contains(string(got[0].Details), "operator") || !strings.Contains(string(got[0].Details), `"admin": true`) {
		t.Fatalf("entries %+v", got)
	}
}

// TestCLIAuditFailClosed: without the log, a change is not made and
// nothing is exported.
func TestCLIAuditFailClosed(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	storetest.User(t, st, "anna", true)
	storetest.User(t, st, "bob", false)
	detachCurrentPartition(t, url)
	if _, err := runUser(t, url, "", "lock", "bob"); err == nil || !strings.Contains(err.Error(), "audit log") {
		t.Fatalf("lock without audit log: %v", err)
	}
	if u, _ := st.GetUserByName(ctx, "bob"); u.LockedAt != nil {
		t.Error("bob locked without an entry")
	}
	if _, err := runUser(t, url, "", "remove", "bob"); err == nil {
		t.Error("remove without audit log")
	}
	if _, err := st.GetUserByName(ctx, "bob"); err != nil {
		t.Errorf("bob removed without an entry: %v", err)
	}
	out := filepath.Join(t.TempDir(), "x.csv")
	if _, err := run(t, url, "audit", "export", "--since", "2020-01-01", "--format", "csv", "--out", out); err == nil {
		t.Error("export without audit log")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("export written: %v", err)
	}
}

func TestAuditExportCommand(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	anna := storetest.User(t, st, "anna", false)
	e := store.AuditEntry{Actor: store.ActorSession, Action: "auth.login_failed"}
	e.SetUser(anna.ID, anna.Name)
	e.SetTarget(store.TargetUser, "1", "=HYPERLINK(\"http://evil\")")
	e.Detail("name", "-2+3")
	if err := st.Audit(ctx, e); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	export := func(args ...string) error {
		t.Helper()
		_, err := run(t, url, append([]string{"audit", "export"}, args...)...)
		return err
	}
	// The database's day, which the entries carry.
	var today string
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if err := conn.QueryRow(ctx, "SELECT to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD')").Scan(&today); err != nil {
		t.Fatal(err)
	}
	csvOut := filepath.Join(dir, "a.csv")
	if err := export("--since", today, "--until", today, "--format", "csv", "--out", csvOut); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(csvOut); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("csv file: %v %v", fi, err)
	}
	f, _ := os.Open(csvOut)
	records, err := csv.NewReader(f).ReadAll()
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	// Header, the failed login, and the export itself.
	if len(records) != 3 || strings.Join(records[0], ",") != strings.Join(auditCSVHeader, ",") {
		t.Fatalf("csv: %q", records)
	}
	if records[1][8] != `'=HYPERLINK("http://evil")` || records[1][5] != "auth.login_failed" || records[2][5] != "audit.export" {
		t.Errorf("rows %q", records[1:])
	}
	if err := export("--since", today, "--format", "csv", "--out", csvOut); err == nil {
		t.Error("existing file overwritten")
	}

	jsonOut := filepath.Join(dir, "a.jsonl")
	if err := export("--since", today, "--user", "anna", "--action", "login", "--format", "jsonl", "--out", jsonOut); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(jsonOut)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var j auditJSON
	if len(lines) != 1 || json.Unmarshal([]byte(lines[0]), &j) != nil || j.Action != "auth.login_failed" || j.User != "anna" ||
		*j.UserID != anna.ID || !strings.Contains(string(j.Details), `"name":"-2+3"`) {
		t.Fatalf("jsonl: %s", data)
	}
	// To stdout: the three exports so far and this one.
	if out, err := run(t, url, "audit", "export", "--since", today, "--action", "export", "--format", "jsonl"); err != nil ||
		strings.Count(out, `"audit.export"`) != 3 {
		t.Fatalf("stdout: %q, %v", out, err)
	}
	// Periods before the entries are empty.
	empty := filepath.Join(dir, "b.jsonl")
	if err := export("--since", "2020-01-01", "--until", "2020-12-31", "--format", "jsonl", "--out", empty); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(empty); len(data) != 0 {
		t.Errorf("2020: %s", data)
	}
	for _, args := range [][]string{
		{"--since", today, "--format", "xml"},
		{"--since", "yesterday", "--format", "csv"},
		{"--since", today, "--until", "2020-01-01", "--format", "csv"},
		{"--since", today, "--format", "csv", "--action", "nonsense"},
		{"--since", today, "--format", "csv", "--user", "nobody"},
	} {
		if err := export(args...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
	// Every export that ran, also an empty one, is recorded.
	var exports int
	for _, r := range auditLog(t, st) {
		if r.Action == "audit.export" {
			exports++
		}
	}
	if exports != 4 {
		t.Errorf("%d audit.export entries, want 4", exports)
	}
}
