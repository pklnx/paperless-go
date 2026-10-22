package auth

import (
	"bytes"
	"crypto/sha256"
	"strings"
	"testing"
)

func TestNewAPIKey(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		key, hash, hint, err := NewAPIKey()
		if err != nil {
			t.Fatal(err)
		}
		if len(key) != 55 || !strings.HasPrefix(key, "pl_") || !ValidAPIKeyFormat(key) {
			t.Fatalf("key %q", key)
		}
		if strings.Trim(key[3:], "abcdefghijklmnopqrstuvwxyz234567") != "" {
			t.Fatalf("key %q has other characters", key)
		}
		if sum := sha256.Sum256([]byte(key)); !bytes.Equal(hash, sum[:]) || !bytes.Equal(HashAPIKey(key), hash) {
			t.Fatal("hash is not the SHA-256 of the key")
		}
		if hint != key[51:] || len(hint) != 4 {
			t.Fatalf("hint %q of %q", hint, key)
		}
		if seen[key] {
			t.Fatal("duplicate key")
		}
		seen[key] = true
	}
}

func TestValidAPIKeyFormat(t *testing.T) {
	key, _, _, _ := NewAPIKey()
	for _, bad := range []string{
		"", "pl_", key[:54], key + "a", "PL_" + key[3:], "pl_" + strings.ToUpper(key[3:]),
		"pl_" + strings.Repeat("1", 52), "pl_" + strings.Repeat("a", 51) + "-", "xx_" + key[3:],
	} {
		if ValidAPIKeyFormat(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
