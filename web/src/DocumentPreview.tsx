import { useCallback, useEffect, useRef, useState } from "react";
import { documentsApi, type DocumentDetail } from "./api";
import { t } from "./i18n";
import { loadPdf, type PdfDocument, type PdfPageSize } from "./pdfViewer";
import { button } from "./styles";

/** What the detail shows of a document's file. */
export type PreviewSource = { kind: "pdf"; url: string } | { kind: "image"; url: string } | { kind: "heic" } | { kind: "none" };

/**
 * The archive PDF when processing made one, else the original when it is
 * a PDF. An image not processed yet: JPEG and PNG as an image, HEIC as a
 * note (only Safari can show it). Anything else has no preview.
 */
export function previewSource(d: Pick<DocumentDetail, "id" | "hasArchive" | "originalMime">): PreviewSource {
  if (d.hasArchive) return { kind: "pdf", url: documentsApi.fileURL(d.id, "archive") };
  if (d.originalMime === "application/pdf") return { kind: "pdf", url: documentsApi.fileURL(d.id, "original") };
  if (d.originalMime === "image/jpeg" || d.originalMime === "image/png") return { kind: "image", url: documentsApi.fileURL(d.id, "original") };
  if (d.originalMime === "image/heic" || d.originalMime === "image/heif") return { kind: "heic" };
  return { kind: "none" };
}

/** The preview of a document: the PDF viewer, the image, or a note with the download. */
export function Preview({ doc, processing }: { doc: DocumentDetail; processing: boolean }) {
  const src = previewSource(doc);
  if (src.kind === "pdf") return <PdfView key={src.url} url={src.url} />;
  if (src.kind === "image") {
    // A plain image request: the file endpoint's sandbox and attachment
    // headers do not apply to images, and img-src 'self' allows it.
    return (
      <div className="h-full overflow-auto bg-surface-sunken p-3">
        <img src={src.url} alt={t.previewOf(doc.title || doc.originalFileName)} className="mx-auto max-w-full bg-paper shadow" />
      </div>
    );
  }
  return (
    <div className="grid h-full place-items-center bg-surface-sunken p-6 text-center text-sm text-ink-muted">
      <div className="flex flex-col items-center gap-2">
        <p>{src.kind === "heic" && processing ? t.previewHeicProcessing : t.noPreview}</p>
        <a className={button} href={documentsApi.fileURL(doc.id, "original")} download>
          {t.downloadOriginal}
        </a>
      </div>
    </div>
  );
}

/** How much one step of + and − zooms. */
const zoomStep = 1.25;
const minScale = 0.25;
const maxScale = 5;
/** Space around the pages, in CSS pixels. */
const gutter = 24;

type Zoom = "fit" | number;

/**
 * The PDF viewer: pages drawn into canvases, each only while it is near
 * the visible part (an IntersectionObserver), with a text layer for
 * selecting and finding text. Zoom to the width, +, and −, and a page
 * counter. The PDF is drawn, never opened as a document of its own.
 */
export function PdfView({ url }: { url: string }) {
  const [doc, setDoc] = useState<PdfDocument | null>(null);
  const [first, setFirst] = useState<PdfPageSize | null>(null);
  const [error, setError] = useState(false);
  const [zoom, setZoom] = useState<Zoom>("fit");
  const [width, setWidth] = useState(0);
  const [near, setNear] = useState<Set<number>>(new Set());
  const [current, setCurrent] = useState(1);
  const scroller = useRef<HTMLDivElement>(null);
  const [observer, setObserver] = useState<IntersectionObserver | null>(null);

  useEffect(() => {
    let live = true;
    let opened: PdfDocument | null = null;
    setDoc(null);
    setError(false);
    loadPdf(url)
      .then(async (d) => {
        opened = d;
        if (!live) return d.destroy();
        const size = await d.pageSize(1);
        if (!live) return;
        setFirst(size);
        setDoc(d);
      })
      .catch(() => live && setError(true));
    return () => {
      live = false;
      opened?.destroy();
    };
  }, [url]);

  // The width the pages fit into.
  useEffect(() => {
    const el = scroller.current;
    if (!el) return;
    setWidth(el.clientWidth);
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => setWidth(el.clientWidth));
    ro.observe(el);
    return () => ro.disconnect();
  }, [doc]);

  // Which pages are near the visible part: those are drawn.
  useEffect(() => {
    const root = scroller.current;
    if (!doc || !root || typeof IntersectionObserver === "undefined") return;
    const io = new IntersectionObserver(
      (entries) =>
        setNear((prev) => {
          const next = new Set(prev);
          for (const e of entries) {
            const n = Number((e.target as HTMLElement).dataset.page);
            if (e.isIntersecting) next.add(n);
            else next.delete(n);
          }
          return next;
        }),
      { root, rootMargin: "100% 0px" },
    );
    setObserver(io);
    return () => {
      io.disconnect();
      setObserver(null);
    };
  }, [doc]);

  const fitScale = first && width > 0 ? Math.max(minScale, (width - gutter) / first.width) : 1;
  const scale = zoom === "fit" ? fitScale : zoom;
  const setScale = (s: number) => setZoom(Math.min(maxScale, Math.max(minScale, s)));

  const onScroll = useCallback(() => {
    const root = scroller.current;
    if (!root) return;
    // The page at a third of the visible height is the current one.
    const line = root.scrollTop + root.clientHeight / 3;
    let n = 1;
    for (const el of Array.from(root.querySelectorAll<HTMLElement>("[data-page]"))) {
      if (el.offsetTop <= line) n = Number(el.dataset.page);
    }
    setCurrent(n);
  }, []);

  if (error) {
    return (
      <div className="grid h-full place-items-center bg-surface-sunken p-6 text-center text-sm text-danger" role="alert">
        {t.previewFailed}
      </div>
    );
  }
  return (
    <div className="flex h-full min-h-0 flex-col bg-surface-sunken">
      <div className="flex flex-wrap items-center gap-1 border-b border-line px-2 py-1 text-sm" role="toolbar" aria-label={t.viewerTools}>
        <button type="button" className={button} disabled={!doc} aria-pressed={zoom === "fit"} onClick={() => setZoom("fit")}>
          {t.zoomFit}
        </button>
        <button type="button" className={button} disabled={!doc || scale <= minScale} aria-label={t.zoomOut} title={t.zoomOut} onClick={() => setScale(scale / zoomStep)}>
          −
        </button>
        <button type="button" className={button} disabled={!doc || scale >= maxScale} aria-label={t.zoomIn} title={t.zoomIn} onClick={() => setScale(scale * zoomStep)}>
          +
        </button>
        <span className="ml-auto tabular-nums text-ink-muted" aria-live="polite">
          {doc ? t.pageOf(`${current} / ${doc.numPages}`) : t.loading}
        </span>
      </div>
      <div ref={scroller} onScroll={onScroll} data-testid="pdf-pages" className="min-h-0 flex-1 overflow-auto">
        <div className="flex w-max min-w-full flex-col items-center gap-3 p-3">
          {doc &&
            first &&
            observer &&
            Array.from({ length: doc.numPages }, (_, i) => i + 1).map((n) => (
              <PdfPage key={n} doc={doc} n={n} scale={scale} estimate={first} observer={observer} near={near.has(n)} />
            ))}
        </div>
      </div>
    </div>
  );
}

function PdfPage({ doc, n, scale, estimate, observer, near }: { doc: PdfDocument; n: number; scale: number; estimate: PdfPageSize; observer: IntersectionObserver; near: boolean }) {
  const box = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const text = useRef<HTMLDivElement>(null);
  // The page's own size at scale 1 once drawn; before, the first page's.
  const [size, setSize] = useState<PdfPageSize>(estimate);

  useEffect(() => {
    const el = box.current;
    if (!el) return;
    observer.observe(el);
    return () => observer.unobserve(el);
  }, [observer]);

  useEffect(() => {
    if (!near || !canvas.current || !text.current) return;
    const r = doc.renderPage(n, canvas.current, text.current, scale);
    r.promise.then(
      (s) => setSize({ width: s.width / scale, height: s.height / scale }),
      () => {
        // cancelled, or a damaged page: it stays blank
      },
    );
    return () => r.cancel();
  }, [doc, n, scale, near]);

  const w = size.width * scale;
  const h = size.height * scale;
  // CSSOM sizes (not style attributes), which the CSP allows.
  return (
    <div ref={box} data-page={n} aria-label={t.pageNumber(String(n))} role="group" className="relative shrink-0 bg-paper shadow" style={{ width: w, height: h }}>
      {near && (
        <>
          {/* A new canvas per scale: pdf.js refuses a canvas that is still being drawn. */}
          <canvas key={scale} ref={canvas} aria-hidden="true" className="absolute inset-0" style={{ width: w, height: h }} />
          <div key={`t${scale}`} ref={text} className="textLayer" />
        </>
      )}
    </div>
  );
}
