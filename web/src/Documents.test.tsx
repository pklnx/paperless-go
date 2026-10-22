import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import type { DocumentSummary } from "./api";
import { DocumentsHome, WindowDrop, searchPageSize } from "./Documents";
import { SNIPPET_START, SNIPPET_STOP } from "./snippet";
import { FakeXHR, stubXHR } from "./fakeXHR";
import { t } from "./i18n";
import { UploadQueue } from "./uploads";

const doc = (id: string, title: string): DocumentSummary => ({
  id,
  title,
  createdAt: "2026-10-21T10:00:00Z",
  documentDate: "2026-10-21T10:00:00Z",
  originalFileName: `${title}.pdf`,
  originalMime: "application/pdf",
  originalSize: 10,
  hasArchive: false,
});

let docs: DocumentSummary[];
let listCalls: number;
/** The search parameters of each search request. */
let searches: URLSearchParams[];
/** The answer to a search, by its cursor ("" for the first page). */
let searchPages: Record<string, { status?: number; body: unknown }>;

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

beforeEach(() => {
  docs = [];
  listCalls = 0;
  searches = [];
  searchPages = {};
  stubXHR();
  vi.stubGlobal("fetch", async (url: string) => {
    if (url === "/api/documents?limit=10") {
      listCalls++;
      return json(200, { documents: docs, maxUploadSize: 1000 });
    }
    if (url.startsWith("/api/documents?q=")) {
      const p = new URLSearchParams(url.slice(url.indexOf("?") + 1));
      searches.push(p);
      const page = searchPages[p.get("cursor") ?? ""];
      return page ? json(page.status ?? 200, page.body) : new Response(null, { status: 404 });
    }
    return new Response(null, { status: 404 });
  });
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const file = (name: string, size = 10) => new File([new Uint8Array(size)], name, { type: "application/pdf" });

function dragEvent(type: string, files: File[]) {
  const e = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(e, "dataTransfer", { value: { types: ["Files"], files, dropEffect: "none" } });
  return e;
}

async function renderHome() {
  const queue = new UploadQueue();
  render(<DocumentsHome queue={queue} />);
  // The list (and with it the size limit) has loaded.
  await vi.waitFor(() => expect(queue.maxSize).toBe(1000));
  return queue;
}

const rows = () => within(screen.getByRole("list", { name: t.uploadsLabel })).getAllByRole("listitem");

describe("DocumentsHome", () => {
  it("uploads picked files two at a time, with progress and results", async () => {
    docs = [doc("0", "Old‮ letter")];
    await renderHome();
    // The recent list strips bidi controls.
    expect(await screen.findByText("Old letter")).toBeTruthy();

    fireEvent.change(screen.getByTestId("file-picker"), { target: { files: [file("a.pdf"), file("b.pdf"), file("c.pdf")] } });
    expect(rows()).toHaveLength(3);
    expect(FakeXHR.all).toHaveLength(2);
    expect(within(rows()[2]!).getByRole("status").textContent).toBe(t.uploadWaiting);

    act(() => FakeXHR.all[0]!.progress(3, 10));
    expect(within(rows()[0]!).getByRole("status").textContent).toBe("30 %");
    expect((within(rows()[0]!).getByRole("progressbar") as HTMLProgressElement).value).toBe(30);

    docs = [doc("1", "a"), ...docs];
    await act(async () => FakeXHR.all[0]!.respond(201, doc("1", "a")));
    expect(within(rows()[0]!).getByRole("status").textContent).toBe(t.uploadImported);
    // The third file starts once a slot is free; the list reloads.
    expect(FakeXHR.all).toHaveLength(3);
    await vi.waitFor(() => expect(listCalls).toBe(2));
    expect(await screen.findByText("a")).toBeTruthy();

    await act(async () => FakeXHR.all[1]!.respond(200, { duplicate: true, existing: { id: "0", title: "Old letter" } }));
    expect(within(rows()[1]!).getByRole("status").textContent).toBe(t.uploadDuplicateOf("Old letter"));
    await act(async () => FakeXHR.all[2]!.respond(422, { error: "the file cannot be read", reason: "unreadable" }));
    expect(within(rows()[2]!).getByRole("status").textContent).toBe(t.uploadUnreadable);

    fireEvent.click(screen.getByRole("button", { name: t.clearFinished }));
    expect(screen.queryByRole("list", { name: t.uploadsLabel })).toBeNull();
  });

  it("refuses a file over the limit before sending it", async () => {
    await renderHome();
    fireEvent.change(screen.getByTestId("file-picker"), { target: { files: [file("big.pdf", 1001)] } });
    expect(FakeXHR.all).toHaveLength(0);
    expect(within(rows()[0]!).getByRole("status").textContent).toBe(t.uploadTooLarge("1,000 B"));
  });

  it("cancels an upload", async () => {
    await renderHome();
    fireEvent.change(screen.getByTestId("file-picker"), { target: { files: [file("a.pdf")] } });
    fireEvent.click(screen.getByRole("button", { name: t.cancelUpload("a.pdf") }));
    await vi.waitFor(() => expect(within(rows()[0]!).getByRole("status").textContent).toBe(t.uploadCancelled));
    expect(FakeXHR.all[0]!.aborted).toBe(true);
    expect(screen.queryByRole("button", { name: t.cancelUpload("a.pdf") })).toBeNull();
  });
});

const field = () => screen.getByRole("searchbox", { name: t.searchLabel });

function searchFor(q: string) {
  fireEvent.change(field(), { target: { value: q } });
  fireEvent.submit(screen.getByRole("search"));
}

const results = () => within(screen.getByRole("list", { name: t.searchResults })).getAllByRole("listitem");

describe("DocumentsHome search", () => {
  it("searches on Enter and shows snippets as text with marks", async () => {
    docs = [doc("0", "Recent")];
    searchPages[""] = {
      body: {
        documents: [{ ...doc("1", "Telekom"), snippet: `<script>alert(1)</script> Ihre ${SNIPPET_START}Rechnung${SNIPPET_STOP} <b>fett</b>` }],
        maxUploadSize: 1000,
      },
    };
    await renderHome();
    fireEvent.change(field(), { target: { value: "Rechnung" } });
    // Typing does not search.
    expect(searches).toHaveLength(0);
    fireEvent.submit(screen.getByRole("search"));
    expect(await screen.findByText("Telekom")).toBeTruthy();
    expect(searches).toHaveLength(1);
    expect(searches[0]!.get("q")).toBe("Rechnung");
    expect(searches[0]!.get("limit")).toBe(String(searchPageSize));
    expect(searches[0]!.get("tz")).toBe(Intl.DateTimeFormat().resolvedOptions().timeZone);
    expect(searches[0]!.has("cursor")).toBe(false);
    // The results replace the recent list.
    expect(screen.queryByRole("list", { name: t.recentTitle })).toBeNull();

    const row = results()[0]!;
    const marks = row.querySelectorAll("mark");
    expect(marks).toHaveLength(1);
    expect(marks[0]!.textContent).toBe("Rechnung");
    // Markup in the text stays text.
    expect(row.querySelector("script")).toBeNull();
    expect(row.querySelector("b")).toBeNull();
    expect(row.textContent).toContain("<script>alert(1)</script> Ihre Rechnung <b>fett</b>");
    expect(row.textContent).not.toContain(SNIPPET_START);
  });

  it("loads more with the cursor, then returns to the recent list when the field is cleared", async () => {
    docs = [doc("0", "Recent")];
    searchPages[""] = { body: { documents: [doc("1", "First")], nextCursor: "c1", maxUploadSize: 1000 } };
    searchPages["c1"] = { body: { documents: [doc("2", "Second")], maxUploadSize: 1000 } };
    await renderHome();
    searchFor("tag:Steuer");
    expect(await screen.findByText("First")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: t.searchMore }));
    expect(await screen.findByText("Second")).toBeTruthy();
    expect(searches.map((p) => [p.get("q"), p.get("cursor")])).toEqual([
      ["tag:Steuer", null],
      ["tag:Steuer", "c1"],
    ]);
    expect(results()).toHaveLength(2);
    // The last page has no cursor: no button.
    expect(screen.queryByRole("button", { name: t.searchMore })).toBeNull();

    fireEvent.change(field(), { target: { value: "" } });
    expect(screen.queryByRole("list", { name: t.searchResults })).toBeNull();
    expect(within(screen.getByRole("list", { name: t.recentTitle })).getByText("Recent")).toBeTruthy();
    expect(searches).toHaveLength(2);
  });

  it("shows an empty state and the error of a refused search", async () => {
    searchPages[""] = { body: { documents: [], maxUploadSize: 1000 } };
    await renderHome();
    searchFor("nichts");
    expect(await screen.findByText(t.searchEmpty)).toBeTruthy();

    searchPages[""] = { status: 400, body: { error: "query too long" } };
    searchFor("a ".repeat(40));
    expect((await screen.findByRole("alert")).textContent).toBe(t.searchTooLong);
    expect(screen.queryByText(t.searchEmpty)).toBeNull();
  });

  it("does not search for blanks", async () => {
    await renderHome();
    searchFor("   ");
    expect(searches).toHaveLength(0);
    expect(screen.getByRole("heading", { name: t.recentTitle })).toBeTruthy();
  });
});

describe("WindowDrop", () => {
  it("takes files dropped anywhere and shows where they go while dragging", () => {
    const onFiles = vi.fn();
    render(<WindowDrop onFiles={onFiles} />);
    const files = [file("a.pdf")];
    act(() => {
      window.dispatchEvent(dragEvent("dragenter", files));
    });
    expect(screen.getByText(t.dropHere)).toBeTruthy();
    const drop = dragEvent("drop", files);
    act(() => {
      window.dispatchEvent(drop);
    });
    expect(drop.defaultPrevented).toBe(true);
    expect(onFiles).toHaveBeenCalledWith(files);
    expect(screen.queryByText(t.dropHere)).toBeNull();
  });

  it("ignores drags without files", () => {
    const onFiles = vi.fn();
    render(<WindowDrop onFiles={onFiles} />);
    const e = new Event("dragenter", { bubbles: true, cancelable: true });
    Object.defineProperty(e, "dataTransfer", { value: { types: ["text/plain"], files: [] } });
    act(() => {
      window.dispatchEvent(e);
    });
    expect(screen.queryByText(t.dropHere)).toBeNull();
  });
});
