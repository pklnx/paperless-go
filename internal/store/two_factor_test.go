package store_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/auth"
	"github.com/pklnx/paperless-go/internal/crypto"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

func testSealer(t *testing.T) (*crypto.Sealer, []byte) {
	t.Helper()
	key := bytes.Repeat([]byte{9}, crypto.KeySize)
	s, err := crypto.NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	return s, key
}

// enable2FA turns 2FA on for u at now and returns the secret and the
// recovery codes.
func enable2FA(t *testing.T, st *store.Store, u *store.User, now time.Time) (string, []string) {
	t.Helper()
	ctx := context.Background()
	sealer, key := testSealer(t)
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	codes, err := auth.GenerateRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BeginTwoFactorSetup(ctx, u.ID, secret, sealer); err != nil {
		t.Fatal(err)
	}
	code, err := auth.GenerateTOTP(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ConfirmTwoFactorSetup(ctx, u.ID, code, now, sealer, codes, key); err != nil {
		t.Fatal(err)
	}
	return secret, codes
}

// A user with 2FA, recovery codes, a pending login and a session can be
// removed: the auth tables go with the user.
func TestDeleteUserWithTwoFactor(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	now := time.Now()
	storetest.User(t, st, "admin", true)
	kim := storetest.User(t, st, "kim", false)
	enable2FA(t, st, kim, now)
	kim, _ = st.GetUserByID(ctx, kim.ID)
	if _, err := st.CreateTwoFactorChallenge(ctx, kim.ID, kim.TwoFactorVersion, now, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, sessionID(1), kim.ID, now, now.Add(time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser(ctx, kim.ID); err != nil {
		t.Fatalf("remove a user with 2FA: %v", err)
	}
}

// A sealed secret is bound to its user: copied to another user's row it
// does not open, so no code is accepted.
func TestTwoFactorSecretBoundToUser(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	now := time.Now()
	sealer, key := testSealer(t)
	alice := storetest.User(t, st, "alice", false)
	bob := storetest.User(t, st, "bob", false)
	secret, _ := enable2FA(t, st, alice, now)
	enable2FA(t, st, bob, now)

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, `UPDATE users SET two_factor_secret = (SELECT two_factor_secret FROM users WHERE id = $1),
		two_factor_last_counter = NULL WHERE id = $2`, alice.ID, bob.ID); err != nil {
		t.Fatal(err)
	}
	later := now.Add(auth.TOTPPeriod)
	code, err := auth.GenerateTOTP(secret, later)
	if err != nil {
		t.Fatal(err)
	}
	err = st.ReplaceRecoveryCodes(ctx, bob.ID, code, later, sealer, nil, key)
	if err == nil || errors.Is(err, store.ErrTwoFactorInvalid) {
		t.Fatalf("alice's secret on bob's row: %v", err)
	}
	// On alice's own row it works.
	if err := st.ReplaceRecoveryCodes(ctx, alice.ID, code, later, sealer, nil, key); err != nil {
		t.Fatalf("own secret: %v", err)
	}
}

// Reset works for the last admin and ends their sessions.
func TestResetTwoFactorOfLastAdmin(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	now := time.Now()
	admin := storetest.User(t, st, "admin", true)
	enable2FA(t, st, admin, now)
	if err := st.CreateSession(ctx, sessionID(2), admin.ID, now, now.Add(time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	if err := st.ResetTwoFactor(ctx, admin.ID); err != nil {
		t.Fatal(err)
	}
	state, err := st.GetTwoFactorState(ctx, admin.ID)
	if err != nil || state.Enabled || state.RecoveryCodesLeft != 0 {
		t.Fatalf("after reset: %+v, %v", state, err)
	}
	if _, err := st.GetSession(ctx, sessionID(2), now, now.Add(-time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("session after reset: %v", err)
	}
	if err := st.ResetTwoFactor(ctx, 999999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
}
