import { useCallback, useSyncExternalStore } from "react";
import type { DocumentQuery, DocumentSort } from "./api";

// The UI state lives in the URL query
// (?q=&inbox=&fav=&tag=&corr=&type=&sort=&order=&all=&doc=&view=), so the
// back button, reloads and bookmarks work without a router library, and the
// server serves every view from "/".

export interface ViewState {
  /** The search as typed. */
  q: string;
  /** "1" lists the inbox. */
  inbox: string;
  /** "1" lists the favourites. */
  fav: string;
  /** A tag, correspondent or document type ID. */
  tag: string;
  corr: string;
  type: string;
  /** "", "relevance", "added" or "date". */
  sort: DocumentSort;
  /** "asc", or "" for newest first. */
  order: string;
  /** "1": an admin's "Everyone's documents". */
  all: string;
  /** The open document's ID (#7, part 4). */
  doc: string;
  /** "" for the documents, or a page: "metadata", "profile", "log", "users". */
  view: string;
}

const keys: (keyof ViewState)[] = ["q", "inbox", "fav", "tag", "corr", "type", "sort", "order", "all", "doc", "view"];

const flag = (v: string | null) => (v === "1" ? "1" : "");
const sorts: readonly string[] = ["relevance", "added", "date"];

export function parseState(search: string): ViewState {
  const p = new URLSearchParams(search);
  const sort = p.get("sort") ?? "";
  return {
    q: p.get("q") ?? "",
    inbox: flag(p.get("inbox")),
    fav: flag(p.get("fav")),
    tag: p.get("tag") ?? "",
    corr: p.get("corr") ?? "",
    type: p.get("type") ?? "",
    sort: (sorts.includes(sort) ? sort : "") as DocumentSort,
    order: p.get("order") === "asc" ? "asc" : "",
    all: flag(p.get("all")),
    doc: p.get("doc") ?? "",
    view: p.get("view") ?? "",
  };
}

export function formatState(s: ViewState): string {
  const p = new URLSearchParams();
  for (const k of keys) if (s[k]) p.set(k, s[k]);
  const qs = p.toString();
  return qs ? `?${qs}` : "";
}

/** The sidebar's choices; one at a time. */
export type Filter = { kind: "inbox" } | { kind: "fav" } | { kind: "tag" | "corr" | "type"; id: string };

/** No sidebar filter: every field a filter sets, cleared. */
export const noFilter: Pick<ViewState, "inbox" | "fav" | "tag" | "corr" | "type"> = { inbox: "", fav: "", tag: "", corr: "", type: "" };

/** The patch that makes f the only sidebar filter. */
export function filterPatch(f: Filter | null): Partial<ViewState> {
  if (!f) return { ...noFilter };
  if (f.kind === "inbox") return { ...noFilter, inbox: "1" };
  if (f.kind === "fav") return { ...noFilter, fav: "1" };
  return { ...noFilter, [f.kind]: f.id };
}

/**
 * The state's sidebar filter. A URL with several (typed by hand) counts
 * the first in this order, as the sidebar marks it; the list still uses
 * all of them.
 */
export function activeFilter(s: ViewState): Filter | null {
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
 * admin who did not switch on "Everyone's documents".
 */
export function documentQuery(s: ViewState, ownOnly: boolean): DocumentQuery {
  const q = s.q.trim() ? s.q : "";
  const sort: DocumentSort = s.sort === "relevance" && !q ? "" : s.sort;
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
  };
}

const listeners = new Set<() => void>();
function subscribe(fn: () => void) {
  listeners.add(fn);
  window.addEventListener("popstate", fn);
  return () => {
    listeners.delete(fn);
    window.removeEventListener("popstate", fn);
  };
}
const getSearch = () => window.location.search;

export type UpdateState = (patch: Partial<ViewState>, opts?: { replace?: boolean }) => void;

/** The state of the URL, and a function that changes it: a new history entry unless replace. */
export function useViewState(): [ViewState, UpdateState] {
  const search = useSyncExternalStore(subscribe, getSearch);
  const state = parseState(search);
  const update = useCallback<UpdateState>((patch, opts) => {
    const next = formatState({ ...parseState(window.location.search), ...patch });
    if (next === window.location.search) return;
    const url = window.location.pathname + next;
    if (opts?.replace) window.history.replaceState(null, "", url);
    else window.history.pushState(null, "", url);
    listeners.forEach((fn) => fn());
  }, []);
  return [state, update];
}
