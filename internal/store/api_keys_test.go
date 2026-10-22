package store_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/auth"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

func newKey(t *testing.T, st *store.Store, userID int64, name string, scopes []string, expires *time.Time, now time.Time) (string, *store.APIKey) {
	t.Helper()
	key, hash, hint, err := auth.NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	k, err := st.CreateAPIKey(context.Background(), userID, store.NewAPIKey{Hash: hash, Hint: hint, Name: name, Scopes: scopes, ExpiresAt: expires}, now)
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	return key, k
}

func TestNormalizeScopes(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{"write", "read", "write"}, "[read write]"},
		{[]string{"upload"}, "[upload]"},
		{[]string{"upload", "read", "upload"}, "[read upload]"},
		{[]string{"write", "upload"}, "[write]"}, // write includes upload
		{[]string{"upload", "read", "write"}, "[read write]"},
	} {
		if got, err := store.NormalizeScopes(c.in); err != nil || fmt.Sprint(got) != c.want {
			t.Errorf("%v: %v %v, want %s", c.in, got, err, c.want)
		}
	}
	for _, bad := range [][]string{nil, {}, {"sync"}, {"read", "admin"}, {"upload", "Upload"}} {
		if _, err := store.NormalizeScopes(bad); !errors.Is(err, store.ErrAPIKeyScope) {
			t.Errorf("%v: %v", bad, err)
		}
	}
}

func TestAPIKeys(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	now := time.Now()
	alice, err := st.CreateUser(ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := st.CreateUser(ctx, "bob", "h", true)
	if err != nil {
		t.Fatal(err)
	}

	key, k := newKey(t, st, alice.ID, "backup", []string{store.ScopeRead}, nil, now)
	if k.UserName != "alice" || k.Hint != key[len(key)-4:] {
		t.Fatalf("created %+v", k)
	}
	// Only the hash is stored, never the key.
	var stored []byte
	var plain int
	if err := conn.QueryRow(ctx, "SELECT key_hash FROM api_keys WHERE id = $1", k.ID).Scan(&stored); err != nil || !bytes.Equal(stored, auth.HashAPIKey(key)) {
		t.Fatalf("stored hash %x %v", stored, err)
	}
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM api_keys WHERE position($1 in hint || name || array_to_string(scopes, ',')) > 0", key).Scan(&plain); err != nil || plain != 0 {
		t.Fatalf("the key is stored in plain text: %d %v", plain, err)
	}
	sess, got, err := st.APIKeyLogin(ctx, auth.HashAPIKey(key), now)
	if err != nil || sess.UserID != alice.ID || sess.UserName != "alice" || got.ID != k.ID || !got.HasScope(store.ScopeRead) || got.HasScope(store.ScopeWrite) {
		t.Fatalf("login %+v %+v %v", sess, got, err)
	}
	if _, _, err := st.APIKeyLogin(ctx, auth.HashAPIKey(key+"x"), now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown key: %v", err)
	}

	// Names are unique among the keys that are not revoked.
	h := auth.HashAPIKey("other")
	if _, err := st.CreateAPIKey(ctx, alice.ID, store.NewAPIKey{Hash: h, Hint: "abcd", Name: "backup", Scopes: []string{"read"}}, now); !errors.Is(err, store.ErrAPIKeyName) {
		t.Fatalf("duplicate name: %v", err)
	}
	newKey(t, st, bob.ID, "backup", []string{"read"}, nil, now) // another user's name is no conflict

	// A user's key cannot be revoked by another user; an admin (owner nil)
	// can revoke any. The audit entry names the key and its owner.
	if _, err := st.RevokeAPIKey(ctx, k.ID, &bob.ID, &bob.ID, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoke by another user: %v", err)
	}
	e := &store.AuditEntry{Actor: store.ActorSession, Action: "api_key.revoke"}
	r, err := st.RevokeAPIKey(store.WithAudit(ctx, e), k.ID, &alice.ID, &alice.ID, now)
	if err != nil || r.UserName != "alice" || r.Name != "backup" {
		t.Fatalf("revoke: %+v %v", r, err)
	}
	if e.TargetType != store.TargetAPIKey || e.TargetID != fmt.Sprint(k.ID) || e.TargetName != "backup" || e.Details["owner"] != "alice" {
		t.Fatalf("revoke entry %+v", e)
	}
	if _, err := st.RevokeAPIKey(ctx, k.ID, nil, &bob.ID, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoke twice: %v", err)
	}
	if _, _, err := st.APIKeyLogin(ctx, auth.HashAPIKey(key), now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoked key: %v", err)
	}
	if ref, err := st.FindRefusedAPIKey(ctx, auth.HashAPIKey(key), now); err != nil || ref.Reason != "revoked" || ref.Name != "backup" || ref.UserName != "alice" {
		t.Fatalf("refused: %+v %v", ref, err)
	}
	if _, err := st.FindRefusedAPIKey(ctx, auth.HashAPIKey("pl_unknown"), now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("refused unknown: %v", err)
	}
	newKey(t, st, alice.ID, "backup", []string{"read"}, nil, now) // the name is free again

	// Expiry follows the server clock.
	expires := now.Add(time.Hour)
	expKey, _ := newKey(t, st, alice.ID, "short", []string{"write"}, &expires, now)
	if _, _, err := st.APIKeyLogin(ctx, auth.HashAPIKey(expKey), expires.Add(-time.Second)); err != nil {
		t.Fatalf("before expiry: %v", err)
	}
	if _, _, err := st.APIKeyLogin(ctx, auth.HashAPIKey(expKey), expires); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired key: %v", err)
	}
	if ref, _ := st.FindRefusedAPIKey(ctx, auth.HashAPIKey(expKey), expires); ref == nil || ref.Reason != "expired" {
		t.Fatalf("expired reason: %+v", ref)
	}
	later := expires.Add(time.Minute)

	// Locking suspends the keys; unlocking restores them.
	lockedKey, _ := newKey(t, st, alice.ID, "locked", []string{"read"}, nil, now)
	if err := st.SetUserLocked(ctx, alice.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.APIKeyLogin(ctx, auth.HashAPIKey(lockedKey), later); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("locked owner: %v", err)
	}
	if ref, _ := st.FindRefusedAPIKey(ctx, auth.HashAPIKey(lockedKey), later); ref == nil || ref.Reason != "locked" {
		t.Fatalf("locked reason: %+v", ref)
	}
	if err := st.SetUserLocked(ctx, alice.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.APIKeyLogin(ctx, auth.HashAPIKey(lockedKey), later); err != nil {
		t.Fatalf("after unlock: %v", err)
	}

	// Lists and counts: revoked and expired keys are listed, not counted.
	list, err := st.ListAPIKeys(ctx, alice.ID)
	if err != nil || len(list) != 4 {
		t.Fatalf("list: %d %v", len(list), err)
	}
	states := map[string]int{}
	for i := range list {
		states[list[i].State(later)]++
	}
	if states[store.APIKeyActive] != 2 || states[store.APIKeyRevoked] != 1 || states[store.APIKeyExpired] != 1 {
		t.Fatalf("states %v", states)
	}
	if counts, _ := st.APIKeyCounts(ctx, later); counts[alice.ID] != 2 || counts[bob.ID] != 1 {
		t.Fatalf("counts %v", counts)
	}
	all, err := st.ListAllAPIKeys(ctx)
	if err != nil || len(all) != 5 || all[0].UserName != "alice" || all[4].UserName != "bob" {
		t.Fatalf("all: %d %v", len(all), err)
	}

	// Old revoked and expired keys go after 30 days.
	if n, err := st.DeleteOldAPIKeys(ctx, now.Add(29*24*time.Hour)); err != nil || n != 0 {
		t.Fatalf("delete old too early: %d %v", n, err)
	}
	if n, err := st.DeleteOldAPIKeys(ctx, now.Add(31*24*time.Hour)); err != nil || n != 2 {
		t.Fatalf("delete old: %d %v", n, err)
	}

	// A password reset revokes the keys or keeps them; the user's removal
	// deletes them.
	if err := st.ResetUserPassword(ctx, alice.ID, "h2", store.PasswordReset{MustChange: true}); err != nil {
		t.Fatal(err)
	}
	if counts, _ := st.APIKeyCounts(ctx, later); counts[alice.ID] != 2 {
		t.Fatalf("after reset with keep: %v", counts)
	}
	e = &store.AuditEntry{Actor: store.ActorCLI, Action: "user.password_reset"}
	if err := st.ResetUserPassword(store.WithAudit(ctx, e), alice.ID, "h3", store.PasswordReset{MustChange: true, RevokeAPIKeys: true, By: &bob.ID, Now: later}); err != nil {
		t.Fatal(err)
	}
	if counts, _ := st.APIKeyCounts(ctx, later); counts[alice.ID] != 0 || e.Details["apiKeysRevoked"] != int64(2) {
		t.Fatalf("after reset: %v %v", counts, e.Details)
	}
	var revokedBy int64
	if err := conn.QueryRow(ctx, "SELECT revoked_by FROM api_keys WHERE user_id = $1 AND name = 'locked'", alice.ID).Scan(&revokedBy); err != nil || revokedBy != bob.ID {
		t.Fatalf("revoked_by %d %v", revokedBy, err)
	}
	newKey(t, st, alice.ID, "after", []string{"read"}, nil, time.Now())
	// The count goes by the server clock: at removeAt the "soon" key has
	// expired.
	soon := time.Now().Add(time.Hour)
	newKey(t, st, alice.ID, "soon", []string{"read"}, &soon, time.Now())
	removeAt := soon.Add(time.Hour)
	e = &store.AuditEntry{Actor: store.ActorCLI, Action: "user.remove"}
	if err := st.DeleteUser(store.WithAudit(ctx, e), alice.ID, removeAt); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM api_keys WHERE user_id = $1", alice.ID).Scan(&left); err != nil || left != 0 || e.Details["apiKeysRemoved"] != int64(1) {
		t.Fatalf("after removal: %d %v %v", left, err, e.Details)
	}
	// The revoker's removal keeps the keys they revoked.
	if _, err := st.RevokeAPIKey(ctx, all[4].ID, nil, &bob.ID, later); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(ctx, "carol", "h", true); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser(ctx, bob.ID, time.Now()); err != nil {
		t.Fatalf("remove a revoker: %v", err)
	}
}

func TestAPIKeyLimitAndTouch(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	now := time.Now()
	u, err := st.CreateUser(ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	var first *store.APIKey
	for i := range store.MaxAPIKeys {
		_, k := newKey(t, st, u.ID, fmt.Sprintf("k%d", i), []string{"read"}, nil, now)
		if i == 0 {
			first = k
		}
	}
	_, hash, hint, _ := auth.NewAPIKey()
	if _, err := st.CreateAPIKey(ctx, u.ID, store.NewAPIKey{Hash: hash, Hint: hint, Name: "one more", Scopes: []string{"read"}}, now); !errors.Is(err, store.ErrAPIKeyLimit) {
		t.Fatalf("over the limit: %v", err)
	}
	// A revoked key frees a place.
	if _, err := st.RevokeAPIKey(ctx, first.ID, nil, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAPIKey(ctx, u.ID, store.NewAPIKey{Hash: hash, Hint: hint, Name: "one more", Scopes: []string{"read"}}, now); err != nil {
		t.Fatalf("after revoking: %v", err)
	}
	// A key of a user who does not exist.
	if _, err := st.CreateAPIKey(ctx, u.ID+1000, store.NewAPIKey{Hash: auth.HashAPIKey("x"), Hint: "abcd", Name: "x", Scopes: []string{"read"}}, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}

	// TouchAPIKey writes at most once a minute.
	lastUsed := func() time.Time {
		t.Helper()
		var used time.Time
		if err := conn.QueryRow(ctx, "SELECT last_used_at FROM api_keys WHERE id = $1", first.ID+1).Scan(&used); err != nil {
			t.Fatal(err)
		}
		return used
	}
	if err := st.TouchAPIKey(ctx, first.ID+1, now); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchAPIKey(ctx, first.ID+1, now.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := lastUsed(); !got.Equal(now.Truncate(time.Microsecond)) {
		t.Fatalf("touched twice within a minute: %v, want %v", got, now)
	}
	if err := st.TouchAPIKey(ctx, first.ID+1, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := lastUsed(); !got.Equal(now.Add(2 * time.Minute).Truncate(time.Microsecond)) {
		t.Fatalf("not touched after a minute: %v", got)
	}
}

// TestAPIKeysMigrationDown rolls back the API keys migration with keys
// in the table and applies it again.
func TestAPIKeysMigrationDown(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	newKey(t, st, u.ID, "k", []string{"read"}, nil, time.Now())
	if err := st.MigrateDownTo(ctx, 8); err != nil {
		t.Fatalf("down: %v", err)
	}
	if _, err := st.ListAPIKeys(ctx, u.ID); err == nil {
		t.Fatal("api_keys survived the rollback")
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if keys, err := st.ListAPIKeys(ctx, u.ID); err != nil || len(keys) != 0 {
		t.Fatalf("after up: %v %v", keys, err)
	}
}

const uploadScopeVersion = 17

// insertRawKey stores a key with scopes directly, as an older version or
// another path would, and returns the key's hash.
func insertRawKey(t *testing.T, conn *pgx.Conn, userID int64, name string, scopes []string) ([]byte, error) {
	t.Helper()
	_, hash, hint, err := auth.NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(context.Background(), `INSERT INTO api_keys (user_id, key_hash, hint, name, scopes, created_at)
		VALUES ($1, $2, $3, $4, $5, now())`, userID, hash, hint, name, scopes)
	return hash, err
}

// Migration 00017 allows the upload scope and keeps the existing keys and
// the cardinality check; Down removes upload from keys with another scope,
// deletes the upload-only keys and refuses upload again; Up works again.
func TestAPIKeyUploadScopeMigration(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	conn := connect(t, url)
	u, err := st.CreateUser(ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MigrateDownTo(ctx, uploadScopeVersion-1); err != nil {
		t.Fatal(err)
	}
	// Keys from before the upgrade.
	old := map[string][]string{"r": {"read"}, "w": {"write"}, "rw": {"read", "write"}}
	var rwHash []byte
	for name, scopes := range old {
		h, err := insertRawKey(t, conn, u.ID, name, scopes)
		if err != nil {
			t.Fatalf("old %s: %v", name, err)
		}
		if name == "rw" {
			rwHash = h
		}
	}
	if _, err := insertRawKey(t, conn, u.ID, "u", []string{"upload"}); !checkViolation(err) {
		t.Fatalf("upload before up: %v", err)
	}

	refusals := func(stage string) {
		t.Helper()
		for _, bad := range [][]string{{}, {"admin"}, {"read", "admin"}} {
			if _, err := insertRawKey(t, conn, u.ID, "bad", bad); !checkViolation(err) {
				t.Errorf("%s: scopes %v: %v", stage, bad, err)
			}
		}
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	refusals("after up")
	// The old [read, write] key keeps working, with the upload.
	_, k, err := st.APIKeyLogin(ctx, rwHash, time.Now())
	if err != nil || !k.Allows(store.ScopeRead) || !k.Allows(store.ScopeUpload) || !k.Allows(store.ScopeWrite) {
		t.Fatalf("old key after up: %+v %v", k, err)
	}
	added := map[string][]string{"u": {"upload"}, "ru": {"read", "upload"}, "uw": {"upload", "write"}, "ruw": {"read", "upload", "write"}}
	for name, scopes := range added {
		if _, err := insertRawKey(t, conn, u.ID, name, scopes); err != nil {
			t.Fatalf("after up %s: %v", name, err)
		}
	}

	stored := func() map[string]string {
		t.Helper()
		keys, err := st.ListAPIKeys(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, k := range keys {
			out[k.Name] = fmt.Sprint(k.Scopes)
		}
		return out
	}
	if m, err := st.MigrateDown(ctx); err != nil || m != "00017_api_key_upload_scope.sql" {
		t.Fatalf("down: %q, %v", m, err)
	}
	want := map[string]string{
		"r": "[read]", "w": "[write]", "rw": "[read write]",
		"ru": "[read]", "uw": "[write]", "ruw": "[read write]",
	}
	if got := stored(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("after down: %v, want %v", got, want)
	}
	if _, err := insertRawKey(t, conn, u.ID, "u2", []string{"upload"}); !checkViolation(err) {
		t.Errorf("upload after down: %v", err)
	}
	refusals("after down")

	if _, err := st.Migrate(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	refusals("after down and up")
	if _, err := insertRawKey(t, conn, u.ID, "u", []string{"upload"}); err != nil {
		t.Errorf("upload after down and up: %v", err)
	}
	if got := stored(); fmt.Sprint(got) != fmt.Sprint(map[string]string{
		"r": "[read]", "w": "[write]", "rw": "[read write]", "ru": "[read]", "uw": "[write]", "ruw": "[read write]", "u": "[upload]",
	}) {
		t.Errorf("after down and up: %v", got)
	}
}

// The migration stops when the check it replaces cannot be found exactly
// once, instead of dropping the wrong one.
func TestAPIKeyUploadScopeMigrationNeedsTheCheck(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	conn := connect(t, url)
	if err := st.MigrateDownTo(ctx, uploadScopeVersion-1); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `ALTER TABLE api_keys ADD CONSTRAINT extra_scopes_check CHECK (cardinality(scopes) <= 3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err == nil || !strings.Contains(err.Error(), "expected one check on scopes") {
		t.Fatalf("up with two checks: %v", err)
	}
	if _, err := conn.Exec(ctx, `ALTER TABLE api_keys DROP CONSTRAINT extra_scopes_check`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
}
