package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// The demo's PDFs: small PDF 1.4 files with a text layer, one uncompressed
// content stream per page, in Helvetica with WinAnsiEncoding. pdf.js shows
// them with its bundled standard fonts, and the fake pdftotext
// (tools.go) reads their text back. web/e2e/pdf.ts is the same writer in
// TypeScript: both give the same bytes for the same pages (the golden file
// testdata/sample.pdf holds them).

// Page geometry: A4 in points, 12 pt text from the top left margin.
const (
	pageWidth   = 595
	pageHeight  = 842
	textLeft    = 72
	textTop     = 770
	textLeading = 16
)

// makePDF writes a PDF with one page per entry of pages; each page is its
// lines of text. A line may hold the characters of Latin-1 (WinAnsi agrees
// with it there) except control characters; the file itself is ASCII, with
// the other bytes as octal escapes.
func makePDF(pages [][]string) ([]byte, error) {
	if len(pages) == 0 {
		return nil, errors.New("pdf: no pages")
	}
	var objs []string
	kids := make([]string, len(pages))
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 4+2*i)
	}
	objs = append(objs,
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages)),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
	)
	for i, lines := range pages {
		var content strings.Builder
		fmt.Fprintf(&content, "BT\n/F1 12 Tf\n%d TL\n%d %d Td\n", textLeading, textLeft, textTop)
		for j, line := range lines {
			s, err := pdfString(line)
			if err != nil {
				return nil, err
			}
			if j > 0 {
				content.WriteString("T*\n")
			}
			content.WriteString(s + " Tj\n")
		}
		content.WriteString("ET")
		objs = append(objs,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>",
				pageWidth, pageHeight, 5+2*i),
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", content.Len(), content.String()),
		)
	}

	var b bytes.Buffer
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
	return b.Bytes(), nil
}

// pdfString writes s as a PDF literal string: \, ( and ) escaped, the
// characters above ASCII as octal escapes of their Latin-1 byte.
func pdfString(s string) (string, error) {
	var b strings.Builder
	b.WriteByte('(')
	for _, r := range s {
		switch {
		case r == '\\' || r == '(' || r == ')':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r >= 0x20 && r < 0x7f:
			b.WriteRune(r)
		case r >= 0xa0 && r <= 0xff:
			fmt.Fprintf(&b, "\\%03o", r)
		default:
			return "", fmt.Errorf("pdf: %q cannot be written in WinAnsiEncoding here", r)
		}
	}
	b.WriteByte(')')
	return b.String(), nil
}

// errUnreadable is a file the fake tools cannot read: not a PDF of the
// kind makePDF writes.
var errUnreadable = errors.New("not a PDF with a text layer")

// pdfPages reads the text of each page of a PDF that makePDF wrote: the
// strings shown with Tj in each content stream, one line each. Anything
// else is errUnreadable.
func pdfPages(b []byte) ([][]string, error) {
	if !bytes.HasPrefix(b, []byte("%PDF-")) || !bytes.Contains(b, []byte("%%EOF")) {
		return nil, errUnreadable
	}
	var pages [][]string
	rest := b
	for {
		start := bytes.Index(rest, []byte("\nstream\n"))
		if start < 0 {
			break
		}
		rest = rest[start+len("\nstream\n"):]
		end := bytes.Index(rest, []byte("\nendstream"))
		if end < 0 {
			return nil, errUnreadable
		}
		content := rest[:end]
		rest = rest[end:]
		if !bytes.HasPrefix(content, []byte("BT\n")) {
			continue
		}
		lines, err := shownStrings(content)
		if err != nil {
			return nil, err
		}
		pages = append(pages, lines)
	}
	if len(pages) == 0 {
		return nil, errUnreadable
	}
	return pages, nil
}

// shownStrings returns the literal strings of content that a Tj shows,
// decoded from WinAnsi (Latin-1 here) to UTF-8.
func shownStrings(content []byte) ([]string, error) {
	var out []string
	for i := 0; i < len(content); i++ {
		if content[i] != '(' {
			continue
		}
		s, n, err := readString(content[i:])
		if err != nil {
			return nil, err
		}
		i += n - 1
		if bytes.HasPrefix(content[i+1:], []byte(" Tj")) {
			out = append(out, s)
		}
	}
	return out, nil
}

// readString reads the literal string at the start of b (which starts
// with "(") and returns it and the number of bytes it took.
func readString(b []byte) (string, int, error) {
	var s strings.Builder
	depth := 0
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case c == '(':
			if depth > 0 {
				s.WriteByte(c)
			}
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return s.String(), i + 1, nil
			}
			s.WriteByte(c)
		case c == '\\' && i+3 < len(b) && isOctal(b[i+1]) && isOctal(b[i+2]) && isOctal(b[i+3]):
			s.WriteRune(rune(b[i+1]-'0')<<6 | rune(b[i+2]-'0')<<3 | rune(b[i+3]-'0'))
			i += 3
		case c == '\\' && i+1 < len(b):
			s.WriteByte(b[i+1])
			i++
		default:
			s.WriteRune(rune(c))
		}
	}
	return "", 0, errUnreadable
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }
