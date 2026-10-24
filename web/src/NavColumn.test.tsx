import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Metadata, User } from "./api";
import { t } from "./i18n";
import { columnFocusTarget, NavColumn } from "./NavColumn";
import { useCollapsedTags } from "./tagTree";
import { named, tag } from "./testServer";
import { parseState, type Filter, type ViewState } from "./urlState";

const alice: User = { id: 1, name: "alice", admin: false, mustChangePassword: false, twoFactorEnabled: false, twoFactorRequired: false };
const root: User = { ...alice, id: 2, name: "root", admin: true };

const data = (): Metadata => ({
  tags: [
    tag("t1", "Steuer", null, { subtreeCount: 5, documentCount: 2 }),
    tag("t2", "2024", "t1", { subtreeCount: 3 }),
    tag("t3", "Ärzte"),
    tag("t4", "Orphan", "gone"),
    tag("t5", "arbeit", null, { color: "#d32f2f" }),
  ],
  correspondents: [named("c2", "Zahnarzt", 4), named("c1", "Amt", 7)],
  documentTypes: [named("d1", "Rechnung", 9)],
});

interface HarnessProps {
  state?: ViewState;
  d?: Metadata | null;
  error?: string;
  user?: User;
  select?: (f: Filter | null) => void;
  everyone?: boolean;
  setEveryone?: (on: boolean) => void;
  onClose?: () => void;
}

function Harness({ state = parseState(""), d = data(), error = "", user = alice, select = () => {}, everyone = false, setEveryone = () => {}, onClose }: HarnessProps) {
  const collapsed = useCollapsedTags(user.name, d?.tags ?? null);
  return <NavColumn state={state} select={select} user={user} metadata={{ data: d, error }} collapsed={collapsed} everyone={everyone} setEveryone={setEveryone} onClose={onClose} />;
}

afterEach(() => {
  cleanup();
  localStorage.clear();
});

// The tree's nodes by their accessible name (the label: name, then count).
const node = (name: string | RegExp) => screen.getByRole("treeitem", { name });
const expanded = (name: string | RegExp) => node(name).getAttribute("aria-expanded");
/** Carbon's caret, which toggles a node without selecting it. */
const caret = (name: string | RegExp) => node(name).querySelector<HTMLElement>(":scope > .cds--tree-node__label .cds--tree-parent-node__toggle")!;
/** Whether a node's children are hidden (Carbon hides the collapsed subtree's list). */
const childrenHidden = (name: string | RegExp) => document.getElementById(node(name).getAttribute("aria-owns")!)!.classList.contains("cds--tree-node--hidden");

describe("NavColumn", () => {
  it("is one Carbon TreeView: the tags as a tree, then the correspondents and the types, each sorted by name, with counts", () => {
    render(<Harness />);
    const tree = screen.getByRole("tree", { name: t.browse });
    expect(tree.className).toContain("cds--tree--xs");
    // Three roots; the orphan is a root tag; the parent shows the subtree's count.
    const roots = Array.from(tree.querySelectorAll<HTMLElement>(":scope > [role=treeitem]")).map((n) => n.getAttribute("aria-labelledby")).map((id) => document.getElementById(id!)!.textContent);
    expect(roots).toEqual([t.tags, t.correspondents, t.documentTypes]);
    const tags = within(document.getElementById(node(t.tags).getAttribute("aria-owns")!)!);
    const names = (ul: HTMLElement) => Array.from(ul.querySelectorAll<HTMLElement>(":scope > [role=treeitem]")).map((n) => document.getElementById(n.getAttribute("aria-labelledby")!)!.textContent);
    expect(names(document.getElementById(node(t.tags).getAttribute("aria-owns")!)!)).toEqual(["arbeit 1", "Ärzte 1", "Orphan 1", "Steuer 5"]);
    expect(tags.getByRole("treeitem", { name: "2024 3" })).toBeTruthy();
    expect(expanded("Steuer 5")).toBe("true");
    // The tag colour is a dot next to the name; a tag without one gets a ring.
    const dot = within(node("arbeit 1")).getByTestId("tag-dot");
    expect(dot.style.backgroundColor).toBe("rgb(211, 47, 47)");
    expect(within(node("Ärzte 1")).getByTestId("tag-dot").className).toContain("nav-tree__dot--none");
    expect(names(document.getElementById(node(t.correspondents).getAttribute("aria-owns")!)!)).toEqual(["Amt 7", "Zahnarzt 4"]);
    expect(names(document.getElementById(node(t.documentTypes).getAttribute("aria-owns")!)!)).toEqual(["Rechnung 9"]);
    // No colour dot on a correspondent, and every node's name is its label only (not its children).
    expect(within(node("Amt 7")).queryByTestId("tag-dot")).toBeNull();
  });

  it("selects a tag, a correspondent or a type with a click, and a group only toggles", () => {
    const select = vi.fn();
    render(<Harness select={select} />);
    fireEvent.click(node("2024 3"));
    expect(select).toHaveBeenLastCalledWith({ kind: "tag", id: "t2" });
    fireEvent.click(node("Amt 7"));
    expect(select).toHaveBeenLastCalledWith({ kind: "corr", id: "c1" });
    fireEvent.click(node("Rechnung 9"));
    expect(select).toHaveBeenLastCalledWith({ kind: "type", id: "d1" });
    expect(select).toHaveBeenCalledTimes(3);
    fireEvent.click(node(t.correspondents));
    expect(expanded(t.correspondents)).toBe("false");
    expect(childrenHidden(t.correspondents)).toBe(true);
    expect(select).toHaveBeenCalledTimes(3);
    fireEvent.click(node(t.correspondents));
    expect(expanded(t.correspondents)).toBe("true");
    // Nothing about the groups is stored.
    expect(localStorage.getItem("paperless-go.tags.alice")).toBeNull();
  });

  it("collapses a tag's sub-tags with the caret and remembers it per user", () => {
    render(<Harness />);
    expect(childrenHidden("Steuer 5")).toBe(false);
    fireEvent.click(caret("Steuer 5"));
    expect(expanded("Steuer 5")).toBe("false");
    expect(childrenHidden("Steuer 5")).toBe(true);
    expect(JSON.parse(localStorage.getItem("paperless-go.tags.alice")!)).toEqual({ v: 1, collapsed: ["t1"] });
    cleanup();
    render(<Harness />);
    expect(expanded("Steuer 5")).toBe("false");
    cleanup();
    // Another user starts with everything expanded.
    render(<Harness user={{ ...alice, name: "bob" }} />);
    expect(expanded("Steuer 5")).toBe("true");
  });

  it("marks the selected node from the URL, and a collapsed tag or group that holds it", () => {
    const { rerender } = render(<Harness state={parseState("?tag=t2")} />);
    expect(node("2024 3").getAttribute("aria-selected")).toBe("true");
    expect(screen.getAllByRole("treeitem", { selected: true })).toHaveLength(1);
    fireEvent.click(caret("Steuer 5"));
    expect(node("Steuer 5").className).toContain("nav-tree__node--within");
    fireEvent.click(caret(t.tags));
    expect(node(t.tags).className).toContain("nav-tree__node--within");
    rerender(<Harness state={parseState("?corr=c1")} />);
    expect(node("Amt 7").getAttribute("aria-selected")).toBe("true");
    expect(node("Steuer 5").className).not.toContain("nav-tree__node--within");
    // On a page, and for the lists of the SideNav, nothing in the tree is selected.
    rerender(<Harness state={parseState("?corr=c1&view=log")} />);
    expect(screen.queryAllByRole("treeitem", { selected: true })).toHaveLength(0);
    rerender(<Harness state={parseState("?inbox=1&tag=t2")} />);
    expect(screen.queryAllByRole("treeitem", { selected: true })).toHaveLength(0);
  });

  it("reveals the selected tag's parents once, not on every load of the data", () => {
    localStorage.setItem("paperless-go.tags.alice", JSON.stringify({ v: 1, collapsed: ["t1"] }));
    const { rerender } = render(<Harness state={parseState("?tag=t2")} />);
    expect(expanded("Steuer 5")).toBe("true");
    expect(node("2024 3").getAttribute("aria-selected")).toBe("true");
    // The user collapses the parent again; new data arrives.
    fireEvent.click(caret("Steuer 5"));
    act(() => rerender(<Harness state={parseState("?tag=t2")} d={data()} />));
    expect(expanded("Steuer 5")).toBe("false");
    expect(JSON.parse(localStorage.getItem("paperless-go.tags.alice")!)).toEqual({ v: 1, collapsed: ["t1"] });
  });

  it("works with the keyboard: Left collapses, Right expands, Enter selects", () => {
    const select = vi.fn();
    render(<Harness select={select} />);
    const steuer = node("Steuer 5");
    steuer.focus();
    fireEvent.keyDown(steuer, { key: "ArrowLeft" });
    expect(expanded("Steuer 5")).toBe("false");
    fireEvent.keyDown(steuer, { key: "ArrowRight" });
    expect(expanded("Steuer 5")).toBe("true");
    fireEvent.keyDown(steuer, { key: "ArrowRight" });
    expect(document.activeElement).toBe(node("2024 3"));
    fireEvent.keyDown(document.activeElement!, { key: "Enter" });
    expect(select).toHaveBeenCalledWith({ kind: "tag", id: "t2" });
    // Enter on a group toggles it once (Carbon's Enter), and selects nothing.
    const corr = node(t.correspondents);
    corr.focus();
    fireEvent.keyDown(corr, { key: "Enter" });
    expect(expanded(t.correspondents)).toBe("false");
    expect(select).toHaveBeenCalledTimes(1);
  });

  it("shows the admin's Everyone's documents as a Toggle, and nothing for other users", () => {
    const setEveryone = vi.fn();
    render(<Harness user={root} setEveryone={setEveryone} />);
    const sw = screen.getByRole("switch", { name: t.everyonesDocuments });
    expect(sw.getAttribute("aria-checked")).toBe("false");
    fireEvent.click(sw);
    expect(setEveryone).toHaveBeenCalledWith(true);
    cleanup();
    render(<Harness user={root} everyone />);
    expect(screen.getByRole("switch", { name: t.everyonesDocuments }).getAttribute("aria-checked")).toBe("true");
    cleanup();
    render(<Harness />);
    expect(screen.queryByRole("switch")).toBeNull();
  });

  it("says that it loads, why loading failed, and shows no tree without data", () => {
    render(<Harness d={null} />);
    expect(screen.getByText(t.loading)).toBeTruthy();
    expect(screen.queryByRole("tree")).toBeNull();
    cleanup();
    render(<Harness d={null} error="database unavailable" />);
    expect(screen.getByRole("alert").textContent).toBe(t.loadMetadataFailed("database unavailable"));
    cleanup();
    render(<Harness d={{ tags: [], correspondents: [], documentTypes: [] }} />);
    expect(screen.queryByRole("tree")).toBeNull();
    expect(screen.queryByText(t.loading)).toBeNull();
  });

  it("has a close button only as an overlay, and focuses the selected node, else the first, else the close button", () => {
    render(<Harness />);
    expect(screen.queryByRole("button", { name: t.closeBrowse })).toBeNull();
    const nav = screen.getByRole("navigation", { name: t.browse });
    expect(columnFocusTarget(nav)).toBe(node(t.tags));
    cleanup();
    const onClose = vi.fn();
    render(<Harness state={parseState("?corr=c1")} onClose={onClose} />);
    expect(columnFocusTarget(screen.getByRole("navigation", { name: t.browse }))).toBe(node("Amt 7"));
    fireEvent.click(screen.getByRole("button", { name: t.closeBrowse }));
    expect(onClose).toHaveBeenCalledTimes(1);
    cleanup();
    render(<Harness state={parseState("?corr=c1")} d={{ tags: [], correspondents: [], documentTypes: [] }} onClose={onClose} />);
    expect(columnFocusTarget(screen.getByRole("navigation", { name: t.browse }))).toBe(screen.getByRole("button", { name: t.closeBrowse }));
    // A selected node inside a collapsed parent is hidden: the first visible node then.
    cleanup();
    localStorage.setItem("paperless-go.tags.alice", JSON.stringify({ v: 1, collapsed: ["t1"] }));
    render(<Harness state={parseState("?tag=t2")} />);
    fireEvent.click(caret("Steuer 5"));
    expect(columnFocusTarget(screen.getByRole("navigation", { name: t.browse }))).toBe(node(t.tags));
  });
});

describe("NavColumn in German", () => {
  afterEach(() => {
    vi.resetModules();
    vi.unstubAllGlobals();
  });

  it("names the column, the groups, the switch and the close button in German, nothing in English", async () => {
    vi.resetModules();
    vi.stubGlobal("navigator", { ...navigator, languages: ["de-DE", "en-US"], language: "de-DE" });
    const { NavColumn: German } = await import("./NavColumn");
    const { useCollapsedTags: useGermanCollapsed } = await import("./tagTree");
    const { messagesFor } = await import("./i18n");
    const de = messagesFor("de");
    function GermanHarness() {
      const d = data();
      const collapsed = useGermanCollapsed("root", d.tags);
      return <German state={parseState("")} select={() => {}} user={root} metadata={{ data: d, error: "" }} collapsed={collapsed} everyone={false} setEveryone={() => {}} onClose={() => {}} />;
    }
    render(<GermanHarness />);
    expect(screen.getByRole("navigation", { name: de.browse })).toBeTruthy();
    expect(screen.getByRole("tree", { name: de.browse })).toBeTruthy();
    expect(screen.getByRole("treeitem", { name: de.correspondents })).toBeTruthy();
    expect(screen.getByRole("treeitem", { name: de.documentTypes })).toBeTruthy();
    const sw = screen.getByRole("switch", { name: de.everyonesDocuments });
    expect(sw.parentElement?.textContent).toContain(de.carbon.toggleOff);
    expect(screen.getByRole("button", { name: de.closeBrowse })).toBeTruthy();
    const html = document.body.innerHTML;
    for (const s of ["Browse", "Correspondents", "Document types", "Everyone", ">Off<", ">On<"]) expect(html, s).not.toContain(s);
  });
});
