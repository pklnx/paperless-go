import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { ApiError, type DocumentSummary } from "./api";
import { App } from "./App";
import { bulkErrorText } from "./BulkBar";
import { FakeXHR } from "./fakeXHR";
import { t } from "./i18n";
import { noticeDuration, noticeTimer } from "./Toast";
import { trashDay, trashErrorText } from "./Trash";
import { doc, held, page, stubServer, type Reply, type Request, type Server } from "./testServer";

// The trash in the web UI (#12, part 3) against a fake server: what the
// list, the bulk bar and "Empty trash" send, and what they show.

let server: Server;
let requests: Request[];
/** The server's documents: id → document (deletedAt set for the trash). */
let store: Map<string, DocumentSummary>;

const deleted = { deletedAt: "2026-10-01T08:00:00Z", purgeAt: "2026-10-31T08:00:00Z" };

beforeEach(() => {
  ({ server, requests } = stubServer());
  store = new Map();
  server.documents = (p) => page([...store.values()].filter((d) => (p.get("trashed") === "true") === Boolean(d.deletedAt)));
  server.document = (method, path) => {
    const m = /^\/api\/documents\/([^/]+)(?:\/(restore|purge))?$/.exec(path)!;
    const d = store.get(decodeURIComponent(m[1]!));
    if (!d) return { status: 404, body: { error: "not found" } };
    if (method === "DELETE") {
      if (d.deletedAt) return { status: 409, body: { error: "in the trash", code: "in_trash" } };
      store.set(d.id, { ...d, ...deleted });
      return { status: 204 };
    }
    if (method === "POST" && m[2] === "restore") {
      if (!d.deletedAt) return { status: 409, body: { error: "not in the trash", code: "not_in_trash" } };
      const back = { ...d, deletedAt: null, purgeAt: null };
      store.set(d.id, back);
      return { body: back };
    }
    if (method === "POST" && m[2] === "purge") {
      store.delete(d.id);
      return { status: 204 };
    }
    return { body: d };
  };
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const put = (...ds: DocumentSummary[]) => ds.forEach((d) => store.set(d.id, d));
const gets = (path: string) => requests.filter((r) => r.method === "GET" && r.path === path);
const lists = () => gets("/api/documents").map((r) => new URLSearchParams(r.url.split("?")[1]));
/** The list parameters that matter here: everything but limit and tz. */
const params = (p: URLSearchParams) => {
  const out = new URLSearchParams(p);
  out.delete("limit");
  out.delete("tz");
  return out.toString();
};
const bulks = () => requests.filter((r) => r.path === "/api/documents/bulk").map((r) => r.body);
const sent = (method: string, path: string) => requests.filter((r) => r.method === method && r.path === path);
/** The shell's SideNav: the lists with their counts and the pages. */
const shellNav = () => within(screen.getByRole("navigation", { name: t.navigation }));
const bar = () => within(screen.getByRole("region", { name: t.bulkActions }));
const notice = () => screen.findByTestId("notice");
/** Waits until the shell's notice says text. */
const noticeSays = (text: string) => waitFor(() => expect(screen.getByTestId("notice").textContent).toContain(text));
const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;

/** What a browser does when the focused button is disabled: the focus falls to the body (jsdom keeps it on the button and ignores blur()). */
function dropFocus() {
  document.body.tabIndex = -1;
  document.body.focus();
  document.body.removeAttribute("tabindex");
}

async function start(url = "/") {
  window.history.replaceState(null, "", url);
  render(<App />);
  await waitFor(() => expect(gets("/api/documents").length).toBeGreaterThan(0));
  await waitFor(() => expect(screen.queryByText(t.loading)).toBeNull());
}

async function openTrash() {
  fireEvent.click(await shellNav().findByRole("link", { name: new RegExp(`^${t.trash}`) }));
  await screen.findByRole("heading", { name: t.trash });
  await waitFor(() => expect(screen.queryByText(t.loading)).toBeNull());
}

/** Captures the notices' timers, so a test can let one run out; active are those set and not cleared. */
function holdNoticeTimers() {
  const timers = new Map<number, { fn: () => void; ms: number }>();
  let next = 0;
  vi.spyOn(noticeTimer, "set").mockImplementation((fn, ms) => {
    timers.set(++next, { fn, ms });
    return next;
  });
  vi.spyOn(noticeTimer, "clear").mockImplementation((id) => void timers.delete(id));
  return { active: () => [...timers.values()] };
}

describe("the Trash entry and list", () => {
  it("shows the trash count below Favorites and lists the trash newest deleted first", async () => {
    server.counts = { inbox: 0, favorites: 0, total: 1, trash: 2 };
    put(doc("1", "Live"), doc("2", "Alt", deleted), doc("3", "Älter", deleted));
    await start();
    const entries = shellNav().getAllByRole("link").map((l) => l.textContent);
    const fav = entries.findIndex((e) => e?.startsWith(t.favorites));
    expect(entries[fav + 1]).toBe(`${t.trash} 2`);
    await openTrash();
    expect(params(lists().at(-1)!)).toBe("trashed=true&sort=deleted");
    expect(window.location.search).toBe("?trash=1");
    expect(screen.getByText("Alt")).toBeTruthy();
    expect(screen.queryByText("Live")).toBeNull();
    // Its dates, and Restore and Delete permanently instead of Delete.
    expect(screen.getAllByText(`${t.deletedOn(trashDay(deleted.deletedAt))} · ${t.removedOn(trashDay(deleted.purgeAt))}`)).toHaveLength(2);
    expect(screen.getByRole("button", { name: t.restoreLabel("Alt") })).toBeTruthy();
    expect(screen.getByRole("button", { name: t.purgeLabel("Alt") })).toBeTruthy();
    expect(screen.queryByRole("button", { name: t.deleteDocumentLabel("Alt") })).toBeNull();
  });

  it("searches the trash with the same field, without the sidebar's filters", async () => {
    put(doc("2", "Alt", deleted));
    await start("/?trash=1&tag=t1&inbox=1");
    expect(params(lists().at(-1)!)).toBe("trashed=true&sort=deleted");
    fireEvent.change(screen.getByRole("searchbox", { name: t.searchLabel }), { target: { value: "Rechnung" } });
    fireEvent.keyDown(screen.getByRole("searchbox", { name: t.searchLabel }), { key: "Enter" });
    await waitFor(() => expect(params(lists().at(-1)!)).toBe("q=Rechnung&trashed=true"));
    expect(screen.getByRole("heading", { name: t.trash })).toBeTruthy();
  });

  it("lists an admin's own trash, and everyone's with the switch on", async () => {
    server.user = { id: 1, name: "root", admin: true };
    put(doc("2", "Alt", { ...deleted, ownerId: 2, ownerName: "bob" }));
    await start("/?trash=1");
    expect(params(lists().at(-1)!)).toBe("owner=me&trashed=true&sort=deleted");
    fireEvent.click(screen.getByRole("switch", { name: t.everyonesDocuments }));
    await waitFor(() => expect(params(lists().at(-1)!)).toBe("trashed=true&sort=deleted"));
    expect(await screen.findByText(t.ownedBy("bob"))).toBeTruthy();
  });

  it("shows an empty state for an empty trash, without Empty trash", async () => {
    await start("/?trash=1");
    expect(screen.getByText(t.trashEmpty)).toBeTruthy();
    expect(screen.queryByRole("button", { name: t.emptyTrash })).toBeNull();
  });

  it("renders titles as text, never as HTML, in the trash and in notices", async () => {
    put(doc("2", "<img src=x onerror=alert(1)>", deleted));
    await start("/?trash=1");
    expect(screen.getByText("<img src=x onerror=alert(1)>")).toBeTruthy();
    expect(document.querySelector("img[src=x]")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: t.restoreLabel("<img src=x onerror=alert(1)>") }));
    expect((await notice()).textContent).toContain(t.restoredDocument("<img src=x onerror=alert(1)>"));
    expect(document.querySelector("img[src=x]")).toBeNull();
  });
});

describe("delete with Undo", () => {
  it("moves a document to the trash without a question, and Undo restores it", async () => {
    vi.stubGlobal("confirm", vi.fn(() => false));
    put(doc("1", "Rechnung"), doc("2", "Brief"));
    await start();
    const countsBefore = gets("/api/documents/counts").length;
    fireEvent.click(screen.getByRole("button", { name: t.deleteDocumentLabel("Rechnung") }));
    const n = within(await notice());
    expect(n.getByText(t.movedToTrash("Rechnung"))).toBeTruthy();
    expect(sent("DELETE", "/api/documents/1")).toHaveLength(1);
    expect(window.confirm).not.toHaveBeenCalled();
    expect(screen.queryByRole("link", { name: "Rechnung" })).toBeNull();
    await waitFor(() => expect(gets("/api/documents/counts").length).toBe(countsBefore + 1));

    fireEvent.click(n.getByRole("button", { name: t.undo }));
    await waitFor(() => expect(sent("POST", "/api/documents/1/restore")).toHaveLength(1));
    expect(await screen.findByRole("link", { name: "Rechnung" })).toBeTruthy();
    expect((await notice()).textContent).toContain(t.restoredDocument("Rechnung"));
  });

  it("lets the notice run out after its time without sending anything", async () => {
    const timers = holdNoticeTimers();
    put(doc("1", "Rechnung"));
    await start();
    fireEvent.click(screen.getByRole("button", { name: t.deleteDocumentLabel("Rechnung") }));
    await notice();
    // The focus is in the notice: its time stands still.
    await waitFor(() => expect(document.activeElement?.textContent).toBe(t.undo));
    expect(timers.active()).toHaveLength(0);
    // Focus elsewhere: the full time runs again.
    act(() => screen.getByRole("searchbox", { name: t.searchLabel }).focus());
    expect(timers.active()).toHaveLength(1);
    const timer = timers.active()[0]!;
    expect(timer.ms).toBe(noticeDuration);
    expect(noticeDuration).toBe(8000);
    act(() => timer.fn());
    expect(screen.queryByTestId("notice")).toBeNull();
    expect(requests.filter((r) => r.path.endsWith("/restore"))).toHaveLength(0);
  });

  it("says when Undo comes too late", async () => {
    put(doc("1", "Rechnung"));
    await start();
    fireEvent.click(screen.getByRole("button", { name: t.deleteDocumentLabel("Rechnung") }));
    const n = within(await notice());
    // Removed for good meanwhile (a retention of 0): the server hides it.
    store.delete("1");
    fireEvent.click(n.getByRole("button", { name: t.undo }));
    await waitFor(async () => expect((await notice()).textContent).toContain(t.notInTrashAnyMore));
    expect((await notice()).parentElement!.getAttribute("aria-live")).toBe("assertive");
  });
});

describe("notices and row actions (review follow-ups)", () => {
  it("sends one request for a double click on Delete or Restore, and keeps the Undo", async () => {
    put(doc("1", "Rechnung"), doc("2", "Alt", deleted));
    await start();
    const del = screen.getByRole("button", { name: t.deleteDocumentLabel("Rechnung") });
    fireEvent.click(del);
    fireEvent.click(del);
    await noticeSays(t.movedToTrash("Rechnung"));
    expect(sent("DELETE", "/api/documents/1")).toHaveLength(1);
    expect(within(screen.getByTestId("notice")).getByRole("button", { name: t.undo })).toBeTruthy();

    await openTrash();
    const restore = screen.getByRole("button", { name: t.restoreLabel("Alt") });
    fireEvent.click(restore);
    fireEvent.click(restore);
    await noticeSays(t.restoredDocument("Alt"));
    expect(sent("POST", "/api/documents/2/restore")).toHaveLength(1);
    expect(screen.getByTestId("notice").parentElement!.getAttribute("aria-live")).toBe("polite");
  });

  it("moves the focus to the notice's Undo after a row's Delete", async () => {
    put(doc("1", "Rechnung"));
    await start();
    const del = screen.getByRole("button", { name: t.deleteDocumentLabel("Rechnung") });
    del.focus();
    fireEvent.click(del);
    await notice();
    await waitFor(() => expect(document.activeElement?.textContent).toBe(t.undo));
  });

  it("leaves the focus and the notice's time alone after a mouse click on Delete (#138)", async () => {
    const timers = holdNoticeTimers();
    put(doc("1", "Rechnung"), doc("2", "Brief"));
    await start();
    const del = screen.getByRole("button", { name: t.deleteDocumentLabel("Rechnung") });
    fireEvent.pointerDown(del);
    del.focus();
    fireEvent.click(del);
    await noticeSays(t.movedToTrash("Rechnung"));
    // No focus moved into the notice, and its 8 s run.
    expect(screen.getByTestId("notice").contains(document.activeElement)).toBe(false);
    expect(timers.active().map((x) => x.ms)).toEqual([noticeDuration]);
    // Focus from a pointer click into the notice does not stop the time either.
    const n = within(screen.getByTestId("notice"));
    const close = n.getByRole("button", { name: t.closeDialog });
    fireEvent.pointerDown(close);
    act(() => close.focus());
    expect(timers.active().map((x) => x.ms)).toEqual([noticeDuration]);
    // Tab moves the keyboard focus within it: now the time stands still.
    fireEvent.keyDown(close, { key: "Tab", shiftKey: true });
    act(() => n.getByRole("button", { name: t.undo }).focus());
    expect(timers.active()).toHaveLength(0);
    act(() => screen.getByRole("searchbox", { name: t.searchLabel }).focus());
    expect(timers.active().map((x) => x.ms)).toEqual([noticeDuration]);

    // A keyboard Delete after that still moves the focus to Undo, and pauses.
    const del2 = screen.getByRole("button", { name: t.deleteDocumentLabel("Brief") });
    act(() => del2.focus());
    fireEvent.keyDown(del2, { key: "Enter" });
    fireEvent.click(del2);
    await noticeSays(t.movedToTrash("Brief"));
    await waitFor(() => expect(document.activeElement?.textContent).toBe(t.undo));
    expect(timers.active()).toHaveLength(0);
  });

  it("keeps the notice paused while keyboard focus or pointer remains, and runs once both left (#138)", async () => {
    const timers = holdNoticeTimers();
    put(doc("1", "Rechnung"));
    await start();
    fireEvent.click(screen.getByRole("button", { name: t.deleteDocumentLabel("Rechnung") }));
    await waitFor(() => expect(document.activeElement?.textContent).toBe(t.undo));
    expect(timers.active()).toHaveLength(0);
    const box = screen.getByTestId("notice");
    fireEvent.mouseEnter(box);
    // The keyboard focus leaves, the pointer stays: still paused.
    act(() => screen.getByRole("searchbox", { name: t.searchLabel }).focus());
    expect(timers.active()).toHaveLength(0);
    fireEvent.mouseLeave(box);
    expect(timers.active().map((x) => x.ms)).toEqual([noticeDuration]);
    // And the other way round: the pointer leaves while the keyboard focus stays.
    fireEvent.keyDown(document.activeElement!, { key: "Tab" });
    act(() => within(box).getByRole("button", { name: t.undo }).focus());
    fireEvent.mouseEnter(box);
    fireEvent.mouseLeave(box);
    expect(timers.active()).toHaveLength(0);
  });

  it("keeps the Undo when another page opens within its time", async () => {
    put(doc("1", "Rechnung"));
    await start();
    fireEvent.click(screen.getByRole("button", { name: t.deleteDocumentLabel("Rechnung") }));
    await noticeSays(t.movedToTrash("Rechnung"));
    fireEvent.click(shellNav().getByRole("link", { name: t.log }));
    await waitFor(() => expect(window.location.search).toBe("?view=log"));
    fireEvent.click(within(screen.getByTestId("notice")).getByRole("button", { name: t.undo }));
    await waitFor(() => expect(sent("POST", "/api/documents/1/restore")).toHaveLength(1));
    await noticeSays(t.restoredDocument("Rechnung"));
  });

  it("shows Empty trash's result after the emptied trash took the button away", async () => {
    put(doc("2", "Alt", deleted));
    server.bulk = (b) => {
      if (b.dryRun) return { body: { matched: 1, owners: 1, limit: 5000, fingerprint: "v2.f" } };
      store.clear();
      return { body: { updated: 1 } };
    };
    await start("/?trash=1");
    fireEvent.click(screen.getByRole("button", { name: t.emptyTrash }));
    fireEvent.click(within(await screen.findByRole("dialog", { name: t.emptyTrashTitle })).getByRole("button", { name: t.purgeConfirm(1) }));
    await screen.findByText(t.trashEmpty);
    expect(screen.queryByRole("button", { name: t.emptyTrash })).toBeNull();
    await noticeSays(t.purgedCount(1));
  });

  it("shows a bulk delete's result after the documents and the bar are gone", async () => {
    put(doc("1", "Eins"), doc("2", "Zwei"));
    server.bulk = (b) => {
      for (const id of b.ids as string[]) store.set(id, { ...store.get(id)!, ...deleted });
      return { body: { updated: 2 } };
    };
    await start();
    fireEvent.click(screen.getByRole("checkbox", { name: t.selectDocument("Eins") }));
    fireEvent.click(screen.getByRole("checkbox", { name: t.selectDocument("Zwei") }));
    fireEvent.click(bar().getByRole("button", { name: t.bulkDelete }));
    fireEvent.click(within(await screen.findByRole("dialog", { name: t.bulkTrashTitle })).getByRole("button", { name: t.bulkTrashConfirm(2) }));
    await waitFor(() => expect(screen.queryByRole("region", { name: t.bulkActions })).toBeNull());
    await noticeSays(t.bulkTrashed(2));
  });

  it("counts again when the trash opens, and hides the upload panel there", async () => {
    put(doc("1", "Live"));
    await start();
    expect(screen.getByTestId("file-picker")).toBeTruthy();
    const before = gets("/api/documents/counts").length;
    await openTrash();
    await waitFor(() => expect(gets("/api/documents/counts").length).toBeGreaterThan(before));
    expect(screen.queryByTestId("file-picker")).toBeNull();
    expect(screen.queryByText(t.uploadTitle)).toBeNull();
  });

  it("words single-document errors about that document", () => {
    expect(trashErrorText(new ApiError(409, "x", { code: "in_trash" }), "trash")).toBe(t.alreadyInTrash);
    expect(trashErrorText(new ApiError(409, "x", { code: "not_in_trash" }), "restore")).toBe(t.restoredMeanwhile);
    expect(trashErrorText(new ApiError(404, "x"), "trash")).toBe(t.documentNotFound);
    expect(trashErrorText(new ApiError(404, "x"), "purge")).toBe(t.notInTrashAnyMore);
    expect(trashErrorText(new ApiError(503, "x", {}, 5), "restore")).toBe(t.tryAgainIn(5));
  });
});

describe("second review follow-ups", () => {
  it("brings the restored document back into a list mounted anew since the delete", async () => {
    put(doc("1", "Rechnung"), doc("2", "Brief"));
    await start();
    fireEvent.click(screen.getByRole("button", { name: t.deleteDocumentLabel("Rechnung") }));
    await noticeSays(t.movedToTrash("Rechnung"));
    // Another page and back: a new list, loaded without the row.
    fireEvent.click(shellNav().getByRole("link", { name: t.manageMetadata }));
    await waitFor(() => expect(screen.queryByRole("link", { name: "Brief" })).toBeNull());
    fireEvent.click(shellNav().getByRole("link", { name: new RegExp(`^${t.allDocuments}`) }));
    await screen.findByRole("link", { name: "Brief" });
    expect(screen.queryByRole("link", { name: "Rechnung" })).toBeNull();
    fireEvent.click(within(screen.getByTestId("notice")).getByRole("button", { name: t.undo }));
    expect(await screen.findByRole("link", { name: "Rechnung" })).toBeTruthy();
  });

  it("does not open the purge question again when its request fails after Cancel, and says so in the notice", async () => {
    put(doc("2", "Alt", deleted));
    const answer = held();
    const plain = server.document;
    server.document = (method, path, body) => (path.endsWith("/purge") ? answer.promise : plain(method, path, body));
    await start("/?trash=1");
    fireEvent.click(screen.getByRole("button", { name: t.purgeLabel("Alt") }));
    const ask = within(await screen.findByRole("dialog", { name: t.purgeTitle("Alt") }));
    fireEvent.click(ask.getByRole("button", { name: t.purgeConfirm(1) }));
    await waitFor(() => expect(sent("POST", "/api/documents/2/purge")).toHaveLength(1));
    fireEvent.click(ask.getByRole("button", { name: t.cancel }));
    await act(async () => answer.release({ status: 503, body: { error: "busy" } } as Reply));
    await noticeSays(t.tryAgainLater);
    expect(screen.queryByRole("dialog", { name: t.purgeTitle("Alt") })).toBeNull();
  });

  it("keeps the notice's live regions in the page, and returns the focus after Undo", async () => {
    put(doc("1", "Rechnung"));
    await start();
    // Mounted empty before any notice, so a new text is announced.
    expect(screen.getByTestId("notice-polite").getAttribute("aria-live")).toBe("polite");
    expect(screen.getByTestId("notice-assertive").getAttribute("aria-live")).toBe("assertive");
    expect(screen.getByTestId("notice-polite").textContent).toBe("");
    fireEvent.click(screen.getByRole("button", { name: t.deleteDocumentLabel("Rechnung") }));
    await waitFor(() => expect(document.activeElement?.textContent).toBe(t.undo));
    expect(screen.getByTestId("notice-polite").contains(screen.getByTestId("notice"))).toBe(true);
    fireEvent.click(within(screen.getByTestId("notice")).getByRole("button", { name: t.undo }));
    expect(document.activeElement).toBe(document.getElementById("list-title"));
  });

  it("pauses the notice's time while the pointer is on it", async () => {
    const timers = holdNoticeTimers();
    put(doc("1", "Rechnung"));
    await start();
    fireEvent.click(screen.getByRole("button", { name: t.deleteDocumentLabel("Rechnung") }));
    await waitFor(() => expect(document.activeElement?.textContent).toBe(t.undo));
    act(() => screen.getByRole("searchbox", { name: t.searchLabel }).focus());
    expect(timers.active()).toHaveLength(1);
    fireEvent.mouseEnter(screen.getByTestId("notice"));
    expect(timers.active()).toHaveLength(0);
    fireEvent.mouseLeave(screen.getByTestId("notice"));
    expect(timers.active().map((x) => x.ms)).toEqual([noticeDuration]);
  });

  it("shows Empty trash by the own trash's count, not by the rows shown", async () => {
    server.counts = { inbox: 0, favorites: 0, total: 0, trash: 2 };
    // A search in the trash that finds nothing: the own trash still has 2.
    server.documents = () => page([]);
    await start("/?trash=1&q=nichts");
    expect(await screen.findByRole("button", { name: t.emptyTrash })).toBeTruthy();
  });

  it("hides Empty my trash from an admin whose own trash is empty while others' documents show", async () => {
    server.user = { id: 1, name: "root", admin: true };
    server.counts = { inbox: 0, favorites: 0, total: 0, trash: 1 };
    server.ownCounts = { inbox: 0, favorites: 0, total: 0, trash: 0 };
    put(doc("2", "Von Bob", { ...deleted, ownerId: 3, ownerName: "bob" }));
    await start("/?trash=1&all=1");
    await screen.findByText("Von Bob");
    await waitFor(() => expect(gets("/api/documents/counts").some((r) => r.url.endsWith("?owner=me"))).toBe(true));
    await waitFor(() => expect(screen.queryByRole("button", { name: t.emptyMyTrash })).toBeNull());
    // With an own trashed document it shows.
    server.ownCounts = { inbox: 0, favorites: 0, total: 0, trash: 1 };
    fireEvent.click(screen.getByRole("button", { name: t.purgeLabel("Von Bob") }));
    fireEvent.click(within(await screen.findByRole("dialog", { name: t.purgeTitle("Von Bob") })).getByRole("button", { name: t.purgeConfirm(1) }));
    expect(await screen.findByRole("button", { name: t.emptyMyTrash })).toBeTruthy();
  });
});

describe("restore and delete permanently in the trash", () => {
  it("restores a row at once and drops it from the trash", async () => {
    put(doc("2", "Alt", deleted), doc("3", "Bleibt", deleted));
    await start("/?trash=1");
    fireEvent.click(screen.getByRole("button", { name: t.restoreLabel("Alt") }));
    await waitFor(() => expect(screen.queryByText("Alt")).toBeNull());
    expect(sent("POST", "/api/documents/2/restore")).toHaveLength(1);
    expect(screen.getByText("Bleibt")).toBeTruthy();
  });

  it("asks before Delete permanently; Cancel sends nothing, the confirmation purges", async () => {
    put(doc("2", "Alt", deleted));
    await start("/?trash=1");
    fireEvent.click(screen.getByRole("button", { name: t.purgeLabel("Alt") }));
    let ask = within(await screen.findByRole("dialog", { name: t.purgeTitle("Alt") }));
    expect(ask.getByText(t.purgeText(1))).toBeTruthy();
    fireEvent.click(ask.getByRole("button", { name: t.cancel }));
    expect(requests.filter((r) => r.path.endsWith("/purge"))).toHaveLength(0);

    fireEvent.click(screen.getByRole("button", { name: t.purgeLabel("Alt") }));
    ask = within(await screen.findByRole("dialog", { name: t.purgeTitle("Alt") }));
    fireEvent.click(ask.getByRole("button", { name: t.purgeConfirm(1) }));
    await waitFor(() => expect(screen.queryByText("Alt")).toBeNull());
    expect(sent("POST", "/api/documents/2/purge")).toHaveLength(1);
    expect((await notice()).textContent).toContain(t.purgedDocument("Alt"));
  });
});

describe("Empty trash", () => {
  const own = { trashed: true, owner: "me" };

  it("counts the own trash, asks with the count, purges exactly that set, and asks again when it changed", async () => {
    put(doc("2", "Alt", deleted), doc("3", "Älter", deleted));
    let dryRuns = 0;
    let purges = 0;
    server.bulk = (b) => {
      if (b.dryRun) {
        dryRuns++;
        return { body: { matched: dryRuns === 1 ? 2 : 3, owners: 1, limit: 5000, fingerprint: `v2.f${dryRuns}` } };
      }
      purges++;
      return purges === 1 ? { status: 409, body: { error: "changed", code: "match_changed", matched: 3 } } : { body: { updated: 3 } };
    };
    await start("/?trash=1");
    fireEvent.click(screen.getByRole("button", { name: t.emptyTrash }));
    let ask = within(await screen.findByRole("dialog", { name: t.emptyTrashTitle }));
    expect(ask.getByText(t.purgeText(2))).toBeTruthy();
    expect(bulks()).toEqual([{ match: own, dryRun: true, action: "purge" }]);
    fireEvent.click(ask.getByRole("button", { name: t.purgeConfirm(2) }));

    // 409 match_changed: counted again, asked again with the new number, nothing resent by itself.
    ask = within(await screen.findByRole("dialog", { name: t.emptyTrashTitle }));
    expect(ask.getByText(t.bulkMatchChanged(3))).toBeTruthy();
    expect(bulks()).toEqual([
      { match: own, dryRun: true, action: "purge" },
      { match: own, fingerprint: "v2.f1", action: "purge" },
      { match: own, dryRun: true, action: "purge" },
    ]);
    fireEvent.click(ask.getByRole("button", { name: t.purgeConfirm(3) }));
    expect(await screen.findByText(t.purgedCount(3))).toBeTruthy();
    expect(bulks().at(-1)).toEqual({ match: own, fingerprint: "v2.f2", action: "purge" });
  });

  it("empties only the admin's own trash also with everyone's documents shown, and says so", async () => {
    server.user = { id: 1, name: "root", admin: true };
    put(doc("2", "Alt", { ...deleted, ownerId: 2, ownerName: "bob" }));
    server.bulk = (b) => (b.dryRun ? { body: { matched: 1, owners: 1, limit: 5000, fingerprint: "v2.f" } } : { body: { updated: 1 } });
    await start("/?trash=1&all=1");
    expect(screen.queryByRole("button", { name: t.emptyTrash })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: t.emptyMyTrash }));
    const ask = within(await screen.findByRole("dialog", { name: t.emptyTrashTitle }));
    expect(ask.getByText(t.emptyTrashOwnOnly)).toBeTruthy();
    fireEvent.click(ask.getByRole("button", { name: t.purgeConfirm(1) }));
    await screen.findByText(t.purgedCount(1));
    expect(bulks()).toEqual([
      { match: own, dryRun: true, action: "purge" },
      { match: own, fingerprint: "v2.f", action: "purge" },
    ]);
  });

  it("says when the trash is empty or too large to empty at once, and asks nothing", async () => {
    put(doc("2", "Alt", deleted));
    let matched = 0;
    server.bulk = () => ({ body: matched > 5000 ? { matched, owners: 0, limit: 5000 } : { matched, owners: 1, limit: 5000, fingerprint: "v2.f" } });
    await start("/?trash=1");
    fireEvent.click(screen.getByRole("button", { name: t.emptyTrash }));
    expect(await screen.findByText(t.emptyTrashNothing)).toBeTruthy();
    matched = 6000;
    fireEvent.click(screen.getByRole("button", { name: t.emptyTrash }));
    expect(await screen.findByText(t.emptyTrashTooMany(5000))).toBeTruthy();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(bulks().every((b) => b?.dryRun === true)).toBe(true);
  });
});

describe("bulk delete, restore and purge", () => {
  const box = (name: string) => screen.getByRole("checkbox", { name: t.selectDocument(name) });

  it("asks with the count before moving checked documents to the trash", async () => {
    put(doc("1", "Eins"), doc("2", "Zwei"), doc("3", "Drei"));
    server.bulk = () => ({ body: { updated: 2 } });
    await start();
    fireEvent.click(box("Eins"));
    fireEvent.click(box("Zwei"));
    fireEvent.click(bar().getByRole("button", { name: t.bulkDelete }));
    const ask = within(await screen.findByRole("dialog", { name: t.bulkTrashTitle }));
    expect(ask.getByText(t.bulkTrashText(2))).toBeTruthy();
    expect(bulks()).toEqual([]);
    fireEvent.click(ask.getByRole("button", { name: t.bulkTrashConfirm(2) }));
    await noticeSays(t.bulkTrashed(2));
    expect(bulks()).toEqual([{ ids: ["1", "2"], action: "trash" }]);
  });

  it("moves every match of a search to the trash through the dry run of the action", async () => {
    const many = Array.from({ length: 3 }, (_, i) => doc(String(i + 1), `Doc ${i + 1}`));
    server.documents = (p) => (p.get("cursor") ? page([]) : page(many, "next"));
    server.bulk = (b) => (b.dryRun ? { body: { matched: 70, owners: 1, limit: 5000, fingerprint: "v2.t" } } : { body: { updated: 70 } });
    await start("/?q=Rechnung");
    fireEvent.click(screen.getByRole("checkbox", { name: t.selectAllLoaded }));
    fireEvent.click(screen.getByRole("button", { name: t.selectAllMatches }));
    await screen.findByText(t.allMatchesSelected(70));
    fireEvent.click(bar().getByRole("button", { name: t.bulkDelete }));
    const ask = within(await screen.findByRole("dialog", { name: t.bulkTrashTitle }));
    expect(ask.getByText(t.bulkEveryMatch)).toBeTruthy();
    fireEvent.click(ask.getByRole("button", { name: t.bulkTrashConfirm(70) }));
    await noticeSays(t.bulkTrashed(70));
    const match = { q: "Rechnung", tz };
    expect(bulks()).toEqual([
      { match, dryRun: true },
      { match, dryRun: true, action: "trash" },
      { match, fingerprint: "v2.t", action: "trash" },
    ]);
  });

  it("restores checked documents of the trash without asking, and asks before deleting them permanently", async () => {
    put(doc("2", "Alt", deleted), doc("3", "Älter", deleted));
    server.bulk = () => ({ body: { updated: 2 } });
    await start("/?trash=1");
    fireEvent.click(box("Alt"));
    fireEvent.click(box("Älter"));
    expect(bar().queryByRole("button", { name: t.bulkDelete })).toBeNull();
    expect(bar().queryByRole("button", { name: t.bulkTags })).toBeNull();
    fireEvent.click(bar().getByRole("button", { name: t.restore }));
    await noticeSays(t.bulkRestored(2));
    expect(bulks()).toEqual([{ ids: ["2", "3"], action: "restore" }]);

    fireEvent.click(bar().getByRole("button", { name: t.purge }));
    const ask = within(await screen.findByRole("dialog", { name: t.purgeBulkTitle }));
    expect(ask.getByText(t.purgeText(2))).toBeTruthy();
    // Own documents: no owners to name.
    expect(ask.queryByText(/^Owners:|^Besitzer:/)).toBeNull();
    fireEvent.click(ask.getByRole("button", { name: t.purgeConfirm(2) }));
    await noticeSays(t.purgedCount(2));
    expect(bulks().at(-1)).toEqual({ ids: ["2", "3"], action: "purge" });
  });

  it("names the owners when an admin deletes other users' checked documents permanently", async () => {
    server.user = { id: 1, name: "root", admin: true };
    put(doc("2", "Von Bob", { ...deleted, ownerId: 3, ownerName: "bob" }), doc("3", "Von Alice", { ...deleted, ownerId: 2, ownerName: "alice" }), doc("4", "Meins", { ...deleted, ownerId: 1, ownerName: "root" }));
    server.bulk = () => ({ body: { updated: 3 } });
    await start("/?trash=1&all=1");
    fireEvent.click(box("Von Bob"));
    fireEvent.click(box("Von Alice"));
    fireEvent.click(box("Meins"));
    fireEvent.click(bar().getByRole("button", { name: t.purge }));
    const ask = within(await screen.findByRole("dialog", { name: t.purgeBulkTitle }));
    expect(ask.getByText(t.purgeOwners("alice, bob, root"))).toBeTruthy();
    fireEvent.click(ask.getByRole("button", { name: t.purgeConfirm(3) }));
    await noticeSays(t.purgedCount(3));
    expect(bulks()).toEqual([{ ids: ["2", "3", "4"], action: "purge" }]);
  });

  it("names the owner when an admin deletes another user's document permanently from its row", async () => {
    server.user = { id: 1, name: "root", admin: true };
    put(doc("2", "Von Bob", { ...deleted, ownerId: 3, ownerName: "bob" }));
    await start("/?trash=1&all=1");
    fireEvent.click(screen.getByRole("button", { name: t.purgeLabel("Von Bob") }));
    const ask = within(await screen.findByRole("dialog", { name: t.purgeTitle("Von Bob") }));
    expect(ask.getByText(t.purgeOwners("bob"))).toBeTruthy();
  });

  it("refuses to delete every match permanently while an admin sees everyone's trash", async () => {
    server.user = { id: 1, name: "root", admin: true };
    const many = [doc("2", "Alt", { ...deleted, ownerId: 3, ownerName: "bob" })];
    server.documents = (p) => (p.get("cursor") ? page([]) : page(many, "next"));
    server.bulk = () => ({ body: { matched: 70, owners: 2, limit: 5000, fingerprint: "v2.r" } });
    await start("/?trash=1&all=1");
    fireEvent.click(screen.getByRole("checkbox", { name: t.selectAllLoaded }));
    fireEvent.click(screen.getByRole("button", { name: t.selectAllMatches }));
    await screen.findByText(t.allMatchesSelected(70));
    // The count of "Select all matches" in the trash is a dry run of restore.
    expect(bulks()).toEqual([{ match: { trashed: true, tz }, dryRun: true, action: "restore" }]);
    fireEvent.click(bar().getByRole("button", { name: t.purge }));
    expect(await bar().findByText(t.purgeMatchOwnOnly)).toBeTruthy();
    expect(bulks()).toHaveLength(1);
  });

  it("deletes every match of the own trash permanently with owner me", async () => {
    const many = [doc("2", "Alt", deleted)];
    server.documents = (p) => (p.get("cursor") ? page([]) : page(many, "next"));
    server.bulk = (b) => (b.dryRun ? { body: { matched: 70, owners: 1, limit: 5000, fingerprint: "v2.p" } } : { body: { updated: 70 } });
    await start("/?trash=1&q=Rechnung");
    fireEvent.click(screen.getByRole("checkbox", { name: t.selectAllLoaded }));
    fireEvent.click(screen.getByRole("button", { name: t.selectAllMatches }));
    await screen.findByText(t.allMatchesSelected(70));
    fireEvent.click(bar().getByRole("button", { name: t.purge }));
    const ask = within(await screen.findByRole("dialog", { name: t.purgeBulkTitle }));
    expect(ask.getByText(t.bulkEveryMatch)).toBeTruthy();
    fireEvent.click(ask.getByRole("button", { name: t.purgeConfirm(70) }));
    await noticeSays(t.purgedCount(70));
    const match = { q: "Rechnung", tz, trashed: true, owner: "me" };
    expect(bulks().slice(1)).toEqual([
      { match, dryRun: true, action: "purge" },
      { match, fingerprint: "v2.p", action: "purge" },
    ]);
  });
});

describe("the duplicate upload of a trashed document", () => {
  it("restores it from the upload panel, and the list and counts load again", async () => {
    put(doc("1", "Live"), doc("9", "Old", deleted));
    await start();
    fireEvent.change(screen.getByTestId("file-picker"), { target: { files: [new File([new Uint8Array(3)], "old.pdf", { type: "application/pdf" })] } });
    await act(async () => FakeXHR.all[0]!.respond(200, { duplicate: true, existing: { id: "9", title: "Old" }, trashed: true }));
    const listsBefore = gets("/api/documents").length;
    const countsBefore = gets("/api/documents/counts").length;
    fireEvent.click(screen.getByRole("button", { name: t.uploadRestoreLabel("old.pdf") }));
    expect(await screen.findByRole("link", { name: "Old" })).toBeTruthy();
    expect(sent("POST", "/api/documents/9/restore")).toHaveLength(1);
    expect(gets("/api/documents").length).toBe(listsBefore + 1);
    await waitFor(() => expect(gets("/api/documents/counts").length).toBe(countsBefore + 1));
  });
});

describe("bulkErrorText for the trash", () => {
  it("explains the state errors and a 503 with its wait", () => {
    expect(bulkErrorText(new ApiError(409, "x", { code: "in_trash" }))).toEqual({ text: t.bulkInTrash, retry: false });
    expect(bulkErrorText(new ApiError(409, "x", { code: "not_in_trash" }))).toEqual({ text: t.bulkNotInTrash, retry: false });
    expect(bulkErrorText(new ApiError(503, "x", {}, 5))).toEqual({ text: t.tryAgainIn(5), retry: true });
    expect(bulkErrorText(new ApiError(503, "x", {}))).toEqual({ text: t.serverBusy, retry: true });
  });
});

describe("the trash's Carbon modals (#150, part 3)", () => {
  const dialog = (name: string) => screen.findByRole("dialog", { name });
  const gone = (name: string) => waitFor(() => expect(screen.queryByRole("dialog", { name })).toBeNull());

  it("sends nothing from Empty trash's question on Cancel, Escape or the close button: the dry runs are the only requests", async () => {
    put(doc("2", "Alt", deleted));
    const counted = { body: { matched: 1, owners: 1, limit: 5000, fingerprint: "v2.f" } };
    let hold: ReturnType<typeof held> | null = null;
    server.bulk = (b) => (b.dryRun ? (hold ? hold.promise : counted) : { body: { updated: 1 } });
    await start("/?trash=1");
    const empty = () => screen.getByRole("button", { name: t.emptyTrash });
    // While the dry run counts, the button is disabled; a browser drops the
    // focus from a disabled button (jsdom keeps it, so the blur is
    // simulated), and the modal mounts with the focus on the body.
    hold = held();
    empty().focus();
    fireEvent.click(empty());
    expect(empty().hasAttribute("disabled")).toBe(true);
    act(() => dropFocus());
    expect(document.activeElement).toBe(document.body);
    await act(async () => hold!.release(counted));
    hold = null;
    let ask = within(await dialog(t.emptyTrashTitle));
    expect(ask.getByText(t.purgeText(1))).toBeTruthy();
    expect(document.activeElement).toBe(ask.getByRole("button", { name: t.cancel }));
    fireEvent.click(ask.getByRole("button", { name: t.cancel }));
    await gone(t.emptyTrashTitle);
    // The focus is back on the button that asked.
    expect(document.activeElement).toBe(empty());
    fireEvent.click(empty());
    ask = within(await dialog(t.emptyTrashTitle));
    fireEvent.keyDown(ask.getByRole("button", { name: t.purgeConfirm(1) }), { key: "Escape" });
    await gone(t.emptyTrashTitle);
    fireEvent.click(empty());
    ask = within(await dialog(t.emptyTrashTitle));
    fireEvent.click(ask.getByRole("button", { name: t.carbon.closeModal }));
    await gone(t.emptyTrashTitle);
    const dryRun = { match: { trashed: true, owner: "me" }, dryRun: true, action: "purge" };
    expect(bulks()).toEqual([dryRun, dryRun, dryRun]);
    expect(requests.filter((r) => r.method !== "GET" && !(r.body && r.body.dryRun === true))).toHaveLength(0);
    expect(screen.getByText("Alt")).toBeTruthy();
  });

  it("sends nothing from a row's Delete permanently on Escape or the close button, and returns the focus to the row's button", async () => {
    put(doc("2", "Alt", deleted));
    await start("/?trash=1");
    const purge = () => screen.getByRole("button", { name: t.purgeLabel("Alt") });
    purge().focus();
    fireEvent.click(purge());
    let ask = within(await dialog(t.purgeTitle("Alt")));
    fireEvent.keyDown(ask.getByRole("button", { name: t.purgeConfirm(1) }), { key: "Escape" });
    await gone(t.purgeTitle("Alt"));
    expect(document.activeElement).toBe(purge());
    fireEvent.click(purge());
    ask = within(await dialog(t.purgeTitle("Alt")));
    fireEvent.click(ask.getByRole("button", { name: t.carbon.closeModal }));
    await gone(t.purgeTitle("Alt"));
    expect(requests.filter((r) => r.method !== "GET")).toHaveLength(0);
    expect(screen.getByText("Alt")).toBeTruthy();
  });

  it("moves the focus to the list's heading after a purge whose row is gone (a pointer user)", async () => {
    put(doc("2", "Alt", deleted), doc("3", "Bleibt", deleted));
    await start("/?trash=1");
    const purge = screen.getByRole("button", { name: t.purgeLabel("Alt") });
    // A pointer click: the notice takes no focus then (Toast), and the row with its button is gone.
    fireEvent.pointerDown(purge);
    purge.focus();
    fireEvent.click(purge);
    const ask = within(await dialog(t.purgeTitle("Alt")));
    fireEvent.pointerDown(ask.getByRole("button", { name: t.purgeConfirm(1) }));
    fireEvent.click(ask.getByRole("button", { name: t.purgeConfirm(1) }));
    await waitFor(() => expect(screen.queryByText("Alt")).toBeNull());
    expect(sent("POST", "/api/documents/2/purge")).toHaveLength(1);
    await waitFor(() => expect(document.activeElement).toBe(document.getElementById("list-title")));
  });
});
