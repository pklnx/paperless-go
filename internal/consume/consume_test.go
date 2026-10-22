package consume_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pklnx/paperless-go/internal/blobstore"
	"github.com/pklnx/paperless-go/internal/consume"
	"github.com/pklnx/paperless-go/internal/ingest"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// Every file is generated here; no real documents.

func pdf(content string) []byte { return []byte("%PDF-1.7\n" + content + "\n%%EOF\n") }

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// logBuf collects log output.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// count is how many log lines contain all of parts.
func (l *logBuf) count(parts ...string) int {
	n := 0
	for line := range strings.SplitSeq(l.String(), "\n") {
		ok := line != ""
		for _, p := range parts {
			ok = ok && strings.Contains(line, p)
		}
		if ok {
			n++
		}
	}
	return n
}

// countSuffix is how many log lines contain part and end with suffix.
func (l *logBuf) countSuffix(part, suffix string) int {
	n := 0
	for line := range strings.SplitSeq(l.String(), "\n") {
		if strings.Contains(line, part) && strings.HasSuffix(line, suffix) {
			n++
		}
	}
	return n
}

type env struct {
	t      *testing.T
	st     *store.Store
	im     *ingest.Importer
	parent string // holds dir; must stay as it is
	dir    string // the consume folder
	clock  *clock
	log    *logBuf
	c      *consume.Consumer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	return newEnvWith(t, nil)
}

// newEnvWith uses wrap, if not nil, around the real importer.
func newEnvWith(t *testing.T, wrap func(consume.Importer) consume.Importer) *env {
	t.Helper()
	st := storetest.New(t)
	bs, err := blobstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	im, err := ingest.New(st, bs, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "consume")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, st: st, im: im, parent: parent, dir: dir, clock: &clock{t: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)}, log: &logBuf{}}
	var imp consume.Importer = im
	if wrap != nil {
		imp = wrap(im)
	}
	e.c = e.consumer(st, imp)
	return e
}

func (e *env) consumer(users consume.Users, im consume.Importer) *consume.Consumer {
	return consume.New(e.dir, users, im, consume.Options{
		Log: slog.New(slog.NewTextHandler(e.log, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now: e.clock.now,
	})
}

func (e *env) user(name string, admin bool) *store.User {
	e.t.Helper()
	return storetest.User(e.t, e.st, name, admin)
}

func (e *env) scan() consume.Stats {
	e.t.Helper()
	st, err := e.c.Scan(context.Background())
	if err != nil {
		e.t.Fatalf("scan: %v", err)
	}
	return st
}

// settle scans, waits out the settle time on the clock and scans again.
func (e *env) settle() consume.Stats {
	e.t.Helper()
	e.scan()
	e.clock.advance(consume.DefaultSettle)
	return e.scan()
}

func (e *env) path(rel string) string { return filepath.Join(e.dir, filepath.FromSlash(rel)) }

func (e *env) write(rel string, data []byte) string {
	e.t.Helper()
	p := e.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *env) docs(u *store.User) []*store.Document {
	e.t.Helper()
	docs, err := e.st.ListDocuments(context.Background(), store.Scope{UserID: u.ID})
	if err != nil {
		e.t.Fatal(err)
	}
	return docs
}

func exists(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Lstat(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

func mustExist(t *testing.T, p string) {
	t.Helper()
	if !exists(t, p) {
		t.Errorf("%s is gone, want it left in place", p)
	}
}

func mustNotExist(t *testing.T, p string) {
	t.Helper()
	if exists(t, p) {
		t.Errorf("%s exists, want it gone", p)
	}
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestFirstScanWaitsLaterScanImports(t *testing.T) {
	e := newEnv(t)
	anna := e.user("anna", false)
	e.scan() // creates consume/anna
	if fi, err := os.Lstat(e.path("anna")); err != nil || !fi.IsDir() {
		t.Fatalf("consume/anna: %v, want a folder", err)
	}
	p := e.write("anna/Rechnung 2026.pdf", pdf("a"))

	if st := e.scan(); st.Waiting != 1 || st.Imported != 0 {
		t.Fatalf("first scan: %+v, want 1 waiting", st)
	}
	e.clock.advance(consume.DefaultSettle - time.Millisecond)
	if st := e.scan(); st.Waiting != 1 {
		t.Fatalf("scan before the settle time: %+v, want 1 waiting", st)
	}
	mustExist(t, p)
	if n := len(e.docs(anna)); n != 0 {
		t.Fatalf("%d documents before the file settled, want 0", n)
	}

	e.clock.advance(time.Millisecond)
	if st := e.scan(); st.Imported != 1 || st.Waiting != 0 {
		t.Fatalf("scan after the settle time: %+v, want 1 imported", st)
	}
	mustNotExist(t, p)
	docs := e.docs(anna)
	if len(docs) != 1 || docs[0].Title != "Rechnung 2026" || docs[0].OriginalFileName != "Rechnung 2026.pdf" || !docs[0].IsInbox {
		t.Fatalf("documents: %+v", docs)
	}
}

func TestGrowingFileWaits(t *testing.T) {
	e := newEnv(t)
	anna := e.user("anna", false)
	p := e.write("anna/scan.pdf", pdf("part one"))
	e.scan()
	for i := range 3 {
		e.clock.advance(consume.DefaultSettle)
		f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(f, "more %d\n", i)
		_ = f.Close()
		if st := e.scan(); st.Waiting != 1 || st.Imported != 0 {
			t.Fatalf("scan %d of a growing file: %+v, want waiting", i, st)
		}
	}
	e.clock.advance(consume.DefaultSettle)
	if st := e.scan(); st.Imported != 1 {
		t.Fatalf("scan of the settled file: %+v, want imported", st)
	}
	if docs := e.docs(anna); len(docs) != 1 || docs[0].OriginalSize != int64(len(pdf("part one"))+3*len("more 0\n")) {
		t.Fatalf("documents: %+v, want the whole file", docs)
	}
}

func TestUnknownFoldersAndLooseFilesStay(t *testing.T) {
	e := newEnv(t)
	e.user("anna", false)
	stray := e.write("bob/x.pdf", pdf("bob"))
	loose := e.write("loose.pdf", pdf("loose"))
	upper := e.write("Anna/y.pdf", pdf("upper")) // names match exactly
	hiddenRoot := e.write(".DS_Store", []byte("x"))
	for range 3 {
		e.settle()
	}
	for _, p := range []string{stray, loose, upper, hiddenRoot} {
		mustExist(t, p)
	}
	for _, want := range [][]string{{"no user has this name", "folder=bob"}, {"not in a user's folder", "name=loose.pdf"}, {"no user has this name", "folder=Anna"}} {
		if n := e.log.count(want...); n != 1 {
			t.Errorf("%d warnings %q, want 1:\n%s", n, want, e.log)
		}
	}
	if n := e.log.count(".DS_Store"); n != 0 {
		t.Errorf("hidden entry logged: %s", e.log)
	}
}

func TestImportedAndDuplicateFilesDeleted(t *testing.T) {
	e := newEnv(t)
	anna := e.user("anna", false)
	a := e.write("anna/a.pdf", pdf("same"))
	b := e.write("anna/b.pdf", pdf("same"))
	st := e.settle()
	if st.Imported != 1 || st.Duplicates != 1 {
		t.Fatalf("stats: %+v, want 1 imported and 1 duplicate", st)
	}
	mustNotExist(t, a)
	mustNotExist(t, b)
	if n := len(e.docs(anna)); n != 1 {
		t.Fatalf("%d documents, want 1", n)
	}
	// The same file again later: a duplicate, deleted too.
	c := e.write("anna/c.pdf", pdf("same"))
	if st := e.settle(); st.Duplicates != 1 {
		t.Fatalf("stats: %+v, want 1 duplicate", st)
	}
	mustNotExist(t, c)
}

// importFunc adapts a function to consume.Importer.
type importFunc func(ctx context.Context, req ingest.Request) (ingest.Result, error)

func (f importFunc) Import(ctx context.Context, req ingest.Request) (ingest.Result, error) {
	return f(ctx, req)
}

func TestFileChangedWhileImportedStays(t *testing.T) {
	var target string
	e := newEnvWith(t, func(im consume.Importer) consume.Importer {
		return importFunc(func(ctx context.Context, req ingest.Request) (ingest.Result, error) {
			res, err := im.Import(ctx, req)
			// The user saves a new version while the import runs.
			if werr := os.WriteFile(target, pdf("version two, longer"), 0o644); werr != nil {
				panic(werr)
			}
			return res, err
		})
	})
	anna := e.user("anna", false)
	target = e.write("anna/a.pdf", pdf("version one"))
	if st := e.settle(); st.Imported != 1 {
		t.Fatalf("stats: %+v, want imported", st)
	}
	mustExist(t, target)
	if n := len(e.docs(anna)); n != 1 {
		t.Fatalf("%d documents, want 1", n)
	}
}

func TestFileReplacedWhileImportedStays(t *testing.T) {
	var target string
	e := newEnvWith(t, func(im consume.Importer) consume.Importer {
		return importFunc(func(ctx context.Context, req ingest.Request) (ingest.Result, error) {
			res, err := im.Import(ctx, req)
			// Same size and the same modification time, but another file.
			fi, serr := os.Stat(target)
			if serr != nil {
				panic(serr)
			}
			tmp := target + ".new"
			if werr := os.WriteFile(tmp, pdf("version 2"), 0o644); werr != nil {
				panic(werr)
			}
			if werr := os.Chtimes(tmp, fi.ModTime(), fi.ModTime()); werr != nil {
				panic(werr)
			}
			if werr := os.Rename(tmp, target); werr != nil {
				panic(werr)
			}
			return res, err
		})
	})
	e.user("anna", false)
	target = e.write("anna/a.pdf", pdf("version 1"))
	if st := e.settle(); st.Imported != 1 {
		t.Fatalf("stats: %+v, want imported", st)
	}
	mustExist(t, target)
}

func TestRefusedFilesMoveToFailed(t *testing.T) {
	e := newEnv(t)
	anna := e.user("anna", false)
	e.write("anna/notes.txt", []byte("just text"))
	if st := e.settle(); st.Failed != 1 {
		t.Fatalf("stats: %+v, want 1 failed", st)
	}
	mustNotExist(t, e.path("anna/notes.txt"))
	got, err := os.ReadFile(e.path("anna/failed/notes.txt"))
	if err != nil || string(got) != "just text" {
		t.Fatalf("failed/notes.txt: %q, %v", got, err)
	}
	side, err := os.ReadFile(e.path("anna/failed/notes.txt" + consume.ErrorSuffix))
	if err != nil || !strings.Contains(string(side), "notes.txt: unsupported file type") {
		t.Fatalf("sidecar: %q, %v", side, err)
	}

	// The same name again, and a name whose sidecar name alone is taken.
	e.write("anna/notes.txt", []byte("other text"))
	e.write("anna/failed/memo-1.txt"+consume.ErrorSuffix, []byte("left by the user"))
	e.write("anna/failed/memo.txt", []byte("left by the user"))
	e.write("anna/memo.txt", []byte("memo"))
	// Too large for the 1 MiB limit.
	e.write("anna/big.pdf", append(append(pdf(""), make([]byte, 1<<20)...), pdf("")...))
	if st := e.settle(); st.Failed != 3 {
		t.Fatalf("stats: %+v, want 3 failed", st)
	}
	for rel, want := range map[string]string{
		"anna/failed/notes-1.txt": "other text",
		"anna/failed/memo.txt":    "left by the user",
		"anna/failed/memo-2.txt":  "memo",
	} {
		if got, err := os.ReadFile(e.path(rel)); err != nil || string(got) != want {
			t.Errorf("%s: %q, %v; want %q", rel, got, err, want)
		}
	}
	mustExist(t, e.path("anna/failed/notes-1.txt"+consume.ErrorSuffix))
	mustExist(t, e.path("anna/failed/memo-2.txt"+consume.ErrorSuffix))
	mustNotExist(t, e.path("anna/failed/memo-1.txt"))
	side, err = os.ReadFile(e.path("anna/failed/big.pdf" + consume.ErrorSuffix))
	if err != nil || !strings.Contains(string(side), "file too large") {
		t.Fatalf("sidecar of big.pdf: %q, %v", side, err)
	}
	if n := len(e.docs(anna)); n != 0 {
		t.Fatalf("%d documents, want 0", n)
	}
	// Files in failed/ are never consumed again.
	e.write("anna/failed/valid.pdf", pdf("in failed"))
	e.settle()
	mustExist(t, e.path("anna/failed/valid.pdf"))
	if n := len(e.docs(anna)); n != 0 {
		t.Fatalf("%d documents after a PDF in failed/, want 0", n)
	}
}

func TestIgnoredNames(t *testing.T) {
	e := newEnv(t)
	anna := e.user("anna", false)
	var paths []string
	for _, rel := range []string{
		"anna/.hidden.pdf", "anna/scan.pdf.part", "anna/scan.PART", "anna/upload.tmp",
		"anna/~$letter.pdf", "anna/sub/deep.pdf", "anna/failed/old.pdf",
	} {
		paths = append(paths, e.write(rel, pdf(rel)))
	}
	for range 3 {
		if st := e.settle(); st != (consume.Stats{}) {
			t.Fatalf("stats: %+v, want nothing", st)
		}
	}
	for _, p := range paths {
		mustExist(t, p)
	}
	if n := len(e.docs(anna)); n != 0 {
		t.Fatalf("%d documents, want 0", n)
	}
}

func TestUnsafeUserNamesGetNoFolder(t *testing.T) {
	e := newEnv(t)
	var users []*store.User
	for _, name := range []string{"..", ".", ".x"} {
		users = append(users, e.user(name, false))
	}
	outside := filepath.Join(e.parent, "outside.pdf")
	if err := os.WriteFile(outside, pdf("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	loose := e.write("loose.pdf", pdf("loose"))
	for range 3 {
		e.settle()
	}
	mustExist(t, outside)
	mustExist(t, loose)
	if got := listDir(t, e.dir); !slices.Equal(got, []string{"loose.pdf"}) {
		t.Errorf("consume folder: %q, want only loose.pdf", got)
	}
	if got := listDir(t, e.parent); !slices.Equal(got, []string{"consume", "outside.pdf"}) {
		t.Errorf("parent folder: %q", got)
	}
	for _, u := range users {
		if n := len(e.docs(u)); n != 0 {
			t.Errorf("user %q has %d documents, want 0", u.Name, n)
		}
		if n := e.log.countSuffix("cannot be a folder name", "user="+u.Name); n != 1 {
			t.Errorf("%d warnings for %q, want 1:\n%s", n, u.Name, e.log)
		}
	}
}

func TestSymlinksSkipped(t *testing.T) {
	e := newEnv(t)
	anna := e.user("anna", false)
	bert := e.user("bert", false)
	outDir := filepath.Join(e.parent, "elsewhere")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(outDir, "secret.pdf")
	if err := os.WriteFile(outFile, pdf("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	// anna's folder is a link to a folder outside the tree.
	if err := os.Symlink(outDir, e.path("anna")); err != nil {
		t.Fatal(err)
	}
	// bert's folder is real but holds links: out of the tree, and to a
	// file inside it.
	e.write("bert/.keep", nil)
	inside := e.write("bert/sub/inside.pdf", pdf("inside"))
	if err := os.Symlink(outFile, e.path("bert/link.pdf")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub/inside.pdf", e.path("bert/link2.pdf")); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if st := e.settle(); st != (consume.Stats{}) {
			t.Fatalf("stats: %+v, want nothing", st)
		}
	}
	for _, p := range []string{outFile, inside, e.path("bert/link.pdf"), e.path("bert/link2.pdf")} {
		mustExist(t, p)
	}
	if fi, err := os.Lstat(e.path("anna")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("consume/anna: %v, want the link left in place", err)
	}
	mustNotExist(t, filepath.Join(outDir, consume.FailedDir))
	for _, u := range []*store.User{anna, bert} {
		if n := len(e.docs(u)); n != 0 {
			t.Errorf("%s has %d documents, want 0", u.Name, n)
		}
	}
	for _, want := range [][]string{{"folder is a symbolic link", "user=anna"}, {"symbolic link", "file=link.pdf"}, {"symbolic link", "file=link2.pdf"}} {
		if n := e.log.count(want...); n != 1 {
			t.Errorf("%d warnings %q, want 1:\n%s", n, want, e.log)
		}
	}
}

func TestTransientErrorLeavesFile(t *testing.T) {
	failing := errors.New("database is down")
	var fail error
	e := newEnvWith(t, func(im consume.Importer) consume.Importer {
		return importFunc(func(ctx context.Context, req ingest.Request) (ingest.Result, error) {
			if fail != nil {
				return ingest.Result{}, fail
			}
			return im.Import(ctx, req)
		})
	})
	anna := e.user("anna", false)
	p := e.write("anna/a.pdf", pdf("a"))
	q := e.write("anna/b.pdf", pdf("b"))
	e.scan()
	e.clock.advance(consume.DefaultSettle)

	fail = failing
	if _, err := e.c.Scan(context.Background()); !errors.Is(err, failing) {
		t.Fatalf("scan: %v, want the transient error", err)
	}
	mustExist(t, p)
	mustExist(t, q)
	mustNotExist(t, e.path("anna/failed"))

	// A user removed while the scan runs: the files stay, the scan goes on.
	fail = fmt.Errorf("ingest: create document: %w", store.ErrUnknownUser)
	if _, err := e.c.Scan(context.Background()); err != nil {
		t.Fatalf("scan with an unknown user: %v", err)
	}
	mustExist(t, p)
	mustNotExist(t, e.path("anna/failed"))

	// The next scan imports them, without waiting again.
	fail = nil
	if st := e.scan(); st.Imported != 2 {
		t.Fatalf("stats: %+v, want 2 imported", st)
	}
	mustNotExist(t, p)
	mustNotExist(t, q)
	if n := len(e.docs(anna)); n != 2 {
		t.Fatalf("%d documents, want 2", n)
	}
}

func TestCancelledScanLeavesFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	e := newEnvWith(t, func(im consume.Importer) consume.Importer {
		return importFunc(func(_ context.Context, req ingest.Request) (ingest.Result, error) {
			cancel() // shutdown while the import runs
			return im.Import(ctx, req)
		})
	})
	e.user("anna", false)
	p := e.write("anna/a.pdf", pdf("a"))
	e.scan()
	e.clock.advance(consume.DefaultSettle)
	if _, err := e.c.Scan(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("scan: %v, want context.Canceled", err)
	}
	mustExist(t, p)
	mustNotExist(t, e.path("anna/failed"))
}

type failingUsers struct{}

func (failingUsers) ListUsers(context.Context) ([]*store.User, error) {
	return nil, errors.New("database is down")
}

func TestUsersUnavailable(t *testing.T) {
	e := newEnv(t)
	e.write("anna/a.pdf", pdf("a"))
	c := e.consumer(failingUsers{}, e.im)
	if _, err := c.Scan(context.Background()); err == nil {
		t.Fatal("scan without users: no error")
	}
	mustExist(t, e.path("anna/a.pdf"))
}

func TestLockedUserSkipped(t *testing.T) {
	e := newEnv(t)
	anna := e.user("anna", false)
	p := e.write("anna/a.pdf", pdf("a"))
	if err := e.st.SetUserLocked(context.Background(), anna.ID, true); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if st := e.settle(); st != (consume.Stats{}) {
			t.Fatalf("stats: %+v, want nothing", st)
		}
	}
	mustExist(t, p)
	if n := e.log.count("user is locked", "user=anna"); n != 1 {
		t.Errorf("%d warnings, want 1:\n%s", n, e.log)
	}
	if err := e.st.SetUserLocked(context.Background(), anna.ID, false); err != nil {
		t.Fatal(err)
	}
	if st := e.settle(); st.Imported != 1 {
		t.Fatalf("stats after unlocking: %+v, want imported", st)
	}
	mustNotExist(t, p)
}

func TestFoldersForNewUsers(t *testing.T) {
	e := newEnv(t)
	e.user("anna", false)
	e.scan()
	if got := listDir(t, e.dir); !slices.Equal(got, []string{"anna"}) {
		t.Fatalf("consume folder: %q", got)
	}
	e.user("bert", true)
	e.scan()
	if got := listDir(t, e.dir); !slices.Equal(got, []string{"anna", "bert"}) {
		t.Fatalf("consume folder after adding bert: %q", got)
	}
	// A deleted folder comes back.
	if err := os.Remove(e.path("anna")); err != nil {
		t.Fatal(err)
	}
	e.scan()
	if fi, err := os.Lstat(e.path("anna")); err != nil || !fi.IsDir() {
		t.Fatalf("consume/anna: %v", err)
	}
}

func TestAdminFileWithAnotherUsersHashIsImported(t *testing.T) {
	var admin *store.User
	var checked bool
	var e *env
	e = newEnvWith(t, func(im consume.Importer) consume.Importer {
		return importFunc(func(ctx context.Context, req ingest.Request) (ingest.Result, error) {
			res, err := im.Import(ctx, req)
			if req.OwnerID == admin.ID {
				// The row is committed while the file is still there.
				if res.Outcome != ingest.Imported {
					t.Errorf("admin's import: %v, want imported", res.Outcome)
				}
				if _, gerr := e.st.GetDocument(ctx, store.Scope{UserID: admin.ID}, res.Document.ID); gerr != nil {
					t.Errorf("admin's document: %v", gerr)
				}
				mustExist(t, e.path("root/same.pdf"))
				checked = true
			}
			return res, err
		})
	})
	anna := e.user("anna", false)
	admin = e.user("root", true)
	e.write("anna/same.pdf", pdf("shared"))
	if st := e.settle(); st.Imported != 1 {
		t.Fatalf("anna: %+v", st)
	}
	p := e.write("root/same.pdf", pdf("shared"))
	if st := e.settle(); st.Imported != 1 || st.Duplicates != 0 {
		t.Fatalf("admin: %+v, want imported, not a duplicate", st)
	}
	if !checked {
		t.Fatal("the admin's import was not checked")
	}
	mustNotExist(t, p)
	if len(e.docs(anna)) != 1 || len(e.docs(admin)) != 1 {
		t.Fatalf("documents: anna %d, admin %d; want 1 each", len(e.docs(anna)), len(e.docs(admin)))
	}
}

func TestMissingConsumeFolder(t *testing.T) {
	e := newEnv(t)
	e.user("anna", false)
	if err := os.Remove(e.dir); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		e.scan()
	}
	if n := e.log.count("cannot read it"); n != 1 {
		t.Errorf("%d warnings, want 1:\n%s", n, e.log)
	}
	mustNotExist(t, e.dir)
	if err := os.Mkdir(e.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	e.scan()
	mustExist(t, e.path("anna"))
}

func TestRunScansOnTicks(t *testing.T) {
	e := newEnv(t)
	anna := e.user("anna", false)
	p := e.write("anna/a.pdf", pdf("a"))
	tick := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.c.Run(ctx, tick)
	}()
	// Run scans at once (the file waits); a received tick means the scan
	// before it is finished.
	tick <- time.Time{}
	e.clock.advance(consume.DefaultSettle)
	tick <- time.Time{} // the scan after this tick imports
	tick <- time.Time{} // and has finished when this one is received
	cancel()
	<-done
	mustNotExist(t, p)
	if n := len(e.docs(anna)); n != 1 {
		t.Fatalf("%d documents, want 1", n)
	}
}
