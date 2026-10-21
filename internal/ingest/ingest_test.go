package ingest_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"image"
	"image/png"
	"io"
	"math"
	"strings"
	"sync"
	"testing"
	"testing/iotest"

	"github.com/pklnx/paperless-go/internal/blobstore"
	"github.com/pklnx/paperless-go/internal/ingest"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// Test files are generated here; no real documents.

// pdfOfSize is a file detected as PDF with exactly n bytes; distinct
// fill bytes give distinct hashes.
func pdfOfSize(n int, fill byte) []byte {
	b := bytes.Repeat([]byte{fill}, n)
	copy(b, "%PDF-1.7\n")
	return b
}

func pngFile(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

type env struct {
	st    *store.Store
	blobs *blobstore.Store
	user  *store.User
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st := storetest.New(t)
	bs, err := blobstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &env{st: st, blobs: bs, user: storetest.User(t, st, "anna", false)}
}

func (e *env) importer(t *testing.T, maxSize int64) *ingest.Importer {
	t.Helper()
	return e.importerWith(t, e.st, e.blobs, maxSize)
}

func (e *env) importerWith(t *testing.T, st ingest.Store, blobs ingest.Blobs, maxSize int64) *ingest.Importer {
	t.Helper()
	im, err := ingest.New(st, blobs, maxSize)
	if err != nil {
		t.Fatal(err)
	}
	return im
}

func run(t *testing.T, im *ingest.Importer, owner int64, name string, data []byte) ingest.Result {
	t.Helper()
	res, err := im.Import(context.Background(), ingest.Request{OwnerID: owner, FileName: name, Body: bytes.NewReader(data)})
	if err != nil {
		t.Fatalf("import %q: %v", name, err)
	}
	return res
}

// documents lists every owner's documents.
func (e *env) documents(t *testing.T) []*store.Document {
	t.Helper()
	docs, err := e.st.ListDocuments(context.Background(), store.Scope{UserID: e.user.ID, All: true})
	if err != nil {
		t.Fatal(err)
	}
	return docs
}

// blobCount counts the files in the blob store.
func (e *env) blobCount(t *testing.T) int {
	t.Helper()
	return e.entries(t, blobstore.EntryBlob)
}

// entries counts the blob store's entries of a kind.
func (e *env) entries(t *testing.T, kind blobstore.EntryKind) int {
	t.Helper()
	n := 0
	if err := e.blobs.Walk(func(en blobstore.Entry) error {
		if en.Kind == kind {
			n++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNewRefusesNonPositiveLimit(t *testing.T) {
	for _, n := range []int64{0, -1} {
		if _, err := ingest.New(nil, nil, n); err == nil {
			t.Errorf("New(%d) accepted", n)
		}
	}
}

func TestImportDetectsFromContentNotName(t *testing.T) {
	e := newEnv(t)
	im := e.importer(t, 1<<20)
	img := pngFile(t)
	pdf := pdfOfSize(100, 'x')

	res := run(t, im, e.user.ID, "Scan.pdf", img)
	if res.Outcome != ingest.Imported {
		t.Fatalf("png: %+v", res)
	}
	d := res.Document
	if d.OriginalMIME != "image/png" || d.Source != store.SourceImage || d.Title != "Scan" ||
		d.OriginalFileName != "Scan.pdf" || !d.IsInbox || d.OwnerID != e.user.ID ||
		d.OriginalSHA256 != sha(img) || d.OriginalSize != int64(len(img)) || d.ArchiveSHA256 != nil {
		t.Errorf("png document: %+v", d)
	}
	if !e.blobs.Exists(sha(img)) {
		t.Error("png blob missing")
	}

	res = run(t, im, e.user.ID, "photo.jpg", pdf)
	if res.Outcome != ingest.Imported || res.Document.OriginalMIME != "application/pdf" ||
		res.Document.Source != store.SourceFile || !res.Document.IsInbox {
		t.Errorf("pdf: %+v %+v", res, res.Document)
	}
}

func TestImportRefusedStoresNothing(t *testing.T) {
	e := newEnv(t)
	im := e.importer(t, 1<<20)
	res := run(t, im, e.user.ID, "notes.pdf", []byte("not a pdf at all"))
	if res.Outcome != ingest.Refused || res.Refusal == nil || res.Refusal.Reason != ingest.ReasonUnsupported || res.Document != nil {
		t.Errorf("result = %+v", res)
	}
	if n := len(e.documents(t)); n != 0 {
		t.Errorf("%d documents", n)
	}
	if n := e.blobCount(t); n != 0 {
		t.Errorf("%d blobs", n)
	}
}

func TestImportSizeLimit(t *testing.T) {
	// Limits below and above the detection prefix: a file that runs past
	// the limit during detection and one that runs past it while stored.
	for _, limit := range []int{4096, ingest.PrefixSize + 4096} {
		e := newEnv(t)
		im := e.importer(t, int64(limit))
		res := run(t, im, e.user.ID, "big.pdf", pdfOfSize(limit+1, 'a'))
		if res.Outcome != ingest.Refused || res.Refusal.Reason != ingest.ReasonTooLarge {
			t.Errorf("limit %d, one byte more: %+v", limit, res)
		}
		if n := e.blobCount(t); n != 0 {
			t.Errorf("limit %d: %d blobs after refusal", limit, n)
		}
		if n := e.entries(t, blobstore.EntryTemp); n != 0 {
			t.Errorf("limit %d: %d temporary files after refusal", limit, n)
		}
		at := pdfOfSize(limit, 'b')
		res = run(t, im, e.user.ID, "exact.pdf", at)
		if res.Outcome != ingest.Imported || res.Document.OriginalSize != int64(limit) || res.Document.OriginalSHA256 != sha(at) {
			t.Errorf("limit %d, exactly at it: %+v", limit, res)
		}
	}
}

// The largest possible limit must not overflow the size check.
func TestImportMaxInt64Limit(t *testing.T) {
	e := newEnv(t)
	im := e.importer(t, math.MaxInt64)
	pdf := pdfOfSize(ingest.PrefixSize+10, 'm')
	if res := run(t, im, e.user.ID, "m.pdf", pdf); res.Outcome != ingest.Imported || res.Document.OriginalSize != int64(len(pdf)) {
		t.Errorf("result = %+v", res)
	}
}

func TestImportPixelLimitStoresNothing(t *testing.T) {
	e := newEnv(t)
	im := e.importer(t, 1<<20)
	// A PNG header claiming one pixel more than allowed.
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := []byte{0, 0, 0, 13, 'I', 'H', 'D', 'R'}
	ihdr = binary.BigEndian.AppendUint32(ihdr, ingest.MaxPixels+1)
	ihdr = binary.BigEndian.AppendUint32(ihdr, 1)
	ihdr = append(ihdr, 8, 2, 0, 0, 0)
	b.Write(ihdr)
	b.Write(crc(ihdr[4:]))
	res := run(t, im, e.user.ID, "huge.png", b.Bytes())
	if res.Outcome != ingest.Refused || res.Refusal.Reason != ingest.ReasonTooManyPixels {
		t.Errorf("result = %+v", res)
	}
	if n := e.blobCount(t); n != 0 {
		t.Errorf("%d blobs", n)
	}
}

func crc(b []byte) []byte { return binary.BigEndian.AppendUint32(nil, crc32.ChecksumIEEE(b)) }

func TestImportDuplicatePerOwner(t *testing.T) {
	e := newEnv(t)
	bob := storetest.User(t, e.st, "bob", false)
	admin := storetest.User(t, e.st, "root", true)
	im := e.importer(t, 1<<20)
	pdf := pdfOfSize(200, 'd')

	first := run(t, im, e.user.ID, "Rechnung.pdf", pdf)
	if first.Outcome != ingest.Imported {
		t.Fatalf("first: %+v", first)
	}
	again := run(t, im, e.user.ID, "copy.pdf", pdf)
	if again.Outcome != ingest.Duplicate || again.Existing.ID != first.Document.ID || again.Existing.Title != "Rechnung" || again.Document != nil {
		t.Errorf("same owner: %+v", again)
	}
	// Another owner and an admin get their own documents of the same file.
	for _, u := range []*store.User{bob, admin} {
		res := run(t, im, u.ID, "Rechnung.pdf", pdf)
		if res.Outcome != ingest.Imported || res.Document.OwnerID != u.ID || res.Document.ID == first.Document.ID {
			t.Errorf("%s: %+v", u.Name, res)
		}
	}
	if n := len(e.documents(t)); n != 3 {
		t.Errorf("%d documents, want 3", n)
	}
	if n := e.blobCount(t); n != 1 {
		t.Errorf("%d blobs, want 1", n)
	}
}

func TestImportTitle(t *testing.T) {
	e := newEnv(t)
	im := e.importer(t, 1<<20)
	res, err := im.Import(context.Background(), ingest.Request{
		OwnerID: e.user.ID, FileName: "x.pdf", Title: "  Given\ttitle ", Body: bytes.NewReader(pdfOfSize(50, 't')),
	})
	if err != nil || res.Document.Title != "Giventitle" || res.Document.OriginalFileName != "x.pdf" {
		t.Errorf("title override: %+v, %v", res.Document, err)
	}
	res = run(t, im, e.user.ID, "", pdfOfSize(50, 'u'))
	if res.Document.Title != "" || res.Document.OriginalFileName != "" {
		t.Errorf("no name: %+v", res.Document)
	}
	res = run(t, im, e.user.ID, "/in/Ma\u0308rz\x01 \xff.PDF", pdfOfSize(50, 'v'))
	if res.Document.Title != "M\u00e4rz \uFFFD" || res.Document.OriginalFileName != "M\u00e4rz \uFFFD.PDF" {
		t.Errorf("cleaned: %q %q", res.Document.Title, res.Document.OriginalFileName)
	}
}

func TestNames(t *testing.T) {
	long := strings.Repeat("ä", 300)
	for _, tc := range []struct{ in, file, title string }{
		{"Rechnung 2026.pdf", "Rechnung 2026.pdf", "Rechnung 2026"},
		{`C:\scans\Brief.jpeg`, "Brief.jpeg", "Brief"},
		{"a/b/archive.tar.gz", "archive.tar.gz", "archive.tar"},
		{".pdf", ".pdf", ".pdf"},
		{"noext", "noext", "noext"},
		{"..", "", ""},
		{"dir/", "", ""},
		{"tab\there\n.pdf", "tabhere.pdf", "tabhere"},
		{long + ".pdf", strings.Repeat("ä", 127), strings.Repeat("ä", 127)},
		{strings.Repeat("x", 250) + ".pdf", strings.Repeat("x", 250) + ".pdf", strings.Repeat("x", 200)},
	} {
		if got := ingest.FileName(tc.in); got != tc.file {
			t.Errorf("FileName(%q) = %q, want %q", tc.in, got, tc.file)
		}
		if got := ingest.TitleFromFileName(tc.in); got != tc.title {
			t.Errorf("TitleFromFileName(%q) = %q, want %q", tc.in, got, tc.title)
		}
	}
	if got := ingest.Title(strings.Repeat("ü", 250)); got != strings.Repeat("ü", 200) {
		t.Errorf("Title cut to %d runes", len([]rune(got)))
	}
}

// failingStore fails the first CreateDocument or FindDocumentByOriginal
// call it is told to.
type failingStore struct {
	ingest.Store
	mu         sync.Mutex
	failCreate int
	failFind   int
}

var errInjected = errors.New("injected")

func (f *failingStore) CreateDocument(ctx context.Context, sc store.Scope, in store.NewDocument) (*store.Document, error) {
	f.mu.Lock()
	fail := f.failCreate > 0
	f.failCreate--
	f.mu.Unlock()
	if fail {
		return nil, errInjected
	}
	return f.Store.CreateDocument(ctx, sc, in)
}

func (f *failingStore) FindDocumentByOriginal(ctx context.Context, owner int64, hash string) (*store.DocumentRef, error) {
	f.mu.Lock()
	fail := f.failFind > 0
	f.failFind--
	f.mu.Unlock()
	if fail {
		return nil, errInjected
	}
	return f.Store.FindDocumentByOriginal(ctx, owner, hash)
}

type failingBlobs struct {
	ingest.Blobs
	fail bool
}

func (f *failingBlobs) Put(r io.Reader) (blobstore.Blob, bool, error) {
	if f.fail {
		f.fail = false
		_, _ = io.Copy(io.Discard, r)
		return blobstore.Blob{}, false, errInjected
	}
	return f.Blobs.Put(r)
}

func TestImportFailureLeavesNoRowAndRetrySucceeds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		st    *failingStore
		blobs *failingBlobs
	}{
		{"create", &failingStore{failCreate: 1}, &failingBlobs{}},
		{"duplicate check", &failingStore{failFind: 1}, &failingBlobs{}},
		{"blob store", &failingStore{}, &failingBlobs{fail: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			tc.st.Store, tc.blobs.Blobs = e.st, e.blobs
			im := e.importerWith(t, tc.st, tc.blobs, 1<<20)
			pdf := pdfOfSize(300, 'f')
			_, err := im.Import(context.Background(), ingest.Request{OwnerID: e.user.ID, FileName: "f.pdf", Body: bytes.NewReader(pdf)})
			if !errors.Is(err, errInjected) {
				t.Fatalf("error = %v, want the injected one", err)
			}
			if n := len(e.documents(t)); n != 0 {
				t.Fatalf("%d documents after failure", n)
			}
			res := run(t, im, e.user.ID, "f.pdf", pdf)
			if res.Outcome != ingest.Imported {
				t.Errorf("retry: %+v", res)
			}
			if n := len(e.documents(t)); n != 1 {
				t.Errorf("%d documents after retry", n)
			}
		})
	}
}

func TestImportReadErrorIsTransient(t *testing.T) {
	e := newEnv(t)
	im := e.importer(t, 1<<20)
	for _, body := range []io.Reader{
		iotest.ErrReader(errInjected), // during detection
		io.MultiReader(bytes.NewReader(pdfOfSize(ingest.PrefixSize, 'r')), iotest.ErrReader(errInjected)), // while stored
	} {
		res, err := im.Import(context.Background(), ingest.Request{OwnerID: e.user.ID, Body: body})
		if !errors.Is(err, errInjected) || res.Outcome != 0 {
			t.Errorf("Import = %+v, %v; want the read error", res, err)
		}
	}
	if n := len(e.documents(t)); n != 0 {
		t.Errorf("%d documents", n)
	}
}

// barrierStore holds the first two duplicate checks until both have run,
// so that two concurrent imports both pass the check before either
// inserts. Later lookups (the loser's lookup of the existing document)
// pass straight through.
type barrierStore struct {
	ingest.Store
	mu      sync.Mutex
	n       int
	release chan struct{}
}

func (b *barrierStore) FindDocumentByOriginal(ctx context.Context, owner int64, hash string) (*store.DocumentRef, error) {
	ref, err := b.Store.FindDocumentByOriginal(ctx, owner, hash)
	b.mu.Lock()
	b.n++
	n := b.n
	if n == 2 {
		close(b.release)
	}
	b.mu.Unlock()
	if n <= 2 {
		<-b.release
	}
	return ref, err
}

func TestImportConcurrentIdentical(t *testing.T) {
	e := newEnv(t)
	bs := &barrierStore{Store: e.st, release: make(chan struct{})}
	im := e.importerWith(t, bs, e.blobs, 1<<20)
	pdf := pdfOfSize(400, 'c')

	results := make([]ingest.Result, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() {
			results[i], errs[i] = im.Import(context.Background(), ingest.Request{
				OwnerID: e.user.ID, FileName: "same.pdf", Body: bytes.NewReader(pdf),
			})
		})
	}
	wg.Wait()

	var imported, dup *ingest.Result
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("import %d: %v", i, errs[i])
		}
		switch results[i].Outcome {
		case ingest.Imported:
			imported = &results[i]
		case ingest.Duplicate:
			dup = &results[i]
		}
	}
	if imported == nil || dup == nil {
		t.Fatalf("results = %+v, want one imported and one duplicate", results)
	}
	if dup.Existing.ID != imported.Document.ID || dup.Existing.Title != "same" {
		t.Errorf("duplicate names %+v, want %s", dup.Existing, imported.Document.ID)
	}
	if n := len(e.documents(t)); n != 1 {
		t.Errorf("%d documents, want 1", n)
	}
}
