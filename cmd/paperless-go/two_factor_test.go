package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/paperless-go/internal/auth"
	"github.com/pklnx/paperless-go/internal/config"
	"github.com/pklnx/paperless-go/internal/crypto"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

func TestKeygen(t *testing.T) {
	var keys []string
	for range 2 {
		cmd := newRootCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"keygen"})
		if err := cmd.ExecuteContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		line := out.String()
		if !strings.HasSuffix(line, "\n") || strings.Count(line, "\n") != 1 {
			t.Fatalf("keygen output: %q", line)
		}
		key, err := crypto.ParseKey(strings.TrimSpace(line))
		if err != nil || len(key) != crypto.KeySize {
			t.Fatalf("keygen key %q: %v", line, err)
		}
		keys = append(keys, line)
	}
	if keys[0] == keys[1] {
		t.Fatal("keygen printed the same key twice")
	}
}

// serve refuses to start without a valid secret key: without it nobody
// with 2FA, admins included, could log in.
func TestServeNeedsSecretKey(t *testing.T) {
	_, url := storetest.NewWithURL(t)
	t.Setenv(config.EnvSecretKey, "")
	_, err := run(t, url, "serve", "--listen", "127.0.0.1:0")
	if err == nil || !strings.Contains(err.Error(), config.EnvSecretKey) || !strings.Contains(err.Error(), "keygen") {
		t.Fatalf("serve without a key: %v", err)
	}
	t.Setenv(config.EnvSecretKey, "c2hvcnQ=")
	if _, err := run(t, url, "serve", "--listen", "127.0.0.1:0"); err == nil || !strings.Contains(err.Error(), config.EnvSecretKey) {
		t.Fatalf("serve with a short key: %v", err)
	}
}

// A sole admin who lost their authenticator gets back in: reset-2fa works
// for the last admin, and the next login asks for the setup again.
func TestUserReset2FA(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	admin := storetest.User(t, st, "admin", true)
	sealer, err := crypto.NewSealer(bytes.Repeat([]byte{3}, crypto.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := auth.GenerateTOTPSecret()
	codes, _ := auth.GenerateRecoveryCodes()
	if err := st.BeginTwoFactorSetup(ctx, admin.ID, secret, sealer); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	code, _ := auth.GenerateTOTP(secret, now)
	if err := st.ConfirmTwoFactorSetup(ctx, admin.ID, code, now, sealer, codes, bytes.Repeat([]byte{3}, crypto.KeySize)); err != nil {
		t.Fatal(err)
	}
	if out, err := runUser(t, url, "", "list"); err != nil || !strings.Contains(out, "2FA") || !strings.Contains(out, "admin  active  on") {
		t.Fatalf("list with 2FA: %q, %v", out, err)
	}
	out, err := runUser(t, url, "", "reset-2fa", "ADMIN")
	if err != nil || !strings.Contains(out, `"admin" reset`) {
		t.Fatalf("reset-2fa of the last admin: %q, %v", out, err)
	}
	if st, err := st.GetTwoFactorState(ctx, admin.ID); err != nil || st.Enabled || st.RecoveryCodesLeft != 0 {
		t.Fatalf("after reset: %+v, %v", st, err)
	}
	if out, err := runUser(t, url, "", "list"); err != nil || !strings.Contains(out, "admin  active  off") {
		t.Fatalf("list after reset: %q, %v", out, err)
	}
	if _, err := runUser(t, url, "", "reset-2fa", "nobody"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("reset-2fa of an unknown user: %v", err)
	}
}
