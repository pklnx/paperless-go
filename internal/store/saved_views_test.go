package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

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

	for _, bad := range []string{`[]`, `"x"`, `null`, `1`, `{"a":`, ` [ {} ] `} {
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
