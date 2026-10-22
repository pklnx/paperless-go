import { describe, expect, it } from "vitest";
import { clickSelect, maxSelection, pruneSelection, selectAll } from "./selection";

const order = ["a", "b", "c", "d", "e"];
const ids = (s: Set<string>) => [...s].sort();

describe("clickSelect", () => {
  it("toggles one without shift or without an anchor", () => {
    expect(ids(clickSelect(order, new Set(), null, "b", false).ids)).toEqual(["b"]);
    expect(ids(clickSelect(order, new Set(["b"]), null, "b", false).ids)).toEqual([]);
    expect(ids(clickSelect(order, new Set(), null, "c", true).ids)).toEqual(["c"]);
  });
  it("gives the range from the anchor the state of the clicked one, in both directions", () => {
    expect(ids(clickSelect(order, new Set(["b"]), "b", "d", true).ids)).toEqual(["b", "c", "d"]);
    expect(ids(clickSelect(order, new Set(["d"]), "d", "a", true).ids)).toEqual(["a", "b", "c", "d"]);
    expect(ids(clickSelect(order, new Set(["a", "b", "c", "d"]), "a", "c", true).ids)).toEqual(["d"]);
  });
  it("treats an anchor that is no longer loaded as none", () => {
    expect(ids(clickSelect(order, new Set(), "gone", "c", true).ids)).toEqual(["c"]);
  });
  it("stops at the limit and says so, keeping the clicked one", () => {
    const many = Array.from({ length: maxSelection + 10 }, (_, i) => `x${i}`);
    const full = new Set(many.slice(0, maxSelection - 1));
    const r = clickSelect(many, full, "x0", `x${maxSelection + 5}`, true);
    expect(r.capped).toBe(true);
    expect(r.ids.size).toBe(maxSelection);
    expect(r.ids.has(`x${maxSelection + 5}`)).toBe(true);
    const past = clickSelect(many, r.ids, null, `x${maxSelection + 9}`, false);
    expect(past.capped).toBe(true);
    expect(past.ids.has(`x${maxSelection + 9}`)).toBe(false);
  });
});

describe("selectAll", () => {
  it("takes the loaded ones in order, at most the limit", () => {
    expect(selectAll(order)).toEqual({ ids: new Set(order), capped: false });
    const many = Array.from({ length: maxSelection + 1 }, (_, i) => `x${i}`);
    const r = selectAll(many);
    expect(r.capped).toBe(true);
    expect(r.ids.size).toBe(maxSelection);
    expect(r.ids.has(`x${maxSelection}`)).toBe(false);
  });
});

describe("pruneSelection", () => {
  it("drops IDs that are no longer loaded, and keeps the same set otherwise", () => {
    const s = new Set(["a", "z"]);
    expect(ids(pruneSelection(s, order))).toEqual(["a"]);
    const kept = new Set(["a", "b"]);
    expect(pruneSelection(kept, order)).toBe(kept);
  });
});
