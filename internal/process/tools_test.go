//go:build tools

// Tests with the real tools, run in the toolstest stage of the Dockerfile
// (docker build --target toolstest), as UID 65532:
//
//	go test -tags tools -run '^TestTools' ./internal/process
//
// Every document is generated here: PDFs in Go, scans rendered from them
// with pdftoppm, HEIC with heif-enc; pikepdf (installed with ocrmypdf)
// encrypts.

package process

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/pklnx/paperless-go/internal/store"
)

// makePDF writes a PDF with one A4 page per entry of pages, each showing its
// text (ASCII) in Helvetica; an empty entry gives a blank page.
func makePDF(t *testing.T, path string, pages ...string) {
	t.Helper()
	var objs []string
	font := 3 + 2*len(pages)
	kids := make([]string, len(pages))
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 3+2*i)
	}
	objs = append(objs,
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages)))
	for i, text := range pages {
		content := ""
		if text != "" {
			esc := strings.NewReplacer(`\`, `\\`, "(", `\(`, ")", `\)`).Replace(text)
			content = fmt.Sprintf("BT /F1 24 Tf 72 720 Td (%s) Tj ET", esc)
		}
		objs = append(objs,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>", font, 4+2*i),
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	}
	objs = append(objs, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")

	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// encrypt encrypts the PDF in with pikepdf: user is the password needed to
// open it ("" opens without one), owner the one needed to change it;
// allowChanges sets the permission to change it.
func encrypt(t *testing.T, in, out, user, owner string, allowChanges bool) {
	t.Helper()
	script := `import sys, pikepdf
pdf = pikepdf.open(sys.argv[1])
allow = pikepdf.Permissions()
if sys.argv[5] != "yes":
    allow = pikepdf.Permissions(modify_other=False, modify_assembly=False, modify_form=False, modify_annotation=False)
pdf.save(sys.argv[2], encryption=pikepdf.Encryption(user=sys.argv[3], owner=sys.argv[4], R=6, allow=allow))
`
	changes := "no"
	if allowChanges {
		changes = "yes"
	}
	cmd := exec.Command("python3", "-I", "-c", script, in, out, user, owner, changes)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("encrypt: %v\n%s", err, b)
	}
}

func runTools(t *testing.T, write func(path string)) (Result, error) {
	t.Helper()
	return runToolsOn(t, mimePDF, write)
}

// runToolsOn runs the pipeline with the real tools on an original of the
// given type, written by write.
func runToolsOn(t *testing.T, mime string, write func(path string)) (Result, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "original")
	write(path)
	return (&Pipeline{Runner: &ExecRunner{}, Languages: "deu+eng", Jobs: 2}).Run(context.Background(), Input{Dir: dir, Path: path, MIME: mime})
}

const toolsText = "Rechnung 4711 vom 1. Oktober 2026"

func TestToolsTextPDFKeepsOriginal(t *testing.T) {
	res, err := runTools(t, func(p string) { makePDF(t, p, toolsText, "Seite 2 "+toolsText) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Archive != "" || res.PageCount != 2 || res.TextSource != store.TextSourcePDF || res.Note != "" {
		t.Fatalf("result %+v", res)
	}
	pages := strings.Split(res.Text, "\f")
	if len(pages) != 2 || !strings.Contains(pages[0], "Rechnung 4711") || !strings.Contains(pages[1], "Seite 2") {
		t.Fatalf("text %q", res.Text)
	}
}

func TestToolsBlankPage(t *testing.T) {
	// A text page and a blank one: OCR runs on the blank page, finds
	// nothing, and the original stays the archive.
	res, err := runTools(t, func(p string) { makePDF(t, p, toolsText, "") })
	if err != nil {
		t.Fatal(err)
	}
	if res.Archive != "" || res.PageCount != 2 || res.TextSource != store.TextSourcePDF || res.Note != "" ||
		!strings.Contains(res.Text, "Rechnung 4711") {
		t.Fatalf("result %+v", res)
	}
}

func TestToolsDamagedPDF(t *testing.T) {
	_, err := runTools(t, func(p string) {
		if err := os.WriteFile(p, []byte("%PDF-1.4\nnot really a PDF"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	var pe *PermanentError
	if !errors.As(err, &pe) || pe.Msg != msgUnreadablePDF {
		t.Fatalf("damaged PDF: %v", err)
	}
}

func TestToolsUserPassword(t *testing.T) {
	// A PDF that needs a password to open fails at once, without a retry.
	_, err := runTools(t, func(p string) {
		plain := p + ".plain"
		makePDF(t, plain, toolsText)
		encrypt(t, plain, p, "open-sesame", "owner-secret", true)
	})
	var pe *PermanentError
	if !errors.As(err, &pe) || pe.Msg != msgUnreadablePDF {
		t.Fatalf("PDF with a user password: %v", err)
	}
}

func TestToolsOwnerPassword(t *testing.T) {
	// It opens without a password but may not be changed: done, with the
	// original as archive and a note, although a page has no text.
	res, err := runTools(t, func(p string) {
		plain := p + ".plain"
		makePDF(t, plain, toolsText, "")
		encrypt(t, plain, p, "", "owner-secret", false)
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Archive != "" || res.PageCount != 2 || res.Note != msgNoChanges || !strings.Contains(res.Text, "Rechnung 4711") {
		t.Fatalf("result %+v", res)
	}
}

func TestToolsCancel(t *testing.T) {
	// A real tool, stopped through its process group.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var pid int
	r := &ExecRunner{started: func(c *exec.Cmd) {
		pid = c.Process.Pid
		cancel()
	}}
	_, err := r.Run(ctx, Command{Name: "sleep", Args: []string{"600"}, Dir: t.TempDir()})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v", err)
	}
	if err := syscall.Kill(-pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("process group %d still exists: %v", pid, err)
	}
}

func TestToolsLanguages(t *testing.T) {
	if err := CheckLanguages(context.Background(), &ExecRunner{}, t.TempDir(), "deu+eng+osd"); err != nil {
		t.Fatal(err)
	}
}

// scanDPI is the resolution of the rendered scans.
const scanDPI = 200

// renderScan renders an A4 page showing text (makePDF) to a gray image at
// scanDPI with pdftoppm: the pixels of a scan, without a text layer.
func renderScan(t *testing.T, text string) *image.Gray {
	t.Helper()
	dir := t.TempDir()
	in := filepath.Join(dir, "page.pdf")
	makePDF(t, in, text)
	out := filepath.Join(dir, "page")
	cmd := exec.Command("pdftoppm", "-r", fmt.Sprint(scanDPI), "-gray", "-png", "-singlefile", in, out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pdftoppm: %v\n%s", err, b)
	}
	f, err := os.Open(out + ".png")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	g := image.NewGray(img.Bounds())
	draw.Draw(g, g.Bounds(), img, img.Bounds().Min, draw.Src)
	return g
}

// makeScanPDF writes a one-page PDF that shows img at scanDPI as its only
// content, as a scanner writes it, plus overlay as real text at the bottom
// of the page unless it is empty.
func makeScanPDF(t *testing.T, path string, img *image.Gray, overlay string) {
	t.Helper()
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	if _, err := zw.Write(img.Pix); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	pw, ph := float64(w)*72/scanDPI, float64(h)*72/scanDPI
	content := fmt.Sprintf("q %.2f 0 0 %.2f 0 0 cm /Im0 Do Q", pw, ph)
	if overlay != "" {
		content += fmt.Sprintf(" BT /F1 14 Tf 40 30 Td (%s) Tj ET", overlay)
	}
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Resources << /XObject << /Im0 5 0 R >> /Font << /F1 6 0 R >> >> /Contents 4 0 R >>", pw, ph),
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", w, h, z.Len(), z.String()),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// pageWidth returns the width in points of the first page of the PDF at
// path, from pdfinfo.
func pageWidth(t *testing.T, path string) float64 {
	t.Helper()
	out, err := exec.Command("pdfinfo", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(line, "Page size:"); ok {
			var w float64
			if _, err := fmt.Sscanf(strings.TrimSpace(v), "%g", &w); err == nil {
				return w
			}
		}
	}
	t.Fatalf("no page size in %s", out)
	return 0
}

// wantOCR checks that res is OCRed text from a scan of toolsText.
func wantOCR(t *testing.T, res Result, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if res.TextSource != store.TextSourceTesseract || res.Archive == "" || res.PageCount != 1 || res.Note != "" {
		t.Fatalf("result %+v", res)
	}
	if !strings.Contains(res.Text, "Rechnung") || !strings.Contains(res.Text, "4711") {
		t.Fatalf("OCR text %q", res.Text)
	}
	// The archive is a plain PDF that poppler reads, with the text layer.
	out, err := exec.Command("pdftotext", res.Archive, "-").Output()
	if err != nil || !strings.Contains(string(out), "4711") {
		t.Fatalf("archive text %q, %v", out, err)
	}
}

func TestToolsScanGetsOCR(t *testing.T) {
	scan := renderScan(t, toolsText)
	res, err := runTools(t, func(p string) { makeScanPDF(t, p, scan, "") })
	wantOCR(t, res, err)
}

func TestToolsScanWithALittleText(t *testing.T) {
	// The page has a short real text (under MinPageChars) besides the
	// scan: --redo-ocr keeps it and OCRs the image.
	scan := renderScan(t, toolsText)
	res, err := runTools(t, func(p string) { makeScanPDF(t, p, scan, "Seite 1") })
	wantOCR(t, res, err)
	if !strings.Contains(res.Text, "Seite 1") {
		t.Fatalf("the page's own text is gone: %q", res.Text)
	}
}

func TestToolsEmptyUserPassword(t *testing.T) {
	// It opens without a password and allows changes: OCRmyPDF reads it,
	// and the archive is not encrypted.
	scan := renderScan(t, toolsText)
	res, err := runTools(t, func(p string) {
		plain := p + ".plain"
		makeScanPDF(t, plain, scan, "")
		encrypt(t, plain, p, "", "owner-secret", true)
	})
	wantOCR(t, res, err)
	out, err := exec.Command("pdfinfo", res.Archive).Output()
	if err != nil || !strings.Contains(string(out), "Encrypted:       no") {
		t.Fatalf("archive: %s, %v", out, err)
	}
}

func TestToolsSignedPDF(t *testing.T) {
	// OCRmyPDF refuses a signed PDF (it would break the signature): done,
	// with the original and a note.
	scan := renderScan(t, toolsText)
	res, err := runTools(t, func(p string) {
		plain := p + ".plain"
		makeScanPDF(t, plain, scan, "")
		script := `import sys, pikepdf
pdf = pikepdf.open(sys.argv[1])
pdf.Root.AcroForm = pikepdf.Dictionary(SigFlags=3, Fields=pikepdf.Array())
pdf.save(sys.argv[2])
`
		if b, err := exec.Command("python3", "-I", "-c", script, plain, p).CombinedOutput(); err != nil {
			t.Fatalf("sign: %v\n%s", err, b)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Archive != "" || res.Note != msgNotOCRable || res.TextSource != "" {
		t.Fatalf("result %+v", res)
	}
}

func TestToolsJPEG(t *testing.T) {
	// The JPEG says scanDPI (JFIF): the page has the paper's size.
	scan := renderScan(t, toolsText)
	res, err := runToolsOn(t, mimeJPEG, func(p string) {
		var b bytes.Buffer
		if err := jpeg.Encode(&b, scan, &jpeg.Options{Quality: 85}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, jpegWith(b.Bytes(), jfif(1, scanDPI)), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	wantOCR(t, res, err)
	if w := pageWidth(t, res.Archive); w < 590 || w > 600 {
		t.Fatalf("page width %g pt, want A4 (595)", w)
	}
}

func TestToolsPNG(t *testing.T) {
	// Without a resolution the image gets defaultImageDPI.
	scan := renderScan(t, toolsText)
	res, err := runToolsOn(t, mimePNG, func(p string) {
		var b bytes.Buffer
		if err := png.Encode(&b, scan); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	wantOCR(t, res, err)
	want := float64(scan.Bounds().Dx()) * 72 / defaultImageDPI
	if w := pageWidth(t, res.Archive); w < want-2 || w > want+2 {
		t.Fatalf("page width %g pt, want %g", w, want)
	}
}

func TestToolsHEIC(t *testing.T) {
	// The HEIC is made with heif-enc (x265, only in the toolstest stage).
	scan := renderScan(t, toolsText)
	res, err := runToolsOn(t, mimeHEIC, func(p string) {
		src := p + ".png"
		var b bytes.Buffer
		if err := png.Encode(&b, scan); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(src, b.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("heif-enc", "-q", "85", "-o", p, src).CombinedOutput(); err != nil {
			t.Fatalf("heif-enc: %v\n%s", err, out)
		}
		if err := os.Remove(src); err != nil {
			t.Fatal(err)
		}
	})
	wantOCR(t, res, err)
}

func TestToolsDamagedImage(t *testing.T) {
	// The header is fine (the pixel check passes), the data is not.
	_, err := runToolsOn(t, mimePNG, func(p string) {
		b := pngHeader(100, 100)
		if err := os.WriteFile(p, b[:len(b)-12], 0o600); err != nil {
			t.Fatal(err)
		}
	})
	var pe *PermanentError
	if !errors.As(err, &pe) || pe.Msg != msgUnreadableImage {
		t.Fatalf("damaged PNG: %v", err)
	}
}
