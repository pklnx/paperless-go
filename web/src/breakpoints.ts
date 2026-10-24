// Carbon's breakpoints (@carbon/layout 11: md 42rem, lg 66rem). From lg up
// the shell's navigation is a rail (part 2 of #150); below lg it is an
// overlay. layout.css uses the same widths, and Carbon's own CSS ends
// "below lg" at 65.98rem.
import { useSyncExternalStore } from "react";

export const lg = "(min-width: 66rem)";

/** Whether a media query matches right now. False without matchMedia. */
export function matchesNow(query: string): boolean {
  return typeof window.matchMedia === "function" && window.matchMedia(query).matches;
}

/** Whether a media query matches, following changes (resize, rotation). False without matchMedia. */
export function useMediaQuery(query: string): boolean {
  return useSyncExternalStore(
    (changed) => {
      const mql = typeof window.matchMedia === "function" ? window.matchMedia(query) : null;
      mql?.addEventListener("change", changed);
      return () => mql?.removeEventListener("change", changed);
    },
    () => (typeof window.matchMedia === "function" ? window.matchMedia(query).matches : false),
  );
}
