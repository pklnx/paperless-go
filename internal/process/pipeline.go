package process

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/pklnx/paperless-go/internal/store"
)

// MinPageChars is how many characters (not counting white space) a page
// needs in its text layer to count as having text. A page with fewer needs
// OCR.
const MinPageChars = 15

// MaxTextBytes bounds the text layer of one document. A PDF with more text
// (a decompression bomb, or a dump of data rather than a document) fails:
// the text would be read into memory and stored in one row.
const MaxTextBytes = 16 << 20

// MIME types of stored originals (internal/ingest detects them).
const (
	mimePDF  = "application/pdf"
	mimeJPEG = "image/jpeg"
	mimePNG  = "image/png"
	mimeHEIC = "image/heic"
	mimeHEIF = "image/heif"
)

// PermanentError is a failure that a retry cannot fix, such as a damaged
// PDF: the job fails at once. Msg is shown to users.
type PermanentError struct {
	Msg string
}

func (e *PermanentError) Error() string { return e.Msg }

func permanent(msg string) error { return &PermanentError{Msg: msg} }

// User-visible messages. They never contain tool output.
const (
	msgUnreadablePDF = "The PDF cannot be read: it is damaged, or it needs a password to open."
	msgNoPages       = "The PDF has no pages."
	msgTextTooLong   = "The PDF has more than 16 MiB of text, more than one document may have."
	msgUnsupported   = "Documents of this type cannot be processed."
	// msgNoChanges is the note on a PDF that opens without a password
	// but whose permissions forbid changes: OCR would have to change it.
	msgNoChanges = "The PDF's permissions do not allow changes, so pages without text were not OCRed."
	// msgNotOCRable is the note on a PDF that OCRmyPDF refuses although
	// it can be read, for example because it is digitally signed.
	msgNotOCRable = "OCR cannot change this PDF (it may be digitally signed), so pages without text were not OCRed."
	msgNoCopy     = "The PDF does not allow copying its text, so no text was extracted."
)

// Input is one document to process: its original file, already copied into
// the job's work directory Dir.
type Input struct {
	Dir  string
	Path string // the original, inside Dir
	MIME string
}

// Result is what the pipeline made of a document.
type Result struct {
	PageCount int32
	Text      string
	// TextSource is store.TextSourceTesseract when OCR added text,
	// store.TextSourcePDF for a text layer of the PDF alone, and "" without
	// text.
	TextSource string
	// Archive is the archive PDF inside the work directory; "" means the
	// original serves as the archive (a PDF with text on every page, or one
	// that OCR could not or may not change).
	Archive string
	Note    string
}

// Pipeline runs the tools for one document.
type Pipeline struct {
	Runner Runner
	// Languages are the Tesseract languages, joined with "+";
	// DefaultLanguages when empty.
	Languages string
	// Jobs is how many pages OCRmyPDF works on at once (at least 1).
	Jobs int
	// ThumbnailTimeout limits rendering the thumbnail;
	// DefaultThumbnailTimeout when 0. Only tests set it.
	ThumbnailTimeout time.Duration
}

// DefaultLanguages are the OCR languages without configuration.
const DefaultLanguages = "deu+eng"

// Work files of the pipeline, in the job's work directory.
const (
	textFile        = "text.txt"
	archiveFile     = "archive.pdf"
	archiveTextFile = "archive.txt"
)

// Run processes in. Errors that a retry cannot fix are *PermanentError.
//
// A PDF keeps its own text on the pages that have at least MinPageChars
// characters; the other pages are OCRed (OCRmyPDF with --pages), and the
// OCRed PDF becomes the archive when it has more text on those pages than
// before. An image is converted to a PDF first, which is its archive.
func (p *Pipeline) Run(ctx context.Context, in Input) (Result, error) {
	src, converted := in.Path, false
	switch in.MIME {
	case mimePDF:
	case mimeJPEG, mimePNG, mimeHEIC, mimeHEIF:
		pdf, err := p.imageToPDF(ctx, in)
		if err != nil {
			return Result{}, err
		}
		src, converted = pdf, true
	default:
		return Result{}, permanent(msgUnsupported)
	}
	info, err := p.pdfInfo(ctx, in.Dir, src)
	if err != nil {
		return Result{}, err
	}
	text, copyAllowed, err := p.pdfText(ctx, in.Dir, src, textFile)
	if err != nil {
		return Result{}, popplerError(err)
	}
	res := Result{PageCount: info.pages, Text: text}
	if strings.TrimSpace(text) != "" {
		res.TextSource = store.TextSourcePDF
	}
	if converted {
		res.Archive = src
	}
	if !copyAllowed {
		res.Note = msgNoCopy
		return res, nil
	}
	pages := PagesWithoutText(text, int(info.pages))
	if len(pages) == 0 {
		return res, nil
	}
	// OCRmyPDF opens a PDF with an empty user password itself and writes
	// the archive without encryption; the original stays as it is. A PDF
	// whose permissions forbid changes is left alone.
	if info.encrypted && !info.changeAllowed {
		res.Note = msgNoChanges
		return res, nil
	}
	archive, err := p.ocr(ctx, in.Dir, src, pages, hasSomeText(text, pages))
	if errors.Is(err, errNotOCRable) {
		res.Note = msgNotOCRable
		return res, nil
	}
	if err != nil {
		return Result{}, err
	}
	// OCRmyPDF's output that poppler cannot read is retried, not blamed
	// on the document.
	ocrText, ok, err := p.pdfText(ctx, in.Dir, archive, archiveTextFile)
	if err != nil {
		return Result{}, fmt.Errorf("read the OCR output: %w", err)
	}
	if !ok || !gainedText(text, ocrText, pages) {
		// Blank pages, or nothing OCR could read: the PDF stays as it was.
		return res, nil
	}
	res.Text, res.TextSource, res.Archive = ocrText, store.TextSourceTesseract, archive
	return res, nil
}

type pdfInfo struct {
	pages         int32
	encrypted     bool
	changeAllowed bool // the permissions of an encrypted PDF allow changes
}

// pdfInfo reads the page count and whether the PDF is encrypted. A PDF that
// needs a password to open fails with exit status 1, as a damaged one does.
func (p *Pipeline) pdfInfo(ctx context.Context, dir, path string) (pdfInfo, error) {
	out, err := p.Runner.Run(ctx, Command{Name: "pdfinfo", Args: []string{filepath.Base(path)}, Dir: dir})
	if err != nil {
		return pdfInfo{}, popplerError(err)
	}
	info, err := parsePDFInfo(string(out))
	if err != nil {
		return pdfInfo{}, err
	}
	return info, nil
}

// parsePDFInfo reads pdfinfo's output. The document's metadata (title,
// author and so on) comes before Pages and Encrypted and may contain line
// breaks, so the last line of each wins. An encrypted PDF's line lists its
// permissions: "yes (print:yes copy:yes change:no addNotes:no ...)".
func parsePDFInfo(out string) (pdfInfo, error) {
	var info pdfInfo
	pages := ""
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "Pages":
			pages = value
		case "Encrypted":
			info.encrypted = strings.HasPrefix(value, "yes")
			info.changeAllowed = false
			if info.encrypted {
				perms := strings.Fields(strings.Trim(strings.TrimPrefix(value, "yes"), " ()"))
				info.changeAllowed = slices.Contains(perms, "change:yes")
			}
		}
	}
	n, err := strconv.ParseInt(pages, 10, 32)
	if err != nil || n < 0 || n > math.MaxInt32 {
		return pdfInfo{}, permanent(msgUnreadablePDF)
	}
	if n == 0 {
		return pdfInfo{}, permanent(msgNoPages)
	}
	info.pages = int32(n)
	return info, nil
}

// pdfText extracts the text layer of path into the file out, pages
// separated by form feeds. A PDF whose permissions forbid copying text
// gives no text and copyAllowed false (pdftotext exits with status 3).
// Other tool errors come back as they are, for the caller to map.
func (p *Pipeline) pdfText(ctx context.Context, dir, path, out string) (text string, copyAllowed bool, err error) {
	_, err = p.Runner.Run(ctx, Command{
		Name: "pdftotext", Args: []string{"-enc", "UTF-8", filepath.Base(path), out}, Dir: dir,
	})
	var te *ToolError
	if errors.As(err, &te) && te.ExitCode == 3 {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	full := filepath.Join(dir, out)
	fi, err := os.Stat(full)
	if err != nil {
		return "", false, err
	}
	if fi.Size() > MaxTextBytes {
		return "", false, permanent(msgTextTooLong)
	}
	b, err := os.ReadFile(full) //nolint:gosec // in the job's work directory
	if err != nil {
		return "", false, err
	}
	return CleanText(string(b)), true, nil
}

// popplerError maps the exit status of pdfinfo and pdftotext: 1 means the
// PDF cannot be opened (damaged, or it needs a password); anything else may
// be passing, and is retried.
func popplerError(err error) error {
	var te *ToolError
	if errors.As(err, &te) && te.ExitCode == 1 {
		return fmt.Errorf("%w: %w", permanent(msgUnreadablePDF), err)
	}
	return err
}

// CleanText makes extracted text storable: it drops NUL bytes (PostgreSQL
// text cannot hold them) and replaces invalid UTF-8 with U+FFFD. Pages stay
// separated by form feeds; white space at the end goes.
func CleanText(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.ReplaceAll(s, "\x00", "")
	return strings.TrimRightFunc(s, unicode.IsSpace)
}

// PagesWithoutText returns the (0-based) pages of text, split at form
// feeds, that have fewer than MinPageChars characters other than white
// space. pages is the page count; pages missing from text count as
// without text.
func PagesWithoutText(text string, pages int) []int {
	var out []int
	for i, n := range pageChars(text, pages) {
		if n < MinPageChars {
			out = append(out, i)
		}
	}
	return out
}

// pageChars counts the characters other than white space on each of the
// first pages pages of text, split at form feeds.
func pageChars(text string, pages int) []int {
	parts := strings.Split(text, "\f")
	out := make([]int, pages)
	for i := range min(pages, len(parts)) {
		for _, r := range parts[i] {
			if !unicode.IsSpace(r) {
				out[i]++
			}
		}
	}
	return out
}

// errorMessage is the user-visible message for a failed attempt.
func errorMessage(err error) string {
	var pe *PermanentError
	if errors.As(err, &pe) {
		return pe.Msg
	}
	var te *ToolError
	if errors.As(err, &te) {
		if te.ExitCode >= 0 {
			return fmt.Sprintf("%s failed with exit status %d.", te.Tool, te.ExitCode)
		}
		if errors.Is(te.Err, exec.ErrNotFound) {
			return te.Tool + " is not installed."
		}
		return te.Tool + " failed."
	}
	return "Processing failed; the server log has the details."
}
