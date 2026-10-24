import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { App } from "./App";
import { t } from "./i18n";
import type { SavedView } from "./api";
import { viewKey } from "./savedViews";
import { findAlert } from "./test/alerts";
import { held, named, page, savedView, stubServer, tag, type Request, type Server } from "./testServer";

// Saved views in the web UI (#12, part 4) against a fake server: what the
// navigation column shows, what opening a view requests, and what saving,
// renaming, updating, moving and deleting send. Since #150 part 2 the list
// is a Carbon ContainedList with an OverflowMenu per view; every request is
// pinned as method, path and body, as it was before.

let server: Server;
let requests: Request[];

beforeEach(() => {
  ({ server, requests } = stubServer());
  server.documents = () => page([]);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

/** The navigation column (saved views, tags, correspondents, types). */
const column = () => within(screen.getByRole("navigation", { name: t.browse }));
/** The button of a view: aria-current marks the one the list shows. */
const rowOf = (name: string) => column().getByRole("button", { name });
/** A request as method, path and body only. */
const pin = (r: Request | undefined) => (r ? { method: r.method, path: r.path, body: r.body } : undefined);
const lists = () => requests.filter((r) => r.method === "GET" && r.path === "/api/documents").map((r) => new URLSearchParams(r.url.split("?")[1]));
/** The list parameters that matter here: everything but limit and tz. */
const params = (p: URLSearchParams) => {
  const out = new URLSearchParams(p);
  out.delete("limit");
  out.delete("tz");
  return out.toString();
};
const sent = (method: string) => requests.filter((r) => r.method === method && r.path.startsWith("/api/saved-views"));
const viewLoads = () => requests.filter((r) => r.method === "GET" && r.path === "/api/saved-views").length;

async function start(url = "/") {
  window.history.replaceState(null, "", url);
  render(<App />);
  await waitFor(() => expect(lists().length).toBeGreaterThan(0));
  await waitFor(() => expect(screen.queryByText(t.loading)).toBeNull());
}

const web = (p: Record<string, unknown>) => ({ [viewKey]: { v: 1, ...p } });

/** Opens a view's menu in the column; returns the menu. */
async function menuOf(name: string) {
  fireEvent.click(await column().findByRole("button", { name: t.viewActions(name) }));
  return within(await screen.findByRole("menu", { name: t.viewActions(name) }));
}

describe("the Saved views section", () => {
  it("lists the views the web can open in their order, and hides the app's", async () => {
    server.savedViews = [
      savedView("v1", "Steuern", { sortIndex: 0, filter: web({ q: "Steuer" }) }),
      savedView("v2", "From the app", { sortIndex: 1, filter: { inbox: true, sort: "documentDate" } }),
      savedView("v3", "Bank", { sortIndex: 2, filter: { tags: [], ...web({ correspondent: ["c1"] }) } }),
      // Views the web would open with another filter.
      savedView("v4", "Two tags", { sortIndex: 3, filter: web({ tag: ["t1", "t2"] }) }),
      savedView("v5", "Not in the inbox", { sortIndex: 4, filter: web({ inbox: false }) }),
    ];
    await start();
    const list = within(await column().findByRole("list", { name: t.savedViews }));
    // Each row: the view's button, then its menu.
    expect(list.getAllByRole("listitem").map((li) => within(li).getAllByRole("button")[0]!.textContent)).toEqual(["Steuern", "Bank"]);
    for (const name of ["Steuern", "Bank"]) expect(list.getByRole("button", { name: t.viewActions(name) })).toBeTruthy();
    expect(column().queryByText("From the app")).toBeNull();
    expect(column().queryByText("Two tags")).toBeNull();
    expect(column().queryByText("Not in the inbox")).toBeNull();
  });

  it("shows no section without views", async () => {
    server.savedViews = [savedView("v2", "From the app", { filter: { inbox: true } })];
    await start();
    await waitFor(() => expect(viewLoads()).toBe(1));
    expect(column().queryByRole("list", { name: t.savedViews })).toBeNull();
  });

  it("opens a view: its parameters replace the list's, and it is marked while the list shows them", async () => {
    server.correspondents = [named("c1", "Bank")];
    server.tags = [tag("t1", "Steuer")];
    server.savedViews = [savedView("v1", "Steuern 2026", { filter: { tags: [{ id: "x" }], ...web({ q: "added:2026", tag: ["t1"], favorite: true, sort: "date", order: "asc" }) } })];
    await start("/?corr=c1&inbox=1");
    fireEvent.click(await column().findByRole("button", { name: "Steuern 2026" }));
    await waitFor(() => expect(params(lists().at(-1)!)).toBe("q=added%3A2026&tag=t1&favorite=true&sort=date&order=asc"));
    expect(new URLSearchParams(window.location.search).get("sv")).toBe("v1");
    expect(rowOf("Steuern 2026").getAttribute("aria-current")).toBe("page");
    // Another search: the list no longer shows the view, which is no longer marked.
    fireEvent.change(screen.getByRole("searchbox", { name: t.searchLabel }), { target: { value: "other" } });
    fireEvent.submit(screen.getByRole("search"));
    await waitFor(() => expect(rowOf("Steuern 2026").getAttribute("aria-current")).toBeNull());
    // Another navigation entry forgets the view.
    fireEvent.click(screen.getByRole("treeitem", { name: /^Bank/ }));
    await waitFor(() => expect(new URLSearchParams(window.location.search).get("sv")).toBeNull());
  });

  it("applies an admin's Everyone's documents, and only for an admin", async () => {
    server.savedViews = [savedView("v1", "Everyone", { filter: web({ q: "x", everyone: true }) }), savedView("v2", "Mine", { filter: web({ q: "y" }) })];
    server.user = { id: 1, name: "root", admin: true };
    await start();
    fireEvent.click(await column().findByRole("button", { name: "Everyone" }));
    await waitFor(() => expect(params(lists().at(-1)!)).toBe("q=x"));
    expect(new URLSearchParams(window.location.search).get("all")).toBe("1");
    fireEvent.click(column().getByRole("button", { name: "Mine" }));
    await waitFor(() => expect(params(lists().at(-1)!)).toBe("q=y&owner=me"));
    expect(new URLSearchParams(window.location.search).get("all")).toBeNull();
    cleanup();

    server.user = { id: 2, name: "alice", admin: false };
    await start();
    fireEvent.click(await column().findByRole("button", { name: "Everyone" }));
    await waitFor(() => expect(params(lists().at(-1)!)).toBe("q=x"));
    expect(new URLSearchParams(window.location.search).get("all")).toBeNull();
  });
});

describe("Save view…", () => {
  it("is offered for a search or a filter, not for the plain list or the trash", async () => {
    await start();
    expect(screen.queryByRole("button", { name: t.saveView })).toBeNull();
    cleanup();
    await start("/?trash=1&q=x");
    expect(screen.queryByRole("button", { name: t.saveView })).toBeNull();
    cleanup();
    await start("/?inbox=1");
    expect(screen.getByRole("button", { name: t.saveView })).toBeTruthy();
  });

  it("saves the list's parameters under a name, then marks the new view", async () => {
    server.savedView = (_method, _path, body) => ({ status: 201, body: savedView("new", String(body?.name), { filter: body?.filter as Record<string, unknown> }) });
    await start("/?q=Rechnung&tag=t1&sort=date&order=asc");
    fireEvent.click(screen.getByRole("button", { name: t.saveView }));
    const sheet = within(await screen.findByRole("dialog", { name: t.saveViewTitle }));
    const save = sheet.getByRole("button", { name: t.save }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    fireEvent.change(sheet.getByLabelText(t.viewName), { target: { value: "  Steuern  " } });
    const loads = viewLoads();
    server.savedViews = [savedView("new", "Steuern", { filter: web({ q: "Rechnung", tag: ["t1"], sort: "date", order: "asc" }) })];
    fireEvent.click(save);
    await waitFor(() => expect(sent("POST")).toHaveLength(1));
    expect(sent("POST")[0]!.body).toEqual({
      name: "Steuern",
      filter: { [viewKey]: { v: 1, q: "Rechnung", tag: ["t1"], correspondent: [], type: [], inbox: null, favorite: null, sort: "date", order: "asc", everyone: false } },
    });
    await waitFor(() => expect(viewLoads()).toBe(loads + 1));
    expect(new URLSearchParams(window.location.search).get("sv")).toBe("new");
    await waitFor(() => expect(rowOf("Steuern").getAttribute("aria-current")).toBe("page"));
    expect(screen.queryByRole("dialog", { name: t.saveViewTitle })).toBeNull();
  });

  it("shows why saving failed and keeps the sheet open", async () => {
    server.savedView = () => ({ status: 400, body: { error: "invalid filter: query too long", code: "invalid_filter" } });
    await start("/?q=x");
    fireEvent.click(screen.getByRole("button", { name: t.saveView }));
    const sheet = within(await screen.findByRole("dialog", { name: t.saveViewTitle }));
    fireEvent.change(sheet.getByLabelText(t.viewName), { target: { value: "X" } });
    fireEvent.click(sheet.getByRole("button", { name: t.save }));
    expect((await sheet.findByRole("alert")).textContent).toBe(t.failed("invalid filter: query too long"));
  });
});

describe("a view's menu", () => {
  const appKeys = { tags: [{ id: "A1", name: "Tax", key: "tax" }], ascending: false };
  beforeEach(() => {
    server.savedViews = [
      savedView("v1", "First", { sortIndex: 3, filter: { ...appKeys, ...web({ q: "old" }) } }),
      savedView("v2", "Second", { sortIndex: 8, filter: web({ inbox: true }) }),
    ];
  });

  it("offers the five actions, and closes with Escape sending nothing", async () => {
    await start();
    const menu = await menuOf("First");
    expect(menu.getAllByRole("menuitem").map((m) => m.textContent)).toEqual([t.renameView, t.updateView, t.moveViewUp, t.moveViewDown, t.deleteView]);
    fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
    expect(requests.filter((r) => r.method !== "GET")).toHaveLength(0);
  });

  it("renames a view: PATCH { name } with the trimmed name", async () => {
    await start();
    const menu = await menuOf("First");
    fireEvent.click(menu.getByRole("menuitem", { name: t.renameView }));
    const dialog = within(await screen.findByRole("dialog", { name: t.renameView }));
    const field = dialog.getByLabelText(t.viewName) as HTMLInputElement;
    expect(field.value).toBe("First");
    expect(document.activeElement).toBe(field);
    fireEvent.change(field, { target: { value: " Erste " } });
    const loads = viewLoads();
    fireEvent.click(dialog.getByRole("button", { name: t.save }));
    await waitFor(() => expect(sent("PATCH")).toHaveLength(1));
    expect(pin(sent("PATCH")[0])).toEqual({ method: "PATCH", path: "/api/saved-views/v1", body: { name: "Erste" } });
    await waitFor(() => expect(viewLoads()).toBe(loads + 1));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: t.renameView })).toBeNull());
    expect(requests.filter((r) => r.method !== "GET")).toHaveLength(1);
  });

  it("renames with Enter in the field, and sends nothing for an empty name or on Cancel and Escape", async () => {
    await start();
    let menu = await menuOf("First");
    fireEvent.click(menu.getByRole("menuitem", { name: t.renameView }));
    let dialog = within(await screen.findByRole("dialog", { name: t.renameView }));
    fireEvent.change(dialog.getByLabelText(t.viewName), { target: { value: "   " } });
    expect((dialog.getByRole("button", { name: t.save }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.submit(dialog.getByLabelText(t.viewName).closest("form")!);
    expect(sent("PATCH")).toHaveLength(0);
    fireEvent.click(dialog.getByRole("button", { name: t.cancel }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: t.renameView })).toBeNull());
    menu = await menuOf("First");
    fireEvent.click(menu.getByRole("menuitem", { name: t.renameView }));
    dialog = within(await screen.findByRole("dialog", { name: t.renameView }));
    fireEvent.change(dialog.getByLabelText(t.viewName), { target: { value: "Changed" } });
    fireEvent.keyDown(dialog.getByLabelText(t.viewName), { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: t.renameView })).toBeNull());
    expect(sent("PATCH")).toHaveLength(0);
    menu = await menuOf("First");
    fireEvent.click(menu.getByRole("menuitem", { name: t.renameView }));
    dialog = within(await screen.findByRole("dialog", { name: t.renameView }));
    fireEvent.change(dialog.getByLabelText(t.viewName), { target: { value: "Erste" } });
    fireEvent.submit(dialog.getByLabelText(t.viewName).closest("form")!);
    await waitFor(() => expect(sent("PATCH")).toHaveLength(1));
    expect(pin(sent("PATCH")[0])).toEqual({ method: "PATCH", path: "/api/saved-views/v1", body: { name: "Erste" } });
  });

  it("shows why a rename failed and keeps the modal open", async () => {
    server.savedView = () => ({ status: 400, body: { error: "name too long", code: "invalid" } });
    await start();
    const menu = await menuOf("First");
    fireEvent.click(menu.getByRole("menuitem", { name: t.renameView }));
    const dialog = within(await screen.findByRole("dialog", { name: t.renameView }));
    fireEvent.change(dialog.getByLabelText(t.viewName), { target: { value: "X" } });
    fireEvent.click(dialog.getByRole("button", { name: t.save }));
    // Carbon's TextInput keeps an empty alert region of its own; the one with text is the app's.
    const alert = await findAlert();
    expect(alert.textContent).toContain(t.failed("name too long"));
    expect(screen.getByRole("dialog", { name: t.renameView }).contains(alert)).toBe(true);
  });

  it("updates a view to the current filter: PATCH with only the web's part, so the server keeps the app's keys (#143)", async () => {
    await start("/?q=neu&fav=1");
    const menu = await menuOf("First");
    fireEvent.click(menu.getByRole("menuitem", { name: t.updateView }));
    await waitFor(() => expect(sent("PATCH")).toHaveLength(1));
    expect(pin(sent("PATCH")[0])).toEqual({
      method: "PATCH",
      path: "/api/saved-views/v1",
      body: { paperlessGo: { v: 1, q: "neu", tag: [], correspondent: [], type: [], inbox: null, favorite: true, sort: "", order: "", everyone: false } },
    });
    expect(Object.keys(sent("PATCH")[0]!.body!)).toEqual(["paperlessGo"]);
    expect(await screen.findByText(t.viewUpdated("First"))).toBeTruthy();
    expect(requests.filter((r) => r.method !== "GET")).toHaveLength(1);
  });

  it("offers no update while the list is unfiltered, with the hint", async () => {
    await start();
    const menu = await menuOf("First");
    const item = menu.getByRole("menuitem", { name: t.updateView });
    expect(item.getAttribute("aria-disabled")).toBe("true");
    // The hint is the item's description (Carbon's MenuItem has no text slot for it).
    expect(document.getElementById(item.getAttribute("aria-describedby")!)?.textContent).toBe(t.updateViewHint);
    fireEvent.click(item);
    expect(sent("PATCH")).toHaveLength(0);
  });

  it("moves a view down and up by swapping sort indexes, one PATCH after the other, and not past the ends", async () => {
    await start();
    let menu = await menuOf("First");
    expect(menu.getByRole("menuitem", { name: t.moveViewUp }).getAttribute("aria-disabled")).toBe("true");
    fireEvent.click(menu.getByRole("menuitem", { name: t.moveViewDown }));
    await waitFor(() => expect(sent("PATCH")).toHaveLength(2));
    expect(sent("PATCH").map(pin)).toEqual([
      { method: "PATCH", path: "/api/saved-views/v1", body: { sortIndex: 8 } },
      { method: "PATCH", path: "/api/saved-views/v2", body: { sortIndex: 3 } },
    ]);
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
    menu = await menuOf("Second");
    expect(menu.getByRole("menuitem", { name: t.moveViewDown }).getAttribute("aria-disabled")).toBe("true");
    fireEvent.click(menu.getByRole("menuitem", { name: t.moveViewUp }));
    await waitFor(() => expect(sent("PATCH")).toHaveLength(4));
    expect(sent("PATCH").slice(2).map(pin)).toEqual([
      { method: "PATCH", path: "/api/saved-views/v2", body: { sortIndex: 3 } },
      { method: "PATCH", path: "/api/saved-views/v1", body: { sortIndex: 8 } },
    ]);
    expect(requests.filter((r) => r.method !== "GET")).toHaveLength(4);
  });

  it("offers no move or update while a request runs", async () => {
    const answer = held();
    server.savedView = () => answer.promise;
    await start("/?q=x");
    let menu = await menuOf("First");
    fireEvent.click(menu.getByRole("menuitem", { name: t.moveViewDown }));
    await waitFor(() => expect(sent("PATCH")).toHaveLength(1));
    menu = await menuOf("First");
    for (const name of [t.moveViewDown, t.updateView]) expect(menu.getByRole("menuitem", { name }).getAttribute("aria-disabled")).toBe("true");
    fireEvent.click(menu.getByRole("menuitem", { name: t.updateView }));
    expect(sent("PATCH")).toHaveLength(1);
    fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
    server.savedView = (_m, path, body) => ({ status: 200, body: savedView(path.split("/").pop()!, "x", body as Partial<SavedView>) });
    await act(async () => answer.release({ status: 200, body: savedView("v1", "First", { sortIndex: 8 }) }));
    await waitFor(() => expect(sent("PATCH")).toHaveLength(2));
    menu = await menuOf("First");
    await waitFor(() => expect(menu.getByRole("menuitem", { name: t.updateView }).getAttribute("aria-disabled")).not.toBe("true"));
  });

  it("stops moving after a failed PATCH and says why", async () => {
    server.savedView = () => ({ status: 500, body: { error: "boom" } });
    await start();
    const menu = await menuOf("First");
    fireEvent.click(menu.getByRole("menuitem", { name: t.moveViewDown }));
    expect((await screen.findByTestId("notice")).textContent).toContain(t.failed("boom"));
    expect(sent("PATCH").map(pin)).toEqual([{ method: "PATCH", path: "/api/saved-views/v1", body: { sortIndex: 8 } }]);
  });

  it("deletes a view only after asking: DELETE without a body; Cancel and Escape send nothing", async () => {
    await start();
    let menu = await menuOf("Second");
    fireEvent.click(menu.getByRole("menuitem", { name: t.deleteView }));
    let ask = within(await screen.findByRole("dialog", { name: t.deleteViewTitle("Second") }));
    expect(ask.getByText(t.deleteViewText)).toBeTruthy();
    fireEvent.click(ask.getByRole("button", { name: t.cancel }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: t.deleteViewTitle("Second") })).toBeNull());
    expect(sent("DELETE")).toHaveLength(0);
    // The focus is back on the view's menu button.
    expect(document.activeElement).toBe(column().getByRole("button", { name: t.viewActions("Second") }));

    menu = await menuOf("Second");
    fireEvent.click(menu.getByRole("menuitem", { name: t.deleteView }));
    ask = within(await screen.findByRole("dialog", { name: t.deleteViewTitle("Second") }));
    fireEvent.keyDown(ask.getByRole("button", { name: t.cancel }), { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: t.deleteViewTitle("Second") })).toBeNull());
    expect(sent("DELETE")).toHaveLength(0);
    expect(requests.filter((r) => r.method !== "GET")).toHaveLength(0);

    menu = await menuOf("Second");
    fireEvent.click(menu.getByRole("menuitem", { name: t.deleteView }));
    ask = within(await screen.findByRole("dialog", { name: t.deleteViewTitle("Second") }));
    server.savedViews = server.savedViews.slice(0, 1);
    fireEvent.click(ask.getByRole("button", { name: t.deleteView }));
    await waitFor(() => expect(sent("DELETE")).toHaveLength(1));
    expect(pin(sent("DELETE")[0])).toEqual({ method: "DELETE", path: "/api/saved-views/v2", body: undefined });
    expect(await screen.findByText(t.viewDeleted("Second"))).toBeTruthy();
    await waitFor(() => expect(column().queryByRole("button", { name: "Second" })).toBeNull());
    expect(requests.filter((r) => r.method !== "GET")).toHaveLength(1);
  });

  it("says when the view is gone and loads the views again", async () => {
    server.savedView = () => ({ status: 404, body: { error: "not found" } });
    await start();
    const menu = await menuOf("First");
    const loads = viewLoads();
    fireEvent.click(menu.getByRole("menuitem", { name: t.moveViewDown }));
    expect((await screen.findByTestId("notice")).textContent).toContain(t.viewGone);
    await waitFor(() => expect(viewLoads()).toBe(loads + 1));
    // Inside a modal the message shows there, and the views are loaded again as well.
    fireEvent.click((await menuOf("First")).getByRole("menuitem", { name: t.deleteView }));
    const ask = within(await screen.findByRole("dialog", { name: t.deleteViewTitle("First") }));
    fireEvent.click(ask.getByRole("button", { name: t.deleteView }));
    expect((await findAlert()).textContent).toContain(t.viewGone);
    expect(screen.getByRole("dialog", { name: t.deleteViewTitle("First") })).toBeTruthy();
    await waitFor(() => expect(viewLoads()).toBe(loads + 2));
  });
});

describe("the menu in German", () => {
  afterEach(() => {
    vi.resetModules();
  });

  it("names the menu, its items and the modals in German, nothing in English", async () => {
    vi.resetModules();
    vi.stubGlobal("navigator", { ...navigator, languages: ["de-DE", "en-US"], language: "de-DE" });
    const { App: GermanApp } = await import("./App");
    const { messagesFor } = await import("./i18n");
    const de = messagesFor("de");
    ({ server, requests } = stubServer());
    server.documents = () => page([]);
    server.savedViews = [savedView("v1", "Steuern", { filter: web({ q: "Steuer" }) })];
    render(<GermanApp />);
    fireEvent.click(await screen.findByRole("button", { name: de.viewActions("Steuern") }));
    const menu = within(await screen.findByRole("menu", { name: de.viewActions("Steuern") }));
    expect(menu.getAllByRole("menuitem").map((m) => m.textContent)).toEqual([de.renameView, de.updateView, de.moveViewUp, de.moveViewDown, de.deleteView]);
    fireEvent.click(menu.getByRole("menuitem", { name: de.deleteView }));
    const ask = within(await screen.findByRole("dialog", { name: de.deleteViewTitle("Steuern") }));
    expect(ask.getByRole("button", { name: de.cancel })).toBeTruthy();
    expect(ask.getByRole("button", { name: de.deleteView })).toBeTruthy();
    expect(ask.getByRole("button", { name: de.carbon.closeModal })).toBeTruthy();
    const html = document.body.innerHTML;
    for (const s of ["Options", "Rename", "Move up", "Delete", "Cancel", ">Close<", "Focus sentinel", ">danger<"]) expect(html, s).not.toContain(s);
  });
});
