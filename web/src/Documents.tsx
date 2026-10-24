import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { Button, ProgressBar } from "@carbon/react";
import { documentsApi } from "./api";
import { lang, t } from "./i18n";
import { trashErrorText } from "./Trash";
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
    <div className="app-drop">
      <p className="app-drop__box">{t.dropHere}</p>
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

/**
 * A drop area in the look of Carbon's file drop container, with "Choose
 * files" as the view's one primary button (decision 3 of #150). The whole
 * window takes drops (WindowDrop), so the area itself needs no handlers.
 */
function UploadPanel({ queue, items, limit, onRestored }: { queue: UploadQueue; items: UploadItem[]; limit: string; onRestored?: () => void }) {
  const picker = useRef<HTMLInputElement>(null);
  const finished = items.some((i) => i.state.kind !== "waiting" && i.state.kind !== "uploading");
  return (
    <section className="upload-panel">
      <h2 className="upload-panel__title">{t.uploadTitle}</h2>
      <p className="upload-panel__hint app-text-secondary">{t.uploadHint(limit)}</p>
      <div className="upload-panel__actions">
        <Button kind="primary" size="md" onClick={() => picker.current?.click()}>
          {t.chooseFiles}
        </Button>
        {finished && (
          <Button kind="ghost" size="md" onClick={() => queue.clearFinished()}>
            {t.clearFinished}
          </Button>
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
        <ul aria-label={t.uploadsLabel} className="upload-list">
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

/** The class of a row's state text: imported in the success colour, a failure in the error colour, the rest secondary. */
const stateClass: Record<UploadItem["state"]["kind"], string> = {
  waiting: "app-text-secondary",
  uploading: "app-text-secondary",
  imported: "upload-row__state--ok",
  duplicate: "app-text-secondary",
  failed: "app-text-error",
  cancelled: "app-text-secondary",
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
    <li className="upload-row">
      <div className="upload-row__line">
        <span className="upload-row__name">{name}</span>
        <span className="app-text-secondary">{formatSize(item.size, lang)}</span>
        <span className={stateClass[item.state.kind]} role="status">
          {stateText(item.state, t, limit)}
        </span>
        {active && (
          <Button kind="ghost" size="sm" aria-label={t.cancelUpload(name)} onClick={cancel}>
            {t.cancel}
          </Button>
        )}
        {trashedId && (
          <Button kind="ghost" size="sm" aria-label={t.uploadRestoreLabel(name)} disabled={busy} onClick={restore}>
            {t.restore}
          </Button>
        )}
        {retry && (
          <Button kind="ghost" size="sm" aria-label={t.retryUpload(name)} onClick={retry}>
            {t.tryAgain}
          </Button>
        )}
      </div>
      {error && (
        <p role="alert" className="app-text-error">
          {error}
        </p>
      )}
      {item.state.kind === "uploading" && <ProgressBar className="upload-row__progress" size="small" hideLabel label={name} max={100} value={item.state.percent} />}
    </li>
  );
}
