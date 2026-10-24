import { useEffect, useLayoutEffect, useRef, type SyntheticEvent } from "react";
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

/**
 * The element the focus starts on, Carbon's choice: the element marked
 * data-modal-primary-focus, else the secondary button of a danger modal
 * (the safe choice), else the primary button, else the close button.
 */
function initialFocus(modal: HTMLElement, danger: boolean): HTMLElement | null {
  return modal.querySelector<HTMLElement>("[data-modal-primary-focus]") ?? (danger ? modal.querySelector<HTMLElement>(".cds--btn--secondary") : null) ?? modal.querySelector<HTMLElement>(".cds--btn--primary") ?? modal.querySelector<HTMLElement>(".cds--modal-close");
}

/** The floating parts of Carbon widgets a Modal may hold, which render outside it: the focus may go there. */
const floating = '.cds--popover-content, .cds--overflow-menu-options, .cds--tooltip, [role="menu"], [role="listbox"]';

/** The same Modal without the danger style: for forms like Add user. */
export function AppModal({ onRequestClose, returnFocus, danger = false, ...props }: DangerModalProps & { danger?: boolean }) {
  const modal = useRef<HTMLDivElement>(null);
  const opener = useRef<Element | null>(null);
  const fallback = useRef(returnFocus);
  useLayoutEffect(() => {
    fallback.current = returnFocus;
  });
  // Closing: set before the focus goes back, so the trap below lets it.
  const closed = useRef(false);
  useLayoutEffect(() => {
    opener.current = document.activeElement;
    return () => {
      closed.current = true;
      const el = opener.current;
      if (canTakeFocus(el)) {
        el.focus();
        if (document.activeElement === el) return;
      }
      const other = fallback.current?.();
      if (other?.isConnected) other.focus();
    };
  }, []);
  // The focus stays in the Modal while it is open. Something else may move
  // it out after Carbon's initial focus: the SideNav overlay, closing on the
  // click that asked, returns the focus to its menu button. Carbon's own
  // answer to that is a blur handler that wraps the focus a tick later to
  // the last button (Discard) instead of the safe one. So the move is
  // stopped before Carbon sees it (focusout in the capture phase; a focus
  // inside the Modal, or in a floating menu of it, passes), and the focus
  // comes back to the initial element at once. Whatever moved it.
  useEffect(() => {
    const outside = (target: EventTarget | null): target is Node => {
      const root = modal.current;
      return root !== null && !closed.current && target instanceof Node && !root.contains(target) && !(target instanceof Element && target.closest(floating));
    };
    const onFocusOut = (e: FocusEvent) => {
      if (outside(e.relatedTarget)) e.stopPropagation();
    };
    const onFocusIn = (e: FocusEvent) => {
      if (outside(e.target)) initialFocus(modal.current!, danger)?.focus();
    };
    document.addEventListener("focusout", onFocusOut, true);
    document.addEventListener("focusin", onFocusIn, true);
    return () => {
      document.removeEventListener("focusout", onFocusOut, true);
      document.removeEventListener("focusin", onFocusIn, true);
    };
  }, [danger]);
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
