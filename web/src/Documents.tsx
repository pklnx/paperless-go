import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { documentsApi } from "./api";
import { lang, t } from "./i18n";
import { trashErrorText } from "./Trash";
import { button, primary } from "./styles";
import { displayName, formatSize, stateText, type UploadItem, type UploadQueue } from "./uploads";

/** The queue's items, rendered again on each change. */
export function useQueue(queue: UploadQueue): UploadItem[] {
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

/**
 * The upload panel above the document list: choose files, and the uploads
 * in progress. limit is the upload limit in bytes, once known. onRestored
 * follows a Restore of a duplicate's trashed document (lists and counts
 * change).
 */
export function DocumentsUpload({ queue, maxSize, onRestored }: { queue: UploadQueue; maxSize?: number; onRestored?: () => void }) {
  const items = useQueue(queue);
  return <UploadPanel queue={queue} items={items} limit={maxSize ? formatSize(maxSize, lang) : ""} onRestored={onRestored} />;
}

function UploadPanel({ queue, items, limit, onRestored }: { queue: UploadQueue; items: UploadItem[]; limit: string; onRestored?: () => void }) {
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
            <UploadRow
              key={i.id}
              item={i}
              limit={limit}
              cancel={() => queue.cancel(i.id)}
              retry={queue.canRetry(i.id) ? () => queue.retry(i.id) : undefined}
              restored={() => {
                queue.markRestored(i.id);
                onRestored?.();
              }}
            />
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

function UploadRow({ item, limit, cancel, retry, restored }: { item: UploadItem; limit: string; cancel: () => void; retry?: () => void; restored: () => void }) {
  const name = displayName(item.name) || t.untitled;
  const active = item.state.kind === "waiting" || item.state.kind === "uploading";
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const s = item.state;
  // A duplicate of a trashed document: the server restored nothing (#12); Restore does.
  const trashedId = s.kind === "duplicate" && s.trashed && s.existingId ? s.existingId : "";
  const restore = () => {
    setBusy(true);
    setError("");
    documentsApi.restore(trashedId).then(
      () => {
        setBusy(false);
        restored();
      },
      (err: unknown) => {
        setBusy(false);
        setError(trashErrorText(err, "restore"));
      },
    );
  };
  return (
    <li className="flex flex-col gap-1 text-sm">
      <div className="flex flex-wrap items-baseline gap-x-3">
        <span className="min-w-0 basis-full wrap-anywhere sm:basis-0 sm:flex-1">{name}</span>
        <span className="text-ink-muted">{formatSize(item.size, lang)}</span>
        <span className={stateColor[item.state.kind]} role="status">
          {stateText(item.state, t, limit)}
        </span>
        {active && (
          <button type="button" className="text-accent-ink hover:underline" aria-label={t.cancelUpload(name)} onClick={cancel}>
            {t.cancel}
          </button>
        )}
        {trashedId && (
          <button type="button" className="text-accent-ink hover:underline pointer-coarse:min-h-11" aria-label={t.uploadRestoreLabel(name)} disabled={busy} onClick={restore}>
            {t.restore}
          </button>
        )}
        {retry && (
          <button type="button" className="text-accent-ink hover:underline pointer-coarse:min-h-11" aria-label={t.retryUpload(name)} onClick={retry}>
            {t.tryAgain}
          </button>
        )}
      </div>
      {error && (
        <p role="alert" className="text-danger">
          {error}
        </p>
      )}
      {item.state.kind === "uploading" && (
        <progress className="h-1.5 w-full" max={100} value={item.state.percent} aria-label={name} />
      )}
    </li>
  );
}
