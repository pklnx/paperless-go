import { useCallback, useSyncExternalStore } from "react";
import type { DocumentQuery, DocumentSort } from "./api";

// The UI state lives in the URL query
// (?q=&inbox=&fav=&trash=&tag=&corr=&type=&sort=&order=&all=&doc=&view=&sv=), so the
// back button, reloads and bookmarks work without a router library, and the
// server serves every view from "/".

export interface ViewState {
  /** The search as typed. */
  q: string;
  /** "1" lists the inbox. */
  inbox: string;
  /** "1" lists the favourites. */
  fav: string;
  /** "1" lists the trash (#12): the other filters do not apply there. */
  trash: string;
  /** A tag, correspondent or document type ID. */
  tag: string;
  corr: string;
  type: string;
  /** "", "relevance", "added", "date", or "deleted" (the trash only). */
  sort: DocumentSort;
  /** "asc", or "" for newest first. */
  order: string;
  /** "1": an admin's "Everyone's documents". */
  all: string;
  /** The open document's ID (#7, part 4). */
  doc: string;
  /** "" for the documents, or a page: "metadata", "profile", "log", "users". */
  view: string;
  /** The saved view last opened (#12, part 4); the sidebar marks it while the list still shows its filter. */
  sv: string;
}

const keys: (keyof ViewState)[] = ["q", "inbox", "fav", "trash", "tag", "corr", "type", "sort", "order", "all", "doc", "view", "sv"];

const flag = (v: string | null) => (v === "1" ? "1" : "");
const sorts: readonly string[] = ["relevance", "added", "date", "deleted"];

export function parseState(search: string): ViewState {
  const p = new URLSearchParams(search);
  const sort = p.get("sort") ?? "";
  return {
    q: p.get("q") ?? "",
    inbox: flag(p.get("inbox")),
    fav: flag(p.get("fav")),
    trash: flag(p.get("trash")),
    tag: p.get("tag") ?? "",
    corr: p.get("corr") ?? "",
    type: p.get("type") ?? "",
    sort: (sorts.includes(sort) ? sort : "") as DocumentSort,
    order: p.get("order") === "asc" ? "asc" : "",
    all: flag(p.get("all")),
    doc: p.get("doc") ?? "",
    view: p.get("view") ?? "",
    sv: p.get("sv") ?? "",
  };
}

export function formatState(s: ViewState): string {
  const p = new URLSearchParams();
  for (const k of keys) if (s[k]) p.set(k, s[k]);
  const qs = p.toString();
  return qs ? `?${qs}` : "";
}

/** The sidebar's choices; one at a time. */
export type Filter = { kind: "inbox" } | { kind: "fav" } | { kind: "trash" } | { kind: "tag" | "corr" | "type"; id: string };

/** No sidebar filter: every field a filter sets, cleared. */
export const noFilter: Pick<ViewState, "inbox" | "fav" | "trash" | "tag" | "corr" | "type"> = { inbox: "", fav: "", trash: "", tag: "", corr: "", type: "" };

/** The patch that makes f the only sidebar filter. */
export function filterPatch(f: Filter | null): Partial<ViewState> {
  if (!f) return { ...noFilter };
  if (f.kind === "inbox") return { ...noFilter, inbox: "1" };
  if (f.kind === "fav") return { ...noFilter, fav: "1" };
  if (f.kind === "trash") return { ...noFilter, trash: "1" };
  return { ...noFilter, [f.kind]: f.id };
}

/**
 * The state's sidebar filter. A URL with several (typed by hand) counts
 * the first in this order, as the sidebar marks it; the list still uses
 * all of them.
 */
export function activeFilter(s: ViewState): Filter | null {
  if (s.trash) return { kind: "trash" };
  if (s.inbox) return { kind: "inbox" };
  if (s.fav) return { kind: "fav" };
  if (s.tag) return { kind: "tag", id: s.tag };
  if (s.corr) return { kind: "corr", id: s.corr };
  if (s.type) return { kind: "type", id: s.type };
  return null;
}

/**
 * The list request of a state. Relevance needs a search: without one it
 * falls back to the default order (newest added). ownOnly is true for an
 * admin who did not switch on "Everyone's documents". The trash takes the
 * search but none of the sidebar's filters; without a search it is in the
 * order of deletion (newest first), which the request names. Outside the
 * trash "deleted" means the default order.
 */
export function documentQuery(s: ViewState, ownOnly: boolean): DocumentQuery {
  const q = s.q.trim() ? s.q : "";
  if (s.trash === "1") {
    const sort: DocumentSort = s.sort === "" || (s.sort === "relevance" && !q) ? (q ? "" : "deleted") : s.sort;
    return {
      q,
      inbox: false,
      favorite: false,
      tag: "",
      correspondent: "",
      type: "",
      sort,
      ascending: s.order === "asc" && (sort === "added" || sort === "date"),
      ownOnly,
      trashed: true,
    };
  }
  const sort: DocumentSort = (s.sort === "relevance" && !q) || s.sort === "deleted" ? "" : s.sort;
  return {
    q,
    inbox: s.inbox === "1",
    favorite: s.fav === "1",
    tag: s.tag,
    correspondent: s.corr,
    type: s.type,
    sort,
    ascending: s.order === "asc" && sort !== "relevance" && !(sort === "" && q !== ""),
    ownOnly,
    trashed: false,
  };
}

/**
 * Asked before the state changes: by update, and on the back and forward
 * buttons, with the next state; by confirmLeave (logging out) with null,
 * leaving the app. False keeps the current state. The open document's
 * form sets one while it has unsaved changes.
 */
export type NavigationGuard = (next: ViewState | null) => boolean;
let guard: NavigationGuard | null = null;

/** Asks the guard about leaving the app (logging out): true when there is none or it agrees. */
export function confirmLeave(): boolean {
  return !guard || guard(null);
}

/** Sets the guard (null removes it); returns a function that removes this guard again. */
export function setNavigationGuard(g: NavigationGuard | null): () => void {
  guard = g;
  return () => {
    if (guard === g) guard = null;
  };
}

const listeners = new Set<() => void>();
const currentUrl = () => window.location.pathname + window.location.search;
/** The URL the listeners last saw: where a refused back or forward returns to. */
let shownUrl = "";

// One popstate listener for all subscribers, so the guard runs once and
// before any of them sees the new state. A refused back or forward puts
// the shown URL back as a new entry (popstate cannot be cancelled).
function onPopState() {
  if (guard && !guard(parseState(window.location.search))) {
    window.history.pushState(null, "", shownUrl);
    return;
  }
  shownUrl = currentUrl();
  listeners.forEach((fn) => fn());
}

function subscribe(fn: () => void) {
  if (listeners.size === 0) {
    shownUrl = currentUrl();
    window.addEventListener("popstate", onPopState);
  }
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
    if (listeners.size === 0) window.removeEventListener("popstate", onPopState);
  };
}
const getSearch = () => window.location.search;

export type UpdateState = (patch: Partial<ViewState>, opts?: { replace?: boolean }) => void;

/**
 * The state of the URL, and a function that changes it: a new history
 * entry unless replace. The navigation guard may refuse the change.
 */
export function useViewState(): [ViewState, UpdateState] {
  const search = useSyncExternalStore(subscribe, getSearch);
  const state = parseState(search);
  const update = useCallback<UpdateState>((patch, opts) => {
    const nextState = { ...parseState(window.location.search), ...patch };
    const next = formatState(nextState);
    if (next === window.location.search) return;
    if (guard && !guard(nextState)) return;
    const url = window.location.pathname + next;
    if (opts?.replace) window.history.replaceState(null, "", url);
    else window.history.pushState(null, "", url);
    shownUrl = url;
    listeners.forEach((fn) => fn());
  }, []);
  return [state, update];
}
