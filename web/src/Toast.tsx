import { useCallback, useEffect, useRef, useState } from "react";
import { CloseIcon, iconButton } from "./icons";
import { t } from "./i18n";

/** How long a notice stays, in milliseconds: the time for its Undo. */
export const noticeDuration = 8000;

/** The timer of notices; tests replace it to fire it themselves. */
export const noticeTimer = {
  set: (fn: () => void, ms: number): number => window.setTimeout(fn, ms),
  clear: (id: number) => window.clearTimeout(id),
};

export interface Notice {
  text: string;
  error?: boolean;
  /** A button in the notice, such as Undo; clicking it closes the notice first. */
  action?: { label: string; run: () => void };
  /**
   * Move the keyboard focus into the notice (its action, else its close
   * button): after a row action the focused row is gone, and Undo stays
   * one key away. Only for keyboard users: after a pointer click the focus
   * stays where the click left it, and the notice keeps its time.
   */
  focus?: boolean;
}

/** Shows a notice; the shell's, so a notice outlives the page that showed it. */
export type Notify = (n: Notice) => void;

export interface Notices {
  notice: Notice | null;
  show: Notify;
  close: () => void;
  /** Stops the timer while the notice has the keyboard focus or the pointer (WCAG 2.2.1). */
  pause: () => void;
  /** Starts the full time again once keyboard focus and pointer left. */
  resume: () => void;
}

/**
 * One notice at a time: show replaces the current one and starts its timer
 * again; it closes by itself after noticeDuration (not while paused), by
 * its close button, or when its action runs.
 */
export function useNotice(): Notices {
  const [notice, setNotice] = useState<Notice | null>(null);
  const timer = useRef<number | null>(null);
  const paused = useRef(false);
  const shown = useRef<Notice | null>(null);
  const stop = () => {
    if (timer.current !== null) noticeTimer.clear(timer.current);
    timer.current = null;
  };
  const start = () => {
    stop();
    timer.current = noticeTimer.set(() => {
      timer.current = null;
      shown.current = null;
      setNotice(null);
    }, noticeDuration);
  };
  const close = useCallback(() => {
    stop();
    paused.current = false;
    shown.current = null;
    setNotice(null);
  }, []);
  const show = useCallback((n: Notice) => {
    // A notice of the other kind shows in the other live region: the
    // element under the pointer or focus is gone, and no leave or blur
    // ends the pause, so it ends here (a focused new one pauses again).
    if (shown.current && Boolean(shown.current.error) !== Boolean(n.error)) paused.current = false;
    shown.current = n;
    setNotice(n);
    if (paused.current) stop();
    else start();
  }, []);
  const pause = useCallback(() => {
    paused.current = true;
    stop();
  }, []);
  const resume = useCallback(() => {
    paused.current = false;
    if (shown.current) start();
  }, []);
  useEffect(() => stop, []);
  return { notice, show, close, pause, resume };
}

/** Where the focus goes when a focused notice closes: where it was, else the list's heading, else the main area. */
function focusAfterNotice(returnTo: Element | null) {
  if (returnTo instanceof HTMLElement && returnTo.isConnected && returnTo !== document.body && !returnTo.closest("[inert]")) {
    returnTo.focus();
    return;
  }
  const target = document.getElementById("list-title") ?? document.querySelector<HTMLElement>("main");
  target?.focus();
}

/**
 * The notice at the bottom of the screen: its text, its action and a
 * close button. Plain text only. The live regions stay mounted and only
 * their content changes, so screen readers announce each notice.
 */
export function Toast({ notice, close, pause, resume }: Pick<Notices, "notice" | "close" | "pause" | "resume">) {
  const box = useRef<HTMLDivElement>(null);
  // Where the focus was before the notice took it.
  const returnTo = useRef<Element | null>(null);
  // Whether the last input was a key (not a pointer), as :focus-visible
  // decides it: until a pointer is used, the keyboard is assumed.
  const keyboard = useRef(true);
  useEffect(() => {
    // A shortcut with a modifier (Ctrl+C) is no keyboard navigation.
    const onKey = (e: KeyboardEvent) => void (e.ctrlKey || e.metaKey || e.altKey || (keyboard.current = true));
    const onPointer = () => void (keyboard.current = false);
    document.addEventListener("keydown", onKey, true);
    document.addEventListener("pointerdown", onPointer, true);
    document.addEventListener("mousedown", onPointer, true);
    return () => {
      document.removeEventListener("keydown", onKey, true);
      document.removeEventListener("pointerdown", onPointer, true);
      document.removeEventListener("mousedown", onPointer, true);
    };
  }, []);
  // The pause has two reasons, keyboard focus and pointer; the time runs
  // again only once both are gone.
  const focused = useRef(false);
  const hovered = useRef(false);
  const update = () => (focused.current || hovered.current ? pause() : resume());
  const kind = useRef<boolean | null>(null);
  useEffect(() => {
    // A notice of the other kind, or none, is another element (or none):
    // the pointer is not on it, and neither is the focus. One of the same
    // kind keeps its element, and the pause with it (as useNotice does).
    const now = notice ? Boolean(notice.error) : null;
    if (now !== kind.current) hovered.current = false;
    kind.current = now;
    if (!box.current?.contains(document.activeElement)) {
      focused.current = false;
      returnTo.current = null;
    }
    if (!notice?.focus || !keyboard.current) return;
    const button = box.current?.querySelector("button");
    if (!button) return;
    if (!box.current?.contains(document.activeElement)) returnTo.current = document.activeElement;
    button.focus();
  }, [notice]);

  // Closing by a button of the notice: the focus goes back, not to the page's body.
  const closeHere = () => {
    const had = box.current?.contains(document.activeElement) ?? false;
    close();
    if (had) focusAfterNotice(returnTo.current);
  };

  const content = notice && (
    <div
      ref={box}
      data-testid="notice"
      onFocus={() => {
        // Focus from a pointer click does not stop the time; the pointer on it does.
        if (!keyboard.current || focused.current) return;
        focused.current = true;
        update();
      }}
      onBlur={(e) => {
        if (box.current?.contains(e.relatedTarget as Node | null) || !focused.current) return;
        focused.current = false;
        update();
      }}
      onMouseEnter={() => {
        hovered.current = true;
        update();
      }}
      onMouseLeave={() => {
        hovered.current = false;
        update();
      }}
      className="pointer-events-auto flex max-w-lg items-center gap-3 rounded-lg bg-ink py-1.5 pr-1.5 pl-4 text-sm text-bg shadow-lg"
    >
      <span className="min-w-0 wrap-anywhere">{notice.text}</span>
      {notice.action && (
        <button
          type="button"
          className="shrink-0 rounded-md px-2 py-1 font-semibold underline-offset-2 hover:underline pointer-coarse:min-h-11"
          onClick={() => {
            const run = notice.action!.run;
            closeHere();
            run();
          }}
        >
          {notice.action.label}
        </button>
      )}
      <button type="button" className={`${iconButton} text-bg hover:bg-transparent hover:text-bg`} aria-label={t.closeDialog} title={t.closeDialog} onClick={closeHere}>
        <CloseIcon />
      </button>
    </div>
  );
  const region = "pointer-events-none fixed inset-x-0 bottom-[calc(1rem+env(safe-area-inset-bottom))] z-40 flex justify-center px-4";
  return (
    <>
      {/* Plain live regions (no role): they stay in the page empty, and only their content changes. */}
      <div aria-live="polite" aria-atomic="true" data-testid="notice-polite" className={region}>
        {notice && !notice.error && content}
      </div>
      <div aria-live="assertive" aria-atomic="true" data-testid="notice-assertive" className={region}>
        {notice?.error && content}
      </div>
    </>
  );
}
