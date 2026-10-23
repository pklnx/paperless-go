import { describe, expect, it } from "vitest";
import { applyView, canSave, moveView, paramsOf, showsView, shownViews, viewKey, viewParams, withParams, type ViewParams } from "./savedViews";
import { savedView } from "./testServer";
import { parseState } from "./urlState";

const empty = parseState("");
const params = (more: Partial<ViewParams> = {}): ViewParams => ({
  v: 1,
  q: "",
  tag: [],
  correspondent: [],
  type: [],
  inbox: null,
  favorite: null,
  sort: "",
  order: "",
  everyone: false,
  ...more,
});

describe("viewParams", () => {
  it("reads the web's part of a filter, with defaults for what is missing", () => {
    expect(viewParams({ [viewKey]: { v: 1 } })).toEqual(params());
    expect(viewParams({ tags: [{ id: "x" }], [viewKey]: { v: 1, q: "Rechnung", tag: ["t1"], inbox: true, favorite: false, sort: "date", order: "asc", everyone: true } })).toEqual(
      params({ q: "Rechnung", tag: ["t1"], inbox: true, sort: "date", order: "asc", everyone: true }),
    );
  });
  it("hides views without the key, of another version or of another shape", () => {
    for (const f of [
      {},
      { inbox: true, sort: "documentDate" },
      { [viewKey]: null },
      { [viewKey]: [] },
      { [viewKey]: { v: 2 } },
      { [viewKey]: { v: 1, tag: "t1" } },
      { [viewKey]: { v: 1, tag: [1] } },
      { [viewKey]: { v: 1, inbox: "yes" } },
      { [viewKey]: { v: 1, q: 3 } },
      { [viewKey]: { v: 1, sort: "deleted" } },
      { [viewKey]: { v: 1, order: "up" } },
    ]) {
      expect(viewParams(f), JSON.stringify(f)).toBeNull();
    }
    const views = [savedView("a", "App's", { filter: { inbox: true } }), savedView("b", "Web's", { filter: { [viewKey]: { v: 1 } } })];
    expect(shownViews(views).map((s) => s.view.id)).toEqual(["b"]);
  });
});

describe("saving and opening", () => {
  it("saves what the list shows, and keeps the app's keys on an update", () => {
    const s = parseState("?q=%20Rechnung%20&tag=t1&fav=1&sort=date&order=asc&all=1");
    const p = paramsOf(s);
    expect(p).toEqual(params({ q: "Rechnung", tag: ["t1"], favorite: true, sort: "date", order: "asc", everyone: true }));
    expect(withParams({ tags: [{ id: "x" }], [viewKey]: { v: 1, q: "old" } }, p)).toEqual({ tags: [{ id: "x" }], [viewKey]: p });
  });
  it("offers saving for a search, a filter or a sort, not for the plain list, the trash or a page", () => {
    expect(canSave(empty)).toBe(false);
    expect(canSave({ ...empty, q: "  " })).toBe(false);
    for (const more of [{ q: "x" }, { inbox: "1" }, { fav: "1" }, { tag: "t" }, { corr: "c" }, { type: "d" }, { sort: "date" as const }, { order: "asc" }]) {
      expect(canSave({ ...empty, ...more }), JSON.stringify(more)).toBe(true);
    }
    expect(canSave({ ...empty, q: "x", trash: "1" })).toBe(false);
    expect(canSave({ ...empty, q: "x", view: "log" })).toBe(false);
  });
  it("opens a view in place of the list's parameters and marks it", () => {
    const s = parseState("?trash=1&corr=c9&doc=d1&view=log&all=1");
    const p = params({ q: "x", tag: ["t1", "t2"], inbox: true, sort: "added", order: "asc" });
    const opened = { ...s, ...applyView("v1", p, false) };
    expect(opened).toEqual({ ...empty, q: "x", tag: "t1", inbox: "1", sort: "added", order: "asc", all: "1", sv: "v1" });
    // An admin's switch follows the view.
    expect({ ...s, ...applyView("v1", p, true) }.all).toBe("");
    expect({ ...empty, ...applyView("v1", params({ everyone: true }), true) }.all).toBe("1");
  });
  it("marks a view only while the list shows its parameters", () => {
    const p = params({ q: "x", tag: ["t1"] });
    const opened = { ...empty, ...applyView("v1", p, false) };
    expect(showsView(opened, "v1", p, false)).toBe(true);
    expect(showsView(opened, "v2", p, false)).toBe(false);
    expect(showsView({ ...opened, q: "y" }, "v1", p, false)).toBe(false);
    expect(showsView({ ...opened, tag: "" }, "v1", p, false)).toBe(false);
    expect(showsView({ ...opened, view: "log" }, "v1", p, false)).toBe(false);
    // The open document does not count; the admin's switch counts for an admin only.
    expect(showsView({ ...opened, doc: "d1" }, "v1", p, false)).toBe(true);
    expect(showsView({ ...opened, all: "1" }, "v1", p, false)).toBe(true);
    expect(showsView({ ...opened, all: "1" }, "v1", p, true)).toBe(false);
  });
});

describe("moveView", () => {
  const views = [savedView("a", "A", { sortIndex: 1 }), savedView("b", "B", { sortIndex: 4 }), savedView("c", "C", { sortIndex: 9 })];
  it("swaps the sort indexes of two neighbours", () => {
    expect(moveView(views, 1, -1)).toEqual([
      { id: "b", sortIndex: 1 },
      { id: "a", sortIndex: 4 },
    ]);
    expect(moveView(views, 1, 1)).toEqual([
      { id: "b", sortIndex: 9 },
      { id: "c", sortIndex: 4 },
    ]);
  });
  it("does nothing at either end", () => {
    expect(moveView(views, 0, -1)).toEqual([]);
    expect(moveView(views, 2, 1)).toEqual([]);
  });
  it("numbers the views anew when the two have the same index", () => {
    const tied = [savedView("a", "A", { sortIndex: 0 }), savedView("b", "B", { sortIndex: 0 }), savedView("c", "C", { sortIndex: 0 })];
    expect(moveView(tied, 2, -1)).toEqual([
      { id: "c", sortIndex: 1 },
      { id: "b", sortIndex: 2 },
    ]);
    expect(moveView(tied, 0, 1)).toEqual([
      { id: "a", sortIndex: 1 },
      { id: "c", sortIndex: 2 },
    ]);
  });
});
