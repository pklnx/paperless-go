import { useState } from "react";
import { Button, Tag } from "@carbon/react";
import { documentsApi, type DocumentSummary } from "./api";
import { t } from "./i18n";
import { displayName } from "./uploads";

// Parts of a document shared by the list and the detail.

/** The title a document shows: its title, else its file name, else "Untitled". Bidi controls are dropped. */
export const documentName = (d: Pick<DocumentSummary, "title" | "originalFileName">) => displayName(d.title) || displayName(d.originalFileName) || t.untitled;

/**
 * A badge (a Carbon Tag) while a document is queued or being processed;
 * when processing failed, the error and a Retry button. Nothing once it
 * is done.
 */
export function ProcessingStatus({ doc, onChange }: { doc: DocumentSummary; onChange: (d: DocumentSummary) => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const p = doc.processing;
  if (!p || p.state === "done") return null;
  if (p.state === "queued" || p.state === "running") {
    const text = p.state === "running" ? t.processingRunning : p.error ? t.processingRetrying : t.processingQueued;
    return (
      <span className="doc-processing" title={p.error || undefined}>
        <Tag as="span" size="sm" type="gray" role="status">
          {text}
        </Tag>
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
    <div className="doc-processing doc-processing--failed">
      <Tag as="span" size="sm" type="red">
        {t.processingFailed}
      </Tag>
      {p.error && <span className="doc-processing__error app-text-error">{p.error}</span>}
      <Button kind="ghost" size="sm" disabled={busy} aria-label={t.retryDocument(documentName(doc))} onClick={retry}>
        {t.retry}
      </Button>
      {error && (
        <span role="alert" className="app-text-error">
          {error}
        </span>
      )}
    </div>
  );
}
