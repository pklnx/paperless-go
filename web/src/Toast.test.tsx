import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, renderHook } from "@testing-library/react";
import { noticeDuration, noticeTimer, useNotice } from "./Toast";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

/** The notice timers set and not cleared. */
function timers() {
  const live = new Map<number, number>();
  let next = 0;
  vi.spyOn(noticeTimer, "set").mockImplementation((_fn, ms) => {
    live.set(++next, ms);
    return next;
  });
  vi.spyOn(noticeTimer, "clear").mockImplementation((id) => void live.delete(id));
  return () => [...live.values()];
}

describe("useNotice", () => {
  it("keeps a pause for a notice of the same kind, and ends it when one of the other kind replaces it", () => {
    const active = timers();
    const { result } = renderHook(() => useNotice());
    act(() => result.current.show({ text: "moved" }));
    expect(active()).toEqual([noticeDuration]);
    // The pointer is on it.
    act(() => result.current.pause());
    expect(active()).toEqual([]);
    // Same kind, same element under the pointer: still paused.
    act(() => result.current.show({ text: "restored" }));
    expect(active()).toEqual([]);
    // An error shows in the other live region: the hovered element is gone, so its time runs.
    act(() => result.current.show({ text: "failed", error: true }));
    expect(active()).toEqual([noticeDuration]);
    // And back from an error to a status, likewise.
    act(() => result.current.pause());
    act(() => result.current.show({ text: "ok" }));
    expect(active()).toEqual([noticeDuration]);
  });
});
