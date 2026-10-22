import { afterEach, describe, expect, it, vi } from "vitest";
import type { Tag } from "./api";
import { ancestorIds, buildTagTree, chipInk, containsTag, loadCollapsed, prune, saveCollapsed, type TagNode } from "./tagTree";

const tag = (id: string, name: string, parentId: string | null = null): Tag => ({ id, name, parentId, ownerId: 1, color: "" });

const shape = (nodes: TagNode[]): unknown[] => nodes.map((n) => (n.children.length ? [n.item.name, shape(n.children)] : n.item.name));

describe("buildTagTree", () => {
  it("builds the tree from a flat list, sorted by name on every level", () => {
    const tags = [tag("1", "Steuer"), tag("2", "2024", "1"), tag("3", "10", "1"), tag("4", "Arbeit"), tag("5", "Ärzte"), tag("6", "zebra"), tag("7", "Belege", "2")];
    expect(shape(buildTagTree(tags))).toEqual(["Arbeit", "Ärzte", ["Steuer", ["10", ["2024", ["Belege"]]]], "zebra"]);
  });
  it("sorts numbers by value", () => {
    const tree = buildTagTree([tag("1", "P"), tag("2", "10", "1"), tag("3", "2", "1")]);
    expect(tree[0]!.children.map((n) => n.item.name)).toEqual(["2", "10"]);
  });
  it("shows a tag whose parent is missing as a root", () => {
    const tree = buildTagTree([tag("1", "Kind", "gone"), tag("2", "Anderes")]);
    expect(shape(tree)).toEqual(["Anderes", "Kind"]);
  });
  it("finds a tag in a subtree and the tags above it", () => {
    const tags = [tag("1", "A"), tag("2", "B", "1"), tag("3", "C", "2")];
    const [root] = buildTagTree(tags);
    expect(containsTag(root!, "3")).toBe(true);
    expect(containsTag(root!, "1")).toBe(false);
    expect(ancestorIds(tags, "3")).toEqual(["2", "1"]);
    expect(ancestorIds(tags, "1")).toEqual([]);
    expect(ancestorIds(tags, "x")).toEqual([]);
  });
});

describe("collapsed tags", () => {
  afterEach(() => {
    localStorage.clear();
    vi.restoreAllMocks();
  });

  it("are stored per user", () => {
    saveCollapsed("alice", ["1", "2"]);
    expect(loadCollapsed("alice")).toEqual(["1", "2"]);
    expect(loadCollapsed("bob")).toEqual([]);
    saveCollapsed("alice", []);
    expect(localStorage.length).toBe(0);
  });
  it("ignore invalid or blocked storage", () => {
    localStorage.setItem("paperless-go.tags.alice", "{not json");
    expect(loadCollapsed("alice")).toEqual([]);
    localStorage.setItem("paperless-go.tags.alice", JSON.stringify({ v: 2, collapsed: ["1"] }));
    expect(loadCollapsed("alice")).toEqual([]);
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new DOMException("blocked", "SecurityError");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("blocked", "SecurityError");
    });
    expect(loadCollapsed("alice")).toEqual([]);
    expect(() => saveCollapsed("alice", ["1"])).not.toThrow();
  });
  it("drop tags that are gone", () => {
    const ids = ["1", "2"];
    expect(prune(ids, [tag("1", "A"), tag("2", "B")])).toBe(ids);
    expect(prune(ids, [tag("2", "B")])).toEqual(["2"]);
  });
});

describe("chipInk", () => {
  it("picks the text colour that is readable on the tag colour", () => {
    expect(chipInk("#ffffff")).toBe("#000000");
    expect(chipInk("#ffeb3b")).toBe("#000000");
    expect(chipInk("#000000")).toBe("#ffffff");
    expect(chipInk("#1e3a8a")).toBe("#ffffff");
    expect(chipInk("#d32f2f")).toBe("#ffffff");
    expect(chipInk("bad")).toBe("#000000");
  });
});
