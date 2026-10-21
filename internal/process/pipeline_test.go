package process

import (
	"context"
	"errors"
	"fmt"
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

// pdf is what the fake poppler tools report about a PDF.
type pdf struct {
	pages     []string // the text of each page
	encrypted bool
	info      error // pdfinfo's error
	text      error // pdftotext's error
}

// poppler answers pdfinfo and pdftotext as the real tools would for p.
func poppler(p pdf) func(context.Context, Command) ([]byte, error) {
	return func(_ context.Context, c Command) ([]byte, error) {
		switch c.Name {
		case "pdfinfo":
			if p.info != nil {
				return nil, p.info
			}
			enc := "no"
			if p.encrypted {
				enc = "yes (print:yes copy:yes change:no addNotes:no algorithm:AES-256)"
			}
			return fmt.Appendf(nil, "Title:           a title\nPages:           %d\nEncrypted:       %s\nPage size:       595 x 842 pts (A4)\n", len(p.pages), enc), nil
		case "pdftotext":
			if p.text != nil {
				return nil, p.text
			}
			var b strings.Builder
			for _, page := range p.pages {
				b.WriteString(page + "\f")
			}
			return nil, os.WriteFile(filepath.Join(c.Dir, c.Args[len(c.Args)-1]), []byte(b.String()), 0o600)
		}
		return nil, fmt.Errorf("unexpected tool %s", c.Name)
	}
}

func runPipeline(t *testing.T, p pdf, mime string) (Result, error, *fakeRunner) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "original")
	if err := os.WriteFile(path, []byte("%PDF-1.4"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{handle: poppler(p)}
	res, err := (&Pipeline{Runner: f}).Run(context.Background(), Input{Dir: dir, Path: path, MIME: mime})
	return res, err, f
}

const longText = "Rechnung Nr. 4711 vom 1. Oktober"

func TestPipelineTextOnEveryPage(t *testing.T) {
	res, err, f := runPipeline(t, pdf{pages: []string{longText, "Seite 2: " + longText}}, "application/pdf")
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
	_, _, f := runPipeline(t, pdf{pages: []string{longText}}, "application/pdf")
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

func TestPipelineNeedsOCR(t *testing.T) {
	// One page without text is enough, even if the others have some; a
	// page with a little text (a scanner's header) needs OCR too.
	for _, pages := range [][]string{{longText, ""}, {"Page 1 of 2", longText}} {
		_, err, _ := runPipeline(t, pdf{pages: pages}, "application/pdf")
		var pe *PermanentError
		if !errors.As(err, &pe) || pe.Msg != msgOCRLater {
			t.Errorf("pages %q: %v", pages, err)
		}
	}
}

func TestPipelineOwnerPassword(t *testing.T) {
	// Opens without a password but may not be changed: no OCR, the
	// original stays the archive, with a note.
	res, err, _ := runPipeline(t, pdf{pages: []string{longText, ""}, encrypted: true}, "application/pdf")
	if err != nil {
		t.Fatal(err)
	}
	if res.Archive != "" || res.Note != msgOwnerPassword || res.PageCount != 2 || res.Text != longText {
		t.Fatalf("result %+v", res)
	}
	// With text on every page it is an ordinary PDF.
	res, err, _ = runPipeline(t, pdf{pages: []string{longText}, encrypted: true}, "application/pdf")
	if err != nil || res.Note != "" || res.TextSource != store.TextSourcePDF {
		t.Fatalf("encrypted PDF with text: %+v, %v", res, err)
	}
}

func TestPipelineCopyForbidden(t *testing.T) {
	res, err, _ := runPipeline(t, pdf{pages: []string{""}, text: &ToolError{Tool: "pdftotext", ExitCode: 3}}, "application/pdf")
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
		_, err, _ := runPipeline(t, p, "application/pdf")
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
	_, err, _ := runPipeline(t, pdf{info: &ToolError{Tool: "pdfinfo", ExitCode: 99, Stderr: "secret"}}, "application/pdf")
	var pe *PermanentError
	if err == nil || errors.As(err, &pe) {
		t.Fatalf("exit status 99: %v", err)
	}
	if msg := errorMessage(err); msg != "pdfinfo failed with exit status 99." {
		t.Fatalf("message %q", msg)
	}
}

func TestPipelineOtherTypes(t *testing.T) {
	for mime, want := range map[string]string{
		"image/jpeg": msgImagesLater, "image/png": msgImagesLater, "image/heic": msgImagesLater,
		"image/heif": msgImagesLater, "text/plain": msgUnsupported, "": msgUnsupported,
	} {
		_, err, f := runPipeline(t, pdf{}, mime)
		var pe *PermanentError
		if !errors.As(err, &pe) || pe.Msg != want {
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
