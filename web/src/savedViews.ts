import type { SavedView } from "./api";
import { noFilter, type ViewState } from "./urlState";

// Saved views (#12, part 4). A view's filter is the app's filter object
// (DocumentArchive's DocumentFilter.swift); the web keeps its own list
// parameters under one key and leaves the app's keys as they came, so a
// view survives a trip through the app. Views without the key (made by the
// app) are not shown: the mapping between the two formats is the archive
// import's (#10).

/** The filter key of the web's parameters. */
export const viewKey = "paperless-go";

/** The web's parameters of a view: those of GET /api/documents, and an admin's "Everyone's documents". */
export interface ViewParams {
  v: 1;
  q: string;
  /** At most one ID each: the URL holds one. */
  tag: string[];
  correspondent: string[];
  type: string[];
  /** true lists the inbox only; null does not filter. */
  inbox: true | null;
  favorite: true | null;
  sort: "" | "relevance" | "added" | "date";
  order: "" | "asc" | "desc";
  everyone: boolean;
}

const sorts = ["", "relevance", "added", "date"] as const;
const known = ["v", "q", "tag", "correspondent", "type", "inbox", "favorite", "sort", "order", "everyone"];
const isStrings = (v: unknown): v is string[] => Array.isArray(v) && v.every((x) => typeof x === "string");

/** The web's parameters of a filter, or null for a view the web does not show (no key, another version, another shape). */
export function viewParams(filter: Record<string, unknown>): ViewParams | null {
  const p = filter[viewKey];
  if (typeof p !== "object" || p === null || Array.isArray(p)) return null;
  const r = p as Record<string, unknown>;
  if (r.v !== 1) return null;
  // The web shows one ID per kind and the inbox or favourites only as a
  // filter: a view it would open differently (the server refuses such
  // views, but an older or other writer may have stored one) is hidden.
  const list = (k: string) => (r[k] === undefined || r[k] === null ? [] : isStrings(r[k]) && r[k].length <= 1 ? r[k] : null);
  const flag = (k: string) => (r[k] === true ? true : r[k] === null || r[k] === undefined ? null : undefined);
  for (const k of Object.keys(r)) if (!known.includes(k)) return null;
  const tag = list("tag");
  const correspondent = list("correspondent");
  const type = list("type");
  const inbox = flag("inbox");
  const favorite = flag("favorite");
  const sort = r.sort ?? "";
  const order = r.order ?? "";
  if (tag === null || correspondent === null || type === null || inbox === undefined || favorite === undefined) return null;
  if (r.q !== undefined && typeof r.q !== "string") return null;
  if (!(sorts as readonly unknown[]).includes(sort) || (order !== "" && order !== "asc" && order !== "desc")) return null;
  return {
    v: 1,
    q: typeof r.q === "string" ? r.q : "",
    tag,
    correspondent,
    type,
    inbox,
    favorite,
    sort: sort as ViewParams["sort"],
    order: order as ViewParams["order"],
    everyone: r.everyone === true,
  };
}

/** The views the sidebar shows, in the server's order (the manual one). */
export function shownViews(views: readonly SavedView[]): { view: SavedView; params: ViewParams }[] {
  return views.flatMap((view) => {
    const params = viewParams(view.filter);
    return params ? [{ view, params }] : [];
  });
}

/** The parameters of what the list shows now. */
export function paramsOf(s: ViewState): ViewParams {
  return {
    v: 1,
    q: s.q.trim(),
    tag: s.tag ? [s.tag] : [],
    correspondent: s.corr ? [s.corr] : [],
    type: s.type ? [s.type] : [],
    inbox: s.inbox === "1" ? true : null,
    favorite: s.fav === "1" ? true : null,
    sort: s.sort === "deleted" ? "" : s.sort,
    order: s.order === "asc" ? "asc" : "",
    everyone: s.all === "1",
  };
}

/** A view's filter with the web's part replaced by params; the app's keys stay. */
export function withParams(filter: Record<string, unknown>, params: ViewParams): Record<string, unknown> {
  return { ...filter, [viewKey]: params };
}

/**
 * Whether "Save view…" makes sense: the documents' list (not the trash)
 * with a search, a filter or a sort chosen.
 */
export function canSave(s: ViewState): boolean {
  if (s.view !== "" || s.trash === "1") return false;
  return Boolean(s.q.trim() || s.inbox || s.fav || s.tag || s.corr || s.type || s.sort || s.order);
}

/**
 * The URL change that opens a view: its parameters replace the list's, and
 * it is marked (sv). viewParams has already hidden views the URL cannot
 * hold (several IDs of a kind, inbox or favorite false), so the result is
 * exactly the view. An admin's "Everyone's documents" is applied only
 * where the switch is (admin).
 */
export function applyView(id: string, p: ViewParams, admin: boolean): Partial<ViewState> {
  return {
    ...noFilter,
    q: p.q,
    inbox: p.inbox === true ? "1" : "",
    fav: p.favorite === true ? "1" : "",
    tag: p.tag[0] ?? "",
    corr: p.correspondent[0] ?? "",
    type: p.type[0] ?? "",
    sort: p.sort,
    order: p.order === "asc" ? "asc" : "",
    ...(admin ? { all: p.everyone ? "1" : "" } : {}),
    doc: "",
    view: "",
    sv: id,
  };
}

const compared = ["q", "inbox", "fav", "trash", "tag", "corr", "type", "sort", "order", "all", "view"] as const;

/** Whether the list shows what opening the view would show: the sidebar marks it only then. */
export function showsView(s: ViewState, id: string, p: ViewParams, admin: boolean): boolean {
  if (s.sv !== id) return false;
  const opened = { ...s, ...applyView(id, p, admin) };
  return compared.every((k) => (k === "q" ? s.q.trim() === opened.q.trim() : s[k] === opened[k]));
}

/**
 * The sort indexes to send to move the shown view at index i one place up
 * (-1) or down (+1): the two views swap their indexes. When any two shown
 * views have the same index (views from the app may), a swap could move a
 * view past its tie, so the shown views are numbered anew from the lowest
 * index in their new order instead. Only changed indexes are returned;
 * none at either end.
 */
export function moveView(shown: readonly SavedView[], i: number, dir: -1 | 1): { id: string; sortIndex: number }[] {
  const j = i + dir;
  const a = shown[i];
  const b = shown[j];
  if (!a || !b) return [];
  if (new Set(shown.map((v) => v.sortIndex)).size === shown.length) {
    return [
      { id: a.id, sortIndex: b.sortIndex },
      { id: b.id, sortIndex: a.sortIndex },
    ];
  }
  const order = [...shown];
  order[i] = b;
  order[j] = a;
  const base = Math.min(...shown.map((v) => v.sortIndex));
  return order.flatMap((v, k) => (v.sortIndex === base + k ? [] : [{ id: v.id, sortIndex: base + k }]));
}
