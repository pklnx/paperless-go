package process

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/pklnx/paperless-go/internal/store"
)

// MinPageChars is how many characters (not counting white space) a page
// needs in its text layer to count as having text. A page with fewer needs
// OCR.
const MinPageChars = 15

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
	msgUnsupported   = "Documents of this type cannot be processed."
	// Image conversion and OCR come with #5, part 3; until then such
	// documents fail and are queued again with reprocess --failed.
	msgImagesLater = "Images cannot be processed by this version yet; run reprocess --failed after the update."
	msgOCRLater    = "This PDF has pages without text, and this version cannot run OCR yet; run reprocess --failed after the update."
	// msgOwnerPassword is the note on a PDF that opens without a password
	// but forbids changes: OCR would have to change it.
	msgOwnerPassword = "The PDF is protected by an owner password, so pages without text were not OCRed."
	msgNoCopy        = "The PDF does not allow copying its text, so no text was extracted."
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
	PageCount  int32
	Text       string
	TextSource string // store.TextSourcePDF, or "" without text
	// Archive is the archive PDF inside the work directory; "" means the
	// original serves as the archive (a PDF with text on every page, or one
	// that may not be changed).
	Archive string
	Note    string
}

// Pipeline runs the tools for one document.
type Pipeline struct {
	Runner Runner
}

// Run processes in. Errors that a retry cannot fix are *PermanentError.
func (p *Pipeline) Run(ctx context.Context, in Input) (Result, error) {
	switch in.MIME {
	case mimePDF:
	case mimeJPEG, mimePNG, mimeHEIC, mimeHEIF:
		return Result{}, permanent(msgImagesLater)
	default:
		return Result{}, permanent(msgUnsupported)
	}
	info, err := p.pdfInfo(ctx, in)
	if err != nil {
		return Result{}, err
	}
	text, copyAllowed, err := p.pdfText(ctx, in)
	if err != nil {
		return Result{}, err
	}
	res := Result{PageCount: info.pages, Text: text}
	if strings.TrimSpace(text) != "" {
		res.TextSource = store.TextSourcePDF
	}
	if !copyAllowed {
		res.Note = msgNoCopy
		return res, nil
	}
	if len(PagesWithoutText(text, int(info.pages))) == 0 {
		return res, nil
	}
	if info.encrypted {
		res.Note = msgOwnerPassword
		return res, nil
	}
	return Result{}, permanent(msgOCRLater)
}

type pdfInfo struct {
	pages     int32
	encrypted bool
}

// pdfInfo reads the page count and whether the PDF is encrypted. A PDF that
// needs a password to open fails with exit status 1, as a damaged one does.
func (p *Pipeline) pdfInfo(ctx context.Context, in Input) (pdfInfo, error) {
	out, err := p.Runner.Run(ctx, Command{Name: "pdfinfo", Args: []string{filepath.Base(in.Path)}, Dir: in.Dir})
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
// breaks, so the last line of each wins.
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

// pdfText extracts the text layer, pages separated by form feeds. A PDF
// whose permissions forbid copying text gives no text and copyAllowed
// false (pdftotext exits with status 3).
func (p *Pipeline) pdfText(ctx context.Context, in Input) (text string, copyAllowed bool, err error) {
	const out = "text.txt"
	_, err = p.Runner.Run(ctx, Command{
		Name: "pdftotext", Args: []string{"-enc", "UTF-8", filepath.Base(in.Path), out}, Dir: in.Dir,
	})
	var te *ToolError
	if errors.As(err, &te) && te.ExitCode == 3 {
		return "", false, nil
	}
	if err != nil {
		return "", false, popplerError(err)
	}
	b, err := os.ReadFile(filepath.Join(in.Dir, out))
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
	parts := strings.Split(text, "\f")
	var out []int
	for i := range pages {
		n := 0
		if i < len(parts) {
			for _, r := range parts[i] {
				if !unicode.IsSpace(r) {
					n++
				}
			}
		}
		if n < MinPageChars {
			out = append(out, i)
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
