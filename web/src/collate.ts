// Sorting of visible name lists. The server's ORDER BY depends on the
// installation's locale, so the UI sorts every name list itself, in the UI
// language.
import { lang, type Lang } from "./i18n";

const collators = new Map<Lang, Intl.Collator>();

function collatorFor(l: Lang): Intl.Collator {
  let c = collators.get(l);
  if (!c) {
    c = new Intl.Collator(l, { usage: "sort", sensitivity: "variant", numeric: true });
    collators.set(l, c);
  }
  return c;
}

/**
 * A comparator for names: `Intl.Collator` in the language, numeric ("2" before
 * "10"). Strings the collator treats as equal (NFC and NFD "Ä") fall back to
 * UTF-16 code unit order (what `<` compares), so the order is fixed whatever
 * order the input came in.
 */
export function nameCompare(l: Lang = lang): (a: string, b: string) => number {
  const c = collatorFor(l);
  return (a, b) => c.compare(a, b) || (a < b ? -1 : a > b ? 1 : 0);
}

/** A sorted copy of the items by name; the input stays unchanged. */
export function sortByName<T>(items: readonly T[], name: (x: T) => string, l: Lang = lang): T[] {
  const cmp = nameCompare(l);
  return [...items].sort((a, b) => cmp(name(a), name(b)));
}

/** A node of a name tree. */
export interface NameNode<T> {
  item: T;
  children: NameNode<T>[];
}

/**
 * The tree of a flat list with parent ids (tags), every level sorted by name.
 * A node whose parent is not in the list is a root. Nodes in a cycle (the
 * database refuses them) are roots too, so every node appears exactly once
 * and bad data can never loop.
 */
export function nameTree<T extends { id: string; parentId: string | null }>(items: readonly T[], name: (x: T) => string, l: Lang = lang): NameNode<T>[] {
  const ids = new Set(items.map((x) => x.id));
  const byParent = new Map<string | null, T[]>();
  for (const x of items) {
    const key = x.parentId !== null && ids.has(x.parentId) ? x.parentId : null;
    const list = byParent.get(key);
    if (list) list.push(x);
    else byParent.set(key, [x]);
  }
  const visited = new Set<string>();
  const build = (x: T): NameNode<T> => {
    visited.add(x.id);
    const kids = sortByName(byParent.get(x.id) ?? [], name, l);
    const children: NameNode<T>[] = [];
    for (const k of kids) if (!visited.has(k.id)) children.push(build(k));
    return { item: x, children };
  };
  const roots: NameNode<T>[] = [];
  for (const x of sortByName(byParent.get(null) ?? [], name, l)) if (!visited.has(x.id)) roots.push(build(x));
  // Whatever the walk did not reach hangs in a cycle: those nodes become roots.
  for (const x of sortByName(items, name, l)) if (!visited.has(x.id)) roots.push(build(x));
  return roots;
}
