package process

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// errNotOCRable is a PDF that OCRmyPDF refuses although poppler reads it.
var errNotOCRable = errors.New("OCRmyPDF refused the PDF")

// ocr runs OCRmyPDF on the PDF at path for the given (0-based) pages and
// returns the path of its output, a plain PDF (no PDF/A) with an invisible
// text layer on those pages; the other pages stay as they are.
//
// Pages without any text get --skip-text. When one of the pages already
// has a little text (a scanner's header, a page number), --redo-ocr runs
// instead: --skip-text would skip such a page, while --redo-ocr keeps its
// visible text and OCRs the rest of it.
//
// OCRmyPDF's exit status 2 (bad input: a digital signature, a PDF it cannot
// parse), 6 (it found earlier OCR it will not replace) and 8 (encryption)
// give errNotOCRable: the PDF opened, so it ends without OCR. Every other
// failure may pass and is retried.
func (p *Pipeline) ocr(ctx context.Context, dir, path string, pages []int, redo bool) (string, error) {
	mode := "--skip-text"
	if redo {
		mode = "--redo-ocr"
	}
	langs := p.Languages
	if langs == "" {
		langs = DefaultLanguages
	}
	_, err := p.Runner.Run(ctx, Command{
		Name: "ocrmypdf",
		Args: []string{
			mode,
			"--pages", pageRanges(pages),
			"--rotate-pages",
			"-l", langs,
			"--output-type", "pdf",
			"--optimize", "1",
			"--jobs", strconv.Itoa(max(p.Jobs, 1)),
			filepath.Base(path), archiveFile,
		},
		Dir: dir,
		// OCRmyPDF runs --jobs Tesseracts at once; each gets one thread.
		Env: []string{"OMP_THREAD_LIMIT=1"},
	})
	var te *ToolError
	if errors.As(err, &te) {
		switch te.ExitCode {
		case 2, 6, 8:
			return "", fmt.Errorf("%w: %w", errNotOCRable, err)
		}
	}
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, archiveFile), nil
}

// pageRanges writes 0-based, ascending pages as OCRmyPDF's 1-based page
// list, with runs as ranges: 0,1,2,4 gives "1-3,5".
func pageRanges(pages []int) string {
	var b strings.Builder
	for i := 0; i < len(pages); {
		j := i
		for j+1 < len(pages) && pages[j+1] == pages[j]+1 {
			j++
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(pages[i] + 1))
		if j > i {
			b.WriteString("-" + strconv.Itoa(pages[j]+1))
		}
		i = j + 1
	}
	return b.String()
}

// hasSomeText reports whether one of the given pages of text has any
// character other than white space.
func hasSomeText(text string, pages []int) bool {
	chars := pageChars(text, maxPage(pages)+1)
	for _, i := range pages {
		if chars[i] > 0 {
			return true
		}
	}
	return false
}

// gainedText reports whether after has more characters than before on the
// given pages: OCR found text there.
func gainedText(before, after string, pages []int) bool {
	n := maxPage(pages) + 1
	b, a := pageChars(before, n), pageChars(after, n)
	gained := 0
	for _, i := range pages {
		gained += a[i] - b[i]
	}
	return gained > 0
}

func maxPage(pages []int) int {
	m := -1
	for _, i := range pages {
		m = max(m, i)
	}
	return m
}
