// Package storetest provides a throwaway PostgreSQL database for tests.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/auth"
	"github.com/pklnx/paperless-go/internal/store"
)

// EnvURL names the variable holding a PostgreSQL URL with CREATEDB rights.
// The server must be PostgreSQL 17 or newer (store.MinServerVersion); on
// an older one every test fails in migrate with that message.
const EnvURL = "TEST_DATABASE_URL"

// New creates a fresh, migrated database that is dropped when the test ends.
// The test is skipped if TEST_DATABASE_URL is not set.
func New(t *testing.T) *store.Store {
	t.Helper()
	st, _ := NewWithURL(t)
	return st
}

// NewWithURL is like New and also returns the database URL, for tests that
// need to set up state the store API cannot create.
func NewWithURL(t *testing.T) (*store.Store, string) {
	t.Helper()
	return NewWithParams(t, nil)
}

// NewWithParams is NewWithURL with extra connection parameters in the
// store's URL, for example pool_max_conns for a test that needs a pool of
// a known size. The returned URL has none of them.
func NewWithParams(t *testing.T, params map[string]string) (*store.Store, string) {
	t.Helper()
	return NewWithOptions(t, Options{Params: params})
}

// AnyBlob is a store.Blobs that reports every file as present: for tests
// whose documents name hashes no blob store holds. A test of the file
// removal passes the real blob store instead (Options.Blobs).
var AnyBlob store.Blobs = anyBlob{}

type anyBlob struct{}

func (anyBlob) Exists(string) bool { return true }

// Options are NewWithOptions' choices.
type Options struct {
	// Params are extra connection parameters in the store's URL (see
	// NewWithParams).
	Params map[string]string
	// Blobs is the store's blob store (store.Open); nil is AnyBlob.
	Blobs store.Blobs
}

// NewWithOptions is NewWithURL with the choices of o.
func NewWithOptions(t *testing.T, o Options) (*store.Store, string) {
	t.Helper()
	params := o.Params
	blobs := o.Blobs
	if blobs == nil {
		blobs = AnyBlob
	}
	base := os.Getenv(EnvURL)
	if base == "" {
		t.Skip(EnvURL + " not set; skipping database test")
	}
	ctx := context.Background()

	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	name := "paperless_test_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("create database: %v", err)
	}

	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	plain := u.String()
	if len(params) > 0 {
		q := u.Query()
		for k, v := range params {
			q.Set(k, v)
		}
		u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
	}
	st, err := store.Open(ctx, u.String(), blobs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st, plain
}

// Password is the password of users created by User.
const Password = "correct horse battery"

// Hasher hashes with tiny Argon2id parameters, which keeps tests fast. The
// hashes have the same format as real ones.
var Hasher = auth.NewHasher(auth.Params{Memory: 64, Time: 1, Threads: 1})

// User creates a user in st whose password is Password and fails the test
// on error.
func User(t *testing.T, st *store.Store, name string, admin bool) *store.User {
	t.Helper()
	ctx := context.Background()
	hash, err := Hasher.Hash(ctx, Password)
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser(ctx, name, hash, admin)
	if err != nil {
		t.Fatalf("create user %q: %v", name, err)
	}
	return u
}
