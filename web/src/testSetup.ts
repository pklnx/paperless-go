import { configure } from "@testing-library/react";

// waitFor and findBy* wait for their condition until shortly before the
// test itself times out (vitest's testTimeout, 5 s, in vite.config.ts),
// not only testing-library's default 1 s: on a busy CI runner a stubbed
// fetch can take longer than 1 s to render, and a test should fail on a
// missing signal, not on a slow machine. Shortly before, so a failure
// still shows waitFor's message with the DOM.
configure({ asyncUtilTimeout: 4_500 });

// Browser APIs that Carbon components use and jsdom lacks. Tests that need
// a specific answer (theme.test.ts) stub them again. Tests in the node
// environment (build/, scripts/) have no window and need none of it.
if (typeof window !== "undefined") {
  if (typeof window.ResizeObserver === "undefined") {
    window.ResizeObserver = class ResizeObserver {
      observe() {}
      unobserve() {}
      disconnect() {}
    };
  }
  if (typeof window.matchMedia === "undefined") {
    window.matchMedia = (query: string): MediaQueryList => ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    });
  }
}
