// pdf.js behind the interface of pdfViewer.ts. Only imported by loadPdf,
// so the list and the login page never load it.
//
// The legacy build: the modern one uses the newest JavaScript (such as
// Map.prototype.getOrInsertComputed) and fails in browsers a year old,
// iOS Safari among them. The legacy build brings polyfills for them.
import { AnnotationMode, getDocument, GlobalWorkerOptions, TextLayer, type RenderTask } from "pdfjs-dist/legacy/build/pdf.mjs";
// The worker as a file of the build (/assets/…), from the same origin:
// the CSP's script-src 'self' covers workers.
import workerUrl from "pdfjs-dist/legacy/build/pdf.worker.min.mjs?url";
import { pdfjsOptions, type PdfDocument } from "./pdfViewer";

GlobalWorkerOptions.workerSrc = workerUrl;

/** Canvases above this many pixels fail in some browsers (iOS Safari): the resolution is lowered instead. */
const maxCanvasPixels = 16_777_216;

export async function openPdf(url: string): Promise<PdfDocument> {
  const task = getDocument(pdfjsOptions(url));
  const doc = await task.promise;
  return {
    numPages: doc.numPages,
    pageSize: async (n) => {
      const v = (await doc.getPage(n)).getViewport({ scale: 1 });
      return { width: v.width, height: v.height };
    },
    renderPage: (n, canvas, text, scale) => {
      let cancelled = false;
      let render: RenderTask | null = null;
      let layer: TextLayer | null = null;
      const promise = (async () => {
        const page = await doc.getPage(n);
        const viewport = page.getViewport({ scale });
        const size = { width: viewport.width, height: viewport.height };
        if (cancelled) return size;
        const ratio = Math.min(window.devicePixelRatio || 1, Math.sqrt(maxCanvasPixels / (viewport.width * viewport.height)));
        canvas.width = Math.floor(viewport.width * ratio);
        canvas.height = Math.floor(viewport.height * ratio);
        render = page.render({
          canvas,
          viewport,
          transform: ratio === 1 ? undefined : [ratio, 0, 0, ratio, 0, 0],
          // Annotations' appearance is drawn into the canvas; there is no
          // annotation layer, so no links or form fields in the page.
          annotationMode: AnnotationMode.ENABLE,
        });
        await render.promise;
        if (cancelled) return size;
        text.replaceChildren();
        text.style.setProperty("--total-scale-factor", String(scale));
        text.style.setProperty("--scale-round-x", "1px");
        text.style.setProperty("--scale-round-y", "1px");
        layer = new TextLayer({ textContentSource: page.streamTextContent(), container: text, viewport });
        await layer.render();
        return size;
      })();
      return {
        promise,
        cancel: () => {
          cancelled = true;
          render?.cancel();
          layer?.cancel();
        },
      };
    },
    destroy: () => void task.destroy(),
  };
}
