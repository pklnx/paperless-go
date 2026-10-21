package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// addFakePasskey registers a passkey for userID through the store; the
// WebAuthn checks are the web package's.
func addFakePasskey(t *testing.T, st *store.Store, userID int64, name string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	token, err := st.BeginPasskeyRegistration(ctx, userID, name, now, now.Add(time.Minute),
		func(_ []byte, _ [][]byte) ([]byte, error) { return []byte(`{}`), nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.FinishPasskeyRegistration(ctx, userID, token, now, func(_, _ []byte) ([]byte, []byte, error) {
		return []byte("cred-" + name), []byte(`{}`), nil
	}); err != nil {
		t.Fatal(err)
	}
}

func passkeyCount(t *testing.T, st *store.Store, userID int64) int64 {
	t.Helper()
	n, err := st.PasskeyCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n[userID]
}

func TestUserPasskeyCommands(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := storetest.User(t, st, "alice", false)
	addFakePasskey(t, st, alice.ID, "Phone")
	addFakePasskey(t, st, alice.ID, "Key")
	now := time.Now()
	hash := []byte("0123456789abcdef0123456789abcdef")
	if err := st.CreateSession(ctx, hash, alice.ID, now, now.Add(time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	if out, err := runUser(t, url, "", "list"); err != nil || !strings.Contains(out, "PASSKEYS") || !strings.Contains(out, "alice  user  active  off  2 ") {
		t.Fatalf("list: %q, %v", out, err)
	}

	// remove-passkeys removes all and ends the sessions.
	out, err := runUser(t, url, "", "remove-passkeys", "Alice")
	if err != nil || !strings.Contains(out, `2 passkey(s) of "alice" removed`) {
		t.Fatalf("remove-passkeys: %q, %v", out, err)
	}
	if n := passkeyCount(t, st, alice.ID); n != 0 {
		t.Fatalf("%d passkeys left", n)
	}
	if _, err := st.GetSession(ctx, hash, now, now.Add(-time.Hour)); err == nil {
		t.Fatal("session survived remove-passkeys")
	}
	if _, err := runUser(t, url, "", "remove-passkeys", "nobody"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown user: %v", err)
	}

	// reset-password removes them unless told to keep them.
	addFakePasskey(t, st, alice.ID, "Phone")
	if out, err := runUser(t, url, "", "reset-password", "alice", "--keep-passkeys"); err != nil || strings.Contains(out, "removed") {
		t.Fatalf("reset-password --keep-passkeys: %q, %v", out, err)
	}
	if n := passkeyCount(t, st, alice.ID); n != 1 {
		t.Fatalf("%d passkeys after --keep-passkeys", n)
	}
	if out, err := runUser(t, url, "", "reset-password", "alice"); err != nil || !strings.Contains(out, `1 passkey(s) of "alice" removed`) {
		t.Fatalf("reset-password: %q, %v", out, err)
	}
	if n := passkeyCount(t, st, alice.ID); n != 0 {
		t.Fatalf("%d passkeys after reset-password", n)
	}
}
