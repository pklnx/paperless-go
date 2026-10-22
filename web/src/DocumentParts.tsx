import { useState } from "react";
import { documentsApi, type DocumentSummary } from "./api";
import { t } from "./i18n";
import { displayName } from "./uploads";

// Parts of a document shared by the list and the detail.

/** The title a document shows: its title, else its file name, else "Untitled". Bidi controls are dropped. */
export const documentName = (d: Pick<DocumentSummary, "title" | "originalFileName">) => displayName(d.title) || displayName(d.originalFileName) || t.untitled;

const badge = "rounded-sm px-1.5 py-0.5 text-xs font-medium";

/**
 * A badge while a document is queued or being processed; when processing
 * failed, the error and a Retry button. Nothing once it is done.
 */
export function ProcessingStatus({ doc, onChange }: { doc: DocumentSummary; onChange: (d: DocumentSummary) => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const p = doc.processing;
  if (!p || p.state === "done") return null;
  if (p.state === "queued" || p.state === "running") {
    const text = p.state === "running" ? t.processingRunning : p.error ? t.processingRetrying : t.processingQueued;
    return (
      <span role="status" className={`${badge} self-start bg-surface-hover text-ink-muted`} title={p.error}>
        {text}
      </span>
    );
  }
  const retry = () => {
    setBusy(true);
    setError("");
    documentsApi.reprocess(doc.id).then(
      (d) => {
        setBusy(false);
        onChange(d);
      },
      (err: unknown) => {
        setBusy(false);
        setError(t.failed(err instanceof Error ? err.message : String(err)));
      },
    );
  };
  return (
    <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1 text-sm">
      <span className={`${badge} bg-danger-soft text-danger`}>{t.processingFailed}</span>
      {p.error && <span className="min-w-0 wrap-anywhere text-danger">{p.error}</span>}
      <button type="button" className="text-accent-ink hover:underline disabled:opacity-50 pointer-coarse:min-h-11" disabled={busy} aria-label={t.retryDocument(documentName(doc))} onClick={retry}>
        {t.retry}
      </button>
      {error && (
        <span role="alert" className="text-danger">
          {error}
        </span>
      )}
    </div>
  );
}

