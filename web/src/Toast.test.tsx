import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, renderHook, screen, within } from "@testing-library/react";
import { Toast, noticeDuration, noticeTimer, useNotice, type Notify } from "./Toast";

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

describe("Toast", () => {
  it("runs the time again when a notice with a focused Undo is replaced by one without it (#143)", () => {
    const active = timers();
    let show!: Notify;
    function Harness() {
      const n = useNotice();
      show = n.show;
      return <Toast notice={n.notice} close={n.close} pause={n.pause} resume={n.resume} />;
    }
    render(<Harness />);
    // A keyboard user's row action: the focus moves to Undo and the time stops.
    act(() => show({ text: "moved", action: { label: "Undo", run: () => {} }, focus: true }));
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Undo" }));
    expect(active()).toEqual([]);
    // The next notice of the same kind has no Undo: the focused button goes without a blur.
    act(() => show({ text: "restored" }));
    expect(screen.queryByRole("button", { name: "Undo" })).toBeNull();
    expect(active()).toEqual([noticeDuration]);
  });

  /** The shell's notice with its hook, as App.tsx mounts it; returns show and the timers. */
  function mount() {
    const active = timers();
    let show!: Notify;
    function Harness() {
      const n = useNotice();
      show = n.show;
      return (
        <>
          <button type="button">Delete</button>
          <Toast notice={n.notice} close={n.close} pause={n.pause} resume={n.resume} />
        </>
      );
    }
    render(<Harness />);
    return { active, show: (n: Parameters<Notify>[0]) => act(() => show(n)) };
  }

  // Carbon's defaults (role alertdialog with hasFocus) would move the focus
  // into a notice with an action and never let it expire; Escape would
  // close it; two hidden "Focus sentinel" tab stops would sit in the live
  // region. Each test fails with those defaults.
  it("leaves the focus where it was for a pointer action with Undo, and expires", () => {
    const { active, show } = mount();
    const row = screen.getByRole("button", { name: "Delete" });
    // Delete clicked with the pointer: the focus stays on the row's button.
    fireEvent.pointerDown(row);
    act(() => row.focus());
    show({ text: "moved to the trash", action: { label: "Undo", run: () => {} } });
    expect(screen.getByRole("button", { name: "Undo" })).toBeTruthy();
    expect(document.activeElement).toBe(row);
    // Its 8 s run, and the notice is gone when they are over.
    expect(active()).toEqual([noticeDuration]);
    act(() => vi.mocked(noticeTimer.set).mock.calls.at(-1)![0]());
    expect(screen.queryByTestId("notice")).toBeNull();
    expect(screen.queryByRole("button", { name: "Undo" })).toBeNull();
  });

  it("focuses Undo for a keyboard user, and Escape does not close the notice", () => {
    const { active, show } = mount();
    const row = screen.getByRole("button", { name: "Delete" });
    fireEvent.keyDown(row, { key: "Enter" });
    act(() => row.focus());
    show({ text: "moved to the trash", action: { label: "Undo", run: () => {} }, focus: true });
    const undo = screen.getByRole("button", { name: "Undo" });
    expect(document.activeElement).toBe(undo);
    expect(active()).toEqual([]);
    fireEvent.keyDown(undo, { key: "Escape" });
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.getByRole("button", { name: "Undo" })).toBe(undo);
    expect(screen.getByTestId("notice").textContent).toContain("moved to the trash");
  });

  it("renders no focus sentinels and announces through the app's live regions only", () => {
    const { show } = mount();
    show({ text: "restored", action: { label: "Undo", run: () => {} } });
    expect(screen.queryByText("Focus sentinel")).toBeNull();
    expect(document.body.innerHTML).not.toContain("Focus sentinel");
    expect(screen.getByTestId("notice").querySelector('[role="alertdialog"], [role="status"], [role="alert"]')).toBeNull();
    expect(screen.getByTestId("notice-polite").textContent).toContain("restored");
    show({ text: "failed", error: true });
    expect(screen.getByTestId("notice-assertive").textContent).toContain("failed");
    expect(screen.getByTestId("notice-polite").textContent).toBe("");
  });
});

describe("the notice in German", () => {
  // Carbon's English defaults of ActionableNotification that the notice must not show.
  const english = ["close notification", "Close", "error icon", "success icon", "Focus sentinel"];

  afterEach(() => {
    vi.resetModules();
    vi.unstubAllGlobals();
  });

  /** Mounts the notice afresh with the browser set to German: i18n.ts reads the language when it loads. */
  async function renderGerman() {
    vi.resetModules();
    vi.stubGlobal("navigator", { ...navigator, languages: ["de-DE", "en-US"], language: "de-DE" });
    const { Toast: GermanToast, useNotice: useGermanNotice } = await import("./Toast");
    const { messagesFor, t: texts } = await import("./i18n");
    const de = messagesFor("de");
    expect(texts.closeDialog).toBe(de.closeDialog);
    let show!: Notify;
    function Harness() {
      const n = useGermanNotice();
      show = n.show;
      return <GermanToast notice={n.notice} close={n.close} pause={n.pause} resume={n.resume} />;
    }
    render(<Harness />);
    return { de, show: (n: Parameters<Notify>[0]) => act(() => show(n)) };
  }

  it("names the close button and the status icons in German, nothing in English", async () => {
    const { de, show } = await renderGerman();
    // A success notice with Undo, as a delete shows it.
    show({ text: "In den Papierkorb verschoben.", action: { label: de.undo, run: () => {} } });
    const notice = within(screen.getByTestId("notice"));
    expect(notice.getByRole("button", { name: "Schließen" })).toBeTruthy();
    expect(notice.getByRole("button", { name: de.undo })).toBeTruthy();
    expect(notice.getByTitle(de.carbon.status.success)).toBeTruthy();
    for (const s of english) expect(document.body.innerHTML, s).not.toContain(s);

    // An error notice, in the assertive region.
    show({ text: "Fehlgeschlagen.", error: true });
    const error = within(screen.getByTestId("notice"));
    expect(screen.getByTestId("notice-assertive").contains(screen.getByTestId("notice"))).toBe(true);
    expect(error.getByRole("button", { name: "Schließen" })).toBeTruthy();
    expect(error.getByTitle(de.carbon.status.error)).toBeTruthy();
    for (const s of english) expect(document.body.innerHTML, s).not.toContain(s);
  });
});
