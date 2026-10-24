import { useEffect, useId, useRef, useState, type MouseEvent as ReactMouseEvent, type ReactNode } from "react";
import { flushSync } from "react-dom";
import { Button, Checkbox, DismissibleTag, IconButton, Link, Search, Select, SelectItem, Tag } from "@carbon/react";
import { ArrowDown, ArrowUp, Document, Email, Search as SearchIcon, StarFilled, TrashCan } from "@carbon/icons-react";
import { ApiError, bulkApi, bulkMatch, documentsApi, processingPending, type DocumentPage, type DocumentQuery, type DocumentSummary, type Metadata, type User } from "./api";
import { BulkBar, bulkErrorText, type BulkTarget } from "./BulkBar";
import { isPlainClick } from "./clicks";
import { DocumentDetail } from "./DocumentDetail";
import { documentName, ProcessingStatus } from "./DocumentParts";
import { lang, t } from "./i18n";
import { clickSelect, pruneSelection, selectAll, selectionLimit, useLongPress } from "./selection";
import { snippetParts } from "./snippet";
import { chipInk } from "./tagTree";
import type { Notice, Notify } from "./Toast";
import { EmptyTrash, foreignOwners, PurgeConfirm, trashDay, trashErrorText } from "./Trash";
import { activeFilters, filterPatch, formatState, parseState, withoutFilter, type UpdateState, type ViewState } from "./urlState";

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
  err instanceof ApiError && err.status === 400 && err.message === "query too long"
    ? t.searchTooLong
    : err instanceof ApiError && err.body.code === "search_too_broad"
      ? t.searchTooBroad
      : t.failed(err instanceof Error ? err.message : String(err));

/** The list's heading: where the focus goes when a modal's opener is gone (a purged row). */
const listHeading = () => document.getElementById("list-title");

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
  /** More controls next to the sort ("Save view…"). */
  toolbar?: ReactNode;
}

/**
 * The document list: the search field, the active filters as chips, the
 * sort, the documents with "Load more" (keyset paging), and an empty state
 * for each case. The trash (state.trash) is the same list of trashed
 * documents, with Restore and Delete permanently instead of Delete, and
 * "Empty trash". Carbon components and layout.css classes (#150, part 3).
 */
export function DocumentList({ state, update, query, user, metadata, everyone, reload, onMaxUploadSize, onChanged, notify, onUndone, undone = 0, ownTrash = null, toolbar }: Props) {
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
  // From a row the focus goes to the notice (the row is gone); from the
  // detail it stays in the detail, which is still open.
  const restored = (d: DocumentSummary, focus = true) => {
    if (trash) removeDoc(d.id);
    else replaceDoc(d);
    onChanged?.();
    showNotice({ text: t.restoredDocument(documentName(d)), focus });
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
        // The row leaves in a commit of its own, before the modal closes:
        // the closing modal then finds its opener (the row's button) gone
        // and moves the focus to the list's heading, unless the notice
        // takes it (keyboard users).
        flushSync(() => removeDoc(d.id));
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

  // Every filter the list applies gets a chip that says what kind it is
  // ("Tag: Steuer"); the heading names the list. A tag, correspondent or
  // type that is not among the loaded ones (deleted since a saved view or
  // a link named it) shows as "unknown" with a hint.
  const filters = activeFilters(state).map((f) => {
    if (f.kind === "inbox") return { f, chip: t.inbox, name: t.inbox, gone: false };
    if (f.kind === "fav") return { f, chip: t.favorites, name: t.favorites, gone: false };
    if (f.kind === "trash") return { f, chip: t.trash, name: t.trash, gone: false };
    const rows = f.kind === "tag" ? metadata?.tags : f.kind === "corr" ? metadata?.correspondents : metadata?.documentTypes;
    const row = rows?.find((r) => r.id === f.id);
    const name = row?.name ?? t.unknownEntry;
    const chip = f.kind === "tag" ? t.tagFilter(name) : f.kind === "corr" ? t.correspondentFilter(name) : t.typeFilter(name);
    return { f, chip, name, gone: rows !== undefined && !row };
  });
  const chips = filters.filter((x) => x.f.kind !== "trash");
  const title = trash ? t.trash : query.q ? t.searchResults : filters.length > 0 ? filters.map((x) => x.name).join(" · ") : t.allDocuments;
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
        onRestored={(d) => restored(d, false)}
        onStale={onChanged}
        onPurged={(d) => {
          purged(d);
          closeIfOpen(d.id);
        }}
        onTrashError={(text) => showNotice({ text, error: true })}
      />
    )}
    <section className="docs-list" aria-labelledby="list-title" inert={detail}>
      <SearchField q={state.q} search={(q) => update({ q, doc: "" })} />
      <div className="docs-list__head">
        <h2 id="list-title" tabIndex={-1} className="docs-list__title">
          {title}
        </h2>
        <div className="docs-list__tools">
          {toolbar}
          <SortControls state={state} query={query} update={update} />
        </div>
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
      {chips.length > 0 && (
        <div className="docs-filters">
          {chips.map(({ f, chip }) => (
            <DismissibleTag
              key={f.kind}
              size="md"
              type="purple"
              text={chip}
              title={t.removeFilter(chip)}
              dismissTooltipLabel={t.removeFilter(chip)}
              onClose={() => update(withoutFilter(f))}
            />
          ))}
        </div>
      )}
      {chips.map(({ f, gone }) =>
        gone && f.kind !== "inbox" && f.kind !== "fav" && f.kind !== "trash" ? (
          <p key={`gone-${f.kind}`} role="status" className="docs-list__note app-text-secondary">
            {t.filterGone(f.kind, state.sv !== "")}
          </p>
        ) : null,
      )}
      {list.error && (
        <p role="alert" className="docs-list__note app-text-error">
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
      {!docs && list.loading && <p className="docs-list__note app-text-secondary">{t.loading}</p>}
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
        returnFocus={listHeading}
      />
      {docs && docs.length > 0 && (
        <ul aria-label={title} className={`doc-list${selecting ? " doc-list--selecting" : ""}`}>
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
        <div className="docs-list__more">
          <Button kind="tertiary" size="md" disabled={list.loading} onClick={loadMore}>
            {t.loadMore}
          </Button>
        </div>
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

/**
 * The search field (Carbon's Search). It searches on Enter (each search is
 * a document.search entry in the log, so not on every key); emptying it,
 * by the clear button or by hand, lists everything again.
 */
function SearchField({ q, search }: { q: string; search: (q: string) => void }) {
  const [text, setText] = useState(q);
  // Follow the URL when it changes from outside (back button, a cleared search).
  useEffect(() => setText(q), [q]);
  const run = () => search(text.trim() ? text : "");
  return (
    // The form takes the browser's implicit submission (Enter in the one
    // field); Enter is handled below as well, so no submission happens twice.
    <form
      className="docs-search"
      onSubmit={(e) => {
        e.preventDefault();
        run();
      }}
    >
      <Search
        size="lg"
        labelText={t.searchLabel}
        placeholder={t.searchPlaceholder}
        closeButtonLabelText={t.carbon.clearSearch}
        enterKeyHint="search"
        {...({ maxLength: 1000 } as object)}
        value={text}
        onChange={(e) => {
          setText(e.target.value);
          if (e.target.value === "" && q !== "") search("");
        }}
        onKeyDown={(e) => {
          if (e.key !== "Enter") return;
          e.preventDefault();
          run();
        }}
      />
    </form>
  );
}

/** The sort: added or document date, either direction; relevance (one direction) while searching; in the trash also deleted (newest first). */
function SortControls({ state, query, update }: { state: ViewState; query: DocumentQuery; update: UpdateState }) {
  const id = useId();
  const searching = query.q !== "";
  const trash = query.trashed;
  const fallback = searching ? "relevance" : trash ? "deleted" : "added";
  const sort = query.sort || fallback;
  // Relevance and deleted have one direction.
  const relevance = sort === "relevance" || sort === "deleted";
  const direction = query.ascending ? t.sortOldestFirst : t.sortNewestFirst;
  return (
    <div className="docs-sort">
      <Select
        id={id}
        inline
        size="md"
        labelText={t.sortBy}
        value={sort}
        onChange={(e) => {
          const v = e.target.value as ViewState["sort"];
          // The default stays out of the URL.
          update({ sort: v === fallback ? "" : v, order: v === "relevance" || v === "deleted" ? "" : state.order });
        }}
      >
        {searching && <SelectItem value="relevance" text={t.sortRelevance} />}
        {trash && <SelectItem value="deleted" text={t.sortDeleted} />}
        <SelectItem value="added" text={t.sortAdded} />
        <SelectItem value="date" text={t.sortDate} />
      </Select>
      <IconButton
        kind="ghost"
        size="md"
        align="bottom-end"
        label={direction}
        disabled={relevance}
        title={sort === "relevance" ? t.sortRelevanceOrder : direction}
        onClick={() => update({ order: query.ascending ? "" : "asc" })}
      >
        {query.ascending ? <ArrowUp /> : <ArrowDown />}
      </IconButton>
    </div>
  );
}

/** Says why the list is empty and offers the next step (layout.css empty-state, as mail-archive's). */
function EmptyState({ icon, title, text, action }: { icon: ReactNode; title: string; text: string; action?: { label: string; run: () => void } }) {
  return (
    <div className="empty-state">
      <span aria-hidden className="empty-state__icon">
        {icon}
      </span>
      <p className="empty-state__title">{title}</p>
      <p className="empty-state__text app-text-secondary">{text}</p>
      {action && (
        <div className="empty-state__actions">
          <Button kind="tertiary" size="sm" onClick={action.run}>
            {action.label}
          </Button>
        </div>
      )}
    </div>
  );
}

function Empty({ query, clearSearch, showAll }: { query: DocumentQuery; clearSearch: () => void; showAll: () => void }) {
  if (query.trashed && !query.q) return <EmptyState icon={<TrashCan size={32} />} title={t.trashEmpty} text={t.trashEmptyText} />;
  const filtered = query.inbox || query.favorite || query.tag !== "" || query.correspondent !== "" || query.type !== "";
  if (query.q) return <EmptyState icon={<SearchIcon size={32} />} title={t.nothingFound} text={t.nothingFoundSearch} action={{ label: t.clearSearch, run: clearSearch }} />;
  if (query.inbox && !query.favorite && !query.tag && !query.correspondent && !query.type) {
    return <EmptyState icon={<Email size={32} />} title={t.inboxEmpty} text={t.inboxEmptyText} />;
  }
  if (filtered) return <EmptyState icon={<SearchIcon size={32} />} title={t.nothingFound} text={t.nothingFoundFilter} action={{ label: t.showAllDocuments, run: showAll }} />;
  return <EmptyState icon={<Document size={32} />} title={t.noDocuments} text={t.noDocumentsText} />;
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

/** "Select all loaded" (indeterminate while some are checked), the count, and "Select all matches of this search" once every loaded one is checked. */
function SelectionHeader({ allLoaded, some, count, capped, allMatches, moreMatches, error, toggleAll, selectMatches, clear }: HeaderProps) {
  const id = useId();
  return (
    <div className="selection-head">
      <div className="selection-head__row">
        <Checkbox
          id={id}
          className="selection-head__all"
          labelText={t.selectAllLoaded}
          checked={allMatches !== null || allLoaded}
          indeterminate={some && !allLoaded && allMatches === null}
          onChange={toggleAll}
        />
        {(some || allMatches !== null) && (
          <>
            <span className="app-text-secondary" role="status">
              {allMatches !== null ? t.allMatchesSelected(allMatches) : t.selectedCount(count)}
            </span>
            {moreMatches && (
              <Button kind="ghost" size="sm" onClick={selectMatches}>
                {t.selectAllMatches}
              </Button>
            )}
            <Button kind="ghost" size="sm" onClick={clear}>
              {t.clearSelection}
            </Button>
          </>
        )}
      </div>
      {capped && <p className="docs-list__note app-text-secondary">{t.selectionCapped(selectionLimit.max)}</p>}
      {error && (
        <p role="alert" className="docs-list__note app-text-error">
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
  onPick: (shift: boolean) => void;
  /** The row is in the trash: its dates, Restore and Delete permanently instead of Delete. */
  trash: boolean;
  /** A Delete or Restore of the row is in flight. */
  busy: boolean;
  onDelete: () => void;
  onRestore: () => void;
  onPurge: () => void;
}

/**
 * One document: a structured row of Carbon primitives (decision 1 of
 * #150). On phones the checkbox shows once something is selected (before,
 * a long press starts selecting: the list's doc-list--selecting class).
 */
function DocumentRow({ doc: d, metadata, owner, onChange, href, onOpen, checked, onPick, trash, busy, onDelete, onRestore, onPurge }: RowProps) {
  const id = useId();
  const name = documentName(d);
  const press = useLongPress(() => {
    if (!checked) onPick(false);
  });
  const correspondent = d.correspondentId ? metadata?.correspondents.find((c) => c.id === d.correspondentId)?.name : undefined;
  const type = d.documentTypeId ? metadata?.documentTypes.find((c) => c.id === d.documentTypeId)?.name : undefined;
  const tags = (d.tagIds ?? []).flatMap((id) => metadata?.tags.find((x) => x.id === id) ?? []);
  return (
    <li className={`doc-row${checked ? " doc-row--checked" : ""}`} {...press}>
      <div className="doc-row__select">
        <Checkbox
          id={id}
          labelText={t.selectDocument(name)}
          hideLabel
          checked={checked}
          // React drives a checkbox's change by its click, so the key is there.
          onChange={(e) => onPick((e.nativeEvent as MouseEvent).shiftKey === true)}
        />
      </div>
      <Thumbnail url={d.thumbnailUrl} />
      <div className="doc-row__body">
        <div className="doc-row__top">
          <Link
            href={href}
            data-doc-link={d.id}
            className="doc-row__title"
            onClick={(e: ReactMouseEvent<HTMLAnchorElement>) => {
              // A modified click (new tab, window) is the browser's.
              if (!isPlainClick(e)) return;
              e.preventDefault();
              onOpen();
            }}
          >
            {name}
          </Link>
          {d.isInbox && (
            <span className="doc-row__flag doc-row__flag--inbox" title={t.inInbox}>
              <Email size={16} aria-hidden />
              <span className="cds--visually-hidden">{t.inInbox}</span>
            </span>
          )}
          {d.isFavorite && (
            <span className="doc-row__flag doc-row__flag--fav" title={t.favorite}>
              <StarFilled size={16} aria-hidden />
              <span className="cds--visually-hidden">{t.favorite}</span>
            </span>
          )}
          <span className="doc-row__date app-text-secondary">{day(d.documentDate)}</span>
        </div>
        <div className="doc-row__meta app-text-secondary">
          {correspondent && <span className="doc-row__fact">{correspondent}</span>}
          {type && <span className="doc-row__fact">{type}</span>}
          {owner && <span className="doc-row__fact">{t.ownedBy(owner)}</span>}
          <span className="doc-row__actions">
            <Button as="a" kind="ghost" size="sm" href={documentsApi.fileURL(d.id)} download>
              {t.download}
            </Button>
            {trash ? (
              <>
                <Button kind="ghost" size="sm" aria-label={t.restoreLabel(name)} disabled={busy} onClick={onRestore}>
                  {t.restore}
                </Button>
                <Button kind="danger--ghost" size="sm" aria-label={t.purgeLabel(name)} disabled={busy} onClick={onPurge}>
                  {t.purge}
                </Button>
              </>
            ) : (
              <Button kind="ghost" size="sm" aria-label={t.deleteDocumentLabel(name)} disabled={busy} onClick={onDelete}>
                {t.deleteDocument}
              </Button>
            )}
          </span>
        </div>
        {trash && (d.deletedAt || d.purgeAt) && (
          <p className="doc-row__dates app-text-secondary">{[d.deletedAt ? t.deletedOn(trashDay(d.deletedAt)) : "", d.purgeAt ? t.removedOn(trashDay(d.purgeAt)) : ""].filter(Boolean).join(" · ")}</p>
        )}
        {tags.length > 0 && (
          <ul aria-label={t.tags} className="doc-tags">
            {tags.map((x) => (
              // A tag's own colour, with black or white text for contrast; without one Carbon's gray.
              <Tag key={x.id} as="li" size="sm" type={x.color ? undefined : "gray"} className="doc-tags__tag" style={x.color ? { backgroundColor: x.color, color: chipInk(x.color) } : undefined}>
                {x.name}
              </Tag>
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
  if (!url) return <div aria-hidden="true" data-testid="no-thumbnail" className="doc-thumb doc-thumb--none" />;
  return <img src={url} alt="" loading="lazy" decoding="async" className="doc-thumb" />;
}

/** A search snippet: its matches as <mark>, everything else as text. */
export function Snippet({ text }: { text: string }) {
  const parts = snippetParts(text);
  if (parts.length === 0) return null;
  return (
    <p className="doc-snippet app-text-secondary">
      {parts.map((p, i) =>
        p.mark ? (
          <mark key={i} className="app-highlight">
            {p.text}
          </mark>
        ) : (
          <span key={i}>{p.text}</span>
        ),
      )}
    </p>
  );
}
