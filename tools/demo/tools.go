package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pklnx/paperless-go/internal/process"
)

// fakeTools stands in for the OCR tools (process.Runner): the demo needs
// no poppler, Tesseract or OCRmyPDF and makes no network calls. It knows
// the commands the pipeline runs for a PDF with a text layer on every
// page, and reads only the PDFs makePDF writes:
//
//   - tesseract --list-langs: deu and eng, so process.CheckLanguages passes;
//   - pdfinfo: the page count, not encrypted;
//   - pdftotext: the shown strings, one line each, pages ended by form feeds;
//   - pdftoppm: a grey JPEG thumbnail of 362×512 pixels.
//
// Any other tool (ocrmypdf, img2pdf, heif-dec), and a file it cannot read,
// exits with status 1: the pipeline then fails the document for good
// ("The PDF cannot be read"), which gives the demo a Failed document.
type fakeTools struct{}

// Thumbnail size: the shape of an A4 page with its longer side at
// process.ThumbnailSize.
const (
	thumbWidth  = 362
	thumbHeight = process.ThumbnailSize
)

// Run implements process.Runner.
func (fakeTools) Run(ctx context.Context, c process.Command) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", c.Name, err)
	}
	fail := func(msg string) ([]byte, error) {
		return nil, &process.ToolError{Tool: c.Name, ExitCode: 1, Stderr: msg}
	}
	pages := func(name string) ([][]string, error) {
		b, err := os.ReadFile(filepath.Join(c.Dir, filepath.Base(name))) //nolint:gosec // a file in the job's work directory
		if err != nil {
			return nil, err
		}
		return pdfPages(b)
	}
	switch {
	case c.Name == "tesseract" && slices.Equal(c.Args, []string{"--list-langs"}):
		return []byte("List of available languages in \"/demo/tessdata/\" (2):\ndeu\neng\n"), nil
	case c.Name == "pdfinfo" && len(c.Args) == 1:
		p, err := pages(c.Args[0])
		if err != nil {
			return fail(err.Error())
		}
		return fmt.Appendf(nil, "Pages:           %d\nEncrypted:       no\n", len(p)), nil
	case c.Name == "pdftotext" && len(c.Args) == 4 && c.Args[0] == "-enc" && c.Args[1] == "UTF-8":
		p, err := pages(c.Args[2])
		if err != nil {
			return fail(err.Error())
		}
		var text strings.Builder
		for _, lines := range p {
			text.WriteString(strings.Join(lines, "\n"))
			text.WriteString("\n\f")
		}
		if err := os.WriteFile(filepath.Join(c.Dir, filepath.Base(c.Args[3])), []byte(text.String()), 0o600); err != nil {
			return nil, err
		}
		return nil, nil
	case c.Name == "pdftoppm" && len(c.Args) >= 2:
		if _, err := pages(c.Args[len(c.Args)-2]); err != nil {
			return fail(err.Error())
		}
		img := image.NewGray(image.Rect(0, 0, thumbWidth, thumbHeight))
		for i := range img.Pix {
			img.Pix[i] = 0xe8
		}
		// A dark band where the first line of text is.
		for y := 40; y < 52; y++ {
			for x := 40; x < 260; x++ {
				img.SetGray(x, y, color.Gray{Y: 0x40})
			}
		}
		var b bytes.Buffer
		if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 80}); err != nil {
			return nil, err
		}
		stem := filepath.Base(c.Args[len(c.Args)-1])
		if err := os.WriteFile(filepath.Join(c.Dir, stem+".jpg"), b.Bytes(), 0o600); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return fail("not available in the demo")
}
