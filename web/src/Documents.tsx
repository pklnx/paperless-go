import { useEffect, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { ApiError, documentsApi, type DocumentSummary } from "./api";
import { lang, t } from "./i18n";
import { snippetParts } from "./snippet";
import { button, input, primary } from "./styles";
import { displayName, formatSize, stateText, type UploadItem, type UploadQueue } from "./uploads";

/** How many documents the start page lists; the document list (#7) shows all. */
const recentCount = 10;

/** How many search results a page holds. */
export const searchPageSize = 25;

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
    documentsApi.list(recentCount, ctl.signal).then(
      (res) => {
        setDocs(res.documents);
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
      <DocumentSearch>
        <RecentDocuments docs={docs} error={error} />
      </DocumentSearch>
    </div>
  );
}

function RecentDocuments({ docs, error }: { docs: DocumentSummary[] | null; error: string }) {
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
      {docs && docs.length > 0 && <DocumentList label={t.recentTitle} docs={docs} />}
    </section>
  );
}

function DocumentList({ label, docs }: { label: string; docs: DocumentSummary[] }) {
  return (
    <ul aria-label={label} className="flex flex-col divide-y divide-line rounded-lg border border-line">
      {docs.map((d) => (
        <li key={d.id} className="flex flex-wrap items-baseline gap-x-3 gap-y-1 px-3 py-2">
          <span className="min-w-0 basis-full wrap-anywhere sm:basis-0 sm:flex-1">{displayName(d.title) || t.untitled}</span>
          <span className="text-sm text-ink-muted">{new Date(d.createdAt).toLocaleString(lang, { dateStyle: "medium", timeStyle: "short" })}</span>
          <a className="text-sm text-accent-ink hover:underline" href={documentsApi.fileURL(d.id)} download>
            {t.download}
          </a>
          {d.snippet && <Snippet text={d.snippet} />}
        </li>
      ))}
    </ul>
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
          {search.docs && search.docs.length > 0 && <DocumentList label={t.searchResults} docs={search.docs} />}
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
