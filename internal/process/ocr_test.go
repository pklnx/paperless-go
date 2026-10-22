package process

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pklnx/paperless-go/internal/ingest"
	"github.com/pklnx/paperless-go/internal/store"
)

// ocrArgs are OCRmyPDF's arguments for the given mode and pages, on in.
func ocrArgs(mode, pages, in string) []string {
	return []string{mode, "--pages", pages, "--rotate-pages", "-l", "deu+eng", "--output-type", "pdf",
		"--optimize", "1", "--jobs", "3", in, "archive.pdf"}
}

func call(t *testing.T, f *fakeRunner, name string) Command {
	t.Helper()
	for _, c := range f.calls {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("%s did not run: %v", name, f.names())
	return Command{}
}

func TestPipelineOCR(t *testing.T) {
	// The page without text is OCRed; the archive is OCRmyPDF's output.
	ocred := "Seite 2, gelesen von Tesseract"
	res, f, err := runPipeline(t, pdf{pages: []string{longText, ""}, ocr: []string{longText, ocred}}, mimePDF)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(res.Archive) != archiveFile || res.TextSource != store.TextSourceTesseract ||
		res.Text != longText+"\f"+ocred || res.PageCount != 2 || res.Note != "" {
		t.Fatalf("result %+v", res)
	}
	if got := f.names(); !slices.Equal(got, []string{"pdfinfo", "pdftotext", "ocrmypdf", "pdftotext"}) {
		t.Fatalf("tools: %v", got)
	}
	ocr := call(t, f, "ocrmypdf")
	if want := ocrArgs("--skip-text", "2", "original"); !slices.Equal(ocr.Args, want) {
		t.Errorf("ocrmypdf args %q, want %q", ocr.Args, want)
	}
	if !slices.Equal(ocr.Env, []string{"OMP_THREAD_LIMIT=1"}) {
		t.Errorf("ocrmypdf environment %q", ocr.Env)
	}
	if text := f.calls[3]; !slices.Equal(text.Args, []string{"-enc", "UTF-8", "archive.pdf", "archive.txt"}) {
		t.Errorf("pdftotext of the archive: %q", text.Args)
	}
}

func TestPipelineRedoOCR(t *testing.T) {
	// A page with a little text (a scanner's header) gets --redo-ocr:
	// --skip-text would skip it. Only the pages under the threshold are
	// OCRed.
	pages := []string{"Page 1 of 4", longText, "", ""}
	ocr := []string{"Page 1 of 4 " + longText, longText, longText, ""}
	res, f, err := runPipeline(t, pdf{pages: pages, ocr: ocr}, mimePDF)
	if err != nil {
		t.Fatal(err)
	}
	if res.TextSource != store.TextSourceTesseract || !strings.HasPrefix(res.Text, ocr[0]) {
		t.Fatalf("result %+v", res)
	}
	if got, want := call(t, f, "ocrmypdf").Args, ocrArgs("--redo-ocr", "1,3-4", "original"); !slices.Equal(got, want) {
		t.Errorf("ocrmypdf args %q, want %q", got, want)
	}
}

func TestPipelineOCRFindsNothing(t *testing.T) {
	// A blank back side, or a scan OCR cannot read: the PDF stays the
	// archive, with its own text and source.
	for _, tc := range []struct {
		pages, ocr []string
		source     string
	}{
		{[]string{longText, ""}, []string{longText, ""}, store.TextSourcePDF},
		{[]string{""}, []string{"  \n"}, ""},
		// OCR that loses the little text a page had is no gain.
		{[]string{"Page 1 of 1"}, []string{"Page"}, store.TextSourcePDF},
	} {
		res, f, err := runPipeline(t, pdf{pages: tc.pages, ocr: tc.ocr}, mimePDF)
		if err != nil {
			t.Fatal(err)
		}
		if res.Archive != "" || res.TextSource != tc.source || res.Text != CleanText(strings.Join(tc.pages, "\f")) || res.Note != "" {
			t.Errorf("pages %q: result %+v", tc.pages, res)
		}
		if !slices.Contains(f.names(), "ocrmypdf") {
			t.Errorf("pages %q: no OCR", tc.pages)
		}
	}
}

func TestPipelineOCRErrors(t *testing.T) {
	stderr := "DigitalSignatureError: /secret/path"
	// A PDF OCRmyPDF refuses ends done, with the original and a note.
	for _, code := range []int{2, 6, 8} {
		res, _, err := runPipeline(t, pdf{pages: []string{longText, ""}, ocrErr: &ToolError{Tool: "ocrmypdf", ExitCode: code, Stderr: stderr}}, mimePDF)
		if err != nil || res.Archive != "" || res.Note != msgNotOCRable || res.Text != longText || res.TextSource != store.TextSourcePDF {
			t.Errorf("exit status %d: %+v, %v", code, res, err)
		}
	}
	// Anything else is retried, with a message without tool output.
	for _, code := range []int{1, 3, 4, 5, 7, 15, -1} {
		_, _, err := runPipeline(t, pdf{pages: []string{""}, ocrErr: &ToolError{Tool: "ocrmypdf", ExitCode: code, Stderr: stderr}}, mimePDF)
		var pe *PermanentError
		if err == nil || errors.As(err, &pe) {
			t.Errorf("exit status %d: %v", code, err)
		}
		if msg := errorMessage(err); strings.Contains(msg, "secret") {
			t.Errorf("message shows tool output: %q", msg)
		}
	}
	// OCR output poppler cannot read is retried too.
	f := &fakeRunner{}
	f.handle = func(ctx context.Context, c Command) ([]byte, error) {
		if c.Name == "pdftotext" && c.Args[2] == archiveFile {
			return nil, &ToolError{Tool: "pdftotext", ExitCode: 1}
		}
		return poppler(pdf{pages: []string{""}, ocr: []string{longText}})(ctx, c)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "original"), []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := (&Pipeline{Runner: f}).Run(context.Background(), Input{Dir: dir, Path: filepath.Join(dir, "original"), MIME: mimePDF})
	var pe *PermanentError
	if err == nil || errors.As(err, &pe) {
		t.Fatalf("unreadable OCR output: %v", err)
	}
}

func TestPipelineEncrypted(t *testing.T) {
	// Opens without a password but forbids changes: no OCR, the original
	// stays the archive, with a note.
	res, f, err := runPipeline(t, pdf{pages: []string{longText, ""}, encrypted: true}, mimePDF)
	if err != nil {
		t.Fatal(err)
	}
	if res.Archive != "" || res.Note != msgNoChanges || res.PageCount != 2 || res.Text != longText {
		t.Fatalf("result %+v", res)
	}
	if slices.Contains(f.names(), "ocrmypdf") {
		t.Fatal("OCR ran on a PDF that forbids changes")
	}
	// An empty user password that allows changes: OCRmyPDF opens it, and
	// the archive is the OCRed PDF.
	res, _, err = runPipeline(t, pdf{
		pages: []string{longText, ""}, encrypted: true, perms: "print:yes copy:yes change:yes addNotes:yes",
		ocr: []string{longText, longText},
	}, mimePDF)
	if err != nil || res.Note != "" || res.TextSource != store.TextSourceTesseract || res.Archive == "" {
		t.Fatalf("encrypted PDF that allows changes: %+v, %v", res, err)
	}
	// With text on every page it is an ordinary PDF.
	res, _, err = runPipeline(t, pdf{pages: []string{longText}, encrypted: true}, mimePDF)
	if err != nil || res.Note != "" || res.TextSource != store.TextSourcePDF || res.Archive != "" {
		t.Fatalf("encrypted PDF with text: %+v, %v", res, err)
	}
}

func TestPageRanges(t *testing.T) {
	for _, tc := range []struct {
		pages []int
		want  string
	}{
		{[]int{0}, "1"},
		{[]int{0, 1, 2, 4}, "1-3,5"},
		{[]int{1, 3, 5}, "2,4,6"},
		{[]int{0, 1, 3, 4, 9}, "1-2,4-5,10"},
	} {
		if got := pageRanges(tc.pages); got != tc.want {
			t.Errorf("pageRanges(%v) = %q, want %q", tc.pages, got, tc.want)
		}
	}
}

// pngOf encodes a white w×h PNG, with a pHYs chunk of ppm pixels per metre
// unless ppm is 0.
func pngOf(t *testing.T, w, h int, ppm uint32) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	if ppm == 0 {
		return b.Bytes()
	}
	data := binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, ppm), ppm)
	data = append(data, 1)
	return insertPNGChunk(b.Bytes(), "pHYs", data)
}

// pngChunk is one PNG chunk with its CRC.
func pngChunk(typ string, data []byte) []byte {
	chunk := binary.BigEndian.AppendUint32(nil, uint32(len(data))) //nolint:gosec // small
	chunk = append(append(chunk, typ...), data...)
	return binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(chunk[4:]))
}

// insertPNGChunk inserts a chunk after IHDR.
func insertPNGChunk(p []byte, typ string, data []byte) []byte {
	const afterIHDR = 8 + 8 + 13 + 4
	return slices.Concat(p[:afterIHDR], pngChunk(typ, data), p[afterIHDR:])
}

// pngHeader is a PNG signature and IHDR for w×h: enough for DecodeConfig.
func pngHeader(w, h uint32) []byte {
	ihdr := binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, w), h)
	ihdr = append(ihdr, 8, 0, 0, 0, 0)
	return slices.Concat([]byte("\x89PNG\r\n\x1a\n"), pngChunk("IHDR", ihdr), pngChunk("IEND", nil))
}

// jpegWith puts APPn segments (marker, payload) right after the SOI of
// the JPEG j.
func jpegWith(j []byte, segs ...[]byte) []byte {
	out := []byte{0xFF, 0xD8}
	for _, s := range segs {
		out = append(out, s...)
	}
	return append(out, j[2:]...)
}

func segment(marker byte, payload []byte) []byte {
	b := []byte{0xFF, marker}
	b = binary.BigEndian.AppendUint16(b, uint16(len(payload)+2)) //nolint:gosec // small
	return append(b, payload...)
}

// jfif is a JFIF APP0 segment with the given unit (1 inch, 2 cm) and density.
func jfif(unit byte, density uint16) []byte {
	p := append([]byte("JFIF\x00\x01\x02"), unit)
	p = binary.BigEndian.AppendUint16(p, density)
	p = binary.BigEndian.AppendUint16(p, density)
	return segment(0xE0, append(p, 0, 0))
}

// exif is an EXIF APP1 segment whose IFD0 has XResolution num/den and,
// unless unit is 0, ResolutionUnit.
func exif(bo binary.AppendByteOrder, num, den uint32, unit uint16) []byte {
	t := []byte("II")
	if bo == binary.AppendByteOrder(binary.BigEndian) {
		t = []byte("MM")
	}
	t = bo.AppendUint16(t, 42)
	t = bo.AppendUint32(t, 8)
	n := uint16(1)
	if unit != 0 {
		n = 2
	}
	t = bo.AppendUint16(t, n)
	ratOff := uint32(8 + 2 + 12*int(n) + 4) //nolint:gosec // small
	t = bo.AppendUint16(t, 0x011A)
	t = bo.AppendUint16(t, 5)
	t = bo.AppendUint32(t, 1)
	t = bo.AppendUint32(t, ratOff)
	if unit != 0 {
		t = bo.AppendUint16(t, 0x0128)
		t = bo.AppendUint16(t, 3)
		t = bo.AppendUint32(t, 1)
		t = bo.AppendUint16(t, unit)
		t = bo.AppendUint16(t, 0)
	}
	t = bo.AppendUint32(t, 0) // no next IFD
	t = bo.AppendUint32(t, num)
	t = bo.AppendUint32(t, den)
	return segment(0xE1, append([]byte("Exif\x00\x00"), t...))
}

func TestImageDPI(t *testing.T) {
	j := jpegOf(20, 10)
	for _, tc := range []struct {
		name string
		mime string
		data []byte
		want int // 0: no usable resolution
	}{
		{"jpeg without", mimeJPEG, j, 0},
		{"jfif dpi", mimeJPEG, jpegWith(j, jfif(1, 200)), 200},
		{"jfif dpcm", mimeJPEG, jpegWith(j, jfif(2, 236)), 599},
		{"jfif aspect only", mimeJPEG, jpegWith(j, jfif(0, 300)), 0},
		{"jfif camera 72", mimeJPEG, jpegWith(j, jfif(1, 72)), 0},
		{"jfif absurd", mimeJPEG, jpegWith(j, jfif(1, 9000)), 0},
		{"exif little endian", mimeJPEG, jpegWith(j, exif(binary.LittleEndian, 600, 1, 2)), 600},
		{"exif big endian cm", mimeJPEG, jpegWith(j, exif(binary.BigEndian, 2362, 10, 3)), 600},
		{"exif without unit", mimeJPEG, jpegWith(j, exif(binary.BigEndian, 400, 1, 0)), 400},
		{"exif zero denominator", mimeJPEG, jpegWith(j, exif(binary.LittleEndian, 300, 0, 2)), 0},
		{"exif unknown unit", mimeJPEG, jpegWith(j, exif(binary.LittleEndian, 300, 1, 7)), 0},
		{"jfif before exif", mimeJPEG, jpegWith(j, jfif(1, 150), exif(binary.LittleEndian, 600, 1, 2)), 150},
		{"jfif without, exif", mimeJPEG, jpegWith(j, jfif(0, 1), exif(binary.LittleEndian, 600, 1, 2)), 600},
		{"truncated exif", mimeJPEG, jpegWith(j, segment(0xE1, []byte("Exif\x00\x00II*\x00\xff\xff\xff\x00"))), 0},
		{"png without", mimePNG, pngOf(t, 20, 10, 0), 0},
		{"png 300", mimePNG, pngOf(t, 20, 10, 11811), 300},
		{"png 600", mimePNG, pngOf(t, 20, 10, 23622), 600},
	} {
		path := filepath.Join(t.TempDir(), "img")
		if err := os.WriteFile(path, tc.data, 0o600); err != nil {
			t.Fatal(err)
		}
		got, ok := imageDPI(path, tc.mime)
		if (tc.want == 0 && ok) || (tc.want != 0 && (!ok || got != tc.want)) {
			t.Errorf("%s: %d, %v; want %d", tc.name, got, ok, tc.want)
		}
	}
}

func TestPipelineImages(t *testing.T) {
	ocred := []string{"Rechnung 4711 aus einem Foto"}
	for _, tc := range []struct {
		name    string
		mime    string
		data    []byte
		tools   []string
		img2pdf []string
	}{
		{"jpeg", mimeJPEG, jpegWith(jpegOf(40, 20), jfif(1, 200)),
			[]string{"img2pdf", "pdfinfo", "pdftotext", "ocrmypdf", "pdftotext"},
			[]string{"--imgsize", "200dpi", "--rotation=ifvalid", "-o", "image.pdf", "original"}},
		{"png", mimePNG, pngOf(t, 40, 20, 0),
			[]string{"img2pdf", "pdfinfo", "pdftotext", "ocrmypdf", "pdftotext"},
			[]string{"--imgsize", "300dpi", "--rotation=ifvalid", "-o", "image.pdf", "original"}},
		{"heic", mimeHEIC, heifHeader(4032, 3024),
			[]string{"heif-dec", "img2pdf", "pdfinfo", "pdftotext", "ocrmypdf", "pdftotext"},
			[]string{"--imgsize", "300dpi", "--rotation=ifvalid", "-o", "image.pdf", "decoded.jpg"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, f, err := runPipelineOn(t, pdf{pages: []string{""}, ocr: ocred}, tc.mime, tc.data)
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Base(res.Archive) != archiveFile || res.TextSource != store.TextSourceTesseract ||
				res.Text != ocred[0] || res.PageCount != 1 || res.Note != "" {
				t.Fatalf("result %+v", res)
			}
			if got := f.names(); !slices.Equal(got, tc.tools) {
				t.Fatalf("tools %v, want %v", got, tc.tools)
			}
			if got := call(t, f, "img2pdf").Args; !slices.Equal(got, tc.img2pdf) {
				t.Errorf("img2pdf args %q, want %q", got, tc.img2pdf)
			}
			if got, want := call(t, f, "ocrmypdf").Args, ocrArgs("--skip-text", "1", "image.pdf"); !slices.Equal(got, want) {
				t.Errorf("ocrmypdf args %q, want %q", got, want)
			}
			if tc.mime == mimeHEIC {
				if got := call(t, f, "heif-dec").Args; !slices.Equal(got, []string{"--quiet", "-q", "92", "original", "decoded.jpg"}) {
					t.Errorf("heif-dec args %q", got)
				}
			}
		})
	}
}

func TestPipelineImageWithoutText(t *testing.T) {
	// OCR finds nothing: the converted PDF is the archive, without text.
	res, _, err := runPipelineOn(t, pdf{pages: []string{""}, ocr: []string{""}}, mimePNG, pngOf(t, 40, 20, 0))
	if err != nil || filepath.Base(res.Archive) != imagePDF || res.TextSource != "" || res.Text != "" || res.PageCount != 1 {
		t.Fatalf("result %+v, %v", res, err)
	}
	// OCRmyPDF refuses it: the converted PDF, with a note.
	res, _, err = runPipelineOn(t, pdf{pages: []string{""}, ocrErr: &ToolError{Tool: "ocrmypdf", ExitCode: 2}}, mimePNG, pngOf(t, 40, 20, 0))
	if err != nil || filepath.Base(res.Archive) != imagePDF || res.Note != msgNotOCRable {
		t.Fatalf("result %+v, %v", res, err)
	}
}

// heifHeader is a HEIC header (ftyp and meta) whose primary image is w×h,
// as ingest.Detect reads it.
func heifHeader(w, h uint32) []byte {
	box := func(typ string, payload ...[]byte) []byte {
		body := bytes.Join(payload, nil)
		b := binary.BigEndian.AppendUint32(nil, uint32(8+len(body))) //nolint:gosec // small
		return append(append(b, typ...), body...)
	}
	full := func(typ string, payload ...[]byte) []byte {
		return box(typ, append([][]byte{{0, 0, 0, 0}}, payload...)...)
	}
	ispe := full("ispe", binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, w), h))
	ipma := binary.BigEndian.AppendUint32(nil, 1)
	ipma = append(ipma, 0, 1, 1, 0x81)
	meta := full("meta",
		full("hdlr", make([]byte, 4), []byte("pict"), make([]byte, 13)),
		full("pitm", []byte{0, 1}),
		box("iprp", box("ipco", ispe), full("ipma", ipma)))
	return slices.Concat(box("ftyp", []byte("heic\x00\x00\x00\x00mif1heic")), meta, box("mdat", []byte("image data")))
}

// jpegHeader is SOI and a baseline SOF0 segment for w×h, enough for
// DecodeConfig.
func jpegHeader(w, h uint16) []byte {
	b := []byte{0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x0B, 8}
	b = binary.BigEndian.AppendUint16(b, h)
	b = binary.BigEndian.AppendUint16(b, w)
	return append(b, 1, 1, 0x11, 0, 0xFF, 0xDA, 0x00, 0x08)
}

func TestPipelineImagePixelLimit(t *testing.T) {
	// The limit is checked before any tool runs: exactly MaxPixels passes,
	// one pixel more does not.
	const w = 10_000
	h := uint32(ingest.MaxPixels / w)
	for _, tc := range []struct {
		name string
		mime string
		ok   []byte
		over []byte
	}{
		{"png", mimePNG, pngHeader(w, h), pngHeader(w, h+1)},
		{"jpeg", mimeJPEG, jpegHeader(w, uint16(h)), jpegHeader(w, uint16(h+1))},
		{"heic", mimeHEIC, heifHeader(w, h), heifHeader(w, h+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, f, err := runPipelineOn(t, pdf{pages: []string{""}, ocr: []string{""}}, tc.mime, tc.over)
			var pe *PermanentError
			if !errors.As(err, &pe) || pe.Msg != msgTooManyPixels {
				t.Fatalf("over the limit: %v", err)
			}
			if len(f.calls) != 0 {
				t.Fatalf("tools ran: %v", f.names())
			}
			if _, _, err := runPipelineOn(t, pdf{pages: []string{""}, ocr: []string{""}}, tc.mime, tc.ok); err != nil {
				t.Fatalf("at the limit: %v", err)
			}
		})
	}
	// The decoded HEIC is checked again: its header may not tell the truth.
	_, f, err := runPipelineOn(t, pdf{pages: []string{""}, decoded: jpegHeader(65535, 1000)}, mimeHEIC, heifHeader(100, 100))
	var pe *PermanentError
	if !errors.As(err, &pe) || pe.Msg != msgTooManyPixels {
		t.Fatalf("decoded HEIC over the limit: %v", err)
	}
	if got := f.names(); !slices.Equal(got, []string{"heif-dec"}) {
		t.Fatalf("tools: %v", got)
	}
	if !strings.Contains(msgTooManyPixels, "50 megapixels") {
		t.Fatalf("message %q", msgTooManyPixels)
	}
}

func TestPipelineImageErrors(t *testing.T) {
	png := pngOf(t, 40, 20, 0)
	for _, tc := range []struct {
		name      string
		mime      string
		data      []byte
		p         pdf
		permanent bool
	}{
		{"not the stored type", mimeJPEG, png, pdf{}, true},
		{"damaged", mimePNG, []byte("\x89PNG\r\n\x1a\nbroken"), pdf{}, true},
		{"img2pdf cannot read it", mimePNG, png, pdf{convertErr: &ToolError{Tool: "img2pdf", ExitCode: 1, Stderr: "secret"}}, true},
		{"heif-dec cannot read it", mimeHEIC, heifHeader(10, 10), pdf{convertErr: &ToolError{Tool: "heif-dec", ExitCode: 1}}, true},
		{"img2pdf killed", mimePNG, png, pdf{convertErr: &ToolError{Tool: "img2pdf", ExitCode: -1}}, false},
		{"decoded HEIC damaged", mimeHEIC, heifHeader(10, 10), pdf{decoded: []byte("not a JPEG")}, true},
	} {
		_, _, err := runPipelineOn(t, tc.p, tc.mime, tc.data)
		var pe *PermanentError
		if err == nil || errors.As(err, &pe) != tc.permanent {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if tc.permanent && pe.Msg != msgUnreadableImage {
			t.Errorf("%s: message %q", tc.name, pe.Msg)
		}
	}
}
