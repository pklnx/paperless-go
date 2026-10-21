package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/paperless-go/internal/config"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// runUser runs `user args...` against the test database with stdin as
// input, with cheap password hashes.
func runUser(t *testing.T, dbURL, stdin string, args ...string) (string, error) {
	t.Helper()
	old := hasher
	hasher = storetest.Hasher
	t.Cleanup(func() { hasher = old })
	t.Setenv(config.EnvDatabaseURL, dbURL)
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(append([]string{"user"}, args...))
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func TestUserCommands(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()

	if out, err := runUser(t, url, "", "list"); err != nil || !strings.Contains(out, "user add NAME --admin") {
		t.Fatalf("list without users: %q, %v", out, err)
	}
	if _, err := runUser(t, url, "short\n", "add", "patrick", "--admin", "--password-stdin"); err == nil || !strings.Contains(err.Error(), "12 characters") {
		t.Fatalf("short password: %v", err)
	}
	if _, err := runUser(t, url, "", "add", "patrick", "--password-stdin"); err == nil || !strings.Contains(err.Error(), "no password") {
		t.Fatalf("no password: %v", err)
	}
	if _, err := runUser(t, url, "x\n", "add", "Not Valid"); err == nil {
		t.Fatal("invalid name accepted")
	}
	if out, err := runUser(t, url, "correct horse battery\n", "add", "Patrick", "--admin", "--password-stdin"); err != nil || out != "admin \"patrick\" created\n" {
		t.Fatalf("add: %q, %v", out, err)
	}
	u, err := st.GetUserByName(ctx, "patrick")
	if err != nil || !u.IsAdmin || !strings.HasPrefix(u.PasswordHash, "$argon2id$") || u.MustChangePassword {
		t.Fatalf("created user: %+v, %v", u, err)
	}
	if ok, _, _ := storetest.Hasher.Verify(ctx, u.PasswordHash, "correct horse battery"); !ok {
		t.Fatal("password not stored")
	}
	if _, err := runUser(t, url, "correct horse battery\n", "add", "patrick", "--password-stdin"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate: %v", err)
	}

	// The only admin can be neither locked, demoted nor removed.
	for _, args := range [][]string{{"lock", "patrick"}, {"demote", "patrick"}, {"unadmin", "patrick"}, {"remove", "patrick"}} {
		if _, err := runUser(t, url, "", args...); err == nil || !strings.Contains(err.Error(), "last admin") {
			t.Fatalf("%v on the last admin: %v", args, err)
		}
	}

	// Without a terminal, the password is read from stdin anyway.
	if _, err := runUser(t, url, "another horse battery\n", "add", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := runUser(t, url, "", "lock", "alice"); err != nil {
		t.Fatal(err)
	}
	if a, _ := st.GetUserByName(ctx, "alice"); a.LockedAt == nil {
		t.Fatal("alice not locked")
	}
	if out, err := runUser(t, url, "", "list"); err != nil || !strings.Contains(out, "locked") {
		t.Fatalf("list: %q, %v", out, err)
	}
	if _, err := runUser(t, url, "", "unlock", "ALICE"); err != nil {
		t.Fatal(err)
	}
	before, _ := st.GetUserByName(ctx, "alice")
	if _, err := runUser(t, url, "a third horse battery\n", "set-password", "alice", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	if after, _ := st.GetUserByName(ctx, "alice"); after.PasswordHash == before.PasswordHash || after.LockedAt != nil || after.MustChangePassword {
		t.Fatalf("set-password: %+v", after)
	}

	// Promote and demote, also by their aliases.
	if _, err := runUser(t, url, "", "admin", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := runUser(t, url, "", "demote", "patrick"); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.GetUserByName(ctx, "patrick"); p.IsAdmin {
		t.Fatal("patrick still an admin")
	}
	if _, err := runUser(t, url, "", "promote", "patrick"); err != nil {
		t.Fatal(err)
	}

	out, err := runUser(t, url, "", "reset-password", "alice")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	pw := lines[len(lines)-1]
	after, _ := st.GetUserByName(ctx, "alice")
	if ok, _, _ := storetest.Hasher.Verify(ctx, after.PasswordHash, pw); !ok || !after.MustChangePassword || len(pw) != 23 {
		t.Fatalf("reset-password: %q, %+v", out, after)
	}

	if out, err := runUser(t, url, "", "list"); err != nil || !strings.Contains(out, "must change password") || !strings.Contains(out, "patrick") {
		t.Fatalf("list: %q, %v", out, err)
	}
	if _, err := runUser(t, url, "", "remove", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := runUser(t, url, "", "remove", "alice"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("remove twice: %v", err)
	}
}

// remove refuses while the user owns rows and names the counts per
// table; once the user owns nothing, it works, also with a session.
func TestUserRemoveOwner(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	storetest.User(t, st, "admin", true)
	kim := storetest.User(t, st, "kim", false)
	sc := store.Scope{UserID: kim.ID}
	tag, err := st.CreateTag(ctx, sc, store.TagInput{Name: "Tax"})
	if err != nil {
		t.Fatal(err)
	}
	c1, _ := st.CreateCorrespondent(ctx, sc, store.CorrespondentInput{Name: "ACME"})
	c2, _ := st.CreateCorrespondent(ctx, sc, store.CorrespondentInput{Name: "Bank"})
	now := time.Now()
	if err := st.CreateSession(ctx, bytes.Repeat([]byte{1}, 32), kim.ID, now, now.Add(time.Hour), ""); err != nil {
		t.Fatal(err)
	}

	_, err = runUser(t, url, "", "remove", "kim")
	if err == nil || !strings.Contains(err.Error(), `"kim" still owns 1 tag, 2 correspondents`) || !strings.Contains(err.Error(), "user lock kim") {
		t.Fatalf("remove an owner: %v", err)
	}
	for _, e := range []error{st.DeleteTag(ctx, sc, tag.ID), st.DeleteCorrespondent(ctx, sc, c1.ID), st.DeleteCorrespondent(ctx, sc, c2.ID)} {
		if e != nil {
			t.Fatal(e)
		}
	}
	if out, err := runUser(t, url, "", "remove", "kim"); err != nil || out != "user \"kim\" removed\n" {
		t.Fatalf("remove: %q, %v", out, err)
	}
}

func TestServeWarnsWithoutUsers(t *testing.T) {
	st := storetest.New(t)
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	warnWithoutUsers(context.Background(), st, log)
	if !strings.Contains(buf.String(), "no users yet") || !strings.Contains(buf.String(), "user add NAME --admin") {
		t.Fatalf("log: %q", buf.String())
	}
	buf.Reset()
	storetest.User(t, st, "admin", true)
	warnWithoutUsers(context.Background(), st, log)
	if buf.Len() != 0 {
		t.Fatalf("warning with a user: %q", buf.String())
	}
}
