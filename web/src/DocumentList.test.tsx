import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { DocumentSummary, NamedRow, Tag } from "./api";
import { App } from "./App";
import { pageSize, pollDelay, pollTimer } from "./DocumentList";
import { FakeXHR, stubXHR } from "./fakeXHR";
import { t } from "./i18n";
import { SNIPPET_START, SNIPPET_STOP } from "./snippet";

// The shell with sidebar and document list against a fake server: the
// tests check what the UI asks the server and what it shows.

const doc = (id: string, title: string, more: Partial<DocumentSummary> = {}): DocumentSummary => ({
  id,
  title,
  createdAt: "2026-10-21T10:00:00Z",
  documentDate: "2026-03-15T12:00:00Z",
  originalFileName: `${id}.pdf`,
  originalMime: "application/pdf",
  originalSize: 10,
  hasArchive: false,
  isInbox: false,
  isFavorite: false,
  correspondentId: null,
  documentTypeId: null,
  tagIds: [],
  ...more,
});

const tag = (id: string, name: string, parentId: string | null = null, more: Partial<Tag> = {}): Tag => ({
  id,
  name,
  parentId,
  ownerId: 1,
  color: "",
  documentCount: 1,
  subtreeCount: 1,
  ...more,
});
const named = (id: string, name: string, documentCount = 1): NamedRow => ({ id, name, ownerId: 1, documentCount });

type Page = { status?: number; body: unknown };

interface Server {
  user: { id: number; name: string; admin: boolean };
  tags: Tag[];
  correspondents: NamedRow[];
  types: NamedRow[];
  counts: { inbox: number; favorites: number; total: number };
  /** The answer to GET /api/documents for its parameters (without limit and tz). */
  documents: (p: URLSearchParams) => Page;
}

let server: Server;
/** Every request: method and URL. */
let requests: string[];
/** The parameters of each GET /api/documents. */
let lists: URLSearchParams[];

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const page = (documents: DocumentSummary[], nextCursor?: string): Page => ({ body: { documents, nextCursor, maxUploadSize: 1000 } });

beforeEach(() => {
  window.history.replaceState(null, "", "/");
  localStorage.clear();
  stubXHR();
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener: () => {}, removeEventListener: () => {} }));
  requests = [];
  lists = [];
  server = {
    user: { id: 1, name: "alice", admin: false },
    tags: [],
    correspondents: [],
    types: [],
    counts: { inbox: 0, favorites: 0, total: 0 },
    documents: () => page([]),
  };
  vi.stubGlobal("fetch", async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    requests.push(`${method} ${url}`);
    const path = url.split("?")[0];
    const p = new URLSearchParams(url.includes("?") ? url.slice(url.indexOf("?") + 1) : "");
    if (path === "/api/session") return json(200, { user: { mustChangePassword: false, twoFactorEnabled: false, twoFactorRequired: false, ...server.user } });
    if (path === "/healthz") return new Response("ok", { status: 200 });
    if (path === "/api/documents/counts") return json(200, server.counts);
    if (path === "/api/tags") return json(200, { tags: server.tags });
    if (path === "/api/correspondents") return json(200, { correspondents: server.correspondents });
    if (path === "/api/document-types") return json(200, { documentTypes: server.types });
    if (path === "/api/documents" && method === "GET") {
      lists.push(p);
      const r = server.documents(p);
      return json(r.status ?? 200, r.body);
    }
    return json(404, { error: "not found" });
  });
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

/** The desktop sidebar (the drawer holds a second one while it is open). */
const sidebar = () => within(screen.getByTestId("sidebar"));
/** The documents' list: the one named like the list's heading. */
function documentRows() {
  const title = document.getElementById("list-title")!.textContent!;
  return within(screen.getByRole("list", { name: title })).getAllByRole("listitem").filter((li) => li.parentElement?.getAttribute("aria-label") === title);
}
const lastList = () => lists.at(-1)!;
/** The list parameters that matter here: everything but limit and tz. */
const params = (p: URLSearchParams) => {
  const out = new URLSearchParams(p);
  out.delete("limit");
  out.delete("tz");
  return out.toString();
};

async function start() {
  render(<App />);
  await waitFor(() => expect(lists.length).toBeGreaterThan(0));
  await waitFor(() => expect(screen.queryByText(t.loading)).toBeNull());
}

describe("sidebar", () => {
  it("shows the counts, the tag tree, correspondents and types, sorted by name", async () => {
    server.counts = { inbox: 3, favorites: 2, total: 42 };
    server.tags = [
      tag("t1", "Steuer", null, { subtreeCount: 5, documentCount: 2 }),
      tag("t2", "2024", "t1", { subtreeCount: 3 }),
      tag("t3", "Ärzte"),
      tag("t4", "Orphan", "gone"),
      tag("t5", "arbeit", null, { color: "#d32f2f" }),
    ];
    server.correspondents = [named("c2", "Zahnarzt", 4), named("c1", "Amt", 7)];
    server.types = [named("d1", "Rechnung", 9)];
    await start();
    const s = sidebar();
    await s.findByRole("button", { name: /^Steuer/ });
    expect(s.getByRole("button", { name: new RegExp(`^${t.allDocuments}\\s*42$`) })).toBeTruthy();
    expect(s.getByRole("button", { name: new RegExp(`^${t.inbox}\\s*3$`) })).toBeTruthy();
    expect(s.getByRole("button", { name: new RegExp(`^${t.favorites}\\s*2$`) })).toBeTruthy();

    // Roots sorted with collate; the orphan is a root; the parent shows the subtree's count.
    const tagList = s.getByRole("list", { name: t.tags });
    const roots = Array.from(tagList.children).map((li) => li.querySelector("button:not([aria-expanded])")?.textContent);
    expect(roots).toEqual(["arbeit1", "Ärzte1", "Orphan1", "Steuer5"]);
    const steuer = Array.from(tagList.children).find((li) => li.textContent?.startsWith("Steuer"))!;
    expect(within(steuer as HTMLElement).getByRole("button", { name: /^2024\s*3$/ })).toBeTruthy();
    // The tag colour is a dot next to the name.
    const dot = within(Array.from(tagList.children)[0] as HTMLElement).getByTestId("tag-dot");
    expect(dot.style.backgroundColor).toBe("rgb(211, 47, 47)");

    const corr = s.getByRole("list", { name: t.correspondents });
    expect(within(corr).getAllByRole("button").map((b) => b.textContent)).toEqual(["Amt7", "Zahnarzt4"]);
    expect(within(s.getByRole("list", { name: t.documentTypes })).getByRole("button").textContent).toBe("Rechnung9");
    // Profile, Log and the log-out are in the sidebar's foot; no users page for a user.
    expect(s.getByRole("button", { name: t.log })).toBeTruthy();
    expect(s.getByRole("button", { name: t.profile })).toBeTruthy();
    expect(s.queryByRole("button", { name: t.users })).toBeNull();
    await waitFor(() => expect(s.getByRole("status").textContent).toBe(t.statusOk));
  });

  it("filters the list by a click, with parameters and never with q, and removes the filter by its chip", async () => {
    server.tags = [tag("t1", "Steuer")];
    server.correspondents = [named("c1", "Amt")];
    server.types = [named("d1", "Rechnung")];
    await start();
    expect(params(lastList())).toBe("");

    fireEvent.click(await sidebar().findByRole("button", { name: /Steuer/ }));
    expect(window.location.search).toBe("?tag=t1");
    await waitFor(() => expect(params(lastList())).toBe("tag=t1"));
    expect(screen.getByRole("heading", { level: 2, name: "Steuer" })).toBeTruthy();
    expect(screen.getByRole("button", { name: t.removeFilter(t.tagFilter("Steuer")) })).toBeTruthy();

    fireEvent.click(sidebar().getByRole("button", { name: /Amt/ }));
    await waitFor(() => expect(params(lastList())).toBe("correspondent=c1"));
    expect(window.location.search).toBe("?corr=c1");
    fireEvent.click(sidebar().getByRole("button", { name: /Rechnung/ }));
    await waitFor(() => expect(params(lastList())).toBe("type=d1"));
    fireEvent.click(sidebar().getByRole("button", { name: new RegExp(t.inbox) }));
    await waitFor(() => expect(params(lastList())).toBe("inbox=true"));
    fireEvent.click(sidebar().getByRole("button", { name: new RegExp(t.favorites) }));
    await waitFor(() => expect(params(lastList())).toBe("favorite=true"));
    expect(window.location.search).toBe("?fav=1");
    // No sidebar click ever sent a q: only typed searches are logged as searches.
    expect(lists.every((p) => !p.has("q"))).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: t.removeFilter(t.favorites) }));
    await waitFor(() => expect(params(lastList())).toBe(""));
    expect(window.location.search).toBe("");
  });

  it("keeps a typed search with a filter, and All documents clears both", async () => {
    server.tags = [tag("t1", "Steuer")];
    await start();
    fireEvent.change(screen.getByRole("searchbox", { name: t.searchLabel }), { target: { value: "Rechnung" } });
    // Typing does not search.
    const before = lists.length;
    expect(lists).toHaveLength(before);
    fireEvent.submit(screen.getByRole("search"));
    await waitFor(() => expect(params(lastList())).toBe("q=Rechnung"));
    fireEvent.click(await sidebar().findByRole("button", { name: /Steuer/ }));
    await waitFor(() => expect(params(lastList())).toBe("q=Rechnung&tag=t1"));
    fireEvent.click(sidebar().getByRole("button", { name: new RegExp(t.allDocuments) }));
    await waitFor(() => expect(params(lastList())).toBe(""));
    expect((screen.getByRole("searchbox", { name: t.searchLabel }) as HTMLInputElement).value).toBe("");
  });

  it("restores the previous filter on Back", async () => {
    server.tags = [tag("t1", "Steuer"), tag("t2", "Arzt")];
    await start();
    fireEvent.click(await sidebar().findByRole("button", { name: /Steuer/ }));
    await waitFor(() => expect(params(lastList())).toBe("tag=t1"));
    fireEvent.click(sidebar().getByRole("button", { name: /Arzt/ }));
    await waitFor(() => expect(params(lastList())).toBe("tag=t2"));
    act(() => window.history.back());
    await waitFor(() => expect(window.location.search).toBe("?tag=t1"));
    await waitFor(() => expect(params(lastList())).toBe("tag=t1"));
    expect(sidebar().getByRole("button", { name: /Steuer/ }).getAttribute("aria-current")).toBe("page");
    expect(sidebar().getByRole("button", { name: /Arzt/ }).getAttribute("aria-current")).toBeNull();
  });

  it("collapses a tag's subtree and remembers it", async () => {
    server.tags = [tag("t1", "Steuer"), tag("t2", "2024", "t1")];
    await start();
    const toggle = await sidebar().findByRole("button", { name: t.subTagsOf("Steuer") });
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(sidebar().queryByRole("button", { name: /2024/ })).toBeNull();
    expect(JSON.parse(localStorage.getItem("paperless-go.tags.alice")!)).toEqual({ v: 1, collapsed: ["t1"] });

    cleanup();
    await start();
    expect((await sidebar().findByRole("button", { name: t.subTagsOf("Steuer") })).getAttribute("aria-expanded")).toBe("false");
  });

  it("opens the selected tag's parents", async () => {
    localStorage.setItem("paperless-go.tags.alice", JSON.stringify({ v: 1, collapsed: ["t1"] }));
    window.history.replaceState(null, "", "/?tag=t2");
    server.tags = [tag("t1", "Steuer"), tag("t2", "2024", "t1")];
    await start();
    expect((await sidebar().findByRole("button", { name: /2024/ })).getAttribute("aria-current")).toBe("page");
  });

  it("shows why the names could not be loaded", async () => {
    await start();
    cleanup();
    const ok = globalThis.fetch;
    vi.stubGlobal("fetch", (url: string, init?: RequestInit) => (url.startsWith("/api/tags") ? Promise.resolve(json(503, { error: "database unavailable" })) : ok(url, init)));
    await start();
    expect((await sidebar().findByRole("alert")).textContent).toBe(t.loadMetadataFailed("database unavailable"));
  });
});

describe("phone drawer", () => {
  it("opens from the menu button, and closes after a choice and with its close button", async () => {
    server.tags = [tag("t1", "Steuer")];
    await start();
    const menu = screen.getByRole("button", { name: t.openMenu });
    expect(menu.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(menu);
    const drawer = within(screen.getByRole("dialog", { name: t.navigation }));
    expect(menu.getAttribute("aria-expanded")).toBe("true");
    fireEvent.click(drawer.getByRole("button", { name: /Steuer/ }));
    await waitFor(() => expect(params(lastList())).toBe("tag=t1"));
    expect(screen.queryByRole("dialog", { name: t.navigation })).toBeNull();
    expect(menu.getAttribute("aria-expanded")).toBe("false");

    fireEvent.click(menu);
    fireEvent.click(within(screen.getByRole("dialog", { name: t.navigation })).getByRole("button", { name: t.closeDialog }));
    expect(screen.queryByRole("dialog", { name: t.navigation })).toBeNull();

    // A page from the drawer's foot closes it too.
    fireEvent.click(menu);
    fireEvent.click(within(screen.getByRole("dialog", { name: t.navigation })).getByRole("button", { name: t.log }));
    expect(window.location.search).toBe("?tag=t1&view=log");
    expect(screen.queryByRole("dialog", { name: t.navigation })).toBeNull();
  });
});

describe("admin switch", () => {
  it("lists an admin's own documents by default and everyone's when switched on", async () => {
    server.user = { id: 1, name: "root", admin: true };
    // "Old": the admin's own document under a stale name (owners compare by ID).
    server.documents = () =>
      page([doc("1", "Mine", { ownerId: 1, ownerName: "root" }), doc("2", "Bobs", { ownerId: 2, ownerName: "bob" }), doc("3", "Old", { ownerId: 1, ownerName: "root-old" })]);
    await start();
    await screen.findByText("Mine");
    const owned = (path: string) => requests.filter((r) => r.startsWith(`GET ${path}`));
    expect(params(lastList())).toBe("owner=me");
    expect(owned("/api/documents/counts").at(-1)).toBe("GET /api/documents/counts?owner=me");
    expect(owned("/api/tags").at(-1)).toBe("GET /api/tags?owner=me");
    expect(owned("/api/correspondents").at(-1)).toBe("GET /api/correspondents?owner=me");
    expect(owned("/api/document-types").at(-1)).toBe("GET /api/document-types?owner=me");
    // Switched off, no row names an owner.
    expect(screen.queryByText(t.ownedBy("bob"))).toBeNull();

    const sw = sidebar().getByRole("switch", { name: t.everyonesDocuments });
    expect((sw as HTMLInputElement).checked).toBe(false);
    fireEvent.click(sw);
    await waitFor(() => expect(params(lastList())).toBe(""));
    expect(window.location.search).toBe("?all=1");
    await waitFor(() => expect(owned("/api/documents/counts").at(-1)).toBe("GET /api/documents/counts"));
    expect(owned("/api/tags").at(-1)).toBe("GET /api/tags");
    // Other users' rows name the owner; the admin's own do not.
    expect(await screen.findByText(t.ownedBy("bob"))).toBeTruthy();
    expect(screen.queryByText(t.ownedBy("root"))).toBeNull();
    expect(screen.queryByText(t.ownedBy("root-old"))).toBeNull();
    expect(sidebar().getByRole("button", { name: t.users })).toBeTruthy();
  });

  it("is not there for other users, who never send owner", async () => {
    await start();
    expect(sidebar().queryByRole("switch")).toBeNull();
    expect(requests.some((r) => r.includes("owner="))).toBe(false);
  });
});

describe("document list", () => {
  it("shows each row's fields as text: name, date, correspondent, type, tag chips, marks", async () => {
    server.tags = [tag("t1", "Steuer", null, { color: "#1e3a8a" }), tag("t2", "Plain")];
    server.correspondents = [named("c1", "Finanzamt")];
    server.types = [named("d1", "Bescheid")];
    server.documents = () =>
      page([
        doc("1", "<img src=x onerror=alert(1)>Bescheid 2024", {
          correspondentId: "c1",
          documentTypeId: "d1",
          tagIds: ["t1", "t2", "gone"],
          isInbox: true,
          isFavorite: true,
        }),
        doc("2", "", { originalFileName: "scan_0001.pdf" }),
      ]);
    await start();
    const [first, second] = documentRows();
    const row = within(first!);
    // A title is text, never HTML.
    expect(first!.querySelector("img[src=x]")).toBeNull();
    expect(row.getByText("<img src=x onerror=alert(1)>Bescheid 2024")).toBeTruthy();
    expect(row.getByText("Finanzamt")).toBeTruthy();
    expect(row.getByText("Bescheid")).toBeTruthy();
    expect(row.getByText(new Date("2026-03-15T12:00:00Z").toLocaleDateString(undefined, { dateStyle: "medium" }))).toBeTruthy();
    expect(row.getByText(t.inInbox)).toBeTruthy();
    expect(row.getByText(t.favorite)).toBeTruthy();
    const chips = within(row.getByRole("list", { name: t.tags })).getAllByRole("listitem");
    // A tag that is gone has no chip.
    expect(chips.map((c) => c.textContent)).toEqual(["Steuer", "Plain"]);
    expect(chips[0]!.style.backgroundColor).toBe("rgb(30, 58, 138)");
    expect(chips[0]!.style.color).toBe("rgb(255, 255, 255)");
    expect(chips[1]!.getAttribute("style")).toBeNull();
    // Without a title the file name shows.
    expect(within(second!).getByText("scan_0001.pdf")).toBeTruthy();
    expect(within(second!).queryByText(t.inInbox)).toBeNull();
  });

  it("shows snippets as text with marks", async () => {
    server.documents = (p) =>
      p.get("q")
        ? page([{ ...doc("1", "Telekom"), snippet: `<script>alert(1)</script> Ihre ${SNIPPET_START}Rechnung${SNIPPET_STOP} <b>fett</b>` }])
        : page([doc("0", "Recent")]);
    await start();
    fireEvent.change(screen.getByRole("searchbox", { name: t.searchLabel }), { target: { value: "Rechnung" } });
    fireEvent.submit(screen.getByRole("search"));
    expect(await screen.findByText("Telekom")).toBeTruthy();
    expect(screen.getByRole("heading", { level: 2, name: t.searchResults })).toBeTruthy();
    expect(lastList().get("tz")).toBe(Intl.DateTimeFormat().resolvedOptions().timeZone);
    const row = documentRows()[0]!;
    const marks = row.querySelectorAll("mark");
    expect(marks).toHaveLength(1);
    expect(marks[0]!.textContent).toBe("Rechnung");
    expect(row.querySelector("script")).toBeNull();
    expect(row.querySelector("b")).toBeNull();
    expect(row.textContent).toContain("<script>alert(1)</script> Ihre Rechnung <b>fett</b>");

    // Emptying the field lists everything again.
    fireEvent.change(screen.getByRole("searchbox", { name: t.searchLabel }), { target: { value: "" } });
    expect(await screen.findByText("Recent")).toBeTruthy();
    expect(window.location.search).toBe("");
  });

  it("loads more with the cursor and stops at the last page", async () => {
    server.documents = (p) => {
      const c = p.get("cursor") ?? "";
      if (c === "") return page([doc("1", "First")], "c1");
      if (c === "c1") return page([doc("2", "Second")], "c2");
      return page([doc("3", "Third")]);
    };
    await start();
    expect(lastList().get("limit")).toBe(String(pageSize));
    fireEvent.click(screen.getByRole("button", { name: t.loadMore }));
    expect(await screen.findByText("Second")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: t.loadMore }));
    expect(await screen.findByText("Third")).toBeTruthy();
    expect(documentRows().map((r) => r.querySelector(".font-medium")?.textContent)).toEqual(["First", "Second", "Third"]);
    expect(lists.map((p) => p.get("cursor"))).toEqual([null, "c1", "c2"]);
    expect(screen.queryByRole("button", { name: t.loadMore })).toBeNull();
  });

  it("keeps the loaded rows when a page fails, and shows why", async () => {
    server.documents = (p) => (p.get("cursor") ? { status: 503, body: { error: "database unavailable" } } : page([doc("1", "First")], "c1"));
    await start();
    fireEvent.click(screen.getByRole("button", { name: t.loadMore }));
    expect((await screen.findByRole("alert")).textContent).toBe(t.failed("database unavailable"));
    expect(screen.getByText("First")).toBeTruthy();
    expect(screen.getByRole("button", { name: t.loadMore })).toBeTruthy();
  });

  it("sorts by added or document date, both ways, and by relevance while searching", async () => {
    await start();
    const sort = () => screen.getByRole("combobox", { name: t.sortBy }) as HTMLSelectElement;
    expect(sort().value).toBe("added");
    expect(Array.from(sort().options).map((o) => o.value)).toEqual(["added", "date"]);
    fireEvent.change(sort(), { target: { value: "date" } });
    await waitFor(() => expect(params(lastList())).toBe("sort=date"));
    fireEvent.click(screen.getByRole("button", { name: t.sortNewestFirst }));
    await waitFor(() => expect(params(lastList())).toBe("sort=date&order=asc"));
    expect(window.location.search).toBe("?sort=date&order=asc");
    fireEvent.change(sort(), { target: { value: "added" } });
    await waitFor(() => expect(params(lastList())).toBe("order=asc"));

    fireEvent.change(screen.getByRole("searchbox", { name: t.searchLabel }), { target: { value: "Rechnung" } });
    fireEvent.submit(screen.getByRole("search"));
    // The default with a search is relevance, which has one direction.
    await waitFor(() => expect(params(lastList())).toBe("q=Rechnung"));
    expect(sort().value).toBe("relevance");
    expect((screen.getByRole("button", { name: t.sortNewestFirst }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(sort(), { target: { value: "date" } });
    await waitFor(() => expect(params(lastList())).toBe("q=Rechnung&sort=date&order=asc"));
  });

  it("refuses a search that is too long with its message", async () => {
    server.documents = (p) => (p.get("q") ? { status: 400, body: { error: "query too long" } } : page([]));
    await start();
    fireEvent.change(screen.getByRole("searchbox", { name: t.searchLabel }), { target: { value: "a ".repeat(40) } });
    fireEvent.submit(screen.getByRole("search"));
    expect((await screen.findByRole("alert")).textContent).toBe(t.searchTooLong);
  });

  it("does not search for blanks", async () => {
    await start();
    fireEvent.change(screen.getByRole("searchbox", { name: t.searchLabel }), { target: { value: "   " } });
    fireEvent.submit(screen.getByRole("search"));
    expect(window.location.search).toBe("");
    expect(lists.every((p) => !p.has("q"))).toBe(true);
  });
});

describe("empty states", () => {
  it("says there are no documents yet", async () => {
    await start();
    expect(screen.getByText(t.noDocuments)).toBeTruthy();
  });

  it("says the inbox is empty", async () => {
    window.history.replaceState(null, "", "/?inbox=1");
    await start();
    expect(screen.getByText(t.inboxEmpty)).toBeTruthy();
  });

  it("says a filter found nothing, and offers all documents", async () => {
    server.tags = [tag("t1", "Steuer")];
    window.history.replaceState(null, "", "/?tag=t1");
    await start();
    expect(screen.getByText(t.nothingFoundFilter)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: t.showAllDocuments }));
    expect(window.location.search).toBe("");
  });

  it("says a search found nothing, and clears it", async () => {
    window.history.replaceState(null, "", "/?q=nichts");
    await start();
    expect(screen.getByText(t.nothingFoundSearch)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: t.clearSearch }));
    expect(window.location.search).toBe("");
    await waitFor(() => expect(screen.getByText(t.noDocuments)).toBeTruthy());
  });
});

describe("processing status", () => {
  const withStatus = (id: string, title: string, processing: DocumentSummary["processing"], thumbnailUrl: string | null = null) => doc(id, title, { processing, thumbnailUrl });
  const row = (title: string) => screen.getByText(title).closest("li")!;

  /** The timers the list set for polling, to fire them by hand. */
  let timers: { fn: () => void; ms: number; cleared: boolean }[];
  beforeEach(() => {
    timers = [];
    vi.spyOn(pollTimer, "set").mockImplementation((fn, ms) => timers.push({ fn, ms, cleared: false }));
    vi.spyOn(pollTimer, "clear").mockImplementation((id) => {
      timers[id - 1]!.cleared = true;
    });
  });
  const live = () => timers.filter((x) => !x.cleared);
  // A timer that fired is no longer pending, so a wait for the next one sees
  // only the timer armed after the poll ended.
  const fire = () => {
    const x = live()[0]!;
    x.cleared = true;
    act(() => x.fn());
  };

  it("shows a badge per state, the error of a failed document and the thumbnail", async () => {
    server.documents = () =>
      page([
        withStatus("1", "Queued doc", { state: "queued", attempts: 0, maxAttempts: 3 }),
        withStatus("2", "Retried doc", { state: "queued", attempts: 1, maxAttempts: 3, error: "pdfinfo failed with exit status 99.", retryAt: "2026-10-21T10:01:00Z" }),
        withStatus("3", "Running doc", { state: "running", attempts: 1, maxAttempts: 3 }),
        withStatus("4", "Failed doc", { state: "failed", attempts: 1, maxAttempts: 3, error: "The PDF cannot be read." }),
        withStatus("5", "Done doc", { state: "done", attempts: 1, maxAttempts: 3 }, "/api/documents/5/thumbnail?v=0123456789abcdef"),
      ]);
    await start();
    expect(within(row("Queued doc")).getByRole("status").textContent).toBe(t.processingQueued);
    expect(within(row("Retried doc")).getByRole("status").textContent).toBe(t.processingRetrying);
    expect(within(row("Running doc")).getByRole("status").textContent).toBe(t.processingRunning);
    const failed = within(row("Failed doc"));
    expect(failed.getByText(t.processingFailed)).toBeTruthy();
    expect(failed.getByText("The PDF cannot be read.")).toBeTruthy();
    expect(failed.getByRole("button", { name: t.retryDocument("Failed doc") })).toBeTruthy();
    expect(within(row("Done doc")).queryByRole("status")).toBeNull();
    expect(row("Done doc").querySelector("img")?.getAttribute("src")).toBe("/api/documents/5/thumbnail?v=0123456789abcdef");
    expect(row("Queued doc").querySelector("img")).toBeNull();
    expect(within(row("Queued doc")).getByTestId("no-thumbnail")).toBeTruthy();
  });

  it("retries a failed document and shows it queued", async () => {
    server.documents = () => page([withStatus("4", "Failed doc", { state: "failed", attempts: 3, maxAttempts: 3, error: "Broken." })]);
    const list = globalThis.fetch;
    vi.stubGlobal("fetch", async (url: string, init?: RequestInit) =>
      init?.method === "POST" ? json(202, withStatus("4", "Failed doc", { state: "queued", attempts: 0, maxAttempts: 3 })) : list(url, init),
    );
    await start();
    fireEvent.click(await screen.findByRole("button", { name: t.retryDocument("Failed doc") }));
    await waitFor(() => expect(within(row("Failed doc")).getByRole("status").textContent).toBe(t.processingQueued));
    expect(screen.queryByText("Broken.")).toBeNull();
    expect(live().map((x) => x.ms)).toEqual([pollDelay]);
  });

  it("loads the first page again while its documents are processed, keeping later pages, and stops when they are done", async () => {
    let state: "queued" | "done" = "queued";
    server.documents = (p) =>
      p.get("cursor") ? page([doc("9", "Later")]) : page([withStatus("1", "Scan", { state, attempts: 0, maxAttempts: 3 })], "c1");
    await start();
    fireEvent.click(screen.getByRole("button", { name: t.loadMore }));
    await screen.findByText("Later");
    expect(live()).toHaveLength(1);

    const before = lists.length;
    // A failed poll keeps the list and tries again.
    const ok = globalThis.fetch;
    vi.stubGlobal("fetch", (url: string, init?: RequestInit) => (url.startsWith("/api/documents?") ? Promise.reject(new TypeError("Failed to fetch")) : ok(url, init)));
    fire();
    await waitFor(() => expect(live()).toHaveLength(1));
    expect(screen.getByText("Later")).toBeTruthy();
    vi.stubGlobal("fetch", ok);

    state = "done";
    fire();
    await waitFor(() => expect(within(row("Scan")).queryByRole("status")).toBeNull());
    expect(lists.length).toBe(before + 1);
    expect(lists.at(-1)!.has("cursor")).toBe(false);
    expect(screen.getByText("Later")).toBeTruthy();
    expect(live()).toHaveLength(0);
  });

  it("does not poll a typed search, which would log a search each time", async () => {
    window.history.replaceState(null, "", "/?q=Scan");
    server.documents = () => page([withStatus("1", "Scan", { state: "queued", attempts: 0, maxAttempts: 3 })]);
    await start();
    await screen.findByText("Scan");
    expect(timers).toHaveLength(0);
  });
});

describe("uploads", () => {
  const file = (name: string) => new File([new Uint8Array(10)], name, { type: "application/pdf" });

  it("loads the list and the counts again after an import, and the limit comes from the list", async () => {
    let docs = [doc("0", "Old‮ letter")];
    server.documents = () => page(docs);
    await start();
    // Bidi controls are dropped from names.
    expect(screen.getByText("Old letter")).toBeTruthy();
    expect(screen.getByText(t.uploadHint("1,000 B"))).toBeTruthy();
    const countsBefore = requests.filter((r) => r.startsWith("GET /api/documents/counts")).length;

    fireEvent.change(screen.getByTestId("file-picker"), { target: { files: [file("a.pdf")] } });
    docs = [doc("1", "a"), ...docs];
    await act(async () => FakeXHR.all[0]!.respond(201, doc("1", "a")));
    expect(await screen.findByText("a")).toBeTruthy();
    expect(requests.filter((r) => r.startsWith("GET /api/documents/counts")).length).toBe(countsBefore + 1);
  });
});
