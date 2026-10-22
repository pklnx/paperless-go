import { useEffect, useRef, useState } from "react";
import { ApiError, documentsApi, processingPending, type DocumentPage, type DocumentQuery, type DocumentSummary, type Metadata, type User } from "./api";
import { DocumentDetail } from "./DocumentDetail";
import { documentName, ProcessingStatus } from "./DocumentParts";
import { EmptyState } from "./EmptyState";
import { ArrowIcon, CloseIcon, DocumentIcon, InboxIcon, SearchIcon, StarIcon } from "./icons";
import { lang, t } from "./i18n";
import { snippetParts } from "./snippet";
import { button, input } from "./styles";
import { chipInk } from "./tagTree";
import { activeFilter, filterPatch, formatState, type UpdateState, type ViewState } from "./urlState";

/** How many documents a page of the list holds. */
export const pageSize = 50;

/** How long the list waits before it loads its first page again while documents are being processed. */
export const pollDelay = 5000;

/** The timer of the polling; tests replace it to fire it themselves. */
export const pollTimer = {
  set: (fn: () => void, ms: number): number => window.setTimeout(fn, ms),
  clear: (id: number) => window.clearTimeout(id),
};

const errorText = (err: unknown) =>
  err instanceof ApiError && err.status === 400 && err.message === "query too long" ? t.searchTooLong : t.failed(err instanceof Error ? err.message : String(err));


interface ListState {
  /** The request these documents answer (JSON of the DocumentQuery). */
  key: string;
  docs: DocumentSummary[] | null;
  /** The IDs of the first page: the polling refreshes these. */
  first: Set<string>;
  cursor: string;
  loading: boolean;
  error: string;
}

interface Props {
  state: ViewState;
  update: UpdateState;
  /** The request of the list (urlState.documentQuery). */
  query: DocumentQuery;
  user: User;
  /** For names of correspondents, types and tags; null until loaded. */
  metadata: Metadata | null;
  /** Whether an admin's list holds everyone's documents: rows of other users then name the owner. */
  everyone: boolean;
  /** Changes after each import, to load the list again. */
  reload: number;
  /** The upload limit from the server's answer. */
  onMaxUploadSize: (bytes: number) => void;
  /** A document was changed in its detail: counts and names may differ now. */
  onChanged?: () => void;
}

/**
 * The document list: the search field, the active filter as a chip, the
 * sort, the documents with "Load more" (keyset paging), and an empty state
 * for each case.
 */
export function DocumentList({ state, update, query, user, metadata, everyone, reload, onMaxUploadSize, onChanged }: Props) {
  const key = JSON.stringify(query);
  const [list, setList] = useState<ListState>({ key, docs: null, first: new Set(), cursor: "", loading: true, error: "" });
  const ctl = useRef<AbortController | null>(null);
  const [poll, setPoll] = useState(0);
  const onMax = useRef(onMaxUploadSize);
  onMax.current = onMaxUploadSize;
  const queryRef = useRef(query);
  queryRef.current = query;

  // One request at a time per controller: a new list or page aborts the
  // previous one; the polling has its own, so it never aborts "Load more".
  const pollCtl = useRef<AbortController | null>(null);
  const fetchPage = (which: typeof ctl, cursor: string, then: (page: DocumentPage) => void, failed: (err: unknown) => void) => {
    which.current?.abort();
    const c = new AbortController();
    which.current = c;
    documentsApi.list(queryRef.current, pageSize, cursor, c.signal).then(
      (page) => {
        if (c.signal.aborted) return;
        onMax.current(page.maxUploadSize);
        then(page);
      },
      (err: unknown) => {
        if (!c.signal.aborted) failed(err);
      },
    );
  };

  // The first page: on each new request, and after an import. Typed
  // searches are not loaded again after an import, since each first page
  // of a search is a document.search entry in the log.
  const reloadKey = query.q ? 0 : reload;
  useEffect(() => {
    // The same list loaded again (after an import) stays until the new page is there.
    setList((l) => (l.key === key ? { ...l, loading: true } : { key, docs: null, first: new Set(), cursor: "", loading: true, error: "" }));
    pollCtl.current?.abort();
    fetchPage(
      ctl,
      "",
      (page) => setList({ key, docs: page.documents, first: new Set(page.documents.map((d) => d.id)), cursor: page.nextCursor ?? "", loading: false, error: "" }),
      (err) => setList({ key, docs: null, first: new Set(), cursor: "", loading: false, error: errorText(err) }),
    );
    return () => {
      ctl.current?.abort();
      pollCtl.current?.abort();
    };
    // fetchPage reads the query through a ref; key stands for it.
  }, [key, reloadKey]);

  // While documents of the first page are being processed, that page is
  // loaded again every pollDelay and its documents replaced in place. Not
  // for a typed search (see above). Each poll that ends, with or without
  // success, arms the timer again while documents are pending; a failed
  // one keeps the list as it was.
  const [polled, setPolled] = useState(0);
  useEffect(() => {
    if (poll === 0) return;
    fetchPage(
      pollCtl,
      "",
      (page) => {
        const byId = new Map(page.documents.map((d) => [d.id, d]));
        setList((l) => (l.key !== key || !l.docs ? l : { ...l, docs: l.docs.map((d) => byId.get(d.id) ?? d) }));
        setPolled((n) => n + 1);
      },
      () => setPolled((n) => n + 1),
    );
  }, [poll]);
  const pending = !query.q && list.key === key && (list.docs?.some((d) => list.first.has(d.id) && processingPending(d)) ?? false);
  useEffect(() => {
    if (!pending) return;
    const id = pollTimer.set(() => setPoll((n) => n + 1), pollDelay);
    return () => pollTimer.clear(id);
  }, [pending, list, polled]);

  const loadMore = () => {
    const cursor = list.cursor;
    setList((l) => ({ ...l, loading: true, error: "" }));
    fetchPage(
      ctl,
      cursor,
      (page) => setList((l) => ({ ...l, docs: [...(l.docs ?? []), ...page.documents], cursor: page.nextCursor ?? "", loading: false, error: "" })),
      (err) => setList((l) => ({ ...l, loading: false, error: errorText(err) })),
    );
  };

  const replaceDoc = (d: DocumentSummary) => setList((l) => l.docs ? { ...l, docs: l.docs.map((x) => (x.id === d.id ? { ...d, snippet: x.snippet } : x)) } : l);
  // A saved document: its row follows. The inbox list drops a document
  // that left the inbox, as its next load would.
  const savedDoc = (d: DocumentSummary) => {
    if (query.inbox && !d.isInbox) setList((l) => (l.docs ? { ...l, docs: l.docs.filter((x) => x.id !== d.id), first: new Set([...l.first].filter((x) => x !== d.id)) } : l));
    else replaceDoc(d);
    onChanged?.();
  };
  const openDoc = (id: string) => update({ doc: id });

  const filter = activeFilter(state);
  // The chip says what kind of filter it is ("Tag: Steuer"); the heading names the list.
  const [filterName, listName] = (() => {
    if (!filter) return ["", ""];
    if (filter.kind === "inbox") return [t.inbox, t.inbox];
    if (filter.kind === "fav") return [t.favorites, t.favorites];
    const rows = filter.kind === "tag" ? metadata?.tags : filter.kind === "corr" ? metadata?.correspondents : metadata?.documentTypes;
    const name = rows?.find((r) => r.id === filter.id)?.name ?? t.unknownEntry;
    return [filter.kind === "tag" ? t.tagFilter(name) : filter.kind === "corr" ? t.correspondentFilter(name) : t.typeFilter(name), name];
  })();
  const title = query.q ? t.searchResults : filter ? listName : t.allDocuments;
  const docs = list.key === key ? list.docs : null;

  const detail = state.doc !== "" && state.view === "";
  return (
    <>
    {detail && (
      <DocumentDetail id={state.doc} user={user} docs={docs ?? []} inboxList={query.inbox} open={openDoc} close={() => update({ doc: "" })} onSaved={savedDoc} />
    )}
    <section className="flex flex-col gap-3" aria-labelledby="list-title" inert={detail}>
      <SearchField q={state.q} search={(q) => update({ q, doc: "" })} />
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 id="list-title" className="min-w-0 font-semibold wrap-anywhere">
          {title}
        </h2>
        <SortControls state={state} query={query} update={update} />
      </div>
      {filter && (
        <div className="flex flex-wrap gap-2">
          <span className="flex h-8 items-center gap-1 rounded-full border border-accent-soft bg-accent-soft pl-3 text-[13px] font-medium text-accent-soft-ink pointer-coarse:h-11">
            <span className="wrap-anywhere">{filterName}</span>
            <button type="button" aria-label={t.removeFilter(filterName)} title={t.removeFilter(filterName)} className="grid size-7 place-items-center rounded-full pointer-coarse:size-11" onClick={() => update(filterPatch(null))}>
              <CloseIcon />
            </button>
          </span>
        </div>
      )}
      {list.error && (
        <p role="alert" className="text-sm text-danger">
          {list.error}
        </p>
      )}
      {!docs && list.loading && <p className="text-sm text-ink-muted">{t.loading}</p>}
      {docs && docs.length === 0 && <Empty query={query} clearSearch={() => update({ q: "" })} showAll={() => update(filterPatch(null))} />}
      {docs && docs.length > 0 && (
        <ul aria-label={title} className="flex flex-col gap-2 md:gap-0 md:divide-y md:divide-line md:rounded-lg md:border md:border-line">
          {docs.map((d) => (
            <DocumentRow key={d.id} doc={d} metadata={metadata} owner={everyone && d.ownerName && d.ownerName !== user.name ? d.ownerName : ""} onChange={replaceDoc} href={formatState({ ...state, doc: d.id }) || "?"} onOpen={() => openDoc(d.id)} />
          ))}
        </ul>
      )}
      {docs && docs.length > 0 && list.cursor && (
        <button type="button" className={`${button} self-start`} disabled={list.loading} onClick={loadMore}>
          {t.loadMore}
        </button>
      )}
    </section>
    </>
  );
}

/** The search field. It searches on Enter (each search is a document.search entry in the log, so not on every key); emptying it lists everything again. */
function SearchField({ q, search }: { q: string; search: (q: string) => void }) {
  const [text, setText] = useState(q);
  // Follow the URL when it changes from outside (back button, a cleared search).
  useEffect(() => setText(q), [q]);
  return (
    <form
      role="search"
      className="flex gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        search(text.trim() ? text : "");
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
          if (e.target.value === "" && q !== "") search("");
        }}
      />
      <button type="submit" className={`${button} flex items-center gap-1.5`}>
        <SearchIcon />
        <span className="max-sm:sr-only">{t.searchButton}</span>
      </button>
    </form>
  );
}

/** The sort: added or document date, either direction; relevance (one direction) while searching. */
function SortControls({ state, query, update }: { state: ViewState; query: DocumentQuery; update: UpdateState }) {
  const searching = query.q !== "";
  const sort = query.sort || (searching ? "relevance" : "added");
  const relevance = sort === "relevance";
  return (
    <div className="flex items-center gap-1">
      <label className="flex items-center gap-1.5 text-sm text-ink-muted">
        <span className="max-sm:sr-only">{t.sortBy}</span>
        <select
          className={`${input} w-auto py-1 text-ink`}
          value={sort}
          onChange={(e) => {
            const v = e.target.value as ViewState["sort"];
            // The default stays out of the URL.
            update({ sort: v === (searching ? "relevance" : "added") ? "" : v, order: v === "relevance" ? "" : state.order });
          }}
        >
          {searching && <option value="relevance">{t.sortRelevance}</option>}
          <option value="added">{t.sortAdded}</option>
          <option value="date">{t.sortDate}</option>
        </select>
      </label>
      <button
        type="button"
        className={`${button} flex items-center gap-1 disabled:opacity-40`}
        disabled={relevance}
        aria-label={query.ascending ? t.sortOldestFirst : t.sortNewestFirst}
        title={relevance ? t.sortRelevanceOrder : query.ascending ? t.sortOldestFirst : t.sortNewestFirst}
        onClick={() => update({ order: query.ascending ? "" : "asc" })}
      >
        <ArrowIcon up={query.ascending} />
      </button>
    </div>
  );
}

function Empty({ query, clearSearch, showAll }: { query: DocumentQuery; clearSearch: () => void; showAll: () => void }) {
  const filtered = query.inbox || query.favorite || query.tag !== "" || query.correspondent !== "" || query.type !== "";
  if (query.q) {
    return (
      <EmptyState
        icon={<SearchIcon />}
        title={t.nothingFound}
        actions={
          <button type="button" className={button} onClick={clearSearch}>
            {t.clearSearch}
          </button>
        }
      >
        {t.nothingFoundSearch}
      </EmptyState>
    );
  }
  if (query.inbox && !query.favorite && !query.tag && !query.correspondent && !query.type) {
    return (
      <EmptyState icon={<InboxIcon />} title={t.inboxEmpty}>
        {t.inboxEmptyText}
      </EmptyState>
    );
  }
  if (filtered) {
    return (
      <EmptyState
        icon={<SearchIcon />}
        title={t.nothingFound}
        actions={
          <button type="button" className={button} onClick={showAll}>
            {t.showAllDocuments}
          </button>
        }
      >
        {t.nothingFoundFilter}
      </EmptyState>
    );
  }
  return (
    <EmptyState icon={<DocumentIcon />} title={t.noDocuments}>
      {t.noDocumentsText}
    </EmptyState>
  );
}

const day = (iso: string) => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleDateString(lang, { dateStyle: "medium" });
};

interface RowProps {
  doc: DocumentSummary;
  metadata: Metadata | null;
  owner: string;
  onChange: (d: DocumentSummary) => void;
  /** The detail's URL, so the title is a real link (a new tab works). */
  href: string;
  onOpen: () => void;
}

function DocumentRow({ doc: d, metadata, owner, onChange, href, onOpen }: RowProps) {
  const name = documentName(d);
  const correspondent = d.correspondentId ? metadata?.correspondents.find((c) => c.id === d.correspondentId)?.name : undefined;
  const type = d.documentTypeId ? metadata?.documentTypes.find((c) => c.id === d.documentTypeId)?.name : undefined;
  const tags = (d.tagIds ?? []).flatMap((id) => metadata?.tags.find((x) => x.id === id) ?? []);
  return (
    <li className="flex items-start gap-3 rounded-lg border border-line px-3 py-3 md:rounded-none md:border-0 md:py-2">
      <Thumbnail url={d.thumbnailUrl} />
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
          <a
            href={href}
            data-doc-link={d.id}
            className="min-w-0 basis-full font-medium wrap-anywhere hover:underline sm:basis-0 sm:flex-1 pointer-coarse:min-h-11"
            onClick={(e) => {
              if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
              e.preventDefault();
              onOpen();
            }}
          >
            {name}
          </a>
          {d.isInbox && (
            <span className="flex items-center gap-1 text-xs text-accent-ink" title={t.inInbox}>
              <InboxIcon />
              <span className="sr-only">{t.inInbox}</span>
            </span>
          )}
          {d.isFavorite && (
            <span className="flex items-center text-warning" title={t.favorite}>
              <StarIcon filled />
              <span className="sr-only">{t.favorite}</span>
            </span>
          )}
          <span className="text-sm text-ink-muted">{day(d.documentDate)}</span>
        </div>
        <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1 text-sm text-ink-muted">
          {correspondent && <span className="wrap-anywhere">{correspondent}</span>}
          {type && <span className="wrap-anywhere">{type}</span>}
          {owner && <span className="wrap-anywhere">{t.ownedBy(owner)}</span>}
          <a className="text-accent-ink hover:underline pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center" href={documentsApi.fileURL(d.id)} download>
            {t.download}
          </a>
        </div>
        {tags.length > 0 && (
          <ul aria-label={t.tags} className="flex flex-wrap gap-1">
            {tags.map((x) => (
              <li
                key={x.id}
                className={`rounded-full px-2 py-0.5 text-xs font-medium wrap-anywhere ${x.color ? "" : "bg-surface-hover text-ink"}`}
                style={x.color ? { backgroundColor: x.color, color: chipInk(x.color) } : undefined}
              >
                {x.name}
              </li>
            ))}
          </ul>
        )}
        <ProcessingStatus doc={d} onChange={onChange} />
        {d.snippet && <Snippet text={d.snippet} />}
      </div>
    </li>
  );
}

/** The thumbnail of the first page, or an empty frame until processing made one. It is decoration: the title is next to it. */
function Thumbnail({ url }: { url?: string | null }) {
  const frame = "h-16 w-12 shrink-0 rounded-sm border border-line";
  if (!url) return <div aria-hidden="true" data-testid="no-thumbnail" className={`${frame} border-dashed`} />;
  return <img src={url} alt="" loading="lazy" decoding="async" className={`${frame} bg-paper object-cover object-top`} />;
}

/** A search snippet: its matches as <mark>, everything else as text. */
export function Snippet({ text }: { text: string }) {
  const parts = snippetParts(text);
  if (parts.length === 0) return null;
  return (
    <p className="text-sm wrap-anywhere text-ink-muted">
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
