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
   * one key away.
   */
  focus?: boolean;
}

/** Shows a notice; the shell's, so a notice outlives the page that showed it. */
export type Notify = (n: Notice) => void;

/**
 * One notice at a time: show replaces the current one and starts its timer
 * again; it closes by itself after noticeDuration, by its close button, or
 * when its action runs.
 */
export function useNotice(): { notice: Notice | null; show: (n: Notice) => void; close: () => void } {
  const [notice, setNotice] = useState<Notice | null>(null);
  const timer = useRef<number | null>(null);
  const stop = () => {
    if (timer.current !== null) noticeTimer.clear(timer.current);
    timer.current = null;
  };
  const close = useCallback(() => {
    stop();
    setNotice(null);
  }, []);
  const show = useCallback((n: Notice) => {
    stop();
    setNotice(n);
    timer.current = noticeTimer.set(() => {
      timer.current = null;
      setNotice(null);
    }, noticeDuration);
  }, []);
  useEffect(() => stop, []);
  return { notice, show, close };
}

/** The notice at the bottom of the screen: its text, its action and a close button. Plain text only. */
export function Toast({ notice, close }: { notice: Notice | null; close: () => void }) {
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (notice?.focus) box.current?.querySelector("button")?.focus();
  }, [notice]);
  if (!notice) return null;
  return (
    <div className="pointer-events-none fixed inset-x-0 bottom-[calc(1rem+env(safe-area-inset-bottom))] z-40 flex justify-center px-4">
      <div
        ref={box}
        role={notice.error ? "alert" : "status"}
        data-testid="notice"
        className="pointer-events-auto flex max-w-lg items-center gap-3 rounded-lg bg-ink py-1.5 pr-1.5 pl-4 text-sm text-bg shadow-lg"
      >
        <span className="min-w-0 wrap-anywhere">{notice.text}</span>
        {notice.action && (
          <button
            type="button"
            className="shrink-0 rounded-md px-2 py-1 font-semibold underline-offset-2 hover:underline pointer-coarse:min-h-11"
            onClick={() => {
              const run = notice.action!.run;
              close();
              run();
            }}
          >
            {notice.action.label}
          </button>
        )}
        <button type="button" className={`${iconButton} text-bg hover:bg-transparent hover:text-bg`} aria-label={t.closeDialog} title={t.closeDialog} onClick={close}>
          <CloseIcon />
        </button>
      </div>
    </div>
  );
}
