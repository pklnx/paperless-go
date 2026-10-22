import { afterEach, describe, expect, it, vi } from "vitest";
import { pdfjsAssets, pdfjsOptions } from "./pdfViewer";
import { previewSource } from "./DocumentPreview";
import pdfjsPackage from "pdfjs-dist/package.json";
// The pdf.js files the build ships (pdfjsLoader.ts and vite.config.ts).
import pdfjsLibrary from "pdfjs-dist/legacy/build/pdf.mjs?raw";
import pdfjsWorker from "pdfjs-dist/legacy/build/pdf.worker.min.mjs?raw";
import jpxFallback from "pdfjs-dist/wasm/openjpeg_nowasm_fallback.js?raw";
import jbig2Fallback from "pdfjs-dist/wasm/jbig2_nowasm_fallback.js?raw";

const getDocument = vi.fn((_opts: unknown) => ({ promise: Promise.resolve({ numPages: 2 }), destroy: vi.fn() }));
const workerOptions = { workerSrc: "" };
vi.mock("pdfjs-dist/legacy/build/pdf.mjs", () => ({
  getDocument: (o: unknown) => getDocument(o),
  GlobalWorkerOptions: workerOptions,
  AnnotationMode: { ENABLE: 1 },
  TextLayer: class {},
}));

afterEach(() => vi.clearAllMocks());

describe("pdf.js options", () => {
  it("loads everything from the server's own /assets/ and keeps WebAssembly and XFA off", () => {
    expect(pdfjsAssets).toBe(`/assets/pdfjs-${pdfjsPackage.version}/`);
    expect(pdfjsOptions("/api/documents/d1/file?kind=archive")).toEqual({
      url: "/api/documents/d1/file?kind=archive",
      cMapUrl: `/assets/pdfjs-${pdfjsPackage.version}/cmaps/`,
      cMapPacked: true,
      standardFontDataUrl: `/assets/pdfjs-${pdfjsPackage.version}/standard_fonts/`,
      wasmUrl: `/assets/pdfjs-${pdfjsPackage.version}/wasm/`,
      useWasm: false,
      enableXfa: false,
      withCredentials: false,
    });
  });

  it("opens a PDF with these options and a worker from the same origin", async () => {
    const { loadPdf } = await import("./pdfViewer");
    const doc = await loadPdf("/api/documents/d1/file?kind=original");
    expect(doc.numPages).toBe(2);
    expect(getDocument).toHaveBeenCalledWith(pdfjsOptions("/api/documents/d1/file?kind=original"));
    // A path of the build (Vite's ?url), never a CDN.
    expect(workerOptions.workerSrc).toMatch(/^\/(?!\/)/);
    expect(workerOptions.workerSrc).toContain("pdf.worker");
  });

  it("ships pdf.js without eval: the CSP has no 'unsafe-eval'", () => {
    for (const [name, src] of Object.entries({ pdfjsLibrary, pdfjsWorker, jpxFallback, jbig2Fallback })) {
      expect(src.length, name).toBeGreaterThan(1000);
      expect(/(^|[^\w$.])eval\s*\(/.test(src), `${name}: eval(`).toBe(false);
      expect(/new\s+Function\s*\(/.test(src), `${name}: new Function(`).toBe(false);
      // core-js's polyfills in the legacy build fall back to Function("return
      // this") only without globalThis, which every browser of the CSP has.
      const calls = src.match(/(^|[^\w$.])Function\s*\([^)]*\)/g) ?? [];
      expect(calls.map((c) => c.replace(/^[^F]*/, "").replace(/'/g, '"')), name).toEqual(calls.map(() => 'Function("return this")'));
    }
  });
});

describe("previewSource", () => {
  const d = { id: "x" };
  it("prefers the archive PDF, then an original PDF", () => {
    expect(previewSource({ ...d, hasArchive: true, originalMime: "image/heic" })).toEqual({ kind: "pdf", url: "/api/documents/x/file?kind=archive" });
    expect(previewSource({ ...d, hasArchive: false, originalMime: "application/pdf" })).toEqual({ kind: "pdf", url: "/api/documents/x/file?kind=original" });
  });
  it("shows unprocessed JPEG and PNG as images, HEIC and anything else not", () => {
    expect(previewSource({ ...d, hasArchive: false, originalMime: "image/jpeg" })).toEqual({ kind: "image", url: "/api/documents/x/file?kind=original" });
    expect(previewSource({ ...d, hasArchive: false, originalMime: "image/png" }).kind).toBe("image");
    expect(previewSource({ ...d, hasArchive: false, originalMime: "image/heic" }).kind).toBe("heic");
    expect(previewSource({ ...d, hasArchive: false, originalMime: "image/tiff" }).kind).toBe("none");
  });
});
