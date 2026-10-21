package store_test

import (
	"context"
	"testing"

	"github.com/pklnx/paperless-go/internal/store/storetest"
)

func TestMigrateDownAndUp(t *testing.T) {
	st := storetest.New(t) // already migrated up
	ctx := context.Background()

	assertStatus := func(wantApplied bool) {
		t.Helper()
		statuses, err := st.MigrationStatus(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(statuses) == 0 {
			t.Fatal("no migrations found")
		}
		for _, s := range statuses {
			if s.Applied != wantApplied {
				t.Fatalf("%s: applied=%v, want %v", s.Path, s.Applied, wantApplied)
			}
		}
	}
	assertStatus(true)
	before, err := st.Installation(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Roll back every migration, one at a time.
	for {
		statuses, err := st.MigrationStatus(ctx)
		if err != nil {
			t.Fatal(err)
		}
		applied := 0
		for _, s := range statuses {
			if s.Applied {
				applied++
			}
		}
		if applied == 0 {
			break
		}
		if _, err := st.MigrateDown(ctx); err != nil {
			t.Fatalf("down: %v", err)
		}
	}
	assertStatus(false)
	if _, err := st.Installation(ctx); err == nil {
		t.Fatal("installation table should be gone after rolling back")
	}
	if _, err := st.MigrateDown(ctx); err == nil {
		t.Fatal("expected error when nothing is left to roll back")
	}

	// Up again restores a working schema, as a new installation.
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	assertStatus(true)
	after, err := st.Installation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.ID == before.ID {
		t.Errorf("installation ID %s survived a full rollback", after.ID)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	st := storetest.New(t)
	applied, err := st.Migrate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 0 {
		t.Errorf("second Migrate applied %v", applied)
	}
}

func TestInstallation(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a, err := st.Installation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.ID) != 36 || a.CreatedAt.IsZero() {
		t.Errorf("installation = %+v", a)
	}
	b, err := st.Installation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if *a != *b {
		t.Errorf("installation changed between reads: %+v, %+v", a, b)
	}
}
