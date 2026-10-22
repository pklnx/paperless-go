import { useEffect, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { ApiError, documentsApi, processingPending, type DocumentSummary } from "./api";
import { lang, t } from "./i18n";
import { snippetParts } from "./snippet";
import { button, input, primary } from "./styles";
import { displayName, formatSize, stateText, type UploadItem, type UploadQueue } from "./uploads";

/** How many documents the start page lists; the document list (#7) shows all. */
const recentCount = 10;

/** How many search results a page holds. */
export const searchPageSize = 25;

/** How long the start page waits before it loads the list again while documents are being processed. */
export const pollDelay = 5000;

/** The timer of the polling; tests replace it to fire it themselves. */
export const pollTimer = {
  set: (fn: () => void, ms: number): number => window.setTimeout(fn, ms),
  clear: (id: number) => window.clearTimeout(id),
};

/** Replaces the document with the same ID as d in docs. */
const replaceDoc = (docs: DocumentSummary[], d: DocumentSummary) => docs.map((x) => (x.id === d.id ? { ...d, snippet: x.snippet } : x));

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
  const [poll, setPoll] = useState(0);
  /** Counts the loads that ended, with or without success, so the polling goes on after a failed one. */
  const [loaded, setLoaded] = useState(0);
  const imported = items.filter((i) => i.state.kind === "imported").length;

  // Load the list at start, again after each import, and while documents
  // are being processed every pollDelay.
  useEffect(() => {
    const ctl = new AbortController();
    documentsApi.list(recentCount, ctl.signal).then(
      (res) => {
        setDocs(res.documents);
        queue.maxSize = res.maxUploadSize;
        setMaxSize(res.maxUploadSize);
        setError("");
        setLoaded((n) => n + 1);
      },
      (err: unknown) => {
        // An aborted load is followed by another one (or the page is
        // gone): it arms no timer.
        if (ctl.signal.aborted) return;
        setError(t.failed(err instanceof Error ? err.message : String(err)));
        setLoaded((n) => n + 1);
      },
    );
    return () => ctl.abort();
  }, [queue, imported, poll]);

  // Each ended load arms the timer again while a document is pending, also
  // a failed one: while the server stays down, the page keeps trying every
  // pollDelay, and the error stays until a load succeeds. Polling ends when
  // the list shows no pending document.
  const pending = docs?.some(processingPending) ?? false;
  useEffect(() => {
    if (!pending) return;
    const id = pollTimer.set(() => setPoll((n) => n + 1), pollDelay);
    return () => pollTimer.clear(id);
  }, [pending, docs, loaded]);

  const limit = maxSize ? formatSize(maxSize, lang) : "";
  return (
    <div className="flex flex-col gap-6">
      <UploadPanel queue={queue} items={items} limit={limit} />
      <DocumentSearch>
        <RecentDocuments docs={docs} error={error} onChange={(d) => setDocs((ds) => ds && replaceDoc(ds, d))} />
      </DocumentSearch>
    </div>
  );
}

function RecentDocuments({ docs, error, onChange }: { docs: DocumentSummary[] | null; error: string; onChange: (d: DocumentSummary) => void }) {
  return (
    <section className="flex flex-col gap-2">
      <h2 className="font-semibold">{t.recentTitle}</h2>
      {error && (
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
      )}
      {!docs && !error && <p className="text-sm text-ink-muted">{t.loading}</p>}
      {docs && docs.length === 0 && <p className="text-sm text-ink-muted">{t.recentEmpty}</p>}
      {docs && docs.length > 0 && <DocumentList label={t.recentTitle} docs={docs} onChange={onChange} />}
    </section>
  );
}

function DocumentList({ label, docs, onChange }: { label: string; docs: DocumentSummary[]; onChange: (d: DocumentSummary) => void }) {
  return (
    <ul aria-label={label} className="flex flex-col divide-y divide-line rounded-lg border border-line">
      {docs.map((d) => (
        <li key={d.id} className="flex items-start gap-3 px-3 py-2">
          <Thumbnail url={d.thumbnailUrl} />
          <div className="flex min-w-0 flex-1 flex-wrap items-baseline gap-x-3 gap-y-1">
            <span className="min-w-0 basis-full wrap-anywhere sm:basis-0 sm:flex-1">{displayName(d.title) || t.untitled}</span>
            <span className="text-sm text-ink-muted">{new Date(d.createdAt).toLocaleString(lang, { dateStyle: "medium", timeStyle: "short" })}</span>
            <a className="text-sm text-accent-ink hover:underline" href={documentsApi.fileURL(d.id)} download>
              {t.download}
            </a>
            <ProcessingStatus doc={d} onChange={onChange} />
            {d.snippet && <Snippet text={d.snippet} />}
          </div>
        </li>
      ))}
    </ul>
  );
}

/** The thumbnail of the first page, or an empty frame until processing made one. It is decoration: the title is next to it. */
function Thumbnail({ url }: { url?: string | null }) {
  const frame = "h-16 w-12 shrink-0 rounded-sm border border-line";
  if (!url) return <div aria-hidden="true" data-testid="no-thumbnail" className={`${frame} border-dashed`} />;
  return <img src={url} alt="" loading="lazy" decoding="async" className={`${frame} bg-paper object-cover object-top`} />;
}

const badge = "rounded-sm px-1.5 py-0.5 text-xs font-medium";

/**
 * A badge while a document is queued or being processed; when processing
 * failed, the error and a Retry button. Nothing once it is done.
 */
function ProcessingStatus({ doc, onChange }: { doc: DocumentSummary; onChange: (d: DocumentSummary) => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const p = doc.processing;
  if (!p || p.state === "done") return null;
  if (p.state === "queued" || p.state === "running") {
    const text = p.state === "running" ? t.processingRunning : p.error ? t.processingRetrying : t.processingQueued;
    return (
      <span role="status" className={`${badge} bg-surface-hover text-ink-muted`} title={p.error}>
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
    <div className="flex basis-full flex-wrap items-baseline gap-x-2 gap-y-1 text-sm">
      <span className={`${badge} bg-danger-soft text-danger`}>{t.processingFailed}</span>
      {p.error && <span className="min-w-0 wrap-anywhere text-danger">{p.error}</span>}
      <button type="button" className="text-accent-ink hover:underline disabled:opacity-50" disabled={busy} aria-label={t.retryDocument(displayName(doc.title) || t.untitled)} onClick={retry}>
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

/** A search snippet: its matches as <mark>, everything else as text. */
export function Snippet({ text }: { text: string }) {
  const parts = snippetParts(text);
  if (parts.length === 0) return null;
  return (
    <p className="basis-full text-sm wrap-anywhere text-ink-muted">
      {parts.map((p, i) =>
        p.mark ? (
          <mark key={i} className="rounded-sm bg-highlight px-0.5 text-ink">
            {p.text}
          </mark>
        ) : (
          <span key={i}>{p.text}</span>
        ),
      )}
    </p>
  );
}

const searchError = (err: unknown) =>
  err instanceof ApiError && err.status === 400 && err.message === "query too long" ? t.searchTooLong : t.failed(err instanceof Error ? err.message : String(err));

interface SearchState {
  q: string;
  docs: DocumentSummary[] | null;
  cursor: string;
  loading: boolean;
  error: string;
}

/**
 * The search field of the start page. It searches on Enter (each search is
 * a document.search entry in the log, so not on every key) and shows the
 * results with snippets and "Load more" in place of children; an empty
 * field shows children again.
 */
function DocumentSearch({ children }: { children: ReactNode }) {
  const [text, setText] = useState("");
  const [search, setSearch] = useState<SearchState | null>(null);
  const ctl = useRef<AbortController | null>(null);
  useEffect(() => () => ctl.current?.abort(), []);

  const load = (q: string, cursor: string) => {
    ctl.current?.abort();
    const c = new AbortController();
    ctl.current = c;
    setSearch((s) => (cursor && s ? { ...s, loading: true, error: "" } : { q, docs: null, cursor: "", loading: true, error: "" }));
    documentsApi.search(q, searchPageSize, cursor, c.signal).then(
      (page) => {
        if (c.signal.aborted) return;
        setSearch((s) => ({
          q,
          docs: cursor && s?.docs ? [...s.docs, ...page.documents] : page.documents,
          cursor: page.nextCursor ?? "",
          loading: false,
          error: "",
        }));
      },
      (err: unknown) => {
        if (c.signal.aborted) return;
        setSearch((s) => ({ q, docs: cursor ? (s?.docs ?? null) : null, cursor: cursor ? (s?.cursor ?? "") : "", loading: false, error: searchError(err) }));
      },
    );
  };

  const clear = () => {
    ctl.current?.abort();
    setSearch(null);
  };

  return (
    <>
      <form
        role="search"
        className="flex gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          if (text.trim()) load(text, "");
          else clear();
        }}
      >
        <input
          className={input}
          type="search"
          aria-label={t.searchLabel}
          placeholder={t.searchPlaceholder}
          maxLength={1000}
          enterKeyHint="search"
          value={text}
          onChange={(e) => {
            setText(e.target.value);
            if (e.target.value === "") clear();
          }}
        />
        <button type="submit" className={button}>
          {t.searchButton}
        </button>
      </form>
      {search ? (
        <section className="flex flex-col gap-2">
          <h2 className="font-semibold">{t.searchResults}</h2>
          {search.error && (
            <p role="alert" className="text-sm text-danger">
              {search.error}
            </p>
          )}
          {search.loading && !search.docs && <p className="text-sm text-ink-muted">{t.loading}</p>}
          {search.docs && search.docs.length === 0 && <p className="text-sm text-ink-muted">{t.searchEmpty}</p>}
          {search.docs && search.docs.length > 0 && (
            <DocumentList label={t.searchResults} docs={search.docs} onChange={(d) => setSearch((s) => s && s.docs && { ...s, docs: replaceDoc(s.docs, d) })} />
          )}
          {search.cursor && (
            <button type="button" className={`${button} self-start`} disabled={search.loading} onClick={() => load(search.q, search.cursor)}>
              {t.searchMore}
            </button>
          )}
        </section>
      ) : (
        children
      )}
    </>
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
      </div>
      {item.state.kind === "uploading" && (
        <progress className="h-1.5 w-full" max={100} value={item.state.percent} aria-label={name} />
      )}
    </li>
  );
}
