import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { DocumentDetail, NamedRow, Tag } from "./api";
import { App } from "./App";
import { stubXHR } from "./fakeXHR";
import { t } from "./i18n";

// The document detail against a fake server and a fake PDF viewer: what
// it asks the server, what it sends, and where it goes.

const pdf = vi.hoisted(() => ({ loads: [] as string[], renders: [] as number[], numPages: 3 }));
vi.mock("./pdfViewer", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./pdfViewer")>()),
  loadPdf: async (url: string) => {
    pdf.loads.push(url);
    return {
      numPages: pdf.numPages,
      pageSize: async () => ({ width: 600, height: 800 }),
      renderPage: (n: number) => {
        pdf.renders.push(n);
        return { promise: Promise.resolve({ width: 600, height: 800 }), cancel: () => {} };
      },
      destroy: () => {},
    };
  },
}));

/** A stand-in for IntersectionObserver: the test says which pages come into view. */
class FakeIO {
  static all: FakeIO[] = [];
  els: Element[] = [];
  constructor(private cb: (entries: { target: Element; isIntersecting: boolean }[]) => void) {
    FakeIO.all.push(this);
  }
  observe(el: Element) {
    this.els.push(el);
  }
  unobserve(el: Element) {
    this.els = this.els.filter((e) => e !== el);
  }
  disconnect() {
    this.els = [];
  }
  /** Pages n… come into view. */
  show(...pages: number[]) {
    this.cb(this.els.filter((e) => pages.includes(Number((e as HTMLElement).dataset.page))).map((target) => ({ target, isIntersecting: true })));
  }
}
const viewerIO = () => FakeIO.all.filter((io) => io.els.length > 0).at(-1)!;

const doc = (id: string, title: string, more: Partial<DocumentDetail> = {}): DocumentDetail => ({
  id,
  title,
  ownerId: 1,
  createdAt: "2026-10-21T10:00:00Z",
  modifiedAt: "2026-10-21T10:00:00Z",
  documentDate: "2026-03-15T09:30:00Z",
  originalFileName: `${id}.pdf`,
  originalMime: "application/pdf",
  originalSize: 2048,
  hasArchive: true,
  archiveSize: 4096,
  pageCount: 3,
  processingNote: "",
  notes: "",
  isInbox: true,
  isFavorite: false,
  archiveSerialNumber: null,
  correspondentId: null,
  documentTypeId: null,
  tagIds: [],
  processing: { state: "done", attempts: 1, maxAttempts: 3 },
  thumbnailUrl: null,
  ...more,
});
const tag = (id: string, name: string, more: Partial<Tag> = {}): Tag => ({ id, name, parentId: null, ownerId: 1, color: "", documentCount: 1, subtreeCount: 1, ...more });
const named = (id: string, name: string, ownerId = 1): NamedRow => ({ id, name, ownerId, documentCount: 1 });

interface Sent {
  method: string;
  url: string;
  body: unknown;
}

let docs: Map<string, DocumentDetail>;
let order: string[];
let user: { name: string; admin: boolean };
let tags: Tag[];
let correspondents: NamedRow[];
let requests: Sent[];
/** The answer to the next PATCH instead of a success. */
let patchAnswer: { status: number; body: unknown } | null;

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

beforeEach(() => {
  window.history.replaceState(null, "", "/");
  localStorage.clear();
  stubXHR();
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener: () => {}, removeEventListener: () => {} }));
  FakeIO.all = [];
  vi.stubGlobal("IntersectionObserver", FakeIO);
  pdf.loads = [];
  pdf.renders = [];
  pdf.numPages = 3;
  user = { name: "alice", admin: false };
  docs = new Map();
  order = [];
  tags = [];
  correspondents = [];
  requests = [];
  patchAnswer = null;
  vi.stubGlobal("fetch", async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    const body: unknown = typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
    requests.push({ method, url, body });
    const path = url.split("?")[0]!;
    const p = new URLSearchParams(url.includes("?") ? url.slice(url.indexOf("?") + 1) : "");
    if (path === "/api/session") return json(200, { user: { mustChangePassword: false, twoFactorEnabled: false, twoFactorRequired: false, ...user } });
    if (path === "/healthz") return new Response("ok", { status: 200 });
    if (path === "/api/documents/counts") return json(200, { inbox: 0, favorites: 0, total: docs.size });
    if (path === "/api/tags" && method === "GET") return json(200, { tags });
    if (path === "/api/correspondents" && method === "GET") return json(200, { correspondents });
    if (path === "/api/document-types" && method === "GET") return json(200, { documentTypes: [] });
    if (path === "/api/tags" && method === "POST") {
      const b = body as { name: string; ownerId?: number };
      const row = tag(`new-${b.name}`, b.name, { ownerId: b.ownerId ?? 1 });
      tags = [...tags, row];
      return json(201, row);
    }
    if (path === "/api/documents" && method === "GET") {
      const list = order.map((id) => docs.get(id)!).filter((d) => p.get("inbox") !== "true" || d.isInbox);
      return json(200, { documents: list, maxUploadSize: 1000 });
    }
    const m = /^\/api\/documents\/([^/]+)$/.exec(path);
    if (m) {
      const d = docs.get(decodeURIComponent(m[1]!));
      if (!d) return json(404, { error: "not found" });
      if (method === "PATCH") {
        if (patchAnswer) return json(patchAnswer.status, patchAnswer.body);
        const next = { ...d, ...(body as Partial<DocumentDetail>) };
        docs.set(d.id, next);
        return json(200, next);
      }
      return json(200, d);
    }
    return json(404, { error: "not found" });
  });
  vi.stubGlobal("confirm", vi.fn(() => false));
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function serve(...ds: DocumentDetail[]) {
  for (const d of ds) docs.set(d.id, d);
  order = ds.map((d) => d.id);
}

const detail = () => within(screen.getByTestId("document-detail"));
const patches = () => requests.filter((r) => r.method === "PATCH");
const docParam = () => new URLSearchParams(window.location.search).get("doc");

/** Starts the app at url and waits for the open document's form. */
async function openAt(url: string) {
  window.history.replaceState(null, "", url);
  render(<App />);
  await screen.findByTestId("document-detail");
  await waitFor(() => expect(detail().getByLabelText(t.fieldTitle)).toBeTruthy());
}

const titleField = () => detail().getByLabelText(t.fieldTitle) as HTMLInputElement;

describe("opening", () => {
  it("opens from the list's title through ?doc=, and Back closes it", async () => {
    serve(doc("d1", "Rechnung"), doc("d2", "Brief"));
    render(<App />);
    fireEvent.click(await screen.findByRole("link", { name: "Brief" }));
    expect(docParam()).toBe("d2");
    await waitFor(() => expect(titleField().value).toBe("Brief"));
    expect(detail().getByRole("heading", { level: 2 }).textContent).toBe("Brief");
    act(() => window.history.back());
    await waitFor(() => expect(screen.queryByTestId("document-detail")).toBeNull());
    expect(docParam()).toBeNull();
  });

  it("opens directly from the URL (a reload)", async () => {
    serve(doc("d1", "Rechnung", { notes: "Bezahlt" }));
    await openAt("/?doc=d1");
    expect((detail().getByLabelText(t.fieldNotes) as HTMLTextAreaElement).value).toBe("Bezahlt");
    expect(requests.some((r) => r.url === "/api/documents/d1")).toBe(true);
  });

  it("says when the document does not exist", async () => {
    window.history.replaceState(null, "", "/?doc=gone");
    render(<App />);
    await waitFor(() => expect(screen.getByTestId("document-detail").textContent).toContain(t.documentNotFound));
  });

  it("shows titles and notes as text, never as HTML", async () => {
    serve(doc("d1", "<img src=x onerror=alert(1)>", { notes: "<b>bold</b>", processingNote: "<i>note</i>" }));
    await openAt("/?doc=d1");
    const d = screen.getByTestId("document-detail");
    expect(d.querySelector("b, i, img[src=x]")).toBeNull();
    expect(detail().getByRole("heading", { level: 2 }).textContent).toBe("<img src=x onerror=alert(1)>");
    expect(d.textContent).toContain("<i>note</i>");
  });
});

describe("viewer", () => {
  it("shows the archive PDF when there is one, else the original PDF", async () => {
    serve(doc("d1", "A", { hasArchive: true }), doc("d2", "B", { hasArchive: false }));
    await openAt("/?doc=d1");
    await waitFor(() => expect(pdf.loads).toContain("/api/documents/d1/file?kind=archive"));
    fireEvent.click(detail().getByRole("button", { name: t.nextDocument }));
    await waitFor(() => expect(pdf.loads).toContain("/api/documents/d2/file?kind=original"));
    expect(pdf.loads.every((u) => !u.includes("d2/file?kind=archive"))).toBe(true);
  });

  it("draws only the pages in view", async () => {
    pdf.numPages = 5;
    serve(doc("d1", "A"));
    await openAt("/?doc=d1");
    await waitFor(() => expect(screen.getAllByRole("group", { name: /^Page|^Seite/ })).toHaveLength(5));
    expect(pdf.renders).toEqual([]);
    act(() => viewerIO().show(2, 3));
    await waitFor(() => expect([...new Set(pdf.renders)].sort()).toEqual([2, 3]));
    expect(detail().getByText(t.pageOf("1 / 5"))).toBeTruthy();
  });

  it("shows a JPEG not processed yet as an image, and an unprocessed HEIC as a note with the download", async () => {
    serve(
      doc("j", "Foto", { hasArchive: false, originalMime: "image/jpeg", processing: { state: "queued", attempts: 0, maxAttempts: 3 } }),
      doc("h", "Handy", { hasArchive: false, originalMime: "image/heic", processing: { state: "running", attempts: 1, maxAttempts: 3 } }),
    );
    await openAt("/?doc=j");
    const img = (await detail().findByRole("img", { name: t.previewOf("Foto") })) as HTMLImageElement;
    expect(img.getAttribute("src")).toBe("/api/documents/j/file?kind=original");
    fireEvent.click(detail().getByRole("button", { name: t.nextDocument }));
    await waitFor(() => expect(detail().getByText(t.previewHeicProcessing)).toBeTruthy());
    expect(detail().getAllByRole("link", { name: t.downloadOriginal })[0]!.getAttribute("href")).toBe("/api/documents/h/file?kind=original");
    expect(pdf.loads).toEqual([]);
  });
});

describe("editing", () => {
  it("sends only the changed fields, and keeps the time of day of a changed date", async () => {
    serve(doc("d1", "Rechnung", { archiveSerialNumber: 7, notes: "alt" }));
    await openAt("/?doc=d1");
    const save = detail().getByRole("button", { name: t.save }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    fireEvent.change(titleField(), { target: { value: "Rechnung März" } });
    fireEvent.change(detail().getByLabelText(t.fieldASN), { target: { value: "" } });
    fireEvent.change(detail().getByLabelText(t.fieldDate), { target: { value: "2026-04-02" } });
    expect(save.disabled).toBe(false);
    fireEvent.click(save);
    await waitFor(() => expect(patches()).toHaveLength(1));
    const sent = patches()[0]!;
    expect(sent.url).toBe("/api/documents/d1");
    const old = new Date("2026-03-15T09:30:00Z");
    const want = new Date(2026, 3, 2, old.getHours(), old.getMinutes(), old.getSeconds()).toISOString();
    expect(sent.body).toEqual({ title: "Rechnung März", archiveSerialNumber: null, documentDate: want });
    expect(await detail().findByText(t.saved)).toBeTruthy();
    expect(save.disabled).toBe(true);
  });

  it("shows server errors at their field, and names the document with a taken ASN", async () => {
    serve(doc("d1", "A"), doc("d9", "Other"));
    await openAt("/?doc=d1");
    patchAnswer = { status: 409, body: { error: "taken", code: "asn_taken", documentId: "d9" } };
    fireEvent.change(detail().getByLabelText(t.fieldASN), { target: { value: "12" } });
    fireEvent.click(detail().getByRole("button", { name: t.save }));
    const link = await detail().findByRole("link", { name: t.openThatDocument });
    expect(detail().getByLabelText(t.fieldASN).getAttribute("aria-invalid")).toBe("true");
    expect(link.getAttribute("href")).toBe("/?doc=d9");

    patchAnswer = { status: 400, body: { error: "bad", code: "invalid_title" } };
    fireEvent.change(titleField(), { target: { value: "x" } });
    fireEvent.change(detail().getByLabelText(t.fieldASN), { target: { value: "" } });
    fireEvent.click(detail().getByRole("button", { name: t.save }));
    expect((await detail().findByText(t.invalidTitle)).id).toBe(titleField().getAttribute("aria-describedby"));

    // The link opens the other document; the changes are discarded after asking.
    vi.mocked(window.confirm).mockReturnValue(true);
    patchAnswer = { status: 409, body: { error: "taken", code: "asn_taken", documentId: "d9" } };
    fireEvent.change(detail().getByLabelText(t.fieldASN), { target: { value: "12" } });
    fireEvent.click(detail().getByRole("button", { name: t.save }));
    fireEvent.click(await detail().findByRole("link", { name: t.openThatDocument }));
    expect(docParam()).toBe("d9");
    await waitFor(() => expect(titleField().value).toBe("Other"));
  });

  it("refuses an ASN that is no positive number without asking the server", async () => {
    serve(doc("d1", "A"));
    await openAt("/?doc=d1");
    fireEvent.change(detail().getByLabelText(t.fieldASN), { target: { value: "-3" } });
    fireEvent.click(detail().getByRole("button", { name: t.save }));
    expect(await detail().findByText(t.invalidASN)).toBeTruthy();
    expect(patches()).toHaveLength(0);
  });

  it("creates a tag inline, selects it, and saves it with the document", async () => {
    tags = [tag("t1", "Steuer")];
    serve(doc("d1", "A", { tagIds: ["t1"] }));
    await openAt("/?doc=d1");
    const box = detail().getByRole("combobox", { name: t.tags });
    await waitFor(() => expect((box as HTMLInputElement).disabled).toBe(false));
    fireEvent.focus(box);
    fireEvent.change(box, { target: { value: "Arzt" } });
    fireEvent.click(detail().getByRole("option", { name: t.createNamed("Arzt") }));
    expect(await detail().findByRole("button", { name: t.removeChoice("Arzt") })).toBeTruthy();
    const post = requests.find((r) => r.method === "POST" && r.url === "/api/tags")!;
    // A user's own row: no ownerId.
    expect(post.body).toEqual({ name: "Arzt" });
    fireEvent.click(detail().getByRole("button", { name: t.save }));
    await waitFor(() => expect(patches()).toHaveLength(1));
    expect(patches()[0]!.body).toEqual({ tagIds: ["t1", "new-Arzt"] });
  });

  it("chooses an existing correspondent with the keyboard", async () => {
    correspondents = [named("c1", "Finanzamt"), named("c2", "Arzt")];
    serve(doc("d1", "A"));
    await openAt("/?doc=d1");
    const box = detail().getByRole("combobox", { name: t.fieldCorrespondent });
    await waitFor(() => expect((box as HTMLInputElement).disabled).toBe(false));
    fireEvent.focus(box);
    fireEvent.change(box, { target: { value: "fin" } });
    fireEvent.keyDown(box, { key: "Enter" });
    expect(detail().getByRole("button", { name: t.removeChoice("Finanzamt") })).toBeTruthy();
    // Escape in the picker closes its list, not the document.
    fireEvent.change(box, { target: { value: "x" } });
    fireEvent.keyDown(box, { key: "Escape" });
    expect(screen.getByTestId("document-detail")).toBeTruthy();
    fireEvent.click(detail().getByRole("button", { name: t.save }));
    await waitFor(() => expect(patches()[0]?.body).toEqual({ correspondentId: "c1" }));
  });

  it("loads the owner's metadata for an admin and creates rows for the owner", async () => {
    user = { name: "root", admin: true };
    tags = [tag("bt", "Bobs", { ownerId: 2 })];
    serve(doc("d1", "Bobs Brief", { ownerId: 2, ownerName: "bob" }));
    await openAt("/?doc=d1");
    await waitFor(() => expect(requests.some((r) => r.method === "GET" && r.url === "/api/tags?owner=2")).toBe(true));
    expect(requests.some((r) => r.url === "/api/correspondents?owner=2")).toBe(true);
    expect(detail().getByText(t.ownedBy("bob"))).toBeTruthy();
    const box = detail().getByRole("combobox", { name: t.tags });
    await waitFor(() => expect((box as HTMLInputElement).disabled).toBe(false));
    fireEvent.focus(box);
    fireEvent.change(box, { target: { value: "Neu" } });
    fireEvent.click(detail().getByRole("option", { name: t.createNamed("Neu") }));
    await detail().findByRole("button", { name: t.removeChoice("Neu") });
    expect(requests.find((r) => r.method === "POST" && r.url === "/api/tags")!.body).toEqual({ name: "Neu", ownerId: 2 });
  });
});

describe("Done", () => {
  it("in the inbox: saves, takes the document out of the inbox and opens the next one", async () => {
    serve(doc("d1", "A"), doc("d2", "B"), doc("d3", "C"));
    window.history.replaceState(null, "", "/?inbox=1");
    render(<App />);
    fireEvent.click(await screen.findByRole("link", { name: "A" }));
    await waitFor(() => expect(titleField().value).toBe("A"));
    fireEvent.change(titleField(), { target: { value: "A2" } });
    fireEvent.click(detail().getByRole("button", { name: t.done }));
    await waitFor(() => expect(docParam()).toBe("d2"));
    expect(patches()[0]!.body).toEqual({ title: "A2", isInbox: false });
    await waitFor(() => expect(titleField().value).toBe("B"));
    // The inbox list no longer has it; no question about unsaved changes.
    expect(screen.queryByRole("link", { name: "A2" })).toBeNull();
    expect(window.confirm).not.toHaveBeenCalled();
  });

  it("outside the inbox: saves without the inbox and closes", async () => {
    serve(doc("d1", "A"), doc("d2", "B"));
    render(<App />);
    fireEvent.click(await screen.findByRole("link", { name: "A" }));
    await waitFor(() => expect(titleField().value).toBe("A"));
    fireEvent.click(detail().getByRole("button", { name: t.done }));
    await waitFor(() => expect(screen.queryByTestId("document-detail")).toBeNull());
    expect(patches()[0]!.body).toEqual({ isInbox: false });
  });
});

describe("navigation", () => {
  it("goes to the previous and next document with the arrow keys and a swipe, not while typing, and Escape closes", async () => {
    serve(doc("d1", "A"), doc("d2", "B"), doc("d3", "C"));
    render(<App />);
    fireEvent.click(await screen.findByRole("link", { name: "B" }));
    await waitFor(() => expect(titleField().value).toBe("B"));
    fireEvent.keyDown(titleField(), { key: "ArrowRight" });
    expect(docParam()).toBe("d2");
    fireEvent.keyDown(document.body, { key: "ArrowRight" });
    expect(docParam()).toBe("d3");
    await waitFor(() => expect(titleField().value).toBe("C"));
    expect((detail().getByRole("button", { name: t.nextDocument }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.keyDown(document.body, { key: "ArrowLeft" });
    expect(docParam()).toBe("d2");
    // A swipe to the left: the next one.
    const section = screen.getByTestId("document-detail");
    fireEvent.touchStart(section, { touches: [{ clientX: 300, clientY: 400 }] });
    fireEvent.touchEnd(section, { changedTouches: [{ clientX: 100, clientY: 410 }] });
    expect(docParam()).toBe("d3");
    fireEvent.keyDown(document.body, { key: "Escape" });
    expect(docParam()).toBeNull();
    await waitFor(() => expect(screen.queryByTestId("document-detail")).toBeNull());
  });

  it("asks before leaving unsaved changes: buttons, Back, and the page itself", async () => {
    serve(doc("d1", "A"), doc("d2", "B"));
    render(<App />);
    fireEvent.click(await screen.findByRole("link", { name: "A" }));
    await waitFor(() => expect(titleField().value).toBe("A"));
    fireEvent.change(titleField(), { target: { value: "changed" } });

    // Next, refused: stays.
    fireEvent.click(detail().getByRole("button", { name: t.nextDocument }));
    expect(window.confirm).toHaveBeenCalledWith(t.unsavedChanges);
    expect(docParam()).toBe("d1");
    expect(titleField().value).toBe("changed");

    // Leaving the page: the browser asks.
    const unload = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(unload);
    expect(unload.defaultPrevented).toBe(true);

    // Back, refused: the document's URL comes back.
    act(() => window.history.back());
    await waitFor(() => expect(vi.mocked(window.confirm).mock.calls.length).toBe(2));
    await waitFor(() => expect(docParam()).toBe("d1"));
    expect(titleField().value).toBe("changed");

    // Close, accepted: gone without saving.
    vi.mocked(window.confirm).mockReturnValue(true);
    fireEvent.click(detail().getByRole("button", { name: t.closeDocument }));
    expect(docParam()).toBeNull();
    await waitFor(() => expect(screen.queryByTestId("document-detail")).toBeNull());
    expect(patches()).toHaveLength(0);
    // Nothing unsaved any more: the page leaves without a question.
    const later = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(later);
    expect(later.defaultPrevented).toBe(false);
  });

  it("does not ask without changes", async () => {
    serve(doc("d1", "A"), doc("d2", "B"));
    render(<App />);
    fireEvent.click(await screen.findByRole("link", { name: "A" }));
    await waitFor(() => expect(titleField().value).toBe("A"));
    fireEvent.click(detail().getByRole("button", { name: t.nextDocument }));
    expect(docParam()).toBe("d2");
    expect(window.confirm).not.toHaveBeenCalled();
  });
});
