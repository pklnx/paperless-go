// The tag tree of the sidebar and which of its tags are collapsed, per user
// and browser. Only collapsed tags are stored, so the default is everything
// expanded. A convenience, not data: storage may be blocked.
import { useCallback, useEffect, useRef, useState } from "react";
import type { Tag } from "./api";
import { nameTree, type NameNode } from "./collate";

export type TagNode = NameNode<Tag>;

/** The tree of a flat tag list, every level sorted by name (collate.ts); a tag whose parent is missing is a root. */
export function buildTagTree(tags: readonly Tag[]): TagNode[] {
  return nameTree(tags, (x) => x.name);
}

/** Whether the subtree below node (not node itself) holds the tag with id. */
export function containsTag(node: TagNode, id: string): boolean {
  return node.children.some((c) => c.item.id === id || containsTag(c, id));
}

/** The IDs of the tags above id, nearest first; empty for a root or an unknown tag. */
export function ancestorIds(tags: readonly Tag[], id: string): string[] {
  const byId = new Map(tags.map((x) => [x.id, x]));
  const out: string[] = [];
  const seen = new Set([id]);
  let p = byId.get(id)?.parentId ?? null;
  while (p !== null && byId.has(p) && !seen.has(p)) {
    out.push(p);
    seen.add(p);
    p = byId.get(p)?.parentId ?? null;
  }
  return out;
}

const key = (user: string) => `paperless-go.tags.${user}`;

/** The collapsed tag IDs stored for user; none when storage is empty, invalid or blocked. */
export function loadCollapsed(user: string): string[] {
  try {
    const v: unknown = JSON.parse(localStorage.getItem(key(user)) ?? "null");
    if (!v || typeof v !== "object" || !("v" in v) || v.v !== 1 || !("collapsed" in v) || !Array.isArray(v.collapsed)) return [];
    return v.collapsed.filter((x): x is string => typeof x === "string");
  } catch {
    return [];
  }
}

export function saveCollapsed(user: string, ids: readonly string[]) {
  try {
    if (ids.length === 0) localStorage.removeItem(key(user));
    else localStorage.setItem(key(user), JSON.stringify({ v: 1, collapsed: ids }));
  } catch {
    // not persisted; still applied for this page
  }
}

/** Drops IDs of tags that no longer exist; ids itself when nothing changes. */
export function prune(ids: string[], tags: readonly Tag[]): string[] {
  const known = new Set(tags.map((x) => x.id));
  const kept = ids.filter((x) => known.has(x));
  return kept.length === ids.length ? ids : kept;
}

export interface CollapsedTags {
  isCollapsed: (id: string) => boolean;
  toggle: (id: string) => void;
  /** Expands the tags above id, so it shows. */
  reveal: (id: string) => void;
}

/** The sidebar's collapsed tags for user, pruned after each load of tags. */
export function useCollapsedTags(user: string, tags: readonly Tag[] | null): CollapsedTags {
  const [ids, setIds] = useState<string[]>(() => loadCollapsed(user));
  const saved = useRef(JSON.stringify(ids));
  const tagsRef = useRef(tags);
  tagsRef.current = tags;

  useEffect(() => {
    if (tags) setIds((prev) => prune(prev, tags));
  }, [tags]);

  useEffect(() => {
    const v = JSON.stringify(ids);
    if (v === saved.current) return;
    saved.current = v;
    saveCollapsed(user, ids);
  }, [ids, user]);

  return {
    isCollapsed: (id) => ids.includes(id),
    toggle: useCallback((id: string) => setIds((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id])), []),
    reveal: useCallback((id: string) => {
      const all = tagsRef.current;
      if (!all) return;
      const open = new Set(ancestorIds(all, id));
      setIds((prev) => (prev.some((x) => open.has(x)) ? prev.filter((x) => !open.has(x)) : prev));
    }, []),
  };
}

/**
 * The text colour on a tag chip of colour bg ("#RRGGBB"): black or white,
 * whichever contrasts more (WCAG relative luminance). The chip keeps its
 * colour in both themes, so the text depends on the colour only.
 */
export function chipInk(bg: string): "#000000" | "#ffffff" {
  const m = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(bg);
  if (!m) return "#000000";
  const lin = (h: string) => {
    const c = parseInt(h, 16) / 255;
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  };
  const l = 0.2126 * lin(m[1]!) + 0.7152 * lin(m[2]!) + 0.0722 * lin(m[3]!);
  // Contrast with black (l + 0.05) / 0.05 against white 1.05 / (l + 0.05).
  return (l + 0.05) * (l + 0.05) >= 0.05 * 1.05 ? "#000000" : "#ffffff";
}

/** The tags as one list in tree order (each level sorted by name), with their depth: for pickers that indent. */
export function flatTagTree(tags: readonly Tag[]): { tag: Tag; depth: number }[] {
  const out: { tag: Tag; depth: number }[] = [];
  const walk = (nodes: TagNode[], depth: number) => {
    for (const n of nodes) {
      out.push({ tag: n.item, depth });
      walk(n.children, depth + 1);
    }
  };
  walk(buildTagTree(tags), 0);
  return out;
}
