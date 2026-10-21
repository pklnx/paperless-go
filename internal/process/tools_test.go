//go:build tools

// Tests with the real tools, run in the toolstest stage of the Dockerfile
// (docker build --target toolstest), as UID 65532:
//
//	go test -tags tools -run '^TestTools' ./internal/process
//
// Every PDF is generated here; pikepdf (installed with ocrmypdf) encrypts.

package process

import (
	"context"
	"errors"
	"fmt"
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
// open it ("" opens without one), owner the one needed to change it.
func encrypt(t *testing.T, in, out, user, owner string) {
	t.Helper()
	script := `import sys, pikepdf
pdf = pikepdf.open(sys.argv[1])
pdf.save(sys.argv[2], encryption=pikepdf.Encryption(user=sys.argv[3], owner=sys.argv[4], R=6,
    allow=pikepdf.Permissions(modify_other=False, modify_assembly=False, modify_form=False, modify_annotation=False)))
`
	cmd := exec.Command("python3", "-I", "-c", script, in, out, user, owner)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("encrypt: %v\n%s", err, b)
	}
}

func runTools(t *testing.T, write func(path string)) (Result, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "original")
	write(path)
	return (&Pipeline{Runner: &ExecRunner{}}).Run(context.Background(), Input{Dir: dir, Path: path, MIME: mimePDF})
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

func TestToolsBlankPageNeedsOCR(t *testing.T) {
	_, err := runTools(t, func(p string) { makePDF(t, p, toolsText, "") })
	var pe *PermanentError
	if !errors.As(err, &pe) || pe.Msg != msgOCRLater {
		t.Fatalf("PDF with a blank page: %v", err)
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
		encrypt(t, plain, p, "open-sesame", "owner-secret")
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
		encrypt(t, plain, p, "", "owner-secret")
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Archive != "" || res.PageCount != 2 || res.Note != msgOwnerPassword || !strings.Contains(res.Text, "Rechnung 4711") {
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
