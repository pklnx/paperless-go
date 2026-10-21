package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/pklnx/paperless-go/internal/config"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// run executes the command line with args against the test database.
func run(t *testing.T, dbURL string, args ...string) (string, error) {
	t.Helper()
	t.Setenv(config.EnvDatabaseURL, dbURL)
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func TestMigrateCommands(t *testing.T) {
	st, url := storetest.NewWithURL(t) // already migrated
	ctx := context.Background()

	out, err := run(t, url, "migrate")
	if err != nil || out != "database schema is up to date\n" {
		t.Fatalf("migrate: %q, %v", out, err)
	}
	out, err = run(t, url, "migrate", "status")
	if err != nil || !strings.Contains(out, "00001_initial.sql") || !strings.Contains(out, "applied") ||
		strings.Contains(out, "pending") {
		t.Fatalf("migrate status: %q, %v", out, err)
	}
	if err := checkSchema(ctx, st); err != nil {
		t.Errorf("checkSchema after migrate: %v", err)
	}

	if _, err := run(t, url, "migrate", "down"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("migrate down without --yes: %v", err)
	}
	out, err = run(t, url, "migrate", "down", "--yes")
	if err != nil || !strings.HasPrefix(out, "rolled back ") {
		t.Fatalf("migrate down --yes: %q, %v", out, err)
	}
	if err := checkSchema(ctx, st); err == nil || !strings.Contains(err.Error(), "migrate") {
		t.Errorf("checkSchema with a pending migration: %v", err)
	}

	out, err = run(t, url, "migrate", "up")
	if err != nil || !strings.Contains(out, "applied 00001_initial.sql") {
		t.Fatalf("migrate up: %q, %v", out, err)
	}
}

func TestMigrateWithoutDatabaseURL(t *testing.T) {
	if _, err := run(t, "", "migrate"); err == nil || !strings.Contains(err.Error(), config.EnvDatabaseURL) {
		t.Errorf("migrate without a database URL: %v", err)
	}
}
