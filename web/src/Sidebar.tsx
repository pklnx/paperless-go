import { useEffect, useId, useRef, type ReactNode } from "react";
import type { DocumentCounts, Metadata, NamedRow, User } from "./api";
import { sortByName } from "./collate";
import type { Health } from "./health";
import { Chevron, InboxIcon, StarIcon, DocumentIcon, TrashIcon } from "./icons";
import { t } from "./i18n";
import { buildTagTree, containsTag, type CollapsedTags, type TagNode } from "./tagTree";
import { ThemeToggle } from "./ThemeToggle";
import { activeFilter, type Filter, type ViewState } from "./urlState";

const docsURL = "https://pklnx.github.io/paperless-go/";

/** Something loaded for the sidebar: the data, or why it failed. */
export interface Loaded<T> {
  data: T | null;
  error: string;
}

interface Props {
  state: ViewState;
  /** Sets the one sidebar filter; null lists everything. */
  select: (f: Filter | null) => void;
  /** Opens a page: "metadata", "profile", "log" or "users". */
  show: (view: string) => void;
  user: User;
  logout: () => void;
  counts: DocumentCounts | null;
  metadata: Loaded<Metadata>;
  collapsed: CollapsedTags;
  /** An admin's "Everyone's documents". */
  everyone: boolean;
  setEveryone: (on: boolean) => void;
  health: Health;
  /** The "Saved views" section (SavedViews.tsx), below the lists. */
  savedViews?: ReactNode;
}

const item = "flex w-full items-center justify-between gap-2 rounded-md px-2 py-1 text-left text-sm pointer-coarse:min-h-11";
const active = "bg-accent-soft font-semibold text-accent-soft-ink";
const idle = "hover:bg-surface-hover";
/** A collapsed tag that contains the selection. */
const within = "text-accent-soft-ink";
const section = "px-2 pt-2 text-xs font-semibold tracking-wide text-ink-muted uppercase";
/** Deeper levels are not indented further, so names keep some room. */
const maxIndent = 6;
const toggleClass =
  "relative flex size-6 shrink-0 items-center justify-center rounded text-ink-muted hover:bg-surface-hover hover:text-ink pointer-coarse:after:absolute pointer-coarse:after:-inset-2.5";

const sameFilter = (a: Filter | null, b: Filter | null) =>
  a === null || b === null ? a === b : a.kind === b.kind && ("id" in a ? a.id : "") === ("id" in b ? b.id : "");

function Count({ n, on }: { n: number | undefined; on: boolean }) {
  if (n === undefined) return null;
  return <span className={`text-xs tabular-nums ${on ? "" : "text-ink-muted"}`}>{n}</span>;
}

/** The navigation: the document lists with counts, tags, correspondents, types, and the pages. */
export function Sidebar({ state, select, show, user, logout, counts, metadata, collapsed, everyone, setEveryone, health, savedViews }: Props) {
  const docs = state.view === "";
  const current = docs ? activeFilter(state) : null;
  const on = (f: Filter | null) => docs && sameFilter(current, f);
  const { data, error } = metadata;
  const baseId = useId();

  // The selected tag stays visible: its parents are expanded once when the
  // selection changes, or when the tag first shows up in the data.
  const { reveal } = collapsed;
  const revealed = useRef("");
  useEffect(() => {
    if (!state.tag || !data || revealed.current === state.tag) return;
    if (!data.tags.some((x) => x.id === state.tag)) return;
    revealed.current = state.tag;
    reveal(state.tag);
  }, [state.tag, data, reveal]);

  const entry = (f: Filter | null, label: string, icon: ReactNode, n: number | undefined) => (
    <button type="button" className={`${item} ${on(f) ? active : idle}`} aria-current={on(f) ? "page" : undefined} onClick={() => select(f)}>
      <span className="flex min-w-0 items-center gap-2">
        {icon}
        <span className="truncate">{label}</span>
      </span>
      <Count n={n} on={on(f)} />
    </button>
  );

  const named = (kind: "corr" | "type", title: string, rows: NamedRow[]) =>
    rows.length > 0 && (
      <div>
        <h2 className={section}>{title}</h2>
        <ul aria-label={title} className="mt-1 flex flex-col gap-0.5">
          {sortByName(rows, (r) => r.name).map((r) => {
            const f: Filter = { kind, id: r.id };
            return (
              <li key={r.id}>
                <button type="button" className={`${item} ${on(f) ? active : idle}`} aria-current={on(f) ? "page" : undefined} title={r.name} onClick={() => select(f)}>
                  <span className="truncate">{r.name}</span>
                  <Count n={r.documentCount} on={on(f)} />
                </button>
              </li>
            );
          })}
        </ul>
      </div>
    );

  const page = (view: string, label: string) => (
    <button type="button" className={`${item} ${state.view === view ? active : idle}`} aria-current={state.view === view ? "page" : undefined} onClick={() => show(view)}>
      {label}
    </button>
  );

  const status = { checking: t.statusChecking, ok: t.statusOk, down: t.statusDown }[health];
  const statusColor = { checking: "bg-line-control", ok: "bg-ok", down: "bg-danger" }[health];

  return (
    <nav aria-label={t.navigation} className="flex min-h-full flex-col gap-3 p-3">
      <div className="flex flex-col gap-0.5">
        {entry(null, t.allDocuments, <DocumentIcon />, counts?.total)}
        {entry({ kind: "inbox" }, t.inbox, <InboxIcon />, counts?.inbox)}
        {entry({ kind: "fav" }, t.favorites, <StarIcon />, counts?.favorites)}
        {entry({ kind: "trash" }, t.trash, <TrashIcon />, counts?.trash)}
      </div>
      {user.admin && (
        <label className="flex items-center gap-2 px-2 text-sm pointer-coarse:min-h-11">
          <input type="checkbox" role="switch" checked={everyone} onChange={(e) => setEveryone(e.target.checked)} />
          {t.everyonesDocuments}
        </label>
      )}
      {savedViews}
      {error && !data && (
        <p role="alert" className="px-2 text-sm text-danger">
          {t.loadMetadataFailed(error)}
        </p>
      )}
      {!data && !error && <p className="px-2 text-sm text-ink-muted">{t.loading}</p>}
      {data && data.tags.length > 0 && (
        <div>
          <h2 className={section}>{t.tags}</h2>
          <TagList id={`${baseId}-t`} hidden={false} nodes={buildTagTree(data.tags)} depth={0} selected={current?.kind === "tag" ? current.id : null} select={select} collapsed={collapsed} label={t.tags} />
        </div>
      )}
      {data && named("corr", t.correspondents, data.correspondents)}
      {data && named("type", t.documentTypes, data.documentTypes)}

      <div className="mt-auto flex flex-col gap-0.5 border-t border-line pt-2">
        {page("metadata", t.manageMetadata)}
        {page("log", t.log)}
        {user.admin && page("users", t.users)}
        <div className="flex items-center justify-between gap-2 px-2 pt-1 text-sm">
          <button
            type="button"
            className={`truncate hover:underline pointer-coarse:min-h-11 ${state.view === "profile" ? "font-semibold" : "text-ink-muted"}`}
            title={`${t.loggedInAs(user.name)} · ${t.profile}`}
            aria-label={t.profile}
            aria-current={state.view === "profile" ? "page" : undefined}
            onClick={() => show("profile")}
          >
            {user.name}
          </button>
          <span className="flex items-center gap-1">
            <ThemeToggle />
            <button type="button" className="shrink-0 text-accent-ink hover:underline pointer-coarse:min-h-11" onClick={logout}>
              {t.logOut}
            </button>
          </span>
        </div>
        <p className="flex items-center gap-2 px-2 text-xs text-ink-muted">
          <span aria-hidden className={`size-2 shrink-0 rounded-full ${statusColor}`} />
          <span>
            {t.serverStatus}: <span role="status">{status}</span>
          </span>
        </p>
        <a className="px-2 text-xs text-accent-ink hover:underline" href={docsURL} rel="noreferrer">
          {t.documentation}
        </a>
      </div>
    </nav>
  );
}

interface ListProps {
  id: string;
  hidden: boolean;
  nodes: TagNode[];
  depth: number;
  /** The selected tag, or null. */
  selected: string | null;
  select: (f: Filter) => void;
  collapsed: CollapsedTags;
  label?: string;
}

/** The tags of one level. Collapsed lists stay in the page, hidden, so aria-controls always has a target. */
function TagList({ id, hidden, nodes, depth, label, ...rest }: ListProps) {
  return (
    <ul id={id} hidden={hidden} aria-label={label} className={`mt-0.5 flex flex-col gap-0.5 ${depth > 0 && depth <= maxIndent ? "ml-3" : ""}`}>
      {nodes.map((n, i) => (
        <TagItem key={n.item.id} node={n} id={`${id}-${i}`} depth={depth} {...rest} />
      ))}
    </ul>
  );
}

function TagItem({ node, id, depth, selected, select, collapsed }: Omit<ListProps, "nodes" | "hidden"> & { node: TagNode }) {
  const tag = node.item;
  const parent = node.children.length > 0;
  const open = !parent || !collapsed.isCollapsed(tag.id);
  const on = selected === tag.id;
  const inside = !open && selected !== null && containsTag(node, selected);
  return (
    <li>
      <div className="flex items-center gap-0.5">
        {parent ? (
          <button
            type="button"
            className={toggleClass}
            aria-expanded={open}
            aria-controls={`${id}-c`}
            aria-label={t.subTagsOf(tag.name)}
            title={t.subTagsOf(tag.name)}
            onClick={() => collapsed.toggle(tag.id)}
          >
            <Chevron open={open} />
          </button>
        ) : (
          <span className="size-6 shrink-0" />
        )}
        <button
          type="button"
          className={`${item} min-w-0 ${on ? active : inside ? within : idle}`}
          aria-current={on ? "page" : undefined}
          title={tag.name}
          onClick={() => select({ kind: "tag", id: tag.id })}
        >
          <span className="flex min-w-0 items-center gap-2">
            {/* The colour is decoration: the name says which tag it is. */}
            <span aria-hidden data-testid="tag-dot" className={`size-2.5 shrink-0 rounded-full ${tag.color ? "" : "border border-line-control"}`} style={tag.color ? { backgroundColor: tag.color } : undefined} />
            <span className="truncate">{tag.name}</span>
          </span>
          {/* The subtree's distinct documents: what the list shows for the tag. */}
          <Count n={tag.subtreeCount ?? tag.documentCount} on={on} />
        </button>
      </div>
      {parent && <TagList id={`${id}-c`} hidden={!open} nodes={node.children} depth={depth + 1} selected={selected} select={select} collapsed={collapsed} />}
    </li>
  );
}
