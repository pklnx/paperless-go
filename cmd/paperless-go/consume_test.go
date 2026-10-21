package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pklnx/paperless-go/internal/config"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

func TestNewConsumer(t *testing.T) {
	st := storetest.New(t)
	anna := storetest.User(t, st, "anna", false)
	dir := filepath.Join(t.TempDir(), "consume")
	a := &app{cfg: &config.Config{DataDir: t.TempDir(), ConsumeDir: dir, MaxUploadSize: 1 << 20}, store: st}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))

	// A missing folder is logged; the server runs anyway.
	if _, err := newConsumer(a, log); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "not a folder") {
		t.Errorf("log = %q, want a warning about the missing folder", logs.String())
	}

	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	c, err := newConsumer(a, log)
	if err != nil {
		t.Fatal(err)
	}
	if logs.Len() > 0 {
		t.Errorf("log = %q, want nothing", logs.String())
	}
	// It imports into the store and the data directory of the app.
	ctx := context.Background()
	if _, err := c.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "anna", "a.pdf"), []byte("%PDF-1.7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	docs, err := st.ListDocuments(ctx, store.Scope{UserID: anna.ID})
	if err != nil || len(docs) != 0 {
		t.Fatalf("documents after one scan: %d, %v; want none yet", len(docs), err)
	}
}

func TestPrepareConsumeDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "consume")
	var stdout, stderr bytes.Buffer
	env := dataDirEnv{
		isRoot:   true,
		canWrite: func(string) (bool, error) { return false, nil },
		owner:    func(string) (uint32, error) { return 1000, nil },
	}
	prepareDataDir(dir, consumeDirKind, &stdout, &stderr, env)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("consume folder: %v", err)
	}
	for _, want := range []string{"the consume folder " + dir, "CONSUME_DIR in .env, default ./consume", "sudo chown -R 65532:65532 consume"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, want %q", stderr.String(), want)
		}
	}
	stderr.Reset()
	env.userns = true
	prepareDataDir(dir, consumeDirKind, &stdout, &stderr, env)
	if want := `-v "$PWD/consume:/consume" alpine chown -R 65532:65532 /consume`; !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
}
