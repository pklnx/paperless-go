package process

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestWorkDir(t *testing.T) {
	data := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	root := filepath.Join(data, "work")

	// Left behind by a crash: a directory whose lock nobody holds, an old
	// one without a lock, and a loose file.
	stale := filepath.Join(root, workPrefix+"crashed")
	if err := os.MkdirAll(filepath.Join(stale, "job-1-x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, lockName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	unlocked := filepath.Join(root, workPrefix+"old")
	young := filepath.Join(root, workPrefix+"young")
	for _, d := range []string{unlocked, young} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * staleUnlocked)
	if err := os.Chtimes(unlocked, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "loose"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	// Another running process.
	other, err := OpenWorkDir(data, log)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()

	wd, err := OpenWorkDir(data, log)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{filepath.Base(other.Path()), filepath.Base(wd.Path()), filepath.Base(young)}
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Fatalf("work/ holds %q, want %q", names, want)
	}

	job, err := wd.Job(7)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(job) != wd.Path() {
		t.Fatalf("job directory %s outside %s", job, wd.Path())
	}
	if err := wd.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wd.Path()); !os.IsNotExist(err) {
		t.Fatalf("work directory after Close: %v", err)
	}
	// The other process's directory survives a third start.
	third, err := OpenWorkDir(data, log)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = third.Close() }()
	if _, err := os.Stat(other.Path()); err != nil {
		t.Fatalf("a running process's directory was removed: %v", err)
	}
}
