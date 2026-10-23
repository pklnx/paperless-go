package blobstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPutDeduplicates(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	content := "Subject: hi\r\n\r\nhello\r\n"
	sum := sha256.Sum256([]byte(content))
	want := hex.EncodeToString(sum[:])

	b1, created, err := s.Put(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if !created || b1.SHA256 != want || b1.Size != int64(len(content)) {
		t.Fatalf("unexpected first put: %+v created=%v", b1, created)
	}
	if b1.Path != "documents/"+want[:2]+"/"+want[2:4]+"/"+want {
		t.Fatalf("unexpected path %q", b1.Path)
	}

	b2, created, err := s.Put(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if created || b2 != b1 {
		t.Fatalf("second put should dedup: %+v created=%v", b2, created)
	}

	f, err := s.OpenHash(want)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, _ := io.ReadAll(f)
	if string(got) != content {
		t.Fatalf("content mismatch: %q", got)
	}
	if !s.Exists(want) {
		t.Fatal("Exists returned false")
	}

	tmpEntries, err := os.ReadDir(filepath.Join(root, "tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tmpEntries) != 0 {
		t.Fatalf("temp files left behind: %d", len(tmpEntries))
	}
}

// The same bytes stored for different purposes (the original upload, and
// the archive PDF of a PDF that already has a text layer, or the same file
// uploaded by two users) are one file on disk.
func TestPutSameContentDifferentPurposes(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	content := "%PDF-1.7\nsame bytes\n%%EOF\n"
	original, created, err := s.Put(strings.NewReader(content))
	if err != nil || !created {
		t.Fatalf("original: %v created=%v", err, created)
	}
	archive, created, err := s.Put(strings.NewReader(content))
	if err != nil || created {
		t.Fatalf("archive: %v created=%v", err, created)
	}
	if archive != original {
		t.Fatalf("archive %+v, original %+v", archive, original)
	}
	var blobs []Entry
	if err := s.Walk(func(e Entry) error {
		if e.Kind != EntryBlob {
			t.Errorf("unexpected entry %+v", e)
		}
		blobs = append(blobs, e)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(blobs) != 1 || blobs[0].Path != original.Path || blobs[0].SHA256 != original.SHA256 {
		t.Fatalf("want one file %s, got %+v", original.Path, blobs)
	}
	if strings.Contains(original.Path, ".") {
		t.Fatalf("blob name has an extension: %s", original.Path)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestPutCleansUpOnError(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Put(failingReader{}); err == nil {
		t.Fatal("expected error")
	}
	tmpEntries, _ := os.ReadDir(filepath.Join(root, "tmp"))
	if len(tmpEntries) != 0 {
		t.Fatalf("temp files left behind: %d", len(tmpEntries))
	}
}

func TestWalk(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	blob, _, err := s.Put(strings.NewReader("Subject: hi\r\n\r\nhello\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.pdf"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := blob.SHA256
	write := func(rel string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("tmp/put-1")
	write("documents/" + h[:2] + "/" + h[2:4] + "/notes.txt")
	write("documents/" + h[:2] + "/" + h)
	write("documents/zz/00/x")
	write("documents/" + h[:2] + "/ff/" + h)                      // wrong directory for the hash
	write("documents/" + h[:2] + "/" + h[2:4] + "/" + h + ".pdf") // blobs have no extension
	write("export/a.mbox")                                        // not walked
	if err := os.Symlink(outside, filepath.Join(root, "documents", "ab")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.pdf"), filepath.Join(root, "documents", h[:2], h[2:4], strings.Repeat("0", 64))); err != nil {
		t.Fatal(err)
	}

	got := map[string]EntryKind{}
	err = s.Walk(func(e Entry) error {
		if strings.Contains(e.Path, "secret") {
			t.Errorf("followed a symlink: %s", e.Path)
		}
		got[e.Path] = e.Kind
		if e.Kind == EntryBlob && (e.SHA256 != h || e.Size != blob.Size) {
			t.Errorf("blob entry %+v", e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]EntryKind{
		blob.Path:   EntryBlob,
		"tmp/put-1": EntryTemp,
		"documents/" + h[:2] + "/" + h[2:4] + "/notes.txt":                  EntryUnexpected,
		"documents/" + h[:2] + "/" + h:                                      EntryUnexpected,
		"documents/zz":                                                      EntryUnexpected,
		"documents/" + h[:2] + "/ff/" + h:                                   EntryUnexpected,
		"documents/" + h[:2] + "/" + h[2:4] + "/" + h + ".pdf":              EntryUnexpected,
		"documents/ab":                                                      EntryUnexpected,
		"documents/" + h[:2] + "/" + h[2:4] + "/" + strings.Repeat("0", 64): EntryUnexpected,
	}
	if len(got) != len(want) {
		t.Errorf("got %v", got)
	}
	for p, k := range want {
		if g, ok := got[p]; !ok || g != k {
			t.Errorf("%s: got %v (found %v), want %v", p, g, ok, k)
		}
	}
}

// A removal is only durable within an available store root: without it,
// Remove fails instead of reporting the file as gone.
func TestRemoveFailsWithoutRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	blob, _, err := s.Put(strings.NewReader("Subject: gone\r\n\r\nbody\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(blob.SHA256); err == nil {
		t.Fatal("Remove succeeded without the store root")
	}
}

// OpenHash, Exists and Remove accept only lowercase hex SHA-256 hashes: no
// input reaches a path outside documents/ or a name the store did not
// build, and short input does not panic. Files are planted where the
// uppercase and the 65-character names would point, so an unvalidated
// lookup would find them.
func TestHashInputValidated(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("x"))
	valid := hex.EncodeToString(sum[:])
	upper := strings.ToUpper(valid)
	long := valid + "0"
	for _, name := range []string{upper, long} {
		p := filepath.Join(root, "documents", name[0:2], name[2:4], name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("planted"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "outside"), []byte("planted"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{
		"", "a", "ab", "abc", "..", "../outside", "../../../../../../etc/passwd",
		upper, long, valid[:63], valid[:62] + "..", valid[:63] + "/", valid[:63] + "g",
	} {
		if s.Exists(bad) {
			t.Errorf("Exists(%q) = true", bad)
		}
		if f, err := s.OpenHash(bad); !errors.Is(err, ErrNotHash) {
			if f != nil {
				_ = f.Close()
			}
			t.Errorf("OpenHash(%q) = %v, want ErrNotHash", bad, err)
		}
		if err := s.Remove(bad); !errors.Is(err, ErrNotHash) {
			t.Errorf("Remove(%q) = %v, want ErrNotHash", bad, err)
		}
	}
	for _, p := range []string{
		filepath.Join(root, "outside"),
		filepath.Join(root, "documents", upper[0:2], upper[2:4], upper),
		filepath.Join(root, "documents", long[0:2], long[2:4], long),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("planted file %s: %v", p, err)
		}
	}

	// A valid hash without a blob is missing, not malformed.
	if _, err := s.OpenHash(valid); !errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrNotHash) {
		t.Errorf("OpenHash(missing) = %v, want fs.ErrNotExist", err)
	}
	if s.Exists(valid) {
		t.Error("Exists(missing) = true")
	}
}

// Files finds a store's blobs without opening it, and creates nothing.
func TestFilesExists(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	f := OpenFiles(root)
	sum := sha256.Sum256([]byte("x"))
	h := hex.EncodeToString(sum[:])
	if f.Exists(h) || f.Root() != root {
		t.Fatal("found a blob in a missing store")
	}
	if _, err := os.Stat(root); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("OpenFiles created the root: %v", err)
	}
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Put(strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if !f.Exists(h) || !s.Exists(h) || f.Exists("../../etc/passwd") {
		t.Error("Exists")
	}
}

// RemoveTemp removes only temporary files of Put, also one already gone.
func TestRemoveTemp(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "tmp", "put-123")
	if err := os.WriteFile(p, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveTemp("tmp/put-123"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("still there: %v", err)
	}
	if err := s.RemoveTemp("tmp/put-123"); err != nil {
		t.Errorf("already gone: %v", err)
	}
	for _, bad := range []string{"tmp/other", "documents/put-1", "tmp/put-1/../../x", `tmp/put-a\b`, "tmp/put-", "put-1"} {
		if err := s.RemoveTemp(bad); !errors.Is(err, ErrNotTemp) {
			t.Errorf("RemoveTemp(%q): %v", bad, err)
		}
	}
}
