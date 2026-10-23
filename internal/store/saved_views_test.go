package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

func mustView(t *testing.T, st *store.Store, sc store.Scope, in store.SavedViewInput) *store.SavedView {
	t.Helper()
	v, err := st.CreateSavedView(context.Background(), sc, in)
	if err != nil {
		t.Fatalf("create saved view %q: %v", in.Name, err)
	}
	return v
}

// sameJSON reports whether two JSON texts hold the same value.
func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatalf("decode %s: %v", a, err)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return reflect.DeepEqual(va, vb)
}

func TestSavedViewRoundTrip(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}

	// Keys of a newer app version (futureCriterion, a sort value this
	// server does not know) are kept with their values.
	filter := json.RawMessage(`{
		"sort": "relevanceV2", "ascending": false,
		"tags": [{"id": "6F9619FF-8B86-D011-B42D-00C04FC964FF", "name": "Tax", "key": "tax"}],
		"futureCriterion": {"nested": [1, 2.5, null, "x"], "flag": true},
		"dateRange": {"absolute": {"from": "2026-01-01"}}
	}`)
	v := mustView(t, st, alice, store.SavedViewInput{Name: "Taxes", Symbol: "eurosign", SortIndex: 3, Filter: filter})
	got, err := st.GetSavedView(ctx, alice, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.OwnerID != alice.UserID || got.Name != "Taxes" || got.Symbol != "eurosign" || got.SortIndex != 3 ||
		got.CreatedAt.IsZero() || got.ModifiedAt.IsZero() || !sameJSON(t, got.Filter, filter) {
		t.Errorf("saved view = %+v, filter %s", got, got.Filter)
	}

	// Defaults: the app's symbol and an empty filter object.
	plain := mustView(t, st, alice, store.SavedViewInput{Name: "Plain"})
	if plain.Symbol != store.DefaultSavedViewSymbol || string(plain.Filter) != "{}" || plain.SortIndex != 0 {
		t.Errorf("defaults = %+v, filter %s", plain, plain.Filter)
	}
	// Names need not be unique, as in the app.
	mustView(t, st, alice, store.SavedViewInput{Name: "Taxes", SortIndex: 1})

	// Order: sort index first.
	views, err := st.ListSavedViews(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	var order []int64
	for _, v := range views {
		order = append(order, v.SortIndex)
	}
	if !reflect.DeepEqual(order, []int64{0, 1, 3}) {
		t.Errorf("sort indexes = %v, want [0 1 3]", order)
	}

	// Update replaces every field; the new filter's unknown keys stay too.
	newFilter := json.RawMessage(`{"inbox": true, "laterKey": [{"a": "b"}]}`)
	up, err := st.UpdateSavedView(ctx, alice, v.ID, store.SavedViewInput{Name: "Steuern", SortIndex: -1, Filter: newFilter})
	if err != nil {
		t.Fatal(err)
	}
	if up.Name != "Steuern" || up.Symbol != store.DefaultSavedViewSymbol || up.SortIndex != -1 ||
		!sameJSON(t, up.Filter, newFilter) || up.ModifiedAt.Before(got.ModifiedAt) || !up.CreatedAt.Equal(got.CreatedAt) {
		t.Errorf("updated = %+v, filter %s", up, up.Filter)
	}

	for _, bad := range []string{`[]`, `"x"`, `null`, `1`, `{"a":`, ` [ {} ] `, `{"a":"\u0000"}`, `{"a\u0000":1}`, `{"a":[{"b":"x\u0000"}]}`} {
		if _, err := st.CreateSavedView(ctx, alice, store.SavedViewInput{Name: "bad", Filter: json.RawMessage(bad)}); !errors.Is(err, store.ErrInvalidFilter) {
			t.Errorf("create with filter %s: %v, want ErrInvalidFilter", bad, err)
		}
		if _, err := st.UpdateSavedView(ctx, alice, v.ID, store.SavedViewInput{Name: "bad", Filter: json.RawMessage(bad)}); !errors.Is(err, store.ErrInvalidFilter) {
			t.Errorf("update with filter %s: %v, want ErrInvalidFilter", bad, err)
		}
	}
	if got, _ := st.GetSavedView(ctx, alice, v.ID); got.Name != "Steuern" {
		t.Errorf("failed update changed the view: %+v", got)
	}

	if err := st.DeleteSavedView(ctx, alice, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSavedView(ctx, alice, v.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("get after delete: %v", err)
	}
	if err := st.DeleteSavedView(ctx, alice, v.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	if _, err := st.GetSavedView(ctx, alice, "not-a-uuid"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("malformed id: %v", err)
	}
	if _, err := st.CreateSavedView(ctx, store.Scope{All: true}, store.SavedViewInput{Name: "x"}); err == nil {
		t.Error("a scope without user created a saved view")
	}
}

func TestSavedViewFilterMustBeObjectInDatabase(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	v := mustView(t, st, alice, store.SavedViewInput{Name: "v"})
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, "UPDATE saved_views SET filter = '[]' WHERE id = $1", v.ID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "saved_views_filter_object" {
		t.Errorf("array filter: %v, want saved_views_filter_object violation", err)
	}
}

func TestSavedViewScope(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	admin := store.Scope{UserID: storetest.User(t, st, "admin", true).ID, All: true}
	av := mustView(t, st, alice, store.SavedViewInput{Name: "Alice"})
	mustView(t, st, bob, store.SavedViewInput{Name: "Bob"})

	if vs, _ := st.ListSavedViews(ctx, bob); len(vs) != 1 || vs[0].Name != "Bob" {
		t.Errorf("bob's views = %+v", vs)
	}
	for name, err := range map[string]error{
		"get":    second(st.GetSavedView(ctx, bob, av.ID)),
		"update": second(st.UpdateSavedView(ctx, bob, av.ID, store.SavedViewInput{Name: "x"})),
		"delete": st.DeleteSavedView(ctx, bob, av.ID),
	} {
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s across owners: %v, want ErrNotFound", name, err)
		}
	}
	if v, err := st.GetSavedView(ctx, alice, av.ID); err != nil || v.Name != "Alice" {
		t.Errorf("alice's view changed: %+v, %v", v, err)
	}

	if vs, _ := st.ListSavedViews(ctx, admin); len(vs) != 2 {
		t.Errorf("admin sees %d views, want 2", len(vs))
	}
	up, err := st.UpdateSavedView(ctx, admin, av.ID, store.SavedViewInput{Name: "by admin"})
	if err != nil || up.Name != "by admin" || up.OwnerID != alice.UserID {
		t.Errorf("admin update: %+v, %v", up, err)
	}
	if own := mustView(t, st, admin, store.SavedViewInput{Name: "Admin"}); own.OwnerID != admin.UserID {
		t.Errorf("admin's view owner = %d", own.OwnerID)
	}
}

// A patch changes only the fields it names; the stored filter, symbol and
// order stay. SortLast appends after the owner's last view, also over
// negative and tied sort indexes, and counts only the owner's own views.
func TestSavedViewPatchAndSortLast(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}

	first := mustView(t, st, alice, store.SavedViewInput{Name: "First", SortLast: true, SortIndex: 42})
	if first.SortIndex != 0 {
		t.Errorf("first view's sort index = %d, want 0", first.SortIndex)
	}
	mustView(t, st, bob, store.SavedViewInput{Name: "Bob's", SortIndex: 100})
	mustView(t, st, alice, store.SavedViewInput{Name: "Tied", SortIndex: 5})
	mustView(t, st, alice, store.SavedViewInput{Name: "Tied too", SortIndex: 5})
	last := mustView(t, st, alice, store.SavedViewInput{Name: "Last", SortLast: true})
	if last.SortIndex != 6 {
		t.Errorf("appended view's sort index = %d, want 6", last.SortIndex)
	}

	filter := json.RawMessage(`{"tags": [{"id": "x"}], "paperless-go": {"v": 1, "q": "invoice"}}`)
	v := mustView(t, st, alice, store.SavedViewInput{Name: "Old", Symbol: "eurosign", SortIndex: 9, Filter: filter})
	got, err := st.PatchSavedView(ctx, alice, v.ID, store.SavedViewPatch{Name: ptr("New")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "New" || got.Symbol != "eurosign" || got.SortIndex != 9 || !sameJSON(t, got.Filter, filter) {
		t.Errorf("name patch = %+v, filter %s", got, got.Filter)
	}
	got, err = st.PatchSavedView(ctx, alice, v.ID, store.SavedViewPatch{SortIndex: ptr(int64(-3))})
	if err != nil || got.Name != "New" || got.SortIndex != -3 || !sameJSON(t, got.Filter, filter) {
		t.Errorf("sort patch = %+v, %v", got, err)
	}
	other := json.RawMessage(`{"inbox": true}`)
	got, err = st.PatchSavedView(ctx, alice, v.ID, store.SavedViewPatch{Filter: other})
	if err != nil || got.Name != "New" || got.SortIndex != -3 || !sameJSON(t, got.Filter, other) {
		t.Errorf("filter patch = %+v, %v", got, err)
	}
	if _, err := st.PatchSavedView(ctx, alice, v.ID, store.SavedViewPatch{Filter: json.RawMessage(`[]`)}); !errors.Is(err, store.ErrInvalidFilter) {
		t.Errorf("array filter patch: %v", err)
	}
	for _, id := range []string{"not-a-uuid", "00000000-0000-0000-0000-000000000000"} {
		if _, err := st.PatchSavedView(ctx, alice, id, store.SavedViewPatch{Name: ptr("x")}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("patch of %s: %v", id, err)
		}
	}
	if _, err := st.PatchSavedView(ctx, bob, v.ID, store.SavedViewPatch{Name: ptr("x")}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("bob patches alice's view: %v", err)
	}
}

// Each change of a saved view writes its audit entry in the same
// transaction: the view as target with its name, oldName on a rename.
func TestSavedViewAudit(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	conn := connect(t, url)
	entry := func(action string, fields ...string) context.Context {
		e := &store.AuditEntry{Actor: store.ActorCLI, Action: action}
		e.SetTarget(store.TargetSavedView, "", "")
		if fields != nil {
			e.Detail("fields", fields)
		}
		return store.WithAudit(ctx, e)
	}
	v, err := st.CreateSavedView(entry("saved_view.create"), alice, store.SavedViewInput{Name: "Old"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PatchSavedView(entry("saved_view.update", "name"), alice, v.ID, store.SavedViewPatch{Name: ptr("New")}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PatchSavedView(entry("saved_view.update", "sortIndex"), alice, v.ID, store.SavedViewPatch{SortIndex: ptr(int64(2))}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSavedView(entry("saved_view.delete"), alice, v.ID); err != nil {
		t.Fatal(err)
	}
	// A refused change writes nothing.
	if err := st.DeleteSavedView(entry("saved_view.delete"), alice, v.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	rows := auditRows(t, conn)
	want := []struct {
		action, name, details string
	}{
		{"saved_view.create", "Old", "map[name:Old]"},
		{"saved_view.update", "New", "map[fields:[name] name:New oldName:Old]"},
		{"saved_view.update", "New", "map[fields:[sortIndex] name:New]"},
		{"saved_view.delete", "New", "map[name:New]"},
	}
	if len(rows) != len(want) {
		t.Fatalf("%d entries, want %d: %+v", len(rows), len(want), rows)
	}
	for i, w := range want {
		r := rows[i]
		if r.Action != w.action || *r.TargetType != store.TargetSavedView || *r.TargetID != v.ID || *r.TargetName != w.name ||
			fmt.Sprint(r.Details) != w.details {
			t.Errorf("entry %d: %s %v %v %v %v, want %+v", i, r.Action, *r.TargetType, *r.TargetID, *r.TargetName, r.Details, w)
		}
	}
}

// Two patches of one view merge one after the other: the second waits
// for the first's row lock and keeps its change.
func TestSavedViewConcurrentPatches(t *testing.T) {
	st, dbURL := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	v := mustView(t, st, alice, store.SavedViewInput{Name: "Old"})
	second := openWith(t, dbURL, map[string]string{"application_name": "second-patch"})
	watch := connect(t, dbURL)

	locked, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		held := store.WithAfterLock(ctx, func() {
			close(locked)
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
		_, err := st.PatchSavedView(held, alice, v.ID, store.SavedViewPatch{Name: ptr("New")})
		first <- err
	}()
	select {
	case <-locked:
	case err := <-first:
		t.Fatalf("first patch finished before it locked: %v", err)
	case <-ctx.Done():
		t.Fatal("first patch never locked the row")
	}
	done := make(chan error, 1)
	go func() {
		_, err := second.PatchSavedView(ctx, alice, v.ID, store.SavedViewPatch{SortIndex: ptr(int64(7))})
		done <- err
	}()
	waitForLock(ctx, t, watch, "second-patch", done)
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first patch: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("second patch: %v", err)
	}
	got, err := st.GetSavedView(ctx, alice, v.ID)
	if err != nil || got.Name != "New" || got.SortIndex != 7 {
		t.Fatalf("after both patches: %+v, %v; want name New and sort index 7", got, err)
	}
}

// The next sort index stays within the API's range: a stored maximum (here
// the largest bigint, which the API refuses but SQL or an older server
// could write) gives MaxSavedViewSortIndex, not an overflow (#139 review).
func TestSavedViewSortLastAfterHugeIndex(t *testing.T) {
	st := storetest.New(t)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	mustView(t, st, alice, store.SavedViewInput{Name: "Huge", SortIndex: math.MaxInt64})
	for range 2 {
		v := mustView(t, st, alice, store.SavedViewInput{Name: "Next", SortLast: true})
		if v.SortIndex != store.MaxSavedViewSortIndex {
			t.Errorf("next sort index = %d, want %d", v.SortIndex, int64(store.MaxSavedViewSortIndex))
		}
	}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	mustView(t, st, bob, store.SavedViewInput{Name: "Edge", SortIndex: store.MaxSavedViewSortIndex - 1})
	if v := mustView(t, st, bob, store.SavedViewInput{Name: "Next", SortLast: true}); v.SortIndex != store.MaxSavedViewSortIndex {
		t.Errorf("after 2^53-2: %d", v.SortIndex)
	}
}

// Text jsonb cannot hold is ErrInvalidFilter, not an internal error: bytes
// that are not UTF-8 (refused before the write) and a lone surrogate
// escape (refused by PostgreSQL, SQLSTATE 22P02).
func TestSavedViewFilterTextJSONBRefuses(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	v := mustView(t, st, alice, store.SavedViewInput{Name: "v"})
	for _, bad := range []string{"{\"a\":\"\xff\"}", "{\"a\xc3\":1}", `{"a":"\ud800"}`, `{"a":["x","\udfff"]}`, `{"\ud800":1}`} {
		if _, err := st.CreateSavedView(ctx, alice, store.SavedViewInput{Name: "bad", Filter: json.RawMessage(bad)}); !errors.Is(err, store.ErrInvalidFilter) {
			t.Errorf("create with %q: %v, want ErrInvalidFilter", bad, err)
		}
		if _, err := st.PatchSavedView(ctx, alice, v.ID, store.SavedViewPatch{Filter: json.RawMessage(bad)}); !errors.Is(err, store.ErrInvalidFilter) {
			t.Errorf("patch with %q: %v, want ErrInvalidFilter", bad, err)
		}
	}
	// A surrogate pair is a valid character.
	if _, err := st.PatchSavedView(ctx, alice, v.ID, store.SavedViewPatch{Filter: json.RawMessage(`{"a":"😀"}`)}); err != nil {
		t.Errorf("surrogate pair: %v", err)
	}
}
