// The PDF viewer's interface to pdf.js. pdf.js itself (about 1.5 MB with
// its worker) is loaded only when a document's detail opens: loadPdf
// imports pdfjsLoader.ts, which Vite splits into its own chunk. Tests
// replace loadPdf.

/** A page's size in CSS pixels. */
export interface PdfPageSize {
  width: number;
  height: number;
}

/** A page being drawn: promise ends when canvas and text layer are done, with the page's size at the scale. */
export interface PdfRender {
  promise: Promise<PdfPageSize>;
  cancel: () => void;
}

/** An open PDF. Pages are numbered from 1. */
export interface PdfDocument {
  numPages: number;
  /** The size of page n at scale 1. */
  pageSize: (n: number) => Promise<PdfPageSize>;
  /** Draws page n into canvas, and its text (as text nodes, for selecting and finding) into text. */
  renderPage: (n: number, canvas: HTMLCanvasElement, text: HTMLElement, scale: number) => PdfRender;
  destroy: () => void;
}

/** Where the build puts pdf.js's data files (vite.config.ts): the server's own /assets/. */
export const pdfjsAssets = __PDFJS_ASSETS__;

/**
 * The options of pdf.js's getDocument for url. Everything comes from the
 * server itself, and nothing needs more than the UI's CSP allows:
 * - cMaps and standard fonts from /assets/, never a CDN.
 * - No WebAssembly (useWasm: false): the JPEG 2000 and JBIG2 decoders and
 *   colour profiles would need 'wasm-unsafe-eval'. The decoders' JavaScript
 *   versions are loaded from wasmUrl instead; colour profiles fall back to
 *   their alternate colour space.
 * - No XFA forms. The viewer draws no annotation layer and runs no PDF
 *   scripts (pdf.js runs them only in its viewer app, not in the library).
 * pdf.js 6 has no eval of its own (isEvalSupported is gone; the test
 * pdfViewer.test.ts checks the shipped files, and
 * check:dist (scripts/check-dist.mjs) the built ones), and the CSP forbids it anyway.
 * The file is fetched with the session cookie (same origin).
 */
export function pdfjsOptions(url: string) {
  return {
    url,
    cMapUrl: `${pdfjsAssets}cmaps/`,
    cMapPacked: true,
    standardFontDataUrl: `${pdfjsAssets}standard_fonts/`,
    wasmUrl: `${pdfjsAssets}wasm/`,
    useWasm: false,
    enableXfa: false,
    withCredentials: false,
  };
}

/** Opens the PDF at url (a same-origin API URL). */
export async function loadPdf(url: string): Promise<PdfDocument> {
  const m = await import("./pdfjsLoader");
  return m.openPdf(url);
}
