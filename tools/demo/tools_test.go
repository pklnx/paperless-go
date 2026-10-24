package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pklnx/paperless-go/internal/process"
	"github.com/pklnx/paperless-go/internal/store"
)

var update = flag.Bool("update", false, "rewrite testdata/sample.pdf")

// samplePages are the pages of testdata/sample.pdf; web/e2e/pdf.spec.ts
// writes the same pages with web/e2e/pdf.ts and expects the same bytes.
var samplePages = [][]string{
	{"Sample document (golden file)", "Grüße aus Köln: Kündigung", `A backslash \ and parentheses ()`},
	{"Page two of the sample", "Reference word: samplequartz"},
}

// recorder is fakeTools that notes the tools it was asked to run.
type recorder struct {
	mu    sync.Mutex
	names []string
}

func (r *recorder) Run(ctx context.Context, c process.Command) ([]byte, error) {
	r.mu.Lock()
	r.names = append(r.names, c.Name)
	r.mu.Unlock()
	return fakeTools{}.Run(ctx, c)
}

// writeOriginal puts b into a fresh work directory as the pipeline's input.
func writeOriginal(t *testing.T, b []byte) process.Input {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "original")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return process.Input{Dir: dir, Path: path, MIME: "application/pdf"}
}

func mustPDF(t *testing.T, pages [][]string) []byte {
	t.Helper()
	b, err := makePDF(pages)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPipelineReadsGeneratedPDF(t *testing.T) {
	rec := &recorder{}
	p := &process.Pipeline{Runner: rec, Languages: process.DefaultLanguages, Jobs: 1}
	in := writeOriginal(t, mustPDF(t, [][]string{
		{"Electricity bill", "Reference word: amberlight"},
		{"Second page with enough text", "Signed by both parties."},
	}))
	res, err := p.Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if res.PageCount != 2 {
		t.Errorf("page count %d, want 2", res.PageCount)
	}
	if res.TextSource != store.TextSourcePDF {
		t.Errorf("text source %q, want %q", res.TextSource, store.TextSourcePDF)
	}
	if res.Archive != "" {
		t.Errorf("archive %q, want none (the original serves)", res.Archive)
	}
	want := "Electricity bill\nReference word: amberlight\n\fSecond page with enough text\nSigned by both parties."
	if res.Text != want {
		t.Errorf("text %q, want %q", res.Text, want)
	}
	for _, n := range rec.names {
		if n == "ocrmypdf" {
			t.Errorf("OCRmyPDF ran on a PDF with text on every page: %v", rec.names)
		}
	}
}

func TestPipelineDecodesUmlauts(t *testing.T) {
	p := &process.Pipeline{Runner: fakeTools{}, Jobs: 1}
	in := writeOriginal(t, mustPDF(t, [][]string{{"Kündigung des Mietvertrags", "Mit freundlichen Grüßen, Köln"}}))
	res, err := p.Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Kündigung des Mietvertrags\nMit freundlichen Grüßen, Köln"; res.Text != want {
		t.Errorf("text %q, want %q", res.Text, want)
	}
}

func TestThumbnailIsAccepted(t *testing.T) {
	p := &process.Pipeline{Runner: fakeTools{}, Jobs: 1}
	in := writeOriginal(t, mustPDF(t, [][]string{{"A page with a thumbnail of its own"}}))
	path, err := p.Thumbnail(context.Background(), in.Dir, in.Path)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != in.Dir {
		t.Errorf("thumbnail at %s, want it in %s", path, in.Dir)
	}
}

func TestBrokenPDFFailsForGood(t *testing.T) {
	p := &process.Pipeline{Runner: fakeTools{}, Jobs: 1}
	for name, b := range map[string][]byte{
		"no content stream": []byte("%PDF-1.4\nthis is not a PDF\n%%EOF\n"),
		"no PDF header":     []byte("just text\n"),
		"cut off":           mustPDF(t, [][]string{{"A page that is cut off"}})[:200],
	} {
		t.Run(name, func(t *testing.T) {
			_, err := p.Run(context.Background(), writeOriginal(t, b))
			var pe *process.PermanentError
			if !errors.As(err, &pe) {
				t.Fatalf("error %v, want a *process.PermanentError", err)
			}
		})
	}
}

func TestOtherToolsFail(t *testing.T) {
	for _, name := range []string{"ocrmypdf", "img2pdf", "heif-dec", "tesseract"} {
		_, err := fakeTools{}.Run(context.Background(), process.Command{Name: name, Args: []string{"x"}, Dir: t.TempDir()})
		var te *process.ToolError
		if !errors.As(err, &te) || te.ExitCode != 1 {
			t.Errorf("%s: error %v, want a *process.ToolError with exit status 1", name, err)
		}
	}
}

func TestLanguagesPass(t *testing.T) {
	if err := process.CheckLanguages(context.Background(), fakeTools{}, t.TempDir(), "deu+eng"); err != nil {
		t.Fatal(err)
	}
	if err := process.CheckLanguages(context.Background(), fakeTools{}, t.TempDir(), "fra"); err == nil {
		t.Error("fra passed, want it missing")
	}
}

func TestWriterRefusesOtherCharacters(t *testing.T) {
	for _, line := range []string{"Euro €", "tab\there", "日本"} {
		if _, err := makePDF([][]string{{line}}); err == nil {
			t.Errorf("%q was written, want an error", line)
		}
	}
	if _, err := makePDF(nil); err == nil {
		t.Error("a PDF without pages was written")
	}
}

func TestSampleIsGolden(t *testing.T) {
	got := mustPDF(t, samplePages)
	golden := filepath.Join("testdata", "sample.pdf")
	if *update {
		if err := os.WriteFile(golden, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("makePDF differs from %s (run go test -update after a deliberate change, and change web/e2e/pdf.ts the same way):\n%s", golden, got)
	}
	// The file reads back as written, escapes and umlauts included.
	pages, err := pdfPages(want)
	if err != nil {
		t.Fatal(err)
	}
	for i := range samplePages {
		if strings.Join(pages[i], "|") != strings.Join(samplePages[i], "|") {
			t.Errorf("page %d reads %q, want %q", i+1, pages[i], samplePages[i])
		}
	}
}
