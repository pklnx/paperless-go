// The documents view's column: an admin's "Everyone's documents", the saved
// views, and one tree of the tags (nested), the correspondents and the
// document types. From lg up it is the first column of the view; below lg
// it opens as a modal overlay from the list toolbar's Browse button (App).
import { useId, useState, type KeyboardEvent, type ReactNode, type SyntheticEvent } from "react";
import { FeatureFlags, IconButton, Toggle, TreeNode, TreeView } from "@carbon/react";
import { Close } from "@carbon/icons-react";
import type { Metadata, NamedRow, User } from "./api";
import { sortByName } from "./collate";
import { t } from "./i18n";
import { buildTagTree, containsTag, type CollapsedTags, type TagNode } from "./tagTree";
import { activeFilter, type Filter, type ViewState } from "./urlState";

/** Something loaded for the navigation: the data, or why it failed. */
export interface Loaded<T> {
  data: T | null;
  error: string;
}

interface Props {
  state: ViewState;
  /** Sets the one list filter; null lists everything. */
  select: (f: Filter | null) => void;
  user: User;
  metadata: Loaded<Metadata>;
  collapsed: CollapsedTags;
  /** An admin's "Everyone's documents". */
  everyone: boolean;
  setEveryone: (on: boolean) => void;
  /** The "Saved views" list (SavedViews.tsx), above the tree. */
  savedViews?: ReactNode;
  /** Closes the overlay (below lg); no close button without it. */
  onClose?: () => void;
}

type Group = "tags" | "corr" | "type";

/** Node ids: the three groups, a tag, a correspondent and a document type. */
export function nodeIds(base: string) {
  return {
    group: (g: Group) => `${base}-g-${g}`,
    tag: (id: string) => `${base}-t-${id}`,
    named: (kind: "corr" | "type", id: string) => `${base}-${kind}-${id}`,
  };
}

/**
 * A node's name and count, with a tag's colour as decoration (the name says
 * which tag it is); the space keeps name and count apart in the accessible
 * name and in the tooltip.
 */
function Label({ name, count, color }: { name: string; count?: number; color?: string }) {
  return (
    <span className="nav-tree__label" title={name}>
      {color !== undefined && <span aria-hidden data-testid="tag-dot" className={`nav-tree__dot${color ? "" : " nav-tree__dot--none"}`} style={color ? { backgroundColor: color } : undefined} />}
      <span className="nav-tree__name">{name}</span>
      {count !== undefined && " "}
      {count !== undefined && <span className="nav-tree__count">{count}</span>}
    </span>
  );
}

/**
 * Carbon calls a node's onSelect for a click and for Enter or Space. On a
 * node with children Enter also toggles it, so a node that only toggles
 * leaves Enter to that.
 */
const isEnter = (e: SyntheticEvent) => e.type === "keydown" && (e as KeyboardEvent).key === "Enter";

export function NavColumn({ state, select, user, metadata, collapsed, everyone, setEveryone, savedViews, onClose }: Props) {
  const { data, error } = metadata;
  const base = useId();
  const ids = nodeIds(base);
  const docs = state.view === "";
  const current = docs ? activeFilter(state) : null;
  // The three groups stay open unless closed here; nothing is stored.
  const [closedGroups, setClosedGroups] = useState<Group[]>([]);
  const groupOpen = (g: Group) => !closedGroups.includes(g);
  const toggleGroup = (g: Group) => setClosedGroups((c) => (c.includes(g) ? c.filter((x) => x !== g) : [...c, g]));

  const selected = !current ? "" : current.kind === "tag" ? ids.tag(current.id) : current.kind === "corr" || current.kind === "type" ? ids.named(current.kind, current.id) : "";
  const selectedTag = current?.kind === "tag" ? current.id : null;

  const group = (g: Group, label: string, within: boolean, children: ReactNode) => (
    <TreeNode
      id={ids.group(g)}
      aria-labelledby={`${ids.group(g)}__label`}
      className={within && !groupOpen(g) ? "nav-tree__node--within" : undefined}
      label={<Label name={label} />}
      isExpanded={groupOpen(g)}
      onToggle={() => toggleGroup(g)}
      // A group has nothing to select; a click or Space toggles it.
      onSelect={(e: SyntheticEvent) => {
        if (!isEnter(e)) toggleGroup(g);
      }}
    >
      {children}
    </TreeNode>
  );
  const named = (kind: "corr" | "type", rows: NamedRow[]) =>
    sortByName(rows, (r) => r.name).map((r) => (
      <TreeNode
        key={r.id}
        id={ids.named(kind, r.id)}
        aria-labelledby={`${ids.named(kind, r.id)}__label`}
        label={<Label name={r.name} count={r.documentCount} />}
        isExpanded={false}
        onSelect={() => select({ kind, id: r.id })}
      />
    ));

  return (
    <nav aria-label={t.browse} className="nav-browse">
      <div className="nav-browse__head">
        <h2 className="nav-browse__title">{t.browse}</h2>
        {onClose && (
          // Only in the overlay; the column has nothing to close.
          <IconButton kind="ghost" size="sm" align="bottom-end" label={t.closeBrowse} wrapperClasses="nav-column__close" data-drawer-focus onClick={onClose}>
            <Close />
          </IconButton>
        )}
      </div>
      {user.admin && (
        <Toggle
          id={`${base}-everyone`}
          className="nav-browse__toggle"
          size="sm"
          labelText={t.everyonesDocuments}
          labelA={t.carbon.toggleOff}
          labelB={t.carbon.toggleOn}
          toggled={everyone}
          onToggle={setEveryone}
        />
      )}
      {savedViews}
      {error && !data && (
        <p role="alert" className="nav-browse__note app-text-error">
          {t.loadMetadataFailed(error)}
        </p>
      )}
      {!data && !error && <p className="nav-browse__note app-text-secondary">{t.loading}</p>}
      {data && (data.tags.length > 0 || data.correspondents.length > 0 || data.documentTypes.length > 0) && (
        // Controlled: the selection follows the URL, and the expanded tags
        // follow tagTree.ts (saved per user).
        <FeatureFlags enableTreeviewControllable>
          <TreeView className="nav-tree" label={t.browse} hideLabel size="xs" selected={selected ? [selected] : []} active={selected}>
            {data.tags.length > 0 &&
              group(
                "tags",
                t.tags,
                selectedTag !== null,
                buildTagTree(data.tags).map((n) => <TagItem key={n.item.id} node={n} ids={ids} selected={selectedTag} select={select} collapsed={collapsed} />),
              )}
            {data.correspondents.length > 0 && group("corr", t.correspondents, current?.kind === "corr", named("corr", data.correspondents))}
            {data.documentTypes.length > 0 && group("type", t.documentTypes, current?.kind === "type", named("type", data.documentTypes))}
          </TreeView>
        </FeatureFlags>
      )}
    </nav>
  );
}

interface ItemProps {
  node: TagNode;
  ids: ReturnType<typeof nodeIds>;
  /** The selected tag, or null. */
  selected: string | null;
  select: (f: Filter) => void;
  collapsed: CollapsedTags;
}

function TagItem({ node, ids, selected, select, collapsed }: ItemProps) {
  const tag = node.item;
  const parent = node.children.length > 0;
  const open = !parent || !collapsed.isCollapsed(tag.id);
  // The selection inside a collapsed tag: show where it is.
  const inside = !open && selected !== null && containsTag(node, selected);
  const id = ids.tag(tag.id);
  return (
    <TreeNode
      id={id}
      aria-labelledby={`${id}__label`}
      className={inside ? "nav-tree__node--within" : undefined}
      // The subtree's distinct documents: what the list shows for the tag.
      label={<Label name={tag.name} count={tag.subtreeCount ?? tag.documentCount} color={tag.color} />}
      // Always a boolean: Carbon's controlled state must not switch to
      // uncontrolled when a tag gains or loses sub-tags.
      isExpanded={open}
      onToggle={() => collapsed.toggle(tag.id)}
      onSelect={() => select({ kind: "tag", id: tag.id })}
    >
      {parent ? node.children.map((c) => <TagItem key={c.item.id} node={c} ids={ids} selected={selected} select={select} collapsed={collapsed} />) : undefined}
    </TreeNode>
  );
}

/**
 * What the overlay focuses when it opens: the selected node, else the first
 * node, else its close button. Collapsed nodes are hidden and cannot take
 * the focus.
 */
export function columnFocusTarget(root: ParentNode | null | undefined): HTMLElement | null {
  if (!root) return null;
  const visible = (el: HTMLElement) => !el.closest(".cds--tree-node--hidden");
  const nodes = Array.from(root.querySelectorAll<HTMLElement>("[role=treeitem]")).filter(visible);
  return nodes.find((n) => n.getAttribute("aria-selected") === "true") ?? nodes[0] ?? root.querySelector<HTMLElement>("[data-drawer-focus]");
}
