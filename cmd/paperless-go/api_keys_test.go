package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/paperless-go/internal/auth"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

func TestUserAPIKeyCommands(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	u := storetest.User(t, st, "alice", false)
	add := func(name string) string {
		t.Helper()
		key, hash, hint, err := auth.NewAPIKey()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.CreateAPIKey(ctx, u.ID, store.NewAPIKey{Hash: hash, Hint: hint, Name: name, Scopes: []string{"read", "write"}}, time.Now()); err != nil {
			t.Fatal(err)
		}
		return key
	}
	run := func(args ...string) string {
		t.Helper()
		out, err := runUser(t, url, "", args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out
	}
	active := func() int64 {
		t.Helper()
		counts, err := st.APIKeyCounts(ctx, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return counts[u.ID]
	}

	if out := run("api-keys", "alice"); !strings.Contains(out, "no API keys") {
		t.Fatalf("no keys: %q", out)
	}
	key := add("backup")
	if out := run("list"); !strings.Contains(out, "API KEYS") || !strings.Contains(out, "alice  user  active  off  0         1 ") {
		t.Fatalf("list: %q", out)
	}
	out := run("api-keys", "alice")
	if !strings.Contains(out, "backup") || !strings.Contains(out, "pl_…"+key[51:]) || !strings.Contains(out, "read,write") ||
		!strings.Contains(out, "active") || strings.Contains(out, key) {
		t.Fatalf("api-keys: %q", out)
	}
	if _, err := runUser(t, url, "", "api-keys", "nobody"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown user: %v", err)
	}

	// reset-password --keep-api-keys keeps them; a plain reset revokes them.
	if out := run("reset-password", "alice", "--keep-api-keys"); strings.Contains(out, "revoked") {
		t.Fatalf("keep: %q", out)
	}
	if n := active(); n != 1 {
		t.Fatalf("after keep: %d", n)
	}
	if out := run("reset-password", "alice"); !strings.Contains(out, `1 API key(s) of "alice" revoked`) {
		t.Fatalf("reset: %q", out)
	}
	if out := run("api-keys", "alice"); !strings.Contains(out, "revoked") {
		t.Fatalf("state after reset: %q", out)
	}

	add("one")
	add("two")
	if out := run("revoke-api-keys", "alice"); !strings.Contains(out, `2 API key(s) of "alice" revoked`) {
		t.Fatalf("revoke: %q", out)
	}
	if n := active(); n != 0 {
		t.Fatalf("after revoke: %d", n)
	}
	entries := 0
	if err := st.ExportAudit(ctx, store.AuditFilter{Actions: []string{"api_keys"}, Since: time.Now().AddDate(0, 0, -1), Until: time.Now().AddDate(0, 0, 1)},
		func(r store.AuditRecord) error {
			if r.Action != "api_key.revoke" || r.Actor != store.ActorCLI || r.TargetName != "alice" || !strings.Contains(string(r.Details), `"apiKeysRevoked": 2`) {
				t.Errorf("entry %+v %s", r, r.Details)
			}
			entries++
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	if entries != 1 {
		t.Fatalf("%d revoke entries", entries)
	}
}
