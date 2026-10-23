import { useEffect, useRef, useState } from "react";
import { ApiError, bulkApi, bulkMatch, documentsApi, processingPending, type DocumentPage, type DocumentQuery, type DocumentSummary, type Metadata, type User } from "./api";
import { BulkBar, bulkErrorText, type BulkTarget } from "./BulkBar";
import { DocumentDetail } from "./DocumentDetail";
import { documentName, ProcessingStatus } from "./DocumentParts";
import { EmptyState } from "./EmptyState";
import { ArrowIcon, CloseIcon, DocumentIcon, InboxIcon, SearchIcon, StarIcon, TrashIcon } from "./icons";
import { lang, t } from "./i18n";
import { clickSelect, pruneSelection, selectAll, selectionLimit, useLongPress } from "./selection";
import { snippetParts } from "./snippet";
import { button, input } from "./styles";
import { chipInk } from "./tagTree";
import type { Notice, Notify } from "./Toast";
import { EmptyTrash, foreignOwners, PurgeConfirm, trashDay, trashErrorText } from "./Trash";
import { activeFilter, filterPatch, formatState, parseState, type UpdateState, type ViewState } from "./urlState";

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
  /** A document was changed in its detail, or several by a bulk change: counts and names may differ now. */
  onChanged?: () => void;
  /** Shows a notice of the shell (Undo, results): it outlives the list. */
  notify?: Notify;
  /** An Undo went through: the shell counts it (undone), and the list shown then loads again. */
  onUndone?: () => void;
  undone?: number;
  /** The caller's own trash count (Empty trash empties only that); null when unknown. */
  ownTrash?: number | null;
}

/**
 * The document list: the search field, the active filter as a chip, the
 * sort, the documents with "Load more" (keyset paging), and an empty state
 * for each case. The trash (state.trash) is the same list of trashed
 * documents, with Restore and Delete permanently instead of Delete, and
 * "Empty trash".
 */
export function DocumentList({ state, update, query, user, metadata, everyone, reload, onMaxUploadSize, onChanged, notify, onUndone, undone = 0, ownTrash = null }: Props) {
  const key = JSON.stringify(query);
  const [list, setList] = useState<ListState>({ key, docs: null, first: new Set(), cursor: "", loading: true, error: "" });
  const ctl = useRef<AbortController | null>(null);
  const [poll, setPoll] = useState(0);
  const onMax = useRef(onMaxUploadSize);
  onMax.current = onMaxUploadSize;
  const queryRef = useRef(query);
  queryRef.current = query;
  // The list on screen: answers started for another list are dropped.
  const keyRef = useRef(key);
  keyRef.current = key;

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

  const trash = query.trashed;
  const showNotice = (n: Notice) => notify?.(n);
  // Rows with a Delete or Restore in flight: a second click sends nothing.
  const busyRef = useRef(new Set<string>());
  const [busyRows, setBusyRows] = useState<ReadonlySet<string>>(() => new Set());
  const markBusy = (id: string, on: boolean) => {
    if (on) busyRef.current.add(id);
    else busyRef.current.delete(id);
    setBusyRows(new Set(busyRef.current));
  };
  // A document that left the list: deleted, restored from the trash, or deleted permanently.
  const removeDoc = (id: string) =>
    setList((l) => (l.docs ? { ...l, docs: l.docs.filter((x) => x.id !== id), first: new Set([...l.first].filter((x) => x !== id)) } : l));
  const replaceDoc = (d: DocumentSummary) => setList((l) => l.docs ? { ...l, docs: l.docs.map((x) => (x.id === d.id ? { ...d, snippet: x.snippet } : x)) } : l);
  // A saved document: its row follows. The inbox list drops a document
  // that left the inbox, as its next load would.
  const savedDoc = (d: DocumentSummary) => {
    if (query.inbox && !d.isInbox) setList((l) => (l.docs ? { ...l, docs: l.docs.filter((x) => x.id !== d.id), first: new Set([...l.first].filter((x) => x !== d.id)) } : l));
    else replaceDoc(d);
    onChanged?.();
  };
  const openDoc = (id: string) => update({ doc: id });
  // A delete answered late: the detail closes only if it still shows that
  // document (the user may have gone to another one, or closed it).
  const closeIfOpen = (id: string) => {
    if (parseState(window.location.search).doc === id) update({ doc: "" });
  };

  // Undo of a delete: restore, then the list as it is now (the latest
  // refresh, for the list shown when Undo is clicked).
  const refreshRef = useRef<() => void>(() => {});
  const undo = (d: DocumentSummary) => {
    documentsApi.restore(d.id).then(
      () => {
        showNotice({ text: t.restoredDocument(documentName(d)) });
        // Through the shell: the list shown now may be another instance.
        if (onUndone) onUndone();
        else refreshRef.current();
        onChanged?.();
      },
      (err: unknown) => showNotice({ text: trashErrorText(err, "restore"), error: true }),
    );
  };
  // A document moved to the trash (from its row or its detail): out of
  // the list, and a notice with Undo. No question first (#12, decision 4).
  const trashed = (d: DocumentSummary) => {
    removeDoc(d.id);
    onChanged?.();
    showNotice({ text: t.movedToTrash(documentName(d)), action: { label: t.undo, run: () => undo(d) }, focus: true });
  };
  const trashDoc = (d: DocumentSummary) => {
    if (busyRef.current.has(d.id)) return;
    markBusy(d.id, true);
    documentsApi.trash(d.id).then(
      () => {
        markBusy(d.id, false);
        trashed(d);
      },
      (err: unknown) => {
        markBusy(d.id, false);
        showNotice({ text: trashErrorText(err, "trash"), error: true, focus: true });
      },
    );
  };
  // Restored from the trash: out of the trash list, or its row follows.
  const restored = (d: DocumentSummary) => {
    if (trash) removeDoc(d.id);
    else replaceDoc(d);
    onChanged?.();
    showNotice({ text: t.restoredDocument(documentName(d)), focus: true });
  };
  const restoreDoc = (d: DocumentSummary) => {
    if (busyRef.current.has(d.id)) return;
    markBusy(d.id, true);
    documentsApi.restore(d.id).then(
      (back) => {
        markBusy(d.id, false);
        restored(back);
      },
      (err: unknown) => {
        markBusy(d.id, false);
        showNotice({ text: trashErrorText(err, "restore"), error: true, focus: true });
      },
    );
  };
  const purged = (d: DocumentSummary) => {
    removeDoc(d.id);
    onChanged?.();
    showNotice({ text: t.purgedDocument(documentName(d)), focus: true });
  };
  // Deleting permanently always asks first.
  const [purging, setPurging] = useState<{ doc: DocumentSummary; busy: boolean; error: string } | null>(null);
  // Which question is shown: an answer for one closed meanwhile (Cancel)
  // does not open it again; its error goes to the notice.
  const purgeAsk = useRef(0);
  const askPurge = (d: DocumentSummary | null) => {
    purgeAsk.current++;
    setPurging(d && { doc: d, busy: false, error: "" });
  };
  const purgeDoc = () => {
    if (!purging || purging.busy) return;
    const d = purging.doc;
    const mine = purgeAsk.current;
    setPurging({ doc: d, busy: true, error: "" });
    documentsApi.purge(d.id).then(
      () => {
        if (purgeAsk.current === mine) setPurging(null);
        purged(d);
      },
      (err: unknown) => {
        const text = trashErrorText(err, "purge");
        if (purgeAsk.current === mine) setPurging({ doc: d, busy: false, error: text });
        else showNotice({ text, error: true });
      },
    );
  };

  // After a bulk change: every loaded page again, in place, so the
  // rows and the selection stay where they were. Only the first page of
  // a typed search writes a document.search entry, as any reload does.
  const refresh = () => {
    // Started for a list that is no longer shown (the user moved on while
    // the change ran): the shown list has loaded on its own, and its
    // request must not be aborted.
    if (keyRef.current !== key) return;
    const want = list.docs?.length ?? 0;
    ctl.current?.abort();
    pollCtl.current?.abort();
    const c = new AbortController();
    ctl.current = c;
    setList((l) => ({ ...l, loading: true, error: "" }));
    void (async () => {
      const docs: DocumentSummary[] = [];
      let first: DocumentSummary[] | null = null;
      let cursor = "";
      do {
        const page = await documentsApi.list(queryRef.current, pageSize, cursor, c.signal);
        first ??= page.documents;
        docs.push(...page.documents);
        cursor = page.nextCursor ?? "";
      } while (cursor && docs.length < want);
      return { docs, first: first ?? [], cursor };
    })().then(
      (r) => {
        if (!c.signal.aborted && keyRef.current === key) setList({ key, docs: r.docs, first: new Set(r.first.map((d) => d.id)), cursor: r.cursor, loading: false, error: "" });
      },
      (err: unknown) => {
        if (!c.signal.aborted && keyRef.current === key) setList((l) => ({ ...l, loading: false, error: errorText(err) }));
      },
    );
  };
  refreshRef.current = refresh;
  // An Undo since this list was shown (not the count it started with).
  const seenUndone = useRef(undone);
  useEffect(() => {
    if (seenUndone.current === undone) return;
    seenUndone.current = undone;
    refreshRef.current();
  }, [undone]);

  // The selection: checked IDs of loaded documents (at most maxSelection),
  // or, after "Select all matches", every match of the search.
  const [selected, setSelected] = useState<Set<string>>(() => new Set());
  const anchor = useRef<string | null>(null);
  const [capped, setCapped] = useState(false);
  // "All matches": the count and owners of the dry run, for the list it was made for.
  const [matchSel, setMatchSel] = useState<{ key: string; matched: number; owners: number } | null>(null);
  const allMatches = matchSel !== null && matchSel.key === key ? matchSel.matched : null;
  const [selectError, setSelectError] = useState("");
  // Bumped by every choice of the selection, so a late "Select all
  // matches" count does not override a later choice in the same list.
  const selectionGen = useRef(0);
  const clearSelection = () => {
    selectionGen.current++;
    setSelected(new Set());
    anchor.current = null;
    setCapped(false);
    setMatchSel(null);
    setSelectError("");
  };
  // Another list: another selection.
  useEffect(clearSelection, [key]);
  const loaded = list.key === key ? list.docs : null;
  const order = (loaded ?? []).map((d) => d.id);
  // Documents that left the list (after a change, or a reload) leave the selection.
  const orderKey = order.join(",");
  useEffect(() => setSelected((s) => pruneSelection(s, order)), [orderKey]);
  const pick = (id: string, shift: boolean) => {
    selectionGen.current++;
    setMatchSel(null);
    setSelectError("");
    const r = clickSelect(order, selected, anchor.current, id, shift);
    if (!shift) anchor.current = id;
    setSelected(r.ids);
    setCapped(r.capped);
  };
  const allLoaded = order.length > 0 && (selected.size === Math.min(order.length, selectionLimit.max));
  const toggleAll = () => {
    selectionGen.current++;
    setMatchSel(null);
    setSelectError("");
    if (allLoaded) {
      clearSelection();
      return;
    }
    const r = selectAll(order);
    setSelected(r.ids);
    setCapped(r.capped);
  };
  const countMatches = () => {
    setSelectError("");
    const k = key;
    const gen = ++selectionGen.current;
    // In the trash a dry run needs an action that works there; the bar counts again for its own.
    bulkApi.count(bulkMatch(query), trash ? "restore" : undefined).then(
      (c) => {
        // Counted for a list that is no longer shown, or the selection was
        // chosen anew since: not this selection.
        if (keyRef.current !== k || selectionGen.current !== gen) return;
        if (c.matched > c.limit) setSelectError(`${t.bulkMatchCount(c.matched)} ${t.bulkTooMany(c.limit)}`);
        else if (c.matched === 0) setSelectError(t.bulkNoMatches);
        else setMatchSel({ key: k, matched: c.matched, owners: c.owners });
      },
      (err: unknown) => {
        if (keyRef.current === k && selectionGen.current === gen) setSelectError(bulkErrorText(err).text);
      },
    );
  };
  const selectedIds = order.filter((id) => selected.has(id));
  const target: BulkTarget | null = allMatches !== null ? { match: bulkMatch(query), matched: allMatches } : selectedIds.length > 0 ? { ids: selectedIds } : null;
  const owners = new Set((loaded ?? []).filter((d) => selected.has(d.id)).map((d) => d.ownerId));
  // The one owner of the selection, when known. For all matches the dry
  // run says how many owners there are; with one, every loaded row is a
  // match of it.
  const selectedOwner =
    allMatches !== null ? (matchSel?.owners === 1 ? (loaded?.[0]?.ownerId ?? null) : null) : owners.size === 1 ? ([...owners][0] ?? null) : null;
  // How many owners the selection has: the dry run's count for all matches.
  const selectedOwners = allMatches !== null ? (matchSel?.owners ?? 0) : owners.size;
  const selecting = target !== null;
  // An admin's checked documents of other users: the confirmations name their owners.
  const ownerNames = "ids" in (target ?? {}) ? foreignOwners((loaded ?? []).filter((d) => selected.has(d.id)), user) : [];

  const filter = activeFilter(state);
  // The chip says what kind of filter it is ("Tag: Steuer"); the heading names the list.
  const [filterName, listName] = (() => {
    if (!filter) return ["", ""];
    if (filter.kind === "inbox") return [t.inbox, t.inbox];
    if (filter.kind === "fav") return [t.favorites, t.favorites];
    if (filter.kind === "trash") return [t.trash, t.trash];
    const rows = filter.kind === "tag" ? metadata?.tags : filter.kind === "corr" ? metadata?.correspondents : metadata?.documentTypes;
    const name = rows?.find((r) => r.id === filter.id)?.name ?? t.unknownEntry;
    return [filter.kind === "tag" ? t.tagFilter(name) : filter.kind === "corr" ? t.correspondentFilter(name) : t.typeFilter(name), name];
  })();
  const title = trash ? t.trash : query.q ? t.searchResults : filter ? listName : t.allDocuments;
  const docs = list.key === key ? list.docs : null;

  const detail = state.doc !== "" && state.view === "";
  return (
    <>
    {detail && (
      <DocumentDetail
        id={state.doc}
        user={user}
        docs={docs ?? []}
        inboxList={query.inbox}
        open={openDoc}
        close={() => update({ doc: "" })}
        onSaved={savedDoc}
        onTrashed={(d) => {
          trashed(d);
          closeIfOpen(d.id);
        }}
        onRestored={restored}
        onStale={onChanged}
        onPurged={(d) => {
          purged(d);
          closeIfOpen(d.id);
        }}
        onTrashError={(text) => showNotice({ text, error: true })}
      />
    )}
    <section className="flex flex-col gap-3" aria-labelledby="list-title" inert={detail}>
      <SearchField q={state.q} search={(q) => update({ q, doc: "" })} />
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 id="list-title" tabIndex={-1} className="min-w-0 font-semibold wrap-anywhere outline-none">
          {title}
        </h2>
        <SortControls state={state} query={query} update={update} />
      </div>
      {/* Empty trash acts on the own trash, whatever the list shows: by its count, else by the rows. */}
      {trash && (ownTrash !== null ? ownTrash > 0 : (docs?.length ?? 0) > 0) && (
        <EmptyTrash
          everyone={everyone}
          notify={showNotice}
          onDone={() => {
            refresh();
            onChanged?.();
          }}
        />
      )}
      {filter && filter.kind !== "trash" && (
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
      {docs && docs.length > 0 && (
        <SelectionHeader
          allLoaded={allLoaded}
          some={selected.size > 0}
          count={allMatches ?? selected.size}
          capped={capped && allMatches === null}
          allMatches={allMatches}
          moreMatches={allMatches === null && allLoaded && (list.cursor !== "" || capped)}
          error={selectError}
          toggleAll={toggleAll}
          selectMatches={countMatches}
          clear={clearSelection}
        />
      )}
      {!docs && list.loading && <p className="text-sm text-ink-muted">{t.loading}</p>}
      {docs && docs.length === 0 && <Empty query={query} clearSearch={() => update({ q: "" })} showAll={() => update(filterPatch(null))} />}
      <PurgeConfirm
        open={purging !== null}
        title={purging ? t.purgeTitle(documentName(purging.doc)) : ""}
        n={1}
        owners={purging ? foreignOwners([purging.doc], user) : []}
        busy={purging?.busy}
        error={purging?.error}
        confirm={purgeDoc}
        cancel={() => askPurge(null)}
      />
      {docs && docs.length > 0 && (
        <ul aria-label={title} className="flex flex-col gap-2 md:gap-0 md:divide-y md:divide-line md:rounded-lg md:border md:border-line">
          {docs.map((d) => (
            <DocumentRow
              key={d.id}
              doc={d}
              metadata={metadata}
              owner={everyone && d.ownerName && d.ownerId !== user.id ? d.ownerName : ""}
              onChange={replaceDoc}
              href={formatState({ ...state, doc: d.id }) || "?"}
              onOpen={() => openDoc(d.id)}
              checked={allMatches !== null || selected.has(d.id)}
              selecting={selecting}
              onPick={(shift) => pick(d.id, shift)}
              trash={trash}
              busy={busyRows.has(d.id)}
              onDelete={() => trashDoc(d)}
              onRestore={() => restoreDoc(d)}
              onPurge={() => askPurge(d)}
            />
          ))}
        </ul>
      )}
      {docs && docs.length > 0 && list.cursor && (
        <button type="button" className={`${button} self-start`} disabled={list.loading} onClick={loadMore}>
          {t.loadMore}
        </button>
      )}
      {target && (
        <BulkBar
          target={target}
          metadata={metadata}
          ownerId={selectedOwner}
          owners={selectedOwners}
          trash={trash}
          ownerNames={ownerNames}
          purgeByMatch={!everyone}
          notify={showNotice}
          onDone={() => {
            // The count of all matches is stale after a change: back to the checked rows.
            setMatchSel(null);
            refresh();
            onChanged?.();
          }}
        />
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

/** The sort: added or document date, either direction; relevance (one direction) while searching; in the trash also deleted (newest first). */
function SortControls({ state, query, update }: { state: ViewState; query: DocumentQuery; update: UpdateState }) {
  const searching = query.q !== "";
  const trash = query.trashed;
  const fallback = searching ? "relevance" : trash ? "deleted" : "added";
  const sort = query.sort || fallback;
  // Relevance and deleted have one direction.
  const relevance = sort === "relevance" || sort === "deleted";
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
            update({ sort: v === fallback ? "" : v, order: v === "relevance" || v === "deleted" ? "" : state.order });
          }}
        >
          {searching && <option value="relevance">{t.sortRelevance}</option>}
          {trash && <option value="deleted">{t.sortDeleted}</option>}
          <option value="added">{t.sortAdded}</option>
          <option value="date">{t.sortDate}</option>
        </select>
      </label>
      <button
        type="button"
        className={`${button} flex items-center gap-1 disabled:opacity-40`}
        disabled={relevance}
        aria-label={query.ascending ? t.sortOldestFirst : t.sortNewestFirst}
        title={sort === "relevance" ? t.sortRelevanceOrder : query.ascending ? t.sortOldestFirst : t.sortNewestFirst}
        onClick={() => update({ order: query.ascending ? "" : "asc" })}
      >
        <ArrowIcon up={query.ascending} />
      </button>
    </div>
  );
}

function Empty({ query, clearSearch, showAll }: { query: DocumentQuery; clearSearch: () => void; showAll: () => void }) {
  if (query.trashed && !query.q) {
    return (
      <EmptyState icon={<TrashIcon />} title={t.trashEmpty}>
        {t.trashEmptyText}
      </EmptyState>
    );
  }
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

interface HeaderProps {
  allLoaded: boolean;
  some: boolean;
  count: number;
  capped: boolean;
  allMatches: number | null;
  /** Whether the search has more matches than are checked. */
  moreMatches: boolean;
  error: string;
  toggleAll: () => void;
  selectMatches: () => void;
  clear: () => void;
}

/** "Select all loaded", the count, and "Select all matches of this search" once every loaded one is checked. */
function SelectionHeader({ allLoaded, some, count, capped, allMatches, moreMatches, error, toggleAll, selectMatches, clear }: HeaderProps) {
  return (
    <div className="flex flex-col gap-1 text-sm">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <label className="flex items-center gap-2 pointer-coarse:min-h-11">
          <input
            type="checkbox"
            className="size-4 pointer-coarse:size-5"
            checked={allMatches !== null || allLoaded}
            ref={(el) => {
              if (el) el.indeterminate = some && !allLoaded && allMatches === null;
            }}
            onChange={toggleAll}
          />
          {t.selectAllLoaded}
        </label>
        {(some || allMatches !== null) && (
          <>
            <span className="text-ink-muted" role="status">
              {allMatches !== null ? t.allMatchesSelected(allMatches) : t.selectedCount(count)}
            </span>
            {moreMatches && (
              <button type="button" className="text-accent-ink hover:underline pointer-coarse:min-h-11" onClick={selectMatches}>
                {t.selectAllMatches}
              </button>
            )}
            <button type="button" className="text-accent-ink hover:underline pointer-coarse:min-h-11" onClick={clear}>
              {t.clearSelection}
            </button>
          </>
        )}
      </div>
      {capped && <p className="text-ink-muted">{t.selectionCapped(selectionLimit.max)}</p>}
      {error && (
        <p role="alert" className="text-danger">
          {error}
        </p>
      )}
    </div>
  );
}

interface RowProps {
  doc: DocumentSummary;
  metadata: Metadata | null;
  owner: string;
  onChange: (d: DocumentSummary) => void;
  /** The detail's URL, so the title is a real link (a new tab works). */
  href: string;
  onOpen: () => void;
  checked: boolean;
  /** Something is selected: on phones the checkboxes show (before, a long press starts selecting). */
  selecting: boolean;
  onPick: (shift: boolean) => void;
  /** The row is in the trash: its dates, Restore and Delete permanently instead of Delete. */
  trash: boolean;
  /** A Delete or Restore of the row is in flight. */
  busy: boolean;
  onDelete: () => void;
  onRestore: () => void;
  onPurge: () => void;
}

const rowAction = "hover:underline disabled:opacity-50 pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center";

function DocumentRow({ doc: d, metadata, owner, onChange, href, onOpen, checked, selecting, onPick, trash, busy, onDelete, onRestore, onPurge }: RowProps) {
  const name = documentName(d);
  const press = useLongPress(() => {
    if (!checked) onPick(false);
  });
  const correspondent = d.correspondentId ? metadata?.correspondents.find((c) => c.id === d.correspondentId)?.name : undefined;
  const type = d.documentTypeId ? metadata?.documentTypes.find((c) => c.id === d.documentTypeId)?.name : undefined;
  const tags = (d.tagIds ?? []).flatMap((id) => metadata?.tags.find((x) => x.id === id) ?? []);
  return (
    <li className={`flex items-start gap-3 rounded-lg border px-3 py-3 select-none [-webkit-touch-callout:none] md:rounded-none md:border-0 md:py-2 md:select-auto ${checked ? "border-accent bg-accent-soft/40" : "border-line"}`} {...press}>
      <label className={`-m-2 flex shrink-0 items-center justify-center p-2 pointer-coarse:size-11 ${selecting ? "" : "pointer-coarse:hidden"}`}>
        <input
          type="checkbox"
          className="size-4 pointer-coarse:size-5"
          aria-label={t.selectDocument(name)}
          checked={checked}
          onChange={(e) => onPick((e.nativeEvent as MouseEvent).shiftKey === true)}
        />
      </label>
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
          <a className={`${rowAction} text-accent-ink`} href={documentsApi.fileURL(d.id)} download>
            {t.download}
          </a>
          {trash ? (
            <>
              <button type="button" className={`${rowAction} text-accent-ink`} aria-label={t.restoreLabel(name)} disabled={busy} onClick={onRestore}>
                {t.restore}
              </button>
              <button type="button" className={`${rowAction} text-danger`} aria-label={t.purgeLabel(name)} disabled={busy} onClick={onPurge}>
                {t.purge}
              </button>
            </>
          ) : (
            <button type="button" className={`${rowAction} text-accent-ink`} aria-label={t.deleteDocumentLabel(name)} disabled={busy} onClick={onDelete}>
              {t.deleteDocument}
            </button>
          )}
        </div>
        {trash && (d.deletedAt || d.purgeAt) && (
          <p className="text-sm text-ink-muted">{[d.deletedAt ? t.deletedOn(trashDay(d.deletedAt)) : "", d.purgeAt ? t.removedOn(trashDay(d.purgeAt)) : ""].filter(Boolean).join(" · ")}</p>
        )}
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
