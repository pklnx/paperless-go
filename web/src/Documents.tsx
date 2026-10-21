import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { documentsApi, type DocumentSummary } from "./api";
import { lang, t } from "./i18n";
import { button, primary } from "./styles";
import { displayName, formatSize, stateText, type UploadItem, type UploadQueue } from "./uploads";

/** How many documents the start page lists; the document list (#7) shows all. */
const recentCount = 10;

function useQueue(queue: UploadQueue): UploadItem[] {
  return useSyncExternalStore(
    (fn) => queue.subscribe(fn),
    () => queue.list,
  );
}

const hasFiles = (e: DragEvent) => e.dataTransfer?.types.includes("Files") ?? false;

/**
 * Lets the whole window take dropped files: while files are dragged over
 * it, an overlay says where they go. Other drags (text, links) pass.
 */
export function WindowDrop({ onFiles }: { onFiles: (files: File[]) => void }) {
  const [dragging, setDragging] = useState(false);
  const depth = useRef(0);
  const onFilesRef = useRef(onFiles);
  onFilesRef.current = onFiles;

  useEffect(() => {
    const enter = (e: DragEvent) => {
      if (!hasFiles(e)) return;
      e.preventDefault();
      depth.current++;
      setDragging(true);
    };
    const over = (e: DragEvent) => {
      if (!hasFiles(e)) return;
      e.preventDefault();
      if (e.dataTransfer) e.dataTransfer.dropEffect = "copy";
    };
    const leave = (e: DragEvent) => {
      if (!hasFiles(e)) return;
      depth.current = Math.max(0, depth.current - 1);
      if (depth.current === 0) setDragging(false);
    };
    const drop = (e: DragEvent) => {
      if (!hasFiles(e)) return;
      e.preventDefault();
      depth.current = 0;
      setDragging(false);
      const files = Array.from(e.dataTransfer?.files ?? []);
      if (files.length > 0) onFilesRef.current(files);
    };
    window.addEventListener("dragenter", enter);
    window.addEventListener("dragover", over);
    window.addEventListener("dragleave", leave);
    window.addEventListener("drop", drop);
    return () => {
      window.removeEventListener("dragenter", enter);
      window.removeEventListener("dragover", over);
      window.removeEventListener("dragleave", leave);
      window.removeEventListener("drop", drop);
    };
  }, []);

  if (!dragging) return null;
  return (
    <div className="pointer-events-none fixed inset-0 z-50 flex items-center justify-center bg-scrim p-6">
      <p className="rounded-lg border-2 border-dashed border-accent bg-surface-raised px-8 py-10 text-lg font-semibold text-accent-ink">{t.dropHere}</p>
    </div>
  );
}

/** The start page: upload, the uploads in progress and the newest documents. */
export function DocumentsHome({ queue }: { queue: UploadQueue }) {
  const items = useQueue(queue);
  const [docs, setDocs] = useState<DocumentSummary[] | null>(null);
  const [maxSize, setMaxSize] = useState<number | undefined>(queue.maxSize);
  const [error, setError] = useState("");
  const imported = items.filter((i) => i.state.kind === "imported").length;

  // Load the list at start and again after each import.
  useEffect(() => {
    const ctl = new AbortController();
    documentsApi.list(ctl.signal).then(
      (res) => {
        setDocs(res.documents.slice(0, recentCount));
        queue.maxSize = res.maxUploadSize;
        setMaxSize(res.maxUploadSize);
        setError("");
      },
      (err: unknown) => {
        if (!ctl.signal.aborted) setError(t.failed(err instanceof Error ? err.message : String(err)));
      },
    );
    return () => ctl.abort();
  }, [queue, imported]);

  const limit = maxSize ? formatSize(maxSize, lang) : "";
  return (
    <div className="flex flex-col gap-6">
      <UploadPanel queue={queue} items={items} limit={limit} />
      <section className="flex flex-col gap-2">
        <h2 className="font-semibold">{t.recentTitle}</h2>
        {error && (
          <p role="alert" className="text-sm text-danger">
            {error}
          </p>
        )}
        {!docs && !error && <p className="text-sm text-ink-muted">{t.loading}</p>}
        {docs && docs.length === 0 && <p className="text-sm text-ink-muted">{t.recentEmpty}</p>}
        {docs && docs.length > 0 && (
          <ul aria-label={t.recentTitle} className="flex flex-col divide-y divide-line rounded-lg border border-line">
            {docs.map((d) => (
              <li key={d.id} className="flex flex-wrap items-baseline gap-x-3 gap-y-1 px-3 py-2">
                <span className="min-w-0 flex-1 truncate">{displayName(d.title) || t.untitled}</span>
                <span className="text-sm text-ink-muted">{new Date(d.createdAt).toLocaleString(lang, { dateStyle: "medium", timeStyle: "short" })}</span>
                <a className="text-sm text-accent-ink hover:underline" href={documentsApi.fileURL(d.id)} download>
                  {t.download}
                </a>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

function UploadPanel({ queue, items, limit }: { queue: UploadQueue; items: UploadItem[]; limit: string }) {
  const picker = useRef<HTMLInputElement>(null);
  const finished = items.some((i) => i.state.kind !== "waiting" && i.state.kind !== "uploading");
  return (
    <section className="flex flex-col gap-3 rounded-lg border border-dashed border-line-control p-5">
      <h2 className="font-semibold">{t.uploadTitle}</h2>
      <p className="text-sm text-ink-muted">{t.uploadHint(limit)}</p>
      <div className="flex flex-wrap gap-2">
        <button type="button" className={primary} onClick={() => picker.current?.click()}>
          {t.chooseFiles}
        </button>
        {finished && (
          <button type="button" className={button} onClick={() => queue.clearFinished()}>
            {t.clearFinished}
          </button>
        )}
        <input
          ref={picker}
          type="file"
          multiple
          hidden
          data-testid="file-picker"
          accept="application/pdf,image/jpeg,image/png,image/heic,image/heif,.pdf,.jpg,.jpeg,.png,.heic,.heif"
          onChange={(e) => {
            const files = Array.from(e.target.files ?? []);
            e.target.value = "";
            if (files.length > 0) queue.add(files);
          }}
        />
      </div>
      {items.length > 0 && (
        <ul aria-label={t.uploadsLabel} className="flex flex-col gap-1">
          {items.map((i) => (
            <UploadRow key={i.id} item={i} limit={limit} cancel={() => queue.cancel(i.id)} />
          ))}
        </ul>
      )}
    </section>
  );
}

const stateColor: Record<UploadItem["state"]["kind"], string> = {
  waiting: "text-ink-muted",
  uploading: "text-ink-muted",
  imported: "text-ok",
  duplicate: "text-ink-muted",
  failed: "text-danger",
  cancelled: "text-ink-muted",
};

function UploadRow({ item, limit, cancel }: { item: UploadItem; limit: string; cancel: () => void }) {
  const name = displayName(item.name) || t.untitled;
  const active = item.state.kind === "waiting" || item.state.kind === "uploading";
  return (
    <li className="flex flex-col gap-1 text-sm">
      <div className="flex flex-wrap items-baseline gap-x-3">
        <span className="min-w-0 flex-1 truncate">{name}</span>
        <span className="text-ink-muted">{formatSize(item.size, lang)}</span>
        <span className={stateColor[item.state.kind]} role="status">
          {stateText(item.state, t, limit)}
        </span>
        {active && (
          <button type="button" className="text-accent-ink hover:underline" aria-label={t.cancelUpload(name)} onClick={cancel}>
            {t.cancel}
          </button>
        )}
      </div>
      {item.state.kind === "uploading" && (
        <progress className="h-1.5 w-full" max={100} value={item.state.percent} aria-label={name} />
      )}
    </li>
  );
}
