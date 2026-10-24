import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { activeFilter, activeFilters, confirmLeave, documentQuery, filterPatch, formatState, parseState, setNavigationGuard, useViewState, withoutFilter, type ViewState } from "./urlState";

const empty = parseState("");

describe("parseState and formatState", () => {
  it("round-trip every field", () => {
    const s: ViewState = { q: "Rechnung tag:Steuer & mehr", inbox: "1", fav: "1", trash: "1", tag: "t1", corr: "c1", type: "d1", sort: "date", order: "asc", all: "1", doc: "x", view: "log", sv: "v1" };
    expect(parseState(formatState(s))).toEqual(s);
  });
  it("leave defaults out of the URL", () => {
    expect(formatState(empty)).toBe("");
    expect(formatState({ ...empty, tag: "t1" })).toBe("?tag=t1");
  });
  it("drop values they do not know", () => {
    expect(parseState("?inbox=yes&fav=0&sort=title&order=up&all=true")).toEqual(empty);
    expect(parseState("?sort=relevance&order=asc").sort).toBe("relevance");
  });
});

describe("filters", () => {
  it("allow one sidebar filter at a time", () => {
    const s = { ...empty, ...filterPatch({ kind: "tag", id: "t1" }) };
    expect(activeFilter(s)).toEqual({ kind: "tag", id: "t1" });
    const t = { ...s, ...filterPatch({ kind: "inbox" }) };
    expect(t).toEqual({ ...empty, inbox: "1" });
    expect({ ...t, ...filterPatch(null) }).toEqual(empty);
    expect(activeFilter(empty)).toBeNull();
  });
  it("make the trash one of them: choosing it clears the others, and another clears it", () => {
    const s = { ...empty, ...filterPatch({ kind: "tag", id: "t1" }), ...filterPatch({ kind: "trash" }) };
    expect(s).toEqual({ ...empty, trash: "1" });
    expect(activeFilter(s)).toEqual({ kind: "trash" });
    expect({ ...s, ...filterPatch({ kind: "fav" }) }).toEqual({ ...empty, fav: "1" });
    expect(formatState(s)).toBe("?trash=1");
  });
  it("list every filter a URL or saved view combines, and remove one at a time (#143)", () => {
    const s = parseState("?inbox=1&fav=1&tag=t1&corr=c1&type=d1");
    const all = activeFilters(s);
    expect(all).toEqual([{ kind: "inbox" }, { kind: "fav" }, { kind: "tag", id: "t1" }, { kind: "corr", id: "c1" }, { kind: "type", id: "d1" }]);
    expect(formatState({ ...s, ...withoutFilter({ kind: "tag", id: "t1" }) })).toBe("?inbox=1&fav=1&corr=c1&type=d1");
    expect(activeFilters(empty)).toEqual([]);
    expect(activeFilters({ ...s, trash: "1" })).toEqual([{ kind: "trash" }]);
  });
});

describe("documentQuery", () => {
  it("keeps the typed search apart from the filters", () => {
    const q = documentQuery({ ...empty, q: "Rechnung", inbox: "1", corr: "c1" }, true);
    expect(q).toMatchObject({ q: "Rechnung", inbox: true, correspondent: "c1", tag: "", favorite: false, ownOnly: true });
  });
  it("searches nothing for blanks", () => {
    expect(documentQuery({ ...empty, q: "   " }, false).q).toBe("");
  });
  it("drops relevance without a search, and the direction with relevance", () => {
    expect(documentQuery({ ...empty, sort: "relevance", order: "asc" }, false)).toMatchObject({ sort: "", ascending: true });
    expect(documentQuery({ ...empty, q: "x", sort: "relevance", order: "asc" }, false)).toMatchObject({ sort: "relevance", ascending: false });
    // The default with a search is relevance too.
    expect(documentQuery({ ...empty, q: "x", order: "asc" }, false)).toMatchObject({ sort: "", ascending: false });
    expect(documentQuery({ ...empty, q: "x", sort: "date", order: "asc" }, false)).toMatchObject({ sort: "date", ascending: true });
  });
});

describe("documentQuery in the trash", () => {
  it("lists the trash newest deleted first, with the search but without the sidebar's filters", () => {
    expect(documentQuery({ ...empty, trash: "1", tag: "t1", inbox: "1" }, true)).toEqual({
      q: "",
      inbox: false,
      favorite: false,
      tag: "",
      correspondent: "",
      type: "",
      sort: "deleted",
      ascending: false,
      ownOnly: true,
      trashed: true,
    });
    // A search ranks by relevance unless another order is chosen; deleted has one direction.
    expect(documentQuery({ ...empty, trash: "1", q: "x" }, false)).toMatchObject({ q: "x", sort: "", trashed: true });
    expect(documentQuery({ ...empty, trash: "1", q: "x", sort: "deleted", order: "asc" }, false)).toMatchObject({ sort: "deleted", ascending: false });
    expect(documentQuery({ ...empty, trash: "1", sort: "date", order: "asc" }, false)).toMatchObject({ sort: "date", ascending: true });
  });
  it("ignores deleted outside the trash", () => {
    expect(documentQuery({ ...empty, sort: "deleted", order: "asc" }, false)).toMatchObject({ sort: "", ascending: true, trashed: false });
  });
});

describe("useViewState", () => {
  afterEach(() => {
    // Unmounted, so the next test's hook is the only subscriber.
    cleanup();
    window.history.replaceState(null, "", "/");
  });

  it("pushes a history entry per change, replaces on request, and follows popstate", () => {
    window.history.replaceState(null, "", "/");
    const { result } = renderHook(() => useViewState());
    const before = window.history.length;
    act(() => result.current[1]({ tag: "t1" }));
    expect(window.location.search).toBe("?tag=t1");
    expect(result.current[0].tag).toBe("t1");
    expect(window.history.length).toBe(before + 1);
    act(() => result.current[1]({ q: "x" }, { replace: true }));
    expect(window.location.search).toBe("?q=x&tag=t1");
    expect(window.history.length).toBe(before + 1);
    // The same state again adds nothing.
    act(() => result.current[1]({ q: "x" }));
    expect(window.history.length).toBe(before + 1);

    act(() => {
      window.history.replaceState(null, "", "/?inbox=1");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    expect(result.current[0]).toEqual({ ...empty, inbox: "1" });
  });

  it("asks the navigation guard before a change and on Back, and keeps the state when it refuses", () => {
    window.history.replaceState(null, "", "/?doc=d1");
    const { result } = renderHook(() => useViewState());
    let allow = false;
    const asked: (ViewState | null)[] = [];
    const remove = setNavigationGuard((next) => {
      asked.push(next);
      return allow;
    });
    try {
      act(() => result.current[1]({ doc: "d2" }));
      expect(asked.at(-1)?.doc).toBe("d2");
      expect(window.location.search).toBe("?doc=d1");

      // Back (here: the URL changed under the app, then popstate), refused:
      // the shown URL comes back and the state stays.
      act(() => {
        window.history.replaceState(null, "", "/?inbox=1");
        window.dispatchEvent(new PopStateEvent("popstate"));
      });
      expect(asked.at(-1)?.inbox).toBe("1");
      expect(window.location.search).toBe("?doc=d1");
      expect(result.current[0].doc).toBe("d1");

      allow = true;
      act(() => result.current[1]({ doc: "d2" }));
      expect(window.location.search).toBe("?doc=d2");
    } finally {
      remove();
    }
    // Removed: nothing is asked any more.
    const n = asked.length;
    act(() => result.current[1]({ doc: "" }));
    expect(asked).toHaveLength(n);
    expect(window.location.search).toBe("");
  });

  it("asks the guard with null on leaving the app (logout); without a guard leaving is fine", async () => {
    expect(await confirmLeave()).toBe(true);
    const asked: (ViewState | null)[] = [];
    let allow = false;
    const remove = setNavigationGuard((next) => {
      asked.push(next);
      return allow;
    });
    try {
      expect(await confirmLeave()).toBe(false);
      expect(asked).toEqual([null]);
      allow = true;
      expect(await confirmLeave()).toBe(true);
    } finally {
      remove();
    }
    expect(await confirmLeave()).toBe(true);
    expect(asked).toHaveLength(2);
  });
});

// A guard that asks (#150, part 8): it answers with a Promise, and the
// change waits for the answer. The history is jsdom's: back() fires
// popstate in a later task, as a browser does.
describe("useViewState with a guard that asks", () => {
  afterEach(() => {
    cleanup();
    window.history.replaceState(null, "", "/");
  });

  /** A guard whose questions are answered by the test: resolve(n, ok) answers the n-th. */
  function asking() {
    const asked: (ViewState | null)[] = [];
    const answers: ((ok: boolean) => void)[] = [];
    const remove = setNavigationGuard((next) => {
      asked.push(next);
      return new Promise<boolean>((resolve) => answers.push(resolve));
    });
    return { asked, answer: (n: number, ok: boolean) => act(async () => answers[n]!(ok)), remove };
  }
  const popstate = () => window.dispatchEvent(new PopStateEvent("popstate"));
  /** Waits for jsdom's history traversal to show url. */
  const shown = (url: string) => waitFor(() => expect(window.location.pathname + window.location.search).toBe(url));

  it("(a) keeps the URL while the question is open; false keeps it, true applies the change", async () => {
    window.history.replaceState(null, "", "/?doc=d1");
    const { result } = renderHook(() => useViewState());
    const q = asking();
    try {
      act(() => result.current[1]({ doc: "d2" }));
      expect(q.asked).toHaveLength(1);
      expect(q.asked[0]?.doc).toBe("d2");
      expect(window.location.search).toBe("?doc=d1");
      await q.answer(0, false);
      expect(window.location.search).toBe("?doc=d1");
      expect(result.current[0].doc).toBe("d1");

      act(() => result.current[1]({ doc: "d2" }));
      expect(q.asked).toHaveLength(2);
      await q.answer(1, true);
      expect(window.location.search).toBe("?doc=d2");
      expect(result.current[0].doc).toBe("d2");
    } finally {
      q.remove();
    }
  });

  it("(a) on Back pushes the shown URL back at once, and keeps it on a refusal", async () => {
    window.history.replaceState(null, "", "/?doc=d1");
    const { result } = renderHook(() => useViewState());
    const q = asking();
    try {
      act(() => {
        window.history.replaceState(null, "", "/?inbox=1");
        popstate();
      });
      expect(q.asked).toHaveLength(1);
      expect(q.asked[0]?.inbox).toBe("1");
      expect(window.location.search).toBe("?doc=d1");
      expect(result.current[0].doc).toBe("d1");
      await q.answer(0, false);
      expect(window.location.search).toBe("?doc=d1");
      expect(result.current[0].doc).toBe("d1");
    } finally {
      q.remove();
    }
  });

  it("(f) on Back, a yes steps back to the target once without asking again, and the history has no duplicate entry", async () => {
    window.history.replaceState(null, "", "/?inbox=1");
    const { result } = renderHook(() => useViewState());
    act(() => result.current[1]({ doc: "d1" }));
    const length = window.history.length;
    const q = asking();
    try {
      act(() => window.history.back());
      await waitFor(() => expect(q.asked).toHaveLength(1));
      expect(q.asked[0]?.inbox).toBe("1");
      // The shown URL is back as the current entry; the target is the one before it.
      expect(window.location.search).toBe("?inbox=1&doc=d1");
      expect(window.history.length).toBe(length);
      await q.answer(0, true);
      await shown("/?inbox=1");
      expect(result.current[0]).toEqual({ ...empty, inbox: "1" });
      expect(window.history.length).toBe(length);
      // Asked once in all: the step back skipped the guard.
      expect(q.asked).toHaveLength(1);
      // The skip was one shot: the next Back asks again.
      act(() => result.current[1]({ doc: "d2" }));
      await q.answer(1, true);
      expect(window.location.search).toBe("?inbox=1&doc=d2");
      act(() => window.history.back());
      await waitFor(() => expect(q.asked).toHaveLength(3));
      expect(window.location.search).toBe("?inbox=1&doc=d2");
    } finally {
      q.remove();
    }
  });

  it("(f) skips the guard for the target URL only: a popstate to another URL meanwhile is asked about", async () => {
    window.history.replaceState(null, "", "/?inbox=1");
    const { result } = renderHook(() => useViewState());
    act(() => result.current[1]({ doc: "d1" }));
    const q = asking();
    // The step back is the test's: it fires the popstates itself.
    const back = vi.spyOn(window.history, "back").mockImplementation(() => {});
    try {
      act(() => {
        window.history.replaceState(null, "", "/?inbox=1");
        popstate();
      });
      await waitFor(() => expect(q.asked).toHaveLength(1));
      expect(window.location.search).toBe("?inbox=1&doc=d1");
      await q.answer(0, true);
      expect(back).toHaveBeenCalledTimes(1);
      // Another entry shows up first: not the expected one, so the guard is asked; the shown URL comes back.
      act(() => {
        window.history.replaceState(null, "", "/?fav=1");
        popstate();
      });
      expect(q.asked).toHaveLength(2);
      expect(q.asked[1]?.fav).toBe("1");
      expect(window.location.search).toBe("?inbox=1&doc=d1");
      await q.answer(1, false);
      // The skip was consumed: the target itself is asked about now.
      act(() => {
        window.history.replaceState(null, "", "/?inbox=1");
        popstate();
      });
      expect(q.asked).toHaveLength(3);
      expect(window.location.search).toBe("?inbox=1&doc=d1");
      await q.answer(2, false);
    } finally {
      back.mockRestore();
      q.remove();
    }
  });

  it("(b) with a question open from update, a Back only pushes the shown URL back; the answer still applies, the next update asks", async () => {
    window.history.replaceState(null, "", "/?doc=d1");
    const { result } = renderHook(() => useViewState());
    const q = asking();
    try {
      act(() => result.current[1]({ doc: "d2" }));
      expect(q.asked).toHaveLength(1);
      act(() => {
        window.history.replaceState(null, "", "/?inbox=1");
        popstate();
      });
      expect(q.asked).toHaveLength(1);
      expect(window.location.search).toBe("?doc=d1");
      expect(result.current[0].doc).toBe("d1");
      await q.answer(0, true);
      expect(window.location.search).toBe("?doc=d2");
      act(() => result.current[1]({ doc: "d3" }));
      expect(q.asked).toHaveLength(2);
      expect(q.asked[1]?.doc).toBe("d3");
      await q.answer(1, false);
      expect(window.location.search).toBe("?doc=d2");
    } finally {
      q.remove();
    }
  });

  it("(c) removing the guard while a question is open settles it as stay, and the next update goes through", async () => {
    window.history.replaceState(null, "", "/?doc=d1");
    const { result } = renderHook(() => useViewState());
    const q = asking();
    act(() => result.current[1]({ doc: "d2" }));
    expect(q.asked).toHaveLength(1);
    q.remove();
    await act(async () => {});
    expect(window.location.search).toBe("?doc=d1");
    act(() => result.current[1]({ doc: "d3" }));
    expect(window.location.search).toBe("?doc=d3");
    // The late answer of the settled question changes nothing.
    await q.answer(0, true);
    expect(window.location.search).toBe("?doc=d3");
    expect(q.asked).toHaveLength(1);
  });

  it("(c) a new guard settles the old guard's open question too", async () => {
    window.history.replaceState(null, "", "/?doc=d1");
    const { result } = renderHook(() => useViewState());
    const q = asking();
    act(() => result.current[1]({ doc: "d2" }));
    const asked: (ViewState | null)[] = [];
    const remove = setNavigationGuard((next) => {
      asked.push(next);
      return true;
    });
    try {
      act(() => result.current[1]({ doc: "d3" }));
      expect(asked).toHaveLength(1);
      expect(window.location.search).toBe("?doc=d3");
      await q.answer(0, true);
      expect(window.location.search).toBe("?doc=d3");
    } finally {
      remove();
    }
  });

  it("(d) refuses a second update while a question is open, without asking twice", async () => {
    window.history.replaceState(null, "", "/?doc=d1");
    const { result } = renderHook(() => useViewState());
    const q = asking();
    try {
      act(() => result.current[1]({ doc: "d2" }));
      act(() => result.current[1]({ doc: "d3" }));
      expect(q.asked).toHaveLength(1);
      expect(window.location.search).toBe("?doc=d1");
      await q.answer(0, true);
      expect(window.location.search).toBe("?doc=d2");
    } finally {
      q.remove();
    }
  });

  it("(e) confirmLeave resolves with the guard's answer, and false while a question is open", async () => {
    window.history.replaceState(null, "", "/?doc=d1");
    const { result } = renderHook(() => useViewState());
    const q = asking();
    try {
      const leave = confirmLeave();
      expect(q.asked).toEqual([null]);
      await q.answer(0, true);
      expect(await leave).toBe(true);
      const refused = confirmLeave();
      await q.answer(1, false);
      expect(await refused).toBe(false);

      act(() => result.current[1]({ doc: "d2" }));
      expect(q.asked).toHaveLength(3);
      expect(await confirmLeave()).toBe(false);
      expect(q.asked).toHaveLength(3);
      await q.answer(2, false);
      // And the lock of a logout question refuses an update.
      const open = confirmLeave();
      act(() => result.current[1]({ doc: "d2" }));
      expect(q.asked).toHaveLength(4);
      expect(window.location.search).toBe("?doc=d1");
      await q.answer(3, false);
      expect(await open).toBe(false);
    } finally {
      q.remove();
    }
  });

  it("(g) a boolean guard and no guard keep update synchronous", () => {
    window.history.replaceState(null, "", "/?doc=d1");
    const { result } = renderHook(() => useViewState());
    act(() => result.current[1]({ doc: "d2" }));
    expect(window.location.search).toBe("?doc=d2");
    const remove = setNavigationGuard(() => true);
    try {
      let during = "";
      act(() => {
        result.current[1]({ doc: "d3" });
        during = window.location.search;
      });
      expect(during).toBe("?doc=d3");
    } finally {
      remove();
    }
  });
});
