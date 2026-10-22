package consume

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pklnx/paperless-go/internal/ingest"
	"github.com/pklnx/paperless-go/internal/store"
)

// Generated file contents: only their first and last bytes matter here.
var (
	jpegStart = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F', 0}
	pngStart  = append(append([]byte{}, pngSig...), 0, 0, 0, 13, 'I', 'H', 'D', 'R')
	pngIEND   = append([]byte{0, 0, 0, 0}, pngEnd...)
	heicStart = []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'h', 'e', 'i', 'c', 0, 0, 0, 0}
)

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func filler(n int) []byte { return bytes.Repeat([]byte{0x55}, n) }

func TestComplete(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
		want bool
	}{
		{"pdf", []byte("%PDF-1.7\nbody\n%%EOF\n"), true},
		{"pdf after a prefix", cat(filler(100), []byte("%PDF-1.4\n%%EOF")), true},
		{"pdf with padding after EOF", cat([]byte("%PDF-1.7\n%%EOF"), bytes.Repeat([]byte(" "), 1000)), true},
		{"pdf with an earlier EOF only", cat([]byte("%PDF-1.7\n%%EOF\n"), filler(2000)), false},
		{"pdf cut off", cat([]byte("%PDF-1.7\n"), filler(5000)), false},
		{"pdf, %%EOF split by the tail", cat([]byte("%PDF-1.7\n%%EOF"), filler(1021)), false},
		{"jpeg", cat(jpegStart, filler(500), []byte{0xFF, 0xD9}), true},
		{"jpeg with padding", cat(jpegStart, filler(500), []byte{0xFF, 0xD9, 0, 0, 0, 0}), true},
		{"jpeg cut off", cat(jpegStart, filler(500)), false},
		{"png", cat(pngStart, filler(500), pngIEND), true},
		{"png with padding", cat(pngStart, filler(500), pngIEND, []byte{0, 0}), true},
		{"png cut off", cat(pngStart, filler(500)), false},
		{"heic has no marker", cat(heicStart, filler(500)), true},
		{"unsupported", []byte("plain text"), true},
		{"empty", nil, true},
	} {
		got, err := complete(bytes.NewReader(tt.data), int64(len(tt.data)))
		if err != nil || got != tt.want {
			t.Errorf("%s: complete = %v, %v; want %v", tt.name, got, err, tt.want)
		}
	}
}

// consumeEnv is a consume folder with user anna, a counting importer
// that reports duplicates of an existing document, and a manual clock.
type consumeEnv struct {
	c       *Consumer
	im      *countingImporter
	now     time.Time
	userDir string
	logs    *bytes.Buffer
}

func newConsumeEnv(t *testing.T, res ingest.Result) *consumeEnv {
	t.Helper()
	dir := t.TempDir()
	e := &consumeEnv{
		im: &countingImporter{res: res}, now: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC),
		userDir: filepath.Join(dir, "anna"), logs: &bytes.Buffer{},
	}
	if err := os.Mkdir(e.userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	e.c = New(dir, staticUsers{{ID: 1, Name: "anna"}}, e.im, Options{
		Now: func() time.Time { return e.now }, Log: slog.New(slog.NewTextHandler(e.logs, nil)),
	})
	return e
}

func (e *consumeEnv) write(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(e.userDir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// scan scans, then moves the clock on by d.
func (e *consumeEnv) scan(t *testing.T, d time.Duration) Stats {
	t.Helper()
	st, err := e.c.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(d)
	return st
}

var duplicate = ingest.Result{Outcome: ingest.Duplicate, Existing: store.DocumentRef{ID: "doc-1"}}

// A file without its end marker waits; it is imported once it has one, or
// after IncompleteTime unchanged, and deleted only after that import.
func TestIncompleteFilesWait(t *testing.T) {
	for _, tt := range []struct {
		name       string
		start, end []byte
	}{
		{"a.pdf", []byte("%PDF-1.7\n"), []byte("\n%%EOF\n")},
		{"a.jpg", jpegStart, []byte{0xFF, 0xD9}},
		{"a.png", pngStart, pngIEND},
	} {
		t.Run(tt.name+" completed", func(t *testing.T) {
			e := newConsumeEnv(t, duplicate)
			p := e.write(t, tt.name, cat(tt.start, filler(3000)))
			for range 10 {
				if st := e.scan(t, DefaultInterval); st.Waiting != 1 {
					t.Fatalf("stats: %+v, want waiting", st)
				}
			}
			if e.im.calls != 0 {
				t.Fatalf("%d imports of an incomplete file, want 0", e.im.calls)
			}
			// The writer goes on and finishes.
			e.write(t, tt.name, cat(tt.start, filler(3000), tt.end))
			e.scan(t, DefaultSettle)
			e.scan(t, DefaultSettle)
			if e.im.calls != 1 {
				t.Fatalf("%d imports of the finished file, want 1", e.im.calls)
			}
			if _, err := os.Lstat(p); err == nil {
				t.Fatal("the imported file is still there")
			}
		})
		t.Run(tt.name+" after the wait", func(t *testing.T) {
			e := newConsumeEnv(t, duplicate)
			p := e.write(t, tt.name, cat(tt.start, filler(3000)))
			e.scan(t, IncompleteTime-time.Second)
			e.scan(t, time.Second) // 1 s before IncompleteTime: waits
			if e.im.calls != 0 {
				t.Fatalf("%d imports before IncompleteTime, want 0", e.im.calls)
			}
			if _, err := os.Lstat(p); err != nil {
				t.Fatalf("the waiting file: %v", err)
			}
			e.scan(t, 0)
			if e.im.calls != 1 {
				t.Fatalf("%d imports after IncompleteTime, want 1", e.im.calls)
			}
			if _, err := os.Lstat(p); err == nil {
				t.Fatal("the imported file is still there")
			}
		})
	}
	t.Run("heic", func(t *testing.T) {
		e := newConsumeEnv(t, duplicate)
		e.write(t, "a.heic", cat(heicStart, filler(3000)))
		e.scan(t, DefaultSettle)
		e.scan(t, 0)
		if e.im.calls != 1 {
			t.Fatalf("%d imports of a settled HEIC, want 1", e.im.calls)
		}
	})
}

// A duplicate whose document is gone (empty ID) keeps the file; the next
// scan tries it again.
func TestDuplicateWithoutDocumentStays(t *testing.T) {
	e := newConsumeEnv(t, ingest.Result{Outcome: ingest.Duplicate})
	p := e.write(t, "a.pdf", []byte("%PDF-1.7\n%%EOF\n"))
	e.scan(t, DefaultSettle)
	e.scan(t, DefaultInterval)
	if _, err := os.Lstat(p); err != nil {
		t.Fatalf("a.pdf: %v, want it left in place", err)
	}
	e.im.res = duplicate
	e.scan(t, 0)
	if e.im.calls != 2 {
		t.Fatalf("%d imports, want 2", e.im.calls)
	}
	if _, err := os.Lstat(p); err == nil {
		t.Fatal("a.pdf is still there after a real duplicate")
	}
}

// Refused files with long names get a shortened name in failed/ that
// leaves room for the sidecar; the extension stays.
func TestLongNamesInFailed(t *testing.T) {
	e := newConsumeEnv(t, ingest.Result{Outcome: ingest.Refused, Refusal: &ingest.RefusedError{Reason: ingest.ReasonUnsupported}})
	name := strings.Repeat("ä", 125) + ".txt" // 254 bytes
	for i := range 2 {
		e.write(t, name, []byte{byte(i)})
		e.scan(t, DefaultSettle)
		if st := e.scan(t, 0); st.Failed != 1 {
			t.Fatalf("round %d: %+v, want 1 failed", i, st)
		}
	}
	entries, err := os.ReadDir(filepath.Join(e.userDir, FailedDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("failed/: %d entries, want 2 files and 2 sidecars", len(entries))
	}
	for _, en := range entries {
		n := en.Name()
		if len(n) > maxNameBytes || !strings.HasSuffix(strings.TrimSuffix(n, ErrorSuffix), ".txt") {
			t.Errorf("failed/%s (%d bytes)", n, len(n))
		}
	}
	// 255 bytes - ".error.txt" - "-1" - ".txt" leaves 239 bytes: 119 ä.
	if _, err := os.Lstat(filepath.Join(e.userDir, FailedDir, strings.Repeat("ä", 119)+"-1.txt")); err != nil {
		t.Errorf("the second file: %v", err)
	}
}

// A user folder swapped for another folder between the check and the open
// is not scanned.
func TestSwappedUserFolderIsSkipped(t *testing.T) {
	e := newConsumeEnv(t, duplicate)
	e.scan(t, DefaultSettle)
	swap := false
	e.c.openRoot = func(r *os.Root, name string) (*os.Root, error) {
		if swap {
			if err := os.Rename(e.userDir, e.userDir+".old"); err != nil {
				return nil, err
			}
			if err := os.Mkdir(e.userDir, 0o755); err != nil {
				return nil, err
			}
			e.write(t, "planted.pdf", []byte("%PDF-1.7\n%%EOF\n"))
		}
		return r.OpenRoot(name)
	}
	e.write(t, "a.pdf", []byte("%PDF-1.7\n%%EOF\n"))
	e.scan(t, DefaultSettle)
	swap = true
	if st := e.scan(t, DefaultSettle); st != (Stats{}) {
		t.Fatalf("stats of the scan with the swap: %+v, want nothing looked at", st)
	}
	swap = false
	if e.im.calls != 0 {
		t.Fatalf("%d imports from the swapped folder, want 0", e.im.calls)
	}
	if _, err := os.Lstat(filepath.Join(e.userDir, "planted.pdf")); err != nil {
		t.Fatalf("planted.pdf: %v, want it left alone", err)
	}
}

// switchUsers fails with err while it is set and lists anna otherwise.
type switchUsers struct {
	mu  sync.Mutex
	err error
}

func (s *switchUsers) set(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

func (s *switchUsers) ListUsers(context.Context) ([]*store.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return []*store.User{{ID: 1, Name: "anna"}}, nil
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Run logs a failing scan once, not at every tick, and says when it works
// again.
func TestRunLogsRepeatedFailuresOnce(t *testing.T) {
	var logs syncBuffer
	users := &switchUsers{err: io.ErrUnexpectedEOF}
	c := New(t.TempDir(), users, &countingImporter{}, Options{Log: slog.New(slog.NewTextHandler(&logs, nil))})
	tick := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx, tick)
	}()
	for range 3 {
		tick <- time.Time{}
	}
	users.set(nil)
	tick <- time.Time{} // the scan after this one works
	tick <- time.Time{}
	cancel()
	<-done
	if n := strings.Count(logs.String(), "scan stopped"); n != 1 {
		t.Errorf("%d warnings, want 1:\n%s", n, logs.String())
	}
	if n := strings.Count(logs.String(), "scans work again"); n != 1 {
		t.Errorf("%d recoveries logged, want 1:\n%s", n, logs.String())
	}
}
