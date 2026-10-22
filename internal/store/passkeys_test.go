package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/auth"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// fakeCred stands in for the WebAuthn library's credential record: only
// the sign count matters here.
type fakeCred struct {
	Count int `json:"count"`
}

func noBegin(_ []byte, _ [][]byte) ([]byte, error) { return []byte(`{}`), nil }

// registerFake adds a passkey with credential ID id and sign count 0.
func registerFake(t *testing.T, st *store.Store, userID int64, name, id string, now time.Time) error {
	t.Helper()
	ctx := context.Background()
	token, err := st.BeginPasskeyRegistration(ctx, userID, name, "", now, now.Add(time.Minute), noBegin)
	if err != nil {
		return err
	}
	_, err = st.FinishPasskeyRegistration(ctx, userID, token, now, func(_, _ []byte) ([]byte, []byte, error) {
		return []byte(id), []byte(`{"count":0}`), nil
	})
	return err
}

func newSessionHash(t *testing.T) []byte {
	t.Helper()
	_, hash, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func TestPasskeyCeremonies(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	now := time.Now()
	alice := storetest.User(t, st, "alice", false)
	bob := storetest.User(t, st, "bob", false)
	if err := registerFake(t, st, alice.ID, "Phone", "cred-1", now); err != nil {
		t.Fatal(err)
	}
	if err := registerFake(t, st, alice.ID, "Phone", "cred-2", now); !errors.Is(err, store.ErrPasskeyName) {
		t.Fatalf("same name: %v", err)
	}
	if err := registerFake(t, st, bob.ID, "Phone", "cred-1", now); !errors.Is(err, store.ErrPasskeyRegistered) {
		t.Fatalf("credential of another user: %v", err)
	}
	list, err := st.ListPasskeys(ctx, alice.ID)
	if err != nil || len(list) != 1 || !list[0].CreatedAt.Equal(now.Truncate(time.Microsecond)) || list[0].LastUsedAt != nil {
		t.Fatalf("list: %+v %v", list, err)
	}

	// A registration token is single use and bound to its user, also when
	// the check fails.
	token, err := st.BeginPasskeyRegistration(ctx, alice.ID, "Key", "", now, now.Add(time.Minute), noBegin)
	if err != nil {
		t.Fatal(err)
	}
	finish := func(_, _ []byte) ([]byte, []byte, error) { return []byte("cred-3"), []byte(`{}`), nil }
	if _, err := st.FinishPasskeyRegistration(ctx, bob.ID, token, now, finish); !errors.Is(err, store.ErrCeremonyExpired) {
		t.Fatalf("other user: %v", err)
	}
	if _, err := st.FinishPasskeyRegistration(ctx, alice.ID, token, now, finish); !errors.Is(err, store.ErrCeremonyExpired) {
		t.Fatalf("second use: %v", err)
	}

	// Ceremonies expire on the caller's clock, and the cleanup removes
	// them (the refused one and a new one).
	token, err = st.BeginPasskeyLogin(ctx, "192.0.2.1", []byte(`{}`), now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	verify := func(_, _, c []byte) ([]byte, error) { return c, nil }
	later := now.Add(time.Minute)
	if _, err := st.CompletePasskeyLogin(ctx, token, []byte("cred-1"), later, verify, newSessionHash(t), later.Add(time.Hour), ""); !errors.Is(err, store.ErrCeremonyExpired) {
		t.Fatalf("expired: %v", err)
	}
	if _, err := st.BeginPasskeyLogin(ctx, "192.0.2.1", []byte(`{}`), now, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n, err := st.DeleteExpiredWebAuthnCeremonies(ctx, now); err != nil || n != 0 {
		t.Fatalf("cleanup before expiry: %d, %v", n, err)
	}
	if n, err := st.DeleteExpiredWebAuthnCeremonies(ctx, later); err != nil || n != 2 {
		t.Fatalf("cleanup after expiry: %d, %v", n, err)
	}

	// A login token works once, even after a failed check.
	token, err = st.BeginPasskeyLogin(ctx, "192.0.2.1", []byte(`{}`), now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	fail := func(_, _, _ []byte) ([]byte, error) { return nil, errors.New("bad signature") }
	if _, err := st.CompletePasskeyLogin(ctx, token, []byte("cred-1"), now, fail, newSessionHash(t), now.Add(time.Hour), ""); !errors.Is(err, store.ErrPasskeyInvalid) {
		t.Fatalf("bad signature: %v", err)
	}
	if _, err := st.CompletePasskeyLogin(ctx, token, []byte("cred-1"), now, verify, newSessionHash(t), now.Add(time.Hour), ""); !errors.Is(err, store.ErrCeremonyExpired) {
		t.Fatalf("second use: %v", err)
	}

	// An unknown passkey.
	token, err = st.BeginPasskeyLogin(ctx, "192.0.2.1", []byte(`{}`), now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CompletePasskeyLogin(ctx, token, []byte("nobody"), now, verify, newSessionHash(t), now.Add(time.Hour), ""); !errors.Is(err, store.ErrPasskeyUnknown) {
		t.Fatalf("unknown passkey: %v", err)
	}

	// A good login creates the session and records the use on the clock.
	token, err = st.BeginPasskeyLogin(ctx, "192.0.2.1", []byte(`{}`), now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	hash := newSessionHash(t)
	login, err := st.CompletePasskeyLogin(ctx, token, []byte("cred-1"), now, verify, hash, now.Add(time.Hour), "")
	if err != nil || login.User.ID != alice.ID || login.PasskeyName != "Phone" {
		t.Fatalf("login: %+v %v", login, err)
	}
	if _, err := st.GetSession(ctx, hash, now, now.Add(-time.Hour)); err != nil {
		t.Fatalf("session: %v", err)
	}
	list, _ = st.ListPasskeys(ctx, alice.ID)
	if list[0].LastUsedAt == nil || !list[0].LastUsedAt.Equal(now.Truncate(time.Microsecond)) {
		t.Fatalf("last use: %+v", list[0])
	}
}

// A locked user's passkey is refused only after it was checked.
func TestPasskeyLoginLockedUser(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	now := time.Now()
	storetest.User(t, st, "admin", true)
	alice := storetest.User(t, st, "alice", false)
	if err := registerFake(t, st, alice.ID, "Phone", "cred-1", now); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserLocked(ctx, alice.ID, true); err != nil {
		t.Fatal(err)
	}
	token, err := st.BeginPasskeyLogin(ctx, "192.0.2.1", []byte(`{}`), now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	verify := func(_, _, c []byte) ([]byte, error) { return c, nil }
	if _, err := st.CompletePasskeyLogin(ctx, token, []byte("cred-1"), now, verify, newSessionHash(t), now.Add(time.Hour), ""); !errors.Is(err, store.ErrUserLocked) {
		t.Fatalf("locked: %v", err)
	}
	// Nor can a locked user finish a registration.
	token, err = st.BeginPasskeyRegistration(ctx, alice.ID, "Key", "", now, now.Add(time.Minute), noBegin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.FinishPasskeyRegistration(ctx, alice.ID, token, now, func(_, _ []byte) ([]byte, []byte, error) {
		return []byte("cred-2"), []byte(`{}`), nil
	}); !errors.Is(err, store.ErrCeremonyExpired) {
		t.Fatalf("locked registration: %v", err)
	}
}

// waitForLockWait returns once a statement of the database at conn waits
// for a row lock; it fails the test if done delivers first.
func waitForLockWait(t *testing.T, conn *pgx.Conn, done <-chan error) {
	t.Helper()
	ctx := t.Context()
	for {
		var waiting int
		err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting)
		if err == nil && waiting > 0 {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("finished without waiting for the lock: %v", err)
		case <-ctx.Done():
			t.Fatal("never waited for the lock")
		default:
		}
	}
}

// A second login with the same passkey and sign count waits for the first
// (row locks) and is checked against the count the first stored.
func TestPasskeyLoginsSerialized(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	now := time.Now()
	alice := storetest.User(t, st, "alice", false)
	if err := registerFake(t, st, alice.ID, "Phone", "cred-1", now); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	verify := func(_, _, raw []byte) ([]byte, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		var c fakeCred
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		if c.Count >= 1 {
			return nil, errors.New("sign count did not increase")
		}
		return json.Marshal(fakeCred{Count: 1})
	}
	login := func() error {
		token, err := st.BeginPasskeyLogin(ctx, "192.0.2.1", []byte(`{}`), now, now.Add(time.Minute))
		if err != nil {
			return err
		}
		_, err = st.CompletePasskeyLogin(ctx, token, []byte("cred-1"), now, verify, newSessionHash(t), now.Add(time.Hour), "")
		return err
	}
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- login() }()
	<-entered
	go func() { second <- login() }()
	waitForLockWait(t, conn, second)
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first login: %v", err)
	}
	if err := <-second; !errors.Is(err, store.ErrPasskeyInvalid) {
		t.Fatalf("second login: %v, want ErrPasskeyInvalid", err)
	}
}

// Two registrations at 9 passkeys: the second waits for the user row and
// then finds the limit reached.
func TestPasskeyLimitSerialized(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	now := time.Now()
	alice := storetest.User(t, st, "alice", false)
	for i := range store.MaxPasskeys - 1 {
		if err := registerFake(t, st, alice.ID, fmt.Sprintf("Key %d", i), fmt.Sprintf("cred-%d", i), now); err != nil {
			t.Fatal(err)
		}
	}
	tokens := make([]string, 2)
	for i := range tokens {
		var err error
		if tokens[i], err = st.BeginPasskeyRegistration(ctx, alice.ID, fmt.Sprintf("New %d", i), "", now, now.Add(time.Minute), noBegin); err != nil {
			t.Fatal(err)
		}
	}
	entered, release := make(chan struct{}), make(chan struct{})
	finish := func(i int, wait bool) error {
		_, err := st.FinishPasskeyRegistration(ctx, alice.ID, tokens[i], now, func(_, _ []byte) ([]byte, []byte, error) {
			if wait {
				close(entered)
				<-release
			}
			return fmt.Appendf(nil, "new-%d", i), []byte(`{}`), nil
		})
		return err
	}
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- finish(0, true) }()
	<-entered
	go func() { second <- finish(1, false) }()
	waitForLockWait(t, conn, second)
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first registration: %v", err)
	}
	if err := <-second; !errors.Is(err, store.ErrPasskeyLimit) {
		t.Fatalf("second registration: %v, want ErrPasskeyLimit", err)
	}
	if err := registerFake(t, st, alice.ID, "More", "cred-x", now); !errors.Is(err, store.ErrPasskeyLimit) {
		t.Fatalf("begin over the limit: %v", err)
	}
}

func TestPasskeyRemovalAndPasswordReset(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	now := time.Now()
	alice := storetest.User(t, st, "alice", false)
	keep, other := newSessionHash(t), newSessionHash(t)
	for _, h := range [][]byte{keep, other} {
		if err := st.CreateSession(ctx, h, alice.ID, now, now.Add(time.Hour), ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := registerFake(t, st, alice.ID, "Phone", "cred-1", now); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListPasskeys(ctx, alice.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}
	bob := storetest.User(t, st, "bob", false)
	if _, err := st.RemoveOwnPasskey(ctx, bob.ID, list[0].ID, keep); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("remove another user's passkey: %v", err)
	}
	p, err := st.RemoveOwnPasskey(ctx, alice.ID, list[0].ID, keep)
	if err != nil || p.Name != "Phone" || string(p.CredentialID) != "cred-1" {
		t.Fatalf("remove: %+v %v", p, err)
	}
	if _, err := st.GetSession(ctx, keep, now, now.Add(-time.Hour)); err != nil {
		t.Fatalf("own session ended: %v", err)
	}
	if _, err := st.GetSession(ctx, other, now, now.Add(-time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other session still valid: %v", err)
	}

	// A password reset keeps or removes passkeys as asked.
	if err := registerFake(t, st, alice.ID, "Phone", "cred-1", now); err != nil {
		t.Fatal(err)
	}
	if err := st.ResetUserPassword(ctx, alice.ID, "h2", store.PasswordReset{MustChange: true}); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.PasskeyCounts(ctx); n[alice.ID] != 1 {
		t.Fatalf("passkeys after reset with keep: %v", n)
	}
	if err := st.ResetUserPassword(ctx, alice.ID, "h3", store.PasswordReset{MustChange: true, DropPasskeys: true}); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.PasskeyCounts(ctx); n[alice.ID] != 0 {
		t.Fatalf("passkeys after reset: %v", n)
	}

	// Removing all passkeys ends every session.
	if err := registerFake(t, st, alice.ID, "Phone", "cred-1", now); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, keep, alice.ID, now, now.Add(time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	if n, err := st.RemovePasskeys(ctx, alice.ID); err != nil || n != 1 {
		t.Fatalf("remove all: %d, %v", n, err)
	}
	if _, err := st.GetSession(ctx, keep, now, now.Add(-time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("session after removing all passkeys: %v", err)
	}
}

// A user with passkeys and a registration in progress can still be
// removed: the auth rows go with the user.
func TestDeleteUserWithPasskeys(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	now := time.Now()
	kim := storetest.User(t, st, "kim", false)
	if err := registerFake(t, st, kim.ID, "Phone", "cred-1", now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginPasskeyRegistration(ctx, kim.ID, "Key", "", now, now.Add(time.Minute), noBegin); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser(ctx, kim.ID, time.Now()); err != nil {
		t.Fatalf("remove a user with passkeys: %v", err)
	}
}

func TestPasskeyLoginCeremonyCap(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	now := time.Now()
	begin := func(client string, at time.Time) error {
		_, err := st.BeginPasskeyLogin(ctx, client, []byte(`{}`), at, at.Add(time.Minute))
		return err
	}
	// The total, spread over clients that stay within their share.
	per := store.MaxOpenLoginCeremoniesPerClient
	for i := range store.MaxOpenLoginCeremonies {
		if err := begin(fmt.Sprintf("client-%d", i/per), now); err != nil {
			t.Fatalf("ceremony %d: %v", i, err)
		}
	}
	if err := begin("someone-new", now); !errors.Is(err, store.ErrTooManyCeremonies) {
		t.Fatalf("over the cap: %v", err)
	}
	// Expired ones do not count.
	later := now.Add(time.Minute)
	if err := begin("someone-new", later); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
}

// One client cannot take more than its share; another one still can.
func TestPasskeyLoginCeremonyCapPerClient(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	now := time.Now()
	begin := func(client string, at time.Time) error {
		_, err := st.BeginPasskeyLogin(ctx, client, []byte(`{}`), at, at.Add(time.Minute))
		return err
	}
	for i := range store.MaxOpenLoginCeremoniesPerClient {
		if err := begin("2001:db8::/64", now); err != nil {
			t.Fatalf("ceremony %d: %v", i, err)
		}
	}
	if err := begin("2001:db8::/64", now); !errors.Is(err, store.ErrTooManyClientCeremonies) {
		t.Fatalf("over the client's share: %v", err)
	}
	if err := begin("192.0.2.1", now); err != nil {
		t.Fatalf("another client: %v", err)
	}
	if err := begin("2001:db8::/64", now.Add(time.Minute)); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
}

// Concurrent begins from one client stop exactly at its share: the check
// and the insert run under one lock.
func TestPasskeyLoginCeremonyCapConcurrent(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	now := time.Now()
	const n = 40
	errs := make(chan error, n)
	start := make(chan struct{})
	for range n {
		go func() {
			<-start
			_, err := st.BeginPasskeyLogin(ctx, "192.0.2.7", []byte(`{}`), now, now.Add(time.Minute))
			errs <- err
		}()
	}
	close(start)
	ok := 0
	for range n {
		switch err := <-errs; {
		case err == nil:
			ok++
		case !errors.Is(err, store.ErrTooManyClientCeremonies):
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != store.MaxOpenLoginCeremoniesPerClient {
		t.Fatalf("%d ceremonies, want %d", ok, store.MaxOpenLoginCeremoniesPerClient)
	}
}
