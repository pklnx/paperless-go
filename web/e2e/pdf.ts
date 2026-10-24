// Small PDF 1.4 files with a text layer, for the files a test uploads: the
// same writer as tools/demo/pdf.go, byte for byte (pdf.spec.ts checks it
// against tools/demo/testdata/sample.pdf), so the demo's stand-in
// pdftotext reads their text and pdf.js shows them with its standard fonts.

const pageWidth = 595;
const pageHeight = 842;
const textLeft = 72;
const textTop = 770;
const textLeading = 16;

/** A PDF literal string: \, ( and ) escaped, Latin-1 above ASCII as octal escapes. */
function pdfString(s: string): string {
  let out = "(";
  for (const ch of s) {
    const c = ch.codePointAt(0)!;
    if (ch === "\\" || ch === "(" || ch === ")") out += `\\${ch}`;
    else if (c >= 0x20 && c < 0x7f) out += ch;
    else if (c >= 0xa0 && c <= 0xff) out += `\\${c.toString(8).padStart(3, "0")}`;
    else throw new Error(`pdf: ${JSON.stringify(ch)} cannot be written in WinAnsiEncoding here`);
  }
  return `${out})`;
}

/** A PDF with one page per entry of pages; each page is its lines of text. */
export function makePDF(pages: string[][]): Buffer {
  if (pages.length === 0) throw new Error("pdf: no pages");
  const kids = pages.map((_, i) => `${4 + 2 * i} 0 R`).join(" ");
  const objs = [
    "<< /Type /Catalog /Pages 2 0 R >>",
    `<< /Type /Pages /Kids [${kids}] /Count ${pages.length} >>`,
    "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
  ];
  pages.forEach((lines, i) => {
    let content = `BT\n/F1 12 Tf\n${textLeading} TL\n${textLeft} ${textTop} Td\n`;
    lines.forEach((line, j) => {
      if (j > 0) content += "T*\n";
      content += `${pdfString(line)} Tj\n`;
    });
    content += "ET";
    objs.push(
      `<< /Type /Page /Parent 2 0 R /MediaBox [0 0 ${pageWidth} ${pageHeight}] /Resources << /Font << /F1 3 0 R >> >> /Contents ${5 + 2 * i} 0 R >>`,
      `<< /Length ${content.length} >>\nstream\n${content}\nendstream`,
    );
  });
  // Everything is ASCII, so string lengths are byte offsets.
  let out = "%PDF-1.4\n";
  const offsets = objs.map((o, i) => {
    const at = out.length;
    out += `${i + 1} 0 obj\n${o}\nendobj\n`;
    return at;
  });
  const xref = out.length;
  out += `xref\n0 ${objs.length + 1}\n0000000000 65535 f \n`;
  for (const off of offsets) out += `${String(off).padStart(10, "0")} 00000 n \n`;
  out += `trailer\n<< /Size ${objs.length + 1} /Root 1 0 R >>\nstartxref\n${xref}\n%%EOF\n`;
  return Buffer.from(out, "latin1");
}
