package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pklnx/paperless-go/internal/blobstore"
	"github.com/pklnx/paperless-go/internal/config"
	"github.com/pklnx/paperless-go/internal/process"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// fakeTools answers the tools as for a one-page PDF with text, or fails
// pdfinfo with exit status 1 (a damaged PDF) for an original that says so.
type fakeTools struct{}

func (fakeTools) Run(_ context.Context, c process.Command) ([]byte, error) {
	switch c.Name {
	case "tesseract":
		return []byte("List of available languages (3):\ndeu\neng\nosd\n"), nil
	case "pdfinfo":
		b, err := os.ReadFile(filepath.Join(c.Dir, c.Args[0]))
		if err != nil {
			return nil, err
		}
		if strings.Contains(string(b), "damaged") {
			return nil, &process.ToolError{Tool: "pdfinfo", ExitCode: 1}
		}
		return []byte("Pages: 1\nEncrypted: no\n"), nil
	case "pdftotext":
		return nil, os.WriteFile(filepath.Join(c.Dir, c.Args[len(c.Args)-1]), []byte("Rechnung Nr. 4711 vom Oktober\f"), 0o600)
	}
	return nil, fmt.Errorf("unexpected tool %s", c.Name)
}

func useFakeTools(t *testing.T) {
	t.Helper()
	old := newRunner
	newRunner = func() process.Runner { return fakeTools{} }
	t.Cleanup(func() { newRunner = old })
}

func storedDoc(t *testing.T, st *store.Store, blobs *blobstore.Store, owner int64, content string) *store.Document {
	t.Helper()
	b, _, err := blobs.Put(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	d, err := st.CreateDocument(context.Background(), store.Scope{UserID: owner}, store.NewDocument{
		Title: content, Source: store.SourceFile, OriginalSHA256: b.SHA256, OriginalSize: b.Size,
		OriginalMIME: "application/pdf", IsInbox: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestProcessAndReprocessCommands(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	useFakeTools(t)
	data := t.TempDir()
	t.Setenv(config.EnvDataDir, data)
	t.Setenv(config.EnvOperator, "patrick")
	blobs, err := blobstore.New(data)
	if err != nil {
		t.Fatal(err)
	}
	alice := storetest.User(t, st, "alice", false)
	good := storedDoc(t, st, blobs, alice.ID, "%PDF good")
	bad := storedDoc(t, st, blobs, alice.ID, "%PDF damaged")

	out, err := run(t, url, "process", "--until-empty")
	if err != nil {
		t.Fatalf("process: %v\n%s", err, out)
	}
	if !strings.Contains(out, "processed: 1, to retry: 0, failed: 1") || !strings.Contains(out, "reprocess --failed") {
		t.Fatalf("process output: %q", out)
	}
	d, err := st.GetDocument(ctx, store.Scope{All: true}, good.ID)
	if err != nil || d.ExtractedText != "Rechnung Nr. 4711 vom Oktober" || d.ArchiveSHA256 == nil {
		t.Fatalf("processed document: %+v, %v", d, err)
	}
	// The work directory of the command is gone.
	if entries, _ := os.ReadDir(filepath.Join(data, "work")); len(entries) != 0 {
		t.Fatalf("work directory left: %v", entries)
	}

	for _, args := range [][]string{{"reprocess"}, {"reprocess", "--all", "--failed"}, {"reprocess", "--failed", good.ID}} {
		if _, err := run(t, url, args...); err == nil {
			t.Fatalf("%v: no error", args)
		}
	}
	if _, err := run(t, url, "reprocess", "00000000-0000-4000-8000-000000000000"); err == nil ||
		!strings.Contains(err.Error(), "00000000-0000-4000-8000-000000000000") {
		t.Fatalf("reprocess an unknown document: %v", err)
	}
	if out, err := run(t, url, "reprocess", "--failed"); err != nil || out != "queued 1 documents\n" {
		t.Fatalf("reprocess --failed: %q, %v", out, err)
	}
	if jobs, _ := st.DocumentJobs(ctx, bad.ID); jobs[0].State != store.JobQueued {
		t.Fatalf("failed document not queued: %+v", jobs)
	}
	if out, err := run(t, url, "reprocess", good.ID); err != nil || out != "queued 1 documents\n" {
		t.Fatalf("reprocess ID: %q, %v", out, err)
	}
	if out, err := run(t, url, "reprocess", "--all"); err != nil || out != "queued 0 documents\n" {
		t.Fatalf("reprocess --all: %q, %v", out, err)
	}

	var entries []store.AuditRecord
	for _, r := range auditLog(t, st) {
		if r.Action == "document.reprocess" {
			entries = append(entries, r)
		}
	}
	if len(entries) != 3 {
		t.Fatalf("audit entries: %+v", entries)
	}
	if e := entries[0]; e.Actor != store.ActorCLI || !strings.Contains(string(e.Details), `"selection": "failed"`) ||
		!strings.Contains(string(e.Details), `"queued": 1`) || !strings.Contains(string(e.Details), `"operator": "patrick"`) {
		t.Errorf("reprocess --failed entry: %+v %s", e, e.Details)
	}
	if e := entries[1]; e.TargetType != store.TargetDocument || e.TargetID != good.ID {
		t.Errorf("reprocess ID entry: %+v", e)
	}
}

func TestProcessChecksLanguages(t *testing.T) {
	_, url := storetest.NewWithURL(t)
	useFakeTools(t)
	t.Setenv(config.EnvDataDir, t.TempDir())
	t.Setenv(config.EnvOCRLanguages, "deu+fra")
	if _, err := run(t, url, "process", "--until-empty"); err == nil || !strings.Contains(err.Error(), "fra") {
		t.Fatalf("process without French: %v", err)
	}
}
