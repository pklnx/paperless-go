// The selection of the document list: which loaded documents are checked,
// for a bulk change of at most maxSelection of them by ID.
import { useRef } from "react";

/** The most documents a bulk change by ID may name (the server's store.MaxBulkIDs). */
export const maxSelection = 500;

export interface Picked {
  ids: Set<string>;
  /** Some were left out: the selection would have gone past maxSelection. */
  capped: boolean;
}

/** ids with more added, up to maxSelection; the order of more decides which are left out. */
function addCapped(ids: ReadonlySet<string>, more: readonly string[]): Picked {
  const out = new Set(ids);
  let capped = false;
  for (const id of more) {
    if (out.has(id)) continue;
    if (out.size >= maxSelection) {
      capped = true;
      break;
    }
    out.add(id);
  }
  return { ids: out, capped };
}

/**
 * A click on the checkbox of id. Without shift it toggles id. With shift
 * and an anchor (the last plain click) in order, every document between
 * them takes the state the clicked one gets.
 */
export function clickSelect(order: readonly string[], ids: ReadonlySet<string>, anchor: string | null, id: string, shift: boolean): Picked {
  const on = !ids.has(id);
  const a = anchor === null ? -1 : order.indexOf(anchor);
  const b = order.indexOf(id);
  const range = shift && a >= 0 && b >= 0 ? order.slice(Math.min(a, b), Math.max(a, b) + 1) : [id];
  if (!on) {
    const out = new Set(ids);
    for (const x of range) out.delete(x);
    return { ids: out, capped: false };
  }
  // The clicked one first, so it is in even when the cap cuts the range.
  return addCapped(ids, [id, ...range]);
}

/** "Select all loaded": the loaded documents in list order, at most maxSelection. */
export function selectAll(order: readonly string[]): Picked {
  return addCapped(new Set(), order);
}

/** ids without those no longer loaded; ids itself when nothing changes. */
export function pruneSelection(ids: Set<string>, order: readonly string[]): Set<string> {
  const loaded = new Set(order);
  for (const id of ids) if (!loaded.has(id)) return new Set([...ids].filter((x) => loaded.has(x)));
  return ids;
}

/** How long a touch must rest on a row to select it. */
export const longPressDelay = 500;

/** The timer of the long press; tests replace it to fire it themselves. */
export const longPressTimer = {
  set: (fn: () => void, ms: number): number => window.setTimeout(fn, ms),
  clear: (id: number) => window.clearTimeout(id),
};

/**
 * Pointer handlers that call onLongPress when a touch rests on the element
 * for longPressDelay without moving away. Mouse and pen are left alone:
 * they have the checkbox. The click that ends a long press is swallowed,
 * so it does not also open or toggle anything.
 */
export function useLongPress(onLongPress: () => void) {
  const timer = useRef<number | null>(null);
  const start = useRef<{ x: number; y: number } | null>(null);
  const fired = useRef(false);
  const cb = useRef(onLongPress);
  cb.current = onLongPress;
  const cancel = () => {
    if (timer.current !== null) longPressTimer.clear(timer.current);
    timer.current = null;
    start.current = null;
  };
  return {
    onPointerDown: (e: React.PointerEvent) => {
      fired.current = false;
      if (e.pointerType !== "touch") return;
      // Not on the row's own controls: links, buttons, the checkbox.
      if ((e.target as HTMLElement).closest("a, button, input, label")) return;
      cancel();
      start.current = { x: e.clientX, y: e.clientY };
      timer.current = longPressTimer.set(() => {
        timer.current = null;
        fired.current = true;
        cb.current();
      }, longPressDelay);
    },
    onPointerMove: (e: React.PointerEvent) => {
      const s = start.current;
      if (s && Math.hypot(e.clientX - s.x, e.clientY - s.y) > 10) cancel();
    },
    onPointerUp: cancel,
    onPointerCancel: cancel,
    onContextMenu: (e: React.MouseEvent) => {
      // The phone's own menu for a long press.
      if (fired.current || timer.current !== null) e.preventDefault();
    },
    onClickCapture: (e: React.MouseEvent) => {
      if (fired.current) {
        fired.current = false;
        e.preventDefault();
        e.stopPropagation();
      }
    },
  };
}
