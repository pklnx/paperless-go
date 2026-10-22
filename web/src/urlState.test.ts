import { afterEach, describe, expect, it } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { activeFilter, documentQuery, filterPatch, formatState, parseState, useViewState, type ViewState } from "./urlState";

const empty = parseState("");

describe("parseState and formatState", () => {
  it("round-trip every field", () => {
    const s: ViewState = { q: "Rechnung tag:Steuer & mehr", inbox: "1", fav: "1", tag: "t1", corr: "c1", type: "d1", sort: "date", order: "asc", all: "1", doc: "x", view: "log" };
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

describe("useViewState", () => {
  afterEach(() => window.history.replaceState(null, "", "/"));

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
});
