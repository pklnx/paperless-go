package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pklnx/paperless-go/internal/store"
)

// fakeRunner records the commands it gets and answers them with handle.
type fakeRunner struct {
	mu     sync.Mutex
	calls  []Command
	handle func(ctx context.Context, c Command) ([]byte, error)
}

func (f *fakeRunner) Run(ctx context.Context, c Command) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	return f.handle(ctx, c)
}

func (f *fakeRunner) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, c.Name)
	}
	return out
}

// pdf is what the fake tools report about a document.
type pdf struct {
	pages     []string // the text of each page
	encrypted bool
	perms     string // the permissions pdfinfo lists; "" means change:no
	info      error  // pdfinfo's error
	text      error  // pdftotext's error
	// ocr is the text of each page after OCR; nil means OCRmyPDF must not
	// run.
	ocr    []string
	ocrErr error // OCRmyPDF's error
	// convertErr is the error of img2pdf and heif-dec.
	convertErr error
	// decoded is what heif-dec writes; nil means a small JPEG.
	decoded []byte
	// thumb is what pdftoppm writes; nil means a 362×512 JPEG.
	thumb    []byte
	thumbErr error // pdftoppm's error
}

// poppler answers the tools as the real ones would for p: pdfinfo and
// pdftotext on the original (or the image's PDF) and on OCRmyPDF's output,
// OCRmyPDF, img2pdf and heif-dec. Output files are written into the
// command's directory.
func poppler(p pdf) func(context.Context, Command) ([]byte, error) {
	write := func(c Command, name, content string) error {
		return os.WriteFile(filepath.Join(c.Dir, name), []byte(content), 0o600)
	}
	pageText := func(pages []string) string {
		var b strings.Builder
		for _, page := range pages {
			b.WriteString(page + "\f")
		}
		return b.String()
	}
	return func(_ context.Context, c Command) ([]byte, error) {
		switch c.Name {
		case "pdfinfo":
			if p.info != nil {
				return nil, p.info
			}
			enc := "no"
			if p.encrypted {
				perms := p.perms
				if perms == "" {
					perms = "print:yes copy:yes change:no addNotes:no"
				}
				enc = "yes (" + perms + " algorithm:AES-256)"
			}
			return fmt.Appendf(nil, "Title:           a title\nPages:           %d\nEncrypted:       %s\nPage size:       595 x 842 pts (A4)\n", len(p.pages), enc), nil
		case "pdftotext":
			if c.Args[2] == archiveFile {
				return nil, write(c, c.Args[3], pageText(p.ocr))
			}
			if p.text != nil {
				return nil, p.text
			}
			return nil, write(c, c.Args[len(c.Args)-1], pageText(p.pages))
		case "ocrmypdf":
			if p.ocrErr != nil {
				return nil, p.ocrErr
			}
			if p.ocr == nil {
				return nil, errors.New("OCRmyPDF must not run")
			}
			return nil, write(c, c.Args[len(c.Args)-1], "%PDF-1.4 OCRed")
		case "img2pdf":
			if p.convertErr != nil {
				return nil, p.convertErr
			}
			return nil, write(c, c.Args[slices.Index(c.Args, "-o")+1], "%PDF-1.4 image")
		case "heif-dec":
			if p.convertErr != nil {
				return nil, p.convertErr
			}
			out := p.decoded
			if out == nil {
				out = jpegOf(100, 50)
			}
			return nil, os.WriteFile(filepath.Join(c.Dir, c.Args[len(c.Args)-1]), out, 0o600)
		case "pdftoppm":
			if p.thumbErr != nil {
				return nil, p.thumbErr
			}
			out := p.thumb
			if out == nil {
				out = jpegOf(362, 512)
			}
			return nil, os.WriteFile(filepath.Join(c.Dir, c.Args[len(c.Args)-1]+".jpg"), out, 0o600)
		}
		return nil, fmt.Errorf("unexpected tool %s", c.Name)
	}
}

// jpegOf encodes a white w×h JPEG.
func jpegOf(w, h int) []byte {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func runPipeline(t *testing.T, p pdf, mime string) (Result, *fakeRunner, error) {
	t.Helper()
	return runPipelineOn(t, p, mime, []byte("%PDF-1.4"))
}

// runPipelineOn runs the pipeline with the fake tools on an original with
// the given content.
func runPipelineOn(t *testing.T, p pdf, mime string, original []byte) (Result, *fakeRunner, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "original")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{handle: poppler(p)}
	res, err := (&Pipeline{Runner: f, Languages: "deu+eng", Jobs: 3}).Run(context.Background(), Input{Dir: dir, Path: path, MIME: mime})
	if res.Archive != "" && filepath.Dir(res.Archive) != dir {
		t.Fatalf("archive %s outside the work directory %s", res.Archive, dir)
	}
	return res, f, err
}

const longText = "Rechnung Nr. 4711 vom 1. Oktober"

func TestPipelineTextOnEveryPage(t *testing.T) {
	res, f, err := runPipeline(t, pdf{pages: []string{longText, "Seite 2: " + longText}}, "application/pdf")
	if err != nil {
		t.Fatal(err)
	}
	want := Result{PageCount: 2, Text: longText + "\fSeite 2: " + longText, TextSource: store.TextSourcePDF}
	if res != want {
		t.Fatalf("result %+v, want %+v", res, want)
	}
	if got := f.names(); !slices.Equal(got, []string{"pdfinfo", "pdftotext"}) {
		t.Fatalf("tools: %v", got)
	}
}

func TestPipelineArguments(t *testing.T) {
	_, f, _ := runPipeline(t, pdf{pages: []string{longText}}, "application/pdf")
	if len(f.calls) != 2 {
		t.Fatalf("calls: %+v", f.calls)
	}
	info, text := f.calls[0], f.calls[1]
	if !slices.Equal(info.Args, []string{"original"}) {
		t.Errorf("pdfinfo args %q", info.Args)
	}
	if !slices.Equal(text.Args, []string{"-enc", "UTF-8", "original", "text.txt"}) {
		t.Errorf("pdftotext args %q", text.Args)
	}
	if info.Dir == "" || info.Dir != text.Dir {
		t.Errorf("directories %q and %q", info.Dir, text.Dir)
	}
}

func TestPipelineCopyForbidden(t *testing.T) {
	res, _, err := runPipeline(t, pdf{pages: []string{""}, text: &ToolError{Tool: "pdftotext", ExitCode: 3}}, "application/pdf")
	if err != nil || res.Note != msgNoCopy || res.Text != "" || res.TextSource != "" || res.PageCount != 1 {
		t.Fatalf("result %+v, %v", res, err)
	}
}

func TestPipelineUnreadablePDF(t *testing.T) {
	stderr := "Command Line Error: Incorrect password /secret/path"
	for _, p := range []pdf{
		{info: &ToolError{Tool: "pdfinfo", ExitCode: 1, Stderr: stderr}},
		{pages: []string{longText}, text: &ToolError{Tool: "pdftotext", ExitCode: 1, Stderr: stderr}},
		{pages: nil}, // no pages
	} {
		_, _, err := runPipeline(t, p, "application/pdf")
		var pe *PermanentError
		if !errors.As(err, &pe) {
			t.Fatalf("%+v: %v is not permanent", p, err)
		}
		if msg := errorMessage(err); strings.Contains(msg, "secret") || strings.Contains(msg, "password /") {
			t.Fatalf("message shows tool output: %q", msg)
		}
	}
}

func TestPipelineTransientToolError(t *testing.T) {
	_, _, err := runPipeline(t, pdf{info: &ToolError{Tool: "pdfinfo", ExitCode: 99, Stderr: "secret"}}, "application/pdf")
	var pe *PermanentError
	if err == nil || errors.As(err, &pe) {
		t.Fatalf("exit status 99: %v", err)
	}
	if msg := errorMessage(err); msg != "pdfinfo failed with exit status 99." {
		t.Fatalf("message %q", msg)
	}
}

func TestPipelineOtherTypes(t *testing.T) {
	for _, mime := range []string{"text/plain", ""} {
		_, f, err := runPipeline(t, pdf{}, mime)
		var pe *PermanentError
		if !errors.As(err, &pe) || pe.Msg != msgUnsupported {
			t.Errorf("%q: %v", mime, err)
		}
		if len(f.calls) != 0 {
			t.Errorf("%q ran tools: %v", mime, f.names())
		}
	}
}

func TestPagesWithoutText(t *testing.T) {
	fourteen := "abcdefghijklmn"
	for _, tc := range []struct {
		text  string
		pages int
		want  []int
	}{
		{longText, 1, nil},
		{fourteen, 1, []int{0}},
		{fourteen + "o", 1, nil},
		{"a b c d e f g h i j k l m n", 1, []int{0}}, // white space does not count
		{"äöüßäöüßäöüßäöü", 1, nil},                  // characters, not bytes
		{longText + "\f\f" + longText, 3, []int{1}},
		{longText, 3, []int{1, 2}}, // pages missing from the text
		{"", 2, []int{0, 1}},
	} {
		if got := PagesWithoutText(tc.text, tc.pages); !slices.Equal(got, tc.want) {
			t.Errorf("PagesWithoutText(%q, %d) = %v, want %v", tc.text, tc.pages, got, tc.want)
		}
	}
}

func TestCleanText(t *testing.T) {
	for in, want := range map[string]string{
		"Rechnung\x00 4711":     "Rechnung 4711",
		"bad \xff\xfe utf8":     "bad � utf8",
		"page 1\fpage 2\f\n \f": "page 1\fpage 2",
		"   ":                   "",
		"Grüße":                 "Grüße",
	} {
		if got := CleanText(in); got != want {
			t.Errorf("CleanText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParsePDFInfo(t *testing.T) {
	// Metadata comes first and may contain line breaks that look like
	// fields; the tool's own lines come last.
	out := "Title:          x\nPages:          999\nEncrypted:      yes\nAuthor: y\nPages:          3\nEncrypted:      no\n"
	info, err := parsePDFInfo(out)
	if err != nil || info.pages != 3 || info.encrypted {
		t.Fatalf("parsePDFInfo: %+v, %v", info, err)
	}
	for line, want := range map[string]pdfInfo{
		"Encrypted: yes (print:yes copy:yes change:yes addNotes:yes algorithm:AES-256)": {pages: 1, encrypted: true, changeAllowed: true},
		"Encrypted: yes (print:yes copy:yes change:no addNotes:yes algorithm:AES-256)":  {pages: 1, encrypted: true},
		"Encrypted: yes": {pages: 1, encrypted: true},
		// Metadata cannot grant the permission: the last line wins.
		"Encrypted: yes (change:yes)\nEncrypted: yes (print:yes change:no)": {pages: 1, encrypted: true},
		"Encrypted: no": {pages: 1},
	} {
		if got, err := parsePDFInfo("Pages: 1\n" + line + "\n"); err != nil || got != want {
			t.Errorf("%q: %+v, %v; want %+v", line, got, err, want)
		}
	}
	for _, bad := range []string{"", "Pages: x\n", "Pages: -1\n", "Pages: 99999999999\n"} {
		if _, err := parsePDFInfo(bad); err == nil {
			t.Errorf("parsePDFInfo(%q) succeeded", bad)
		}
	}
}

func TestErrorMessage(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{permanent("Broken."), "Broken."},
		{fmt.Errorf("wrapped: %w", permanent("Broken.")), "Broken."},
		{&ToolError{Tool: "pdfinfo", ExitCode: 2, Stderr: "secret"}, "pdfinfo failed with exit status 2."},
		{&ToolError{Tool: "pdfinfo", ExitCode: -1, Err: fmt.Errorf("x: %w", errNotFoundForTest)}, "pdfinfo is not installed."},
		{&ToolError{Tool: "pdfinfo", ExitCode: -1}, "pdfinfo failed."},
		{errors.New("open /data/documents/ab/cd/x: no such file"), "Processing failed; the server log has the details."},
	} {
		if got := errorMessage(tc.err); got != tc.want {
			t.Errorf("errorMessage(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestCheckLanguages(t *testing.T) {
	list := "List of available languages in \"/usr/share/tesseract-ocr/5/tessdata/\" (3):\ndeu\neng\nosd\n"
	f := &fakeRunner{handle: func(_ context.Context, c Command) ([]byte, error) {
		if c.Name != "tesseract" || !slices.Equal(c.Args, []string{"--list-langs"}) {
			return nil, fmt.Errorf("unexpected %+v", c)
		}
		return []byte(list), nil
	}}
	ctx := context.Background()
	if err := CheckLanguages(ctx, f, t.TempDir(), "deu+eng"); err != nil {
		t.Fatal(err)
	}
	err := CheckLanguages(ctx, f, t.TempDir(), "deu+fra+chi_sim")
	if err == nil || !strings.Contains(err.Error(), "fra, chi_sim") || !strings.Contains(err.Error(), "deu, eng, osd") {
		t.Fatalf("missing languages: %v", err)
	}
	broken := &fakeRunner{handle: func(context.Context, Command) ([]byte, error) {
		return nil, &ToolError{Tool: "tesseract", ExitCode: -1, Err: errNotFoundForTest}
	}}
	if err := CheckLanguages(ctx, broken, t.TempDir(), "deu"); err == nil {
		t.Fatal("no error without tesseract")
	}
}

func TestPipelineTextTooLong(t *testing.T) {
	for size, wantErr := range map[int]bool{MaxTextBytes: false, MaxTextBytes + 1: true} {
		dir := t.TempDir()
		path := filepath.Join(dir, "original")
		if err := os.WriteFile(path, []byte("%PDF-1.4"), 0o600); err != nil {
			t.Fatal(err)
		}
		f := &fakeRunner{handle: func(ctx context.Context, c Command) ([]byte, error) {
			if c.Name == "pdftotext" {
				return nil, os.WriteFile(filepath.Join(c.Dir, c.Args[len(c.Args)-1]), bytes.Repeat([]byte("a"), size), 0o600)
			}
			return poppler(pdf{pages: []string{longText}})(ctx, c)
		}}
		res, err := (&Pipeline{Runner: f}).Run(context.Background(), Input{Dir: dir, Path: path, MIME: mimePDF})
		var pe *PermanentError
		if wantErr {
			if !errors.As(err, &pe) || pe.Msg != msgTextTooLong {
				t.Fatalf("%d bytes of text: %v", size, err)
			}
		} else if err != nil || len(res.Text) != size {
			t.Fatalf("%d bytes of text: %d, %v", size, len(res.Text), err)
		}
	}
}

// runThumbnail runs Thumbnail with the fake tools on a PDF in a fresh work
// directory.
func runThumbnail(t *testing.T, p pdf) (string, *fakeRunner, string, error) {
	t.Helper()
	dir := t.TempDir()
	in := filepath.Join(dir, "archive.pdf")
	if err := os.WriteFile(in, []byte("%PDF-1.4"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{handle: poppler(p)}
	path, err := (&Pipeline{Runner: f}).Thumbnail(context.Background(), dir, in)
	return path, f, dir, err
}

func TestThumbnail(t *testing.T) {
	path, f, dir, err := runThumbnail(t, pdf{})
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "thumbnail.jpg") {
		t.Fatalf("path %s", path)
	}
	c := call(t, f, "pdftoppm")
	want := []string{"-f", "1", "-l", "1", "-singlefile", "-scale-to", "512", "-jpeg", "-jpegopt", "quality=80", "archive.pdf", "thumbnail"}
	if !slices.Equal(c.Args, want) || c.Dir != dir {
		t.Fatalf("pdftoppm %q in %s, want %q", c.Args, c.Dir, want)
	}
}

func TestThumbnailRefused(t *testing.T) {
	for name, p := range map[string]pdf{
		"tool error": {thumbErr: &ToolError{Tool: "pdftoppm", ExitCode: 1}},
		"not a JPEG": {thumb: []byte("%PDF-1.4 not an image")},
		"too wide":   {thumb: jpegOf(ThumbnailSize+1, 10)},
		"too tall":   {thumb: jpegOf(10, ThumbnailSize+1)},
		"too large":  {thumb: append(jpegOf(10, 10), make([]byte, MaxThumbnailBytes)...)},
	} {
		if path, _, _, err := runThumbnail(t, p); err == nil || path != "" {
			t.Errorf("%s: %q, %v", name, path, err)
		}
	}
	// At the limits it is fine.
	if _, _, _, err := runThumbnail(t, pdf{thumb: jpegOf(ThumbnailSize, ThumbnailSize)}); err != nil {
		t.Fatalf("512×512: %v", err)
	}
}
