import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { ApiError } from "./api";
import { App } from "./App";
import { t } from "./i18n";
import { metadataErrorText } from "./MetadataPage";
import { named, stubServer, tag, type Request, type Server } from "./testServer";

// The page for tags, correspondents and types against a fake server.

let server: Server;
let requests: Request[];

beforeEach(() => {
  ({ server, requests } = stubServer());
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const changes = () => requests.filter((r) => r.method === "PATCH" || r.method === "DELETE").map((r) => ({ method: r.method, path: r.path, body: r.body }));
const tagLoads = () => requests.filter((r) => r.method === "GET" && r.path === "/api/tags").length;
const tagNames = () => within(within(screen.getByRole("tabpanel")).getByRole("list", { name: t.tags })).getAllByRole("listitem").map((li) => li.querySelector("button")!.textContent);
const page = () => within(screen.getByRole("tabpanel"));

async function open(search = "?view=metadata") {
  window.history.replaceState(null, "", `/${search}`);
  render(<App />);
  await screen.findByRole("tablist", { name: t.manageMetadata });
  await waitFor(() => expect(screen.queryByText(t.loading)).toBeNull());
}

describe("metadata page", () => {
  it("opens from the sidebar and lists the tag tree with counts, names as text", async () => {
    server.tags = [tag("t1", "Steuer", null, { documentCount: 4 }), tag("t2", "2024", "t1", { documentCount: 1 }), tag("t3", "<b>Arzt</b>", null, { documentCount: 0 })];
    render(<App />);
    fireEvent.click(within(await screen.findByTestId("sidebar")).getByRole("button", { name: t.manageMetadata }));
    expect(window.location.search).toBe("?view=metadata");
    // The view switch renders asynchronously: wait for the panel, not just the text in it.
    await within(await screen.findByRole("tabpanel")).findByText("Steuer");
    expect(tagNames()).toEqual(["<b>Arzt</b>", "Steuer", "2024"]);
    expect(document.querySelector("b")).toBeNull();
    const steuer = screen.getByTestId("tag-row-t1");
    expect(within(steuer).getByText(t.documentCount(4))).toBeTruthy();
    // The sub-tag is indented below its parent.
    expect(parseFloat(screen.getByTestId("tag-row-t2").style.paddingLeft)).toBeGreaterThan(parseFloat(steuer.style.paddingLeft));
  });

  it("renames with a PATCH of the name only and loads the names again", async () => {
    server.correspondents = [named("c1", "Finanzamt", 3)];
    await open();
    fireEvent.click(screen.getByRole("tab", { name: t.correspondents }));
    fireEvent.click(page().getByRole("button", { name: t.editEntry("Finanzamt") }));
    const sheet = within(screen.getByRole("dialog", { name: t.editEntry("Finanzamt") }));
    // No colour or parent for a correspondent.
    expect(sheet.queryByText(t.tagColor)).toBeNull();
    fireEvent.change(sheet.getByRole("textbox", { name: t.entryName }), { target: { value: "Finanzamt Köln" } });
    const before = requests.filter((r) => r.path === "/api/correspondents" && r.method === "GET").length;
    fireEvent.click(sheet.getByRole("button", { name: t.save }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(changes()).toEqual([{ method: "PATCH", path: "/api/correspondents/c1", body: { name: "Finanzamt Köln" } }]);
    await waitFor(() => expect(requests.filter((r) => r.path === "/api/correspondents" && r.method === "GET").length).toBe(before + 1));
  });

  it("sends only the changed tag fields: colour from the palette, then a typed one, then the parent", async () => {
    server.tags = [tag("t1", "Steuer", null, { color: "#1E88E5" }), tag("t2", "Arzt")];
    await open();
    const edit = () => {
      fireEvent.click(page().getByRole("button", { name: t.editEntry("Arzt") }));
      return within(screen.getByRole("dialog", { name: t.editEntry("Arzt") }));
    };
    let s = edit();
    fireEvent.click(s.getByRole("button", { name: t.colorSwatch("#43A047") }));
    fireEvent.click(s.getByRole("button", { name: t.save }));
    await waitFor(() => expect(changes()).toEqual([{ method: "PATCH", path: "/api/tags/t2", body: { color: "#43A047" } }]));

    s = edit();
    fireEvent.change(s.getByRole("textbox", { name: t.tagColorHex }), { target: { value: "#12345" } });
    fireEvent.click(s.getByRole("button", { name: t.save }));
    expect(s.getByRole("alert").textContent).toBe(t.invalidColor);
    expect(changes()).toHaveLength(1);
    fireEvent.change(s.getByRole("textbox", { name: t.tagColorHex }), { target: { value: "#abcdef" } });
    fireEvent.click(s.getByRole("button", { name: t.save }));
    await waitFor(() => expect(changes().at(-1)).toEqual({ method: "PATCH", path: "/api/tags/t2", body: { color: "#abcdef" } }));

    s = edit();
    fireEvent.change(s.getByRole("combobox", { name: t.parentTag }), { target: { value: "t1" } });
    fireEvent.click(s.getByRole("button", { name: t.save }));
    await waitFor(() => expect(changes().at(-1)).toEqual({ method: "PATCH", path: "/api/tags/t2", body: { parentId: "t1" } }));

    // Nothing changed: nothing sent.
    s = edit();
    fireEvent.click(s.getByRole("button", { name: t.save }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(changes()).toHaveLength(3);
  });

  it("offers as parents only the same owner's tags, never the tag itself or its sub-tags", async () => {
    server.user = { id: 1, name: "root", admin: true };
    server.tags = [tag("t1", "Steuer"), tag("t2", "2024", "t1"), tag("t3", "Q1", "t2"), tag("t4", "Arzt"), tag("t5", "Bobs", null, { ownerId: 2 })];
    await open("?view=metadata&all=1");
    fireEvent.click(page().getByRole("button", { name: t.editEntry("2024") }));
    const select = within(screen.getByRole("dialog")).getByRole("combobox", { name: t.parentTag }) as HTMLSelectElement;
    expect(select.value).toBe("t1");
    expect(Array.from(select.options).map((o) => o.textContent!.trim())).toEqual([t.topLevel, "Arzt", "Steuer"]);
    fireEvent.change(select, { target: { value: "" } });
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: t.save }));
    await waitFor(() => expect(changes()).toEqual([{ method: "PATCH", path: "/api/tags/t2", body: { parentId: null } }]));
  });

  it("shows a cycle and leaves the tree as it was", async () => {
    server.tags = [tag("t1", "Steuer"), tag("t2", "Arzt")];
    server.metadata = () => ({ status: 409, body: { error: "a tag cannot be placed under itself", code: "tag_cycle" } });
    await open();
    const loads = tagLoads();
    fireEvent.click(page().getByRole("button", { name: t.editEntry("Steuer") }));
    const s = within(screen.getByRole("dialog"));
    fireEvent.change(s.getByRole("combobox", { name: t.parentTag }), { target: { value: "t2" } });
    fireEvent.click(s.getByRole("button", { name: t.save }));
    expect((await s.findByRole("alert")).textContent).toBe(t.tagCycle);
    // The sheet stays open, nothing is loaded again, the tree is unchanged.
    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(tagLoads()).toBe(loads);
    expect(tagNames()).toEqual(["Arzt", "Steuer"]);
  });

  it("moves a tag by dragging it onto another, and to the top level", async () => {
    server.tags = [tag("t1", "Steuer"), tag("t2", "2024", "t1"), tag("t3", "Arzt"), tag("t4", "Bobs", null, { ownerId: 2 })];
    await open();
    const row = (id: string) => screen.getByTestId(`tag-row-${id}`);
    const dt = { setData: () => {}, effectAllowed: "", types: ["text/plain"], files: [] };
    fireEvent.dragStart(row("t3"), { dataTransfer: dt });
    // Not onto another owner's tag.
    fireEvent.dragOver(row("t4"), { dataTransfer: dt });
    fireEvent.drop(row("t4"), { dataTransfer: dt });
    expect(changes()).toEqual([]);
    fireEvent.dragOver(row("t1"), { dataTransfer: dt });
    fireEvent.drop(row("t1"), { dataTransfer: dt });
    await waitFor(() => expect(changes()).toEqual([{ method: "PATCH", path: "/api/tags/t3", body: { parentId: "t1" } }]));

    // A tag with a parent can go to the top level; a parent never into its own sub-tag.
    fireEvent.dragStart(row("t1"), { dataTransfer: dt });
    expect(screen.queryByTestId("drop-top")).toBeNull();
    fireEvent.drop(row("t2"), { dataTransfer: dt });
    fireEvent.dragEnd(row("t1"));
    fireEvent.dragStart(row("t2"), { dataTransfer: dt });
    fireEvent.drop(screen.getByTestId("drop-top"), { dataTransfer: dt });
    await waitFor(() => expect(changes()).toHaveLength(2));
    expect(changes()[1]).toEqual({ method: "PATCH", path: "/api/tags/t2", body: { parentId: null } });
  });

  it("confirms a delete with the documents affected, and deletes", async () => {
    server.tags = [tag("t1", "Steuer", null, { documentCount: 4 }), tag("t2", "2024", "t1"), tag("t3", "Arzt", null, { documentCount: 1 })];
    server.types = [named("d1", "Rechnung", 7)];
    await open();
    fireEvent.click(page().getByRole("button", { name: t.deleteEntry("Steuer") }));
    let s = within(screen.getByRole("dialog", { name: t.deleteTitle("Steuer") }));
    expect(s.getByText(t.deleteTagText(4))).toBeTruthy();
    expect(s.getByText(t.deleteTagChildren)).toBeTruthy();
    fireEvent.click(s.getByRole("button", { name: t.cancel }));
    expect(changes()).toEqual([]);

    // A tag without sub-tags does not mention them.
    fireEvent.click(page().getByRole("button", { name: t.deleteEntry("Arzt") }));
    s = within(screen.getByRole("dialog", { name: t.deleteTitle("Arzt") }));
    expect(s.getByText(t.deleteTagText(1))).toBeTruthy();
    expect(s.queryByText(t.deleteTagChildren)).toBeNull();
    const loads = tagLoads();
    fireEvent.click(s.getByRole("button", { name: t.deleteButton }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(changes()).toEqual([{ method: "DELETE", path: "/api/tags/t3", body: undefined }]);
    await waitFor(() => expect(tagLoads()).toBe(loads + 1));

    fireEvent.click(screen.getByRole("tab", { name: t.documentTypes }));
    fireEvent.click(page().getByRole("button", { name: t.deleteEntry("Rechnung") }));
    s = within(screen.getByRole("dialog", { name: t.deleteTitle("Rechnung") }));
    expect(s.getByText(t.deleteTypeText(7))).toBeTruthy();
    fireEvent.click(s.getByRole("button", { name: t.deleteButton }));
    await waitFor(() => expect(changes().at(-1)).toEqual({ method: "DELETE", path: "/api/document-types/d1", body: undefined }));
  });

  it("names the clashing sub-tag of a refused delete, and still says why without its name", async () => {
    server.tags = [tag("t1", "Steuer"), tag("t2", "Arzt", "t1"), tag("t3", "Arzt")];
    server.metadata = () => ({ status: 409, body: { error: "a sub-tag would get the name", code: "tag_name_conflict", childId: "t2", childName: "Arzt" } });
    await open();
    fireEvent.click(page().getByRole("button", { name: t.deleteEntry("Steuer") }));
    let s = within(screen.getByRole("dialog"));
    fireEvent.click(s.getByRole("button", { name: t.deleteButton }));
    expect((await s.findByRole("alert")).textContent).toBe(t.tagNameConflict("Arzt"));

    server.metadata = () => ({ status: 409, body: { error: "a sub-tag would get the name", code: "tag_name_conflict" } });
    fireEvent.click(s.getByRole("button", { name: t.cancel }));
    fireEvent.click(page().getByRole("button", { name: t.deleteEntry("Steuer") }));
    s = within(screen.getByRole("dialog"));
    fireEvent.click(s.getByRole("button", { name: t.deleteButton }));
    expect((await s.findByRole("alert")).textContent).toBe(t.tagNameConflictUnnamed);
  });

  it("opens the list filtered by a row", async () => {
    server.tags = [tag("t1", "Steuer")];
    server.correspondents = [named("c1", "Amt")];
    await open();
    fireEvent.click(page().getByRole("button", { name: "Steuer" }));
    await waitFor(() => expect(window.location.search).toBe("?tag=t1"));
    expect(await screen.findByRole("heading", { level: 2, name: "Steuer" })).toBeTruthy();
    window.history.replaceState(null, "", "/?view=metadata");
    cleanup();
    await open();
    fireEvent.click(screen.getByRole("tab", { name: t.correspondents }));
    fireEvent.click(page().getByRole("button", { name: "Amt" }));
    await waitFor(() => expect(window.location.search).toBe("?corr=c1"));
  });

  it("uses the admin's own names unless Everyone's documents is on", async () => {
    server.user = { id: 1, name: "root", admin: true };
    await open();
    expect(requests.filter((r) => r.path === "/api/tags").at(-1)!.url).toBe("/api/tags?owner=me");
    cleanup();
    await open("?view=metadata&all=1");
    expect(requests.filter((r) => r.path === "/api/tags").at(-1)!.url).toBe("/api/tags");
  });

  it("names the owner of another user's rows with Everyone's documents on, not the admin's own", async () => {
    server.user = { id: 1, name: "root", admin: true };
    // Owners compare by ID: the admin's own tag under a stale name has no
    // note, another user who is also called "root" has one.
    server.tags = [
      tag("t1", "Steuer", null, { ownerId: 1, ownerName: "root-old" }),
      tag("t2", "Steuer", null, { ownerId: 2, ownerName: "bob" }),
      tag("t3", "Steuer", null, { ownerId: 3, ownerName: "root" }),
    ];
    server.correspondents = [{ ...named("c1", "Amt", 1, 1), ownerName: "root" }, { ...named("c2", "Amt", 1, 2), ownerName: "bob" }];
    await open("?view=metadata&all=1");
    expect(within(screen.getByTestId("tag-row-t2")).getByText(t.ownedBy("bob"))).toBeTruthy();
    expect(within(screen.getByTestId("tag-row-t1")).queryByText(t.ownedBy("root-old"))).toBeNull();
    expect(within(screen.getByTestId("tag-row-t3")).getByText(t.ownedBy("root"))).toBeTruthy();
    fireEvent.click(screen.getByRole("tab", { name: t.correspondents }));
    const rows = within(page().getByRole("list", { name: t.correspondents })).getAllByRole("listitem");
    expect(rows.map((li) => li.textContent?.includes(t.ownedBy("bob")))).toEqual(expect.arrayContaining([true, false]));
    expect(page().queryByText(t.ownedBy("root"))).toBeNull();
  });

  it("names each refusal", () => {
    const err = (status: number, body: Record<string, unknown>) => new ApiError(status, String(body.error ?? ""), body);
    expect(metadataErrorText(err(409, { code: "name_taken" }))).toBe(t.nameTaken);
    expect(metadataErrorText(err(409, { code: "tag_cycle" }))).toBe(t.tagCycle);
    expect(metadataErrorText(err(400, { code: "invalid_name" }))).toBe(t.invalidName);
    expect(metadataErrorText(err(400, { code: "invalid_color" }))).toBe(t.invalidColor);
    expect(metadataErrorText(err(400, { code: "owner_mismatch" }))).toBe(t.parentOtherOwner);
    expect(metadataErrorText(err(404, { error: "not found" }))).toBe(t.entryGone);
    expect(metadataErrorText(err(503, { error: "audit log unavailable" }))).toBe(t.serverBusy);
    expect(metadataErrorText(err(500, { error: "boom" }))).toBe(t.failed("boom"));
  });
});
