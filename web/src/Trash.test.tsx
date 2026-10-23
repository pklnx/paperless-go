import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { ApiError, type DocumentSummary } from "./api";
import { App } from "./App";
import { bulkErrorText } from "./BulkBar";
import { FakeXHR } from "./fakeXHR";
import { t } from "./i18n";
import { noticeDuration, noticeTimer } from "./Toast";
import { trashDay } from "./Trash";
import { doc, page, stubServer, type Request, type Server } from "./testServer";

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
const sidebar = () => within(screen.getByTestId("sidebar"));
const bar = () => within(screen.getByRole("region", { name: t.bulkActions }));
const notice = () => screen.findByTestId("notice");
const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;

async function start(url = "/") {
  window.history.replaceState(null, "", url);
  render(<App />);
  await waitFor(() => expect(gets("/api/documents").length).toBeGreaterThan(0));
  await waitFor(() => expect(screen.queryByText(t.loading)).toBeNull());
}

async function openTrash() {
  fireEvent.click(await sidebar().findByRole("button", { name: new RegExp(`^${t.trash}`) }));
  await screen.findByRole("heading", { name: t.trash });
  await waitFor(() => expect(screen.queryByText(t.loading)).toBeNull());
}

/** Captures the notices' timers, so a test can let one run out. */
function holdNoticeTimers() {
  const timers: { fn: () => void; ms: number }[] = [];
  vi.spyOn(noticeTimer, "set").mockImplementation((fn, ms) => timers.push({ fn, ms }));
  vi.spyOn(noticeTimer, "clear").mockImplementation(() => {});
  return timers;
}

describe("the Trash entry and list", () => {
  it("shows the trash count below Favorites and lists the trash newest deleted first", async () => {
    server.counts = { inbox: 0, favorites: 0, total: 1, trash: 2 };
    put(doc("1", "Live"), doc("2", "Alt", deleted), doc("3", "Älter", deleted));
    await start();
    const entries = sidebar().getAllByRole("button").map((b) => b.textContent);
    const fav = entries.findIndex((e) => e?.startsWith(t.favorites));
    expect(entries[fav + 1]).toBe(`${t.trash}2`);
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
    fireEvent.submit(screen.getByRole("search"));
    await waitFor(() => expect(params(lists().at(-1)!)).toBe("q=Rechnung&trashed=true"));
    expect(screen.getByRole("heading", { name: t.trash })).toBeTruthy();
  });

  it("lists an admin's own trash, and everyone's with the switch on", async () => {
    server.user = { id: 1, name: "root", admin: true };
    put(doc("2", "Alt", { ...deleted, ownerId: 2, ownerName: "bob" }));
    await start("/?trash=1");
    expect(params(lists().at(-1)!)).toBe("owner=me&trashed=true&sort=deleted");
    fireEvent.click(sidebar().getByRole("switch", { name: t.everyonesDocuments }));
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
    const timer = timers.at(-1)!;
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
    expect((await notice()).getAttribute("role")).toBe("alert");
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
    expect(await bar().findByText(t.bulkTrashed(2))).toBeTruthy();
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
    await bar().findByText(t.bulkTrashed(70));
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
    await bar().findByText(t.bulkRestored(2));
    expect(bulks()).toEqual([{ ids: ["2", "3"], action: "restore" }]);

    fireEvent.click(bar().getByRole("button", { name: t.purge }));
    const ask = within(await screen.findByRole("dialog", { name: t.purgeBulkTitle }));
    expect(ask.getByText(t.purgeText(2))).toBeTruthy();
    // Own documents: no owners to name.
    expect(ask.queryByText(/^Owners:|^Besitzer:/)).toBeNull();
    fireEvent.click(ask.getByRole("button", { name: t.purgeConfirm(2) }));
    await bar().findByText(t.purgedCount(2));
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
    await bar().findByText(t.purgedCount(3));
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
    await bar().findByText(t.purgedCount(70));
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
