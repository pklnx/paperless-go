import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { App } from "./App";
import { t } from "./i18n";
import { viewKey } from "./savedViews";
import { named, page, savedView, stubServer, tag, type Request, type Server } from "./testServer";

// Saved views in the web UI (#12, part 4) against a fake server: what the
// sidebar shows, what opening a view requests, and what saving, renaming,
// updating, moving and deleting send.

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

const sidebar = () => within(screen.getByTestId("sidebar"));
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

/** Opens a view's menu in the sidebar; returns the sheet. */
async function menuOf(name: string) {
  fireEvent.click(await sidebar().findByRole("button", { name: t.viewActions(name) }));
  return within(await screen.findByRole("dialog", { name }));
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
    const list = within(await sidebar().findByRole("list", { name: t.savedViews }));
    expect(list.getAllByRole("listitem").map((li) => li.textContent)).toEqual(["Steuern⋯", "Bank⋯"]);
    expect(sidebar().queryByText("From the app")).toBeNull();
    expect(sidebar().queryByText("Two tags")).toBeNull();
    expect(sidebar().queryByText("Not in the inbox")).toBeNull();
  });

  it("shows no section without views", async () => {
    server.savedViews = [savedView("v2", "From the app", { filter: { inbox: true } })];
    await start();
    await waitFor(() => expect(viewLoads()).toBe(1));
    expect(sidebar().queryByRole("list", { name: t.savedViews })).toBeNull();
  });

  it("opens a view: its parameters replace the list's, and it is marked while the list shows them", async () => {
    server.correspondents = [named("c1", "Bank")];
    server.tags = [tag("t1", "Steuer")];
    server.savedViews = [savedView("v1", "Steuern 2026", { filter: { tags: [{ id: "x" }], ...web({ q: "added:2026", tag: ["t1"], favorite: true, sort: "date", order: "asc" }) } })];
    await start("/?corr=c1&inbox=1");
    fireEvent.click(await sidebar().findByRole("button", { name: "Steuern 2026" }));
    await waitFor(() => expect(params(lists().at(-1)!)).toBe("q=added%3A2026&tag=t1&favorite=true&sort=date&order=asc"));
    expect(new URLSearchParams(window.location.search).get("sv")).toBe("v1");
    expect(sidebar().getByRole("button", { name: "Steuern 2026" }).getAttribute("aria-current")).toBe("page");
    // Another search: the list no longer shows the view, which is no longer marked.
    fireEvent.change(screen.getByRole("searchbox", { name: t.searchLabel }), { target: { value: "other" } });
    fireEvent.submit(screen.getByRole("search"));
    await waitFor(() => expect(sidebar().getByRole("button", { name: "Steuern 2026" }).getAttribute("aria-current")).toBeNull());
    // Another sidebar entry forgets the view.
    fireEvent.click(sidebar().getByRole("button", { name: /^Bank/ }));
    await waitFor(() => expect(new URLSearchParams(window.location.search).get("sv")).toBeNull());
  });

  it("applies an admin's Everyone's documents, and only for an admin", async () => {
    server.savedViews = [savedView("v1", "Everyone", { filter: web({ q: "x", everyone: true }) }), savedView("v2", "Mine", { filter: web({ q: "y" }) })];
    server.user = { id: 1, name: "root", admin: true };
    await start();
    fireEvent.click(await sidebar().findByRole("button", { name: "Everyone" }));
    await waitFor(() => expect(params(lists().at(-1)!)).toBe("q=x"));
    expect(new URLSearchParams(window.location.search).get("all")).toBe("1");
    fireEvent.click(sidebar().getByRole("button", { name: "Mine" }));
    await waitFor(() => expect(params(lists().at(-1)!)).toBe("q=y&owner=me"));
    expect(new URLSearchParams(window.location.search).get("all")).toBeNull();
    cleanup();

    server.user = { id: 2, name: "alice", admin: false };
    await start();
    fireEvent.click(await sidebar().findByRole("button", { name: "Everyone" }));
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
    await waitFor(() => expect(sidebar().getByRole("button", { name: "Steuern" }).getAttribute("aria-current")).toBe("page"));
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

  it("renames a view", async () => {
    await start();
    const menu = await menuOf("First");
    fireEvent.click(menu.getByRole("button", { name: t.renameView }));
    const sheet = within(screen.getByRole("dialog", { name: t.renameView }));
    fireEvent.change(sheet.getByLabelText(t.viewName), { target: { value: " Erste " } });
    const loads = viewLoads();
    fireEvent.click(sheet.getByRole("button", { name: t.save }));
    await waitFor(() => expect(sent("PATCH")).toHaveLength(1));
    expect(sent("PATCH")[0]).toMatchObject({ path: "/api/saved-views/v1", body: { name: "Erste" } });
    await waitFor(() => expect(viewLoads()).toBe(loads + 1));
    expect(screen.queryByRole("dialog", { name: t.renameView })).toBeNull();
  });

  it("updates a view to the current filter, sending only the web's part so the server keeps the app's keys (#143)", async () => {
    await start("/?q=neu&fav=1");
    const menu = await menuOf("First");
    fireEvent.click(menu.getByRole("button", { name: t.updateView }));
    await waitFor(() => expect(sent("PATCH")).toHaveLength(1));
    expect(sent("PATCH")[0]!.body).toEqual({
      paperlessGo: { v: 1, q: "neu", tag: [], correspondent: [], type: [], inbox: null, favorite: true, sort: "", order: "", everyone: false },
    });
    expect(await screen.findByText(t.viewUpdated("First"))).toBeTruthy();
  });

  it("offers no update while the list is unfiltered", async () => {
    await start();
    const menu = await menuOf("First");
    expect((menu.getByRole("button", { name: t.updateView }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("moves a view down and up by swapping sort indexes, and not past the ends", async () => {
    await start();
    let menu = await menuOf("First");
    expect((menu.getByRole("button", { name: t.moveViewUp }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(menu.getByRole("button", { name: t.moveViewDown }));
    await waitFor(() => expect(sent("PATCH")).toHaveLength(2));
    expect(sent("PATCH").map((r) => [r.path, r.body])).toEqual([
      ["/api/saved-views/v1", { sortIndex: 8 }],
      ["/api/saved-views/v2", { sortIndex: 3 }],
    ]);
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "First" })).toBeNull());
    menu = await menuOf("Second");
    expect((menu.getByRole("button", { name: t.moveViewDown }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(menu.getByRole("button", { name: t.moveViewUp }));
    await waitFor(() => expect(sent("PATCH")).toHaveLength(4));
    expect(sent("PATCH").slice(2).map((r) => [r.path, r.body])).toEqual([
      ["/api/saved-views/v2", { sortIndex: 3 }],
      ["/api/saved-views/v1", { sortIndex: 8 }],
    ]);
  });

  it("deletes a view only after asking", async () => {
    await start();
    let menu = await menuOf("Second");
    fireEvent.click(menu.getByRole("button", { name: t.deleteView }));
    let ask = within(screen.getByRole("dialog", { name: t.deleteViewTitle("Second") }));
    expect(ask.getByText(t.deleteViewText)).toBeTruthy();
    fireEvent.click(ask.getByRole("button", { name: t.cancel }));
    expect(screen.queryByRole("dialog", { name: t.deleteViewTitle("Second") })).toBeNull();
    expect(sent("DELETE")).toHaveLength(0);

    menu = await menuOf("Second");
    fireEvent.click(menu.getByRole("button", { name: t.deleteView }));
    ask = within(screen.getByRole("dialog", { name: t.deleteViewTitle("Second") }));
    server.savedViews = server.savedViews.slice(0, 1);
    fireEvent.click(ask.getByRole("button", { name: t.deleteView }));
    await waitFor(() => expect(sent("DELETE")).toHaveLength(1));
    expect(sent("DELETE")[0]!.path).toBe("/api/saved-views/v2");
    expect(await screen.findByText(t.viewDeleted("Second"))).toBeTruthy();
    await waitFor(() => expect(sidebar().queryByRole("button", { name: "Second" })).toBeNull());
  });

  it("says when the view is gone and loads the views again", async () => {
    server.savedView = () => ({ status: 404, body: { error: "not found" } });
    await start();
    const menu = await menuOf("First");
    const loads = viewLoads();
    fireEvent.click(menu.getByRole("button", { name: t.moveViewDown }));
    expect((await menu.findByRole("alert")).textContent).toBe(t.viewGone);
    await waitFor(() => expect(viewLoads()).toBe(loads + 1));
  });
});
