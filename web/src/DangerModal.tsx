import { useLayoutEffect, useRef, type SyntheticEvent } from "react";
import { createPortal } from "react-dom";
import { FeatureFlags, Modal, type ModalProps } from "@carbon/react";
import { t } from "./i18n";

export type DangerModalProps = Omit<ModalProps, "open" | "danger" | "closeButtonLabel"> & {
  /**
   * Where the focus goes when the Modal closes, if the element that opened
   * it is gone by then (a menu item of an OverflowMenu, for example).
   */
  returnFocus?: () => HTMLElement | null | undefined;
};

/**
 * A Carbon danger Modal that exists only while it is open: the caller
 * mounts it to ask and unmounts it to close, so each opening starts fresh.
 * It sits in document.body, above the page and the header, and the focus
 * goes back to where it was (the button that opened it) when it closes.
 * Carbon focuses the element marked data-modal-primary-focus, otherwise
 * the secondary button (Cancel): the safe choice.
 */
export function DangerModal(props: DangerModalProps) {
  return <AppModal danger {...props} />;
}

/**
 * Whether the focus can go back to el: still in the document, not the
 * body, not behind an inert ancestor (the closed SideNav overlay keeps
 * the link that opened a question) and not hidden.
 */
function canTakeFocus(el: Element | null): el is HTMLElement {
  if (!(el instanceof HTMLElement) || !el.isConnected || el === document.body || el.closest("[inert]")) return false;
  return typeof el.checkVisibility !== "function" || el.checkVisibility();
}

/** The open AppModals' roots (each a child of document.body), the topmost last. */
const openModals: { root: () => HTMLElement | null }[] = [];
/** Marks what this module made inert, so that it restores only that. */
const INERT_MARK = "data-modal-inert";

/**
 * While an AppModal is open, everything else in document.body is inert
 * (the platform's attribute: no focus, no pointer events, hidden from
 * assistive technology): the page behind it, and the Modals below it.
 * So nothing else can take the focus from the topmost Modal, whether a
 * page effect that returns the focus to a button, or a lower Carbon
 * Modal's own focus wrap and Escape; the focus ends where Carbon put it
 * on open (the safe button), Tab stays inside, and Escape and Enter go
 * to the Modal in front. Called when a Modal opens and when one closes,
 * before the focus goes back, so it can go back into the Modal below.
 */
function syncInert() {
  const top = openModals.at(-1)?.root() ?? null;
  for (const el of Array.from(document.body.children)) {
    if (top !== null && el !== top) {
      if (!el.hasAttribute("inert")) {
        el.setAttribute("inert", "");
        el.setAttribute(INERT_MARK, "");
      }
    } else if (el.hasAttribute(INERT_MARK)) {
      el.removeAttribute("inert");
      el.removeAttribute(INERT_MARK);
    }
  }
}

/** The same Modal without the danger style: for forms like Add user. */
export function AppModal({ onRequestClose, returnFocus, danger = false, ...props }: DangerModalProps & { danger?: boolean }) {
  const modal = useRef<HTMLDivElement>(null);
  const opener = useRef<Element | null>(null);
  const fallback = useRef(returnFocus);
  useLayoutEffect(() => {
    fallback.current = returnFocus;
  });
  // Open: topmost from here on, before Carbon's own effect focuses the
  // first element. Closed: the rest is usable again before the focus goes
  // back to the opener, or to returnFocus when the opener cannot take it.
  // (StrictMode runs this once more in development; the stack is kept
  // by identity, so the order of closing does not matter.)
  useLayoutEffect(() => {
    const me = { root: () => modal.current };
    openModals.push(me);
    syncInert();
    opener.current = document.activeElement;
    return () => {
      const at = openModals.lastIndexOf(me);
      if (at >= 0) openModals.splice(at, 1);
      syncInert();
      const el = opener.current;
      if (canTakeFocus(el)) {
        el.focus();
        if (document.activeElement === el) return;
      }
      const other = fallback.current?.();
      if (other?.isConnected) other.focus();
    };
  }, []);
  // Carbon's Escape listener keeps the handler of the render that opened
  // the Modal; this one always calls the current handler, so a guard like
  // "not while it sends" sees the current state.
  const close = useRef(onRequestClose);
  useLayoutEffect(() => {
    close.current = onRequestClose;
  });
  return createPortal(
    // Without the flag, Carbon adds two hidden "Focus sentinel" tab stops.
    <FeatureFlags enableFocusWrapWithoutSentinels>
      <Modal
        ref={modal}
        open
        danger={danger}
        closeButtonLabel={t.carbon.closeModal}
        onRequestClose={(e: SyntheticEvent<HTMLElement>) => close.current?.(e)}
        {...props}
      />
    </FeatureFlags>,
    document.body,
  );
}
