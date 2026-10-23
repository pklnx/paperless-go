package consume_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/pklnx/paperless-go/internal/consume"
	"github.com/pklnx/paperless-go/internal/store"
)

// restoreFunc adapts a function to consume.Restorer.
type restoreFunc func(ctx context.Context, sc store.Scope, id string) (*store.Document, error)

func (f restoreFunc) RestoreDocument(ctx context.Context, sc store.Scope, id string) (*store.Document, error) {
	return f(ctx, sc, id)
}

// withRestorer replaces e's consumer by one that restores through r and
// writes its audit entries to the store.
func (e *env) withRestorer(r consume.Restorer) {
	e.c = consume.New(e.dir, e.st, e.im, consume.Options{
		Log: slog.New(slog.NewTextHandler(e.log, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now: e.clock.now, Audit: e.st.Audit, Restorer: r,
	})
}

// audit returns the store's audit entries as "action target details".
func (e *env) audit() []string {
	e.t.Helper()
	var out []string
	f := store.AuditFilter{Since: time.Now().Add(-24 * time.Hour), Until: time.Now().Add(24 * time.Hour)}
	if err := e.st.ExportAudit(context.Background(), f, func(r store.AuditRecord) error {
		var d map[string]any
		if err := json.Unmarshal(r.Details, &d); err != nil {
			return err
		}
		out = append(out, fmt.Sprintf("%s %s %s %s %v", r.Actor, r.Action, r.TargetType, r.TargetID, d))
		return nil
	}); err != nil {
		e.t.Fatal(err)
	}
	return out
}

// A file whose document is in the user's trash restores it, audited by
// the system with via consume, and is deleted; the scan's summary counts
// it as restored.
func TestTrashedDuplicateRestored(t *testing.T) {
	e := newEnv(t)
	e.withRestorer(e.st)
	anna := e.user("anna", false)
	e.write("anna/a.pdf", pdf("kept"))
	if st := e.settle(); st.Imported != 1 {
		t.Fatalf("stats: %+v", st)
	}
	doc := e.docs(anna)[0]
	ctx := context.Background()
	if err := e.st.TrashDocument(ctx, store.Scope{UserID: anna.ID}, doc.ID); err != nil {
		t.Fatal(err)
	}
	b := e.write("anna/b.pdf", pdf("kept"))
	if st := e.settle(); st.Restored != 1 || st.Duplicates != 0 || st.Imported != 0 {
		t.Fatalf("stats: %+v, want 1 restored", st)
	}
	mustNotExist(t, b)
	if docs := e.docs(anna); len(docs) != 1 || docs[0].ID != doc.ID {
		t.Fatalf("documents: %+v", docs)
	}
	got := e.audit()
	want := []string{
		fmt.Sprintf("system document.consume user %d map[duplicates:0 failed:0 imported:1 restored:0]", anna.ID),
		fmt.Sprintf("system document.restore document %s map[ownerId:%d via:consume]", doc.ID, anna.ID),
		fmt.Sprintf("system document.consume user %d map[duplicates:0 failed:0 imported:0 restored:1]", anna.ID),
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("audit:\n%v\nwant\n%v", got, want)
	}
}

// A file whose document is due for purge stays until the sweeper deleted
// the row; then the next scan imports it as new. A restore refused for
// the same reason leaves the file too, and without a restorer the file
// is never deleted.
func TestTrashedDuplicateDueForPurgeStays(t *testing.T) {
	e := newEnv(t)
	e.withRestorer(e.st)
	anna := e.user("anna", false)
	sc := store.Scope{UserID: anna.ID}
	ctx := context.Background()
	e.write("anna/a.pdf", pdf("gone soon"))
	e.settle()
	doc := e.docs(anna)[0]
	if err := e.st.TrashDocument(ctx, sc, doc.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.st.PurgeDocument(ctx, sc, doc.ID); err != nil {
		t.Fatal(err)
	}
	b := e.write("anna/b.pdf", pdf("gone soon"))
	if st := e.settle(); st != (consume.Stats{}) {
		t.Errorf("stats while due: %+v", st)
	}
	mustExist(t, b)
	if n := e.log.count("being removed permanently", "b.pdf"); n != 1 {
		t.Errorf("%d log lines about the purge, want 1:\n%s", n, e.log.String())
	}
	if _, err := e.st.PurgeDueDocuments(ctx, time.Now(), store.DefaultTrashRetention, 100); err != nil {
		t.Fatal(err)
	}
	if st := e.scan(); st.Imported != 1 {
		t.Errorf("stats after the purge: %+v", st)
	}
	mustNotExist(t, b)
	docs := e.docs(anna)
	if len(docs) != 1 || docs[0].ID == doc.ID {
		t.Errorf("documents: %+v", docs)
	}

	// The restore finds the document due (it became due after the
	// duplicate check): the file stays.
	if err := e.st.TrashDocument(ctx, sc, docs[0].ID); err != nil {
		t.Fatal(err)
	}
	e.withRestorer(restoreFunc(func(context.Context, store.Scope, string) (*store.Document, error) {
		return nil, store.ErrNotFound
	}))
	c := e.write("anna/c.pdf", pdf("gone soon"))
	if st := e.settle(); st != (consume.Stats{}) {
		t.Errorf("stats with a refused restore: %+v", st)
	}
	mustExist(t, c)

	// Without a restorer nothing is deleted.
	e.withRestorer(nil)
	e.settle()
	mustExist(t, c)
	if n := e.log.count("is in the trash; left in place", "c.pdf"); n != 1 {
		t.Errorf("%d warnings, want 1:\n%s", n, e.log.String())
	}
	if d, err := e.st.GetDocument(ctx, sc, docs[0].ID); err != nil || d.DeletedAt == nil {
		t.Errorf("document: %+v, %v", d, err)
	}
}

// A file whose stored copy the sweeper is removing (its last document was
// purged, and the sweeper marked the file) stays in the consume folder
// and is imported at a later scan.
func TestFileBeingCleanedUpStays(t *testing.T) {
	e := newEnv(t)
	anna := e.user("anna", false)
	sc := store.Scope{UserID: anna.ID}
	ctx := context.Background()
	e.write("anna/a.pdf", pdf("cleaned up"))
	e.settle()
	doc := e.docs(anna)[0]
	if err := e.st.TrashDocument(ctx, sc, doc.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.st.PurgeDocument(ctx, sc, doc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.PurgeDueDocuments(ctx, time.Now(), store.DefaultTrashRetention, 100); err != nil {
		t.Fatal(err)
	}
	if m, err := e.st.MarkBlobRemoval(ctx, doc.OriginalSHA256, time.Now().Add(2*store.BlobGrace)); err != nil || m != store.BlobMarked {
		t.Fatalf("mark: %v, %v", m, err)
	}
	b := e.write("anna/b.pdf", pdf("cleaned up"))
	if st := e.settle(); st != (consume.Stats{}) {
		t.Errorf("stats while the file is cleaned up: %+v", st)
	}
	mustExist(t, b)
	if n := e.log.count("being cleaned up", "b.pdf"); n != 1 {
		t.Errorf("%d log lines about the clean-up, want 1:\n%s", n, e.log.String())
	}
	if err := e.st.FinishBlobRemoval(ctx, doc.OriginalSHA256); err != nil {
		t.Fatal(err)
	}
	if st := e.scan(); st.Imported != 1 {
		t.Errorf("stats after the clean-up: %+v", st)
	}
	mustNotExist(t, b)
}
