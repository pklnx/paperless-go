/// <reference types="node" />
import { readFileSync } from "node:fs";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { documentsApi, type DocumentDetail, type NamedRow, type Tag } from "./api";
import { App } from "./App";
import { stubXHR } from "./fakeXHR";
import { t } from "./i18n";
import { dialogOpen } from "./dialogs";
import { trashDay } from "./Trash";

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
let user: { id: number; name: string; admin: boolean };
let tags: Tag[];
let correspondents: NamedRow[];
let requests: Sent[];
/** The answer to the next PATCH instead of a success. */
let patchAnswer: { status: number; body: unknown } | null;
/** While set, a PATCH waits for it before it answers (a slow server). */
let patchHold: Promise<void> | null;
/** While set, a DELETE or purge waits for it before it answers. */
let trashHold: Promise<void> | null;
/** While set, a POST that creates a tag, correspondent or type waits for it before it answers. */
let createHold: Promise<void> | null;
/** Each saved change gets a new modifiedAt with microseconds, as the server's. */
let stamp: number;

/** Holds back the PATCH answers until the returned function is called. */
function holdPatches(): () => void {
  let release: () => void = () => {};
  patchHold = new Promise<void>((r) => (release = r));
  return () => {
    patchHold = null;
    release();
  };
}

/** The promises of the detail's PATCH calls, to wait until one has been handled. */
function watchPatches(): Promise<unknown>[] {
  const calls: Promise<unknown>[] = [];
  const real = documentsApi.patch;
  vi.spyOn(documentsApi, "patch").mockImplementation((id, body) => {
    const p = real(id, body);
    calls.push(p.catch(() => {}));
    return p;
  });
  return calls;
}

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
  user = { id: 1, name: "alice", admin: false };
  docs = new Map();
  order = [];
  tags = [];
  correspondents = [];
  requests = [];
  patchAnswer = null;
  patchHold = null;
  trashHold = null;
  createHold = null;
  stamp = 0;
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
      if (createHold) await createHold;
      const b = body as { name: string; ownerId?: number };
      const row = tag(`new-${b.name}`, b.name, { ownerId: b.ownerId ?? 1 });
      tags = [...tags, row];
      return json(201, row);
    }
    if ((path === "/api/correspondents" || path === "/api/document-types") && method === "POST") {
      if (createHold) await createHold;
      const b = body as { name: string; ownerId?: number };
      const row = named(`new-${b.name}`, b.name, b.ownerId ?? 1);
      if (path === "/api/correspondents") correspondents = [...correspondents, row];
      return json(201, row);
    }
    if (path === "/api/documents" && method === "GET") {
      const list = order
        .map((id) => docs.get(id))
        .filter((d): d is DocumentDetail => d !== undefined && (p.get("trashed") === "true") === Boolean(d.deletedAt))
        .filter((d) => p.get("inbox") !== "true" || d.isInbox);
      return json(200, { documents: list, maxUploadSize: 1000 });
    }
    // The trash (#12): restore and purge of a trashed document.
    const tr = /^\/api\/documents\/([^/]+)\/(restore|purge)$/.exec(path);
    if (tr && method === "POST") {
      const d = docs.get(decodeURIComponent(tr[1]!));
      if (!d) return json(404, { error: "not found" });
      if (!d.deletedAt) return json(409, { error: "not in the trash", code: "not_in_trash" });
      if (tr[2] === "purge") {
        if (trashHold) await trashHold;
        docs.delete(d.id);
        return new Response(null, { status: 204 });
      }
      const back = { ...d, deletedAt: null, purgeAt: null };
      docs.set(d.id, back);
      return json(200, back);
    }
    const m = /^\/api\/documents\/([^/]+)$/.exec(path);
    if (m) {
      const d = docs.get(decodeURIComponent(m[1]!));
      if (!d) return json(404, { error: "not found" });
      if (method === "DELETE") {
        if (trashHold) await trashHold;
        if (d.deletedAt) return json(409, { error: "in the trash", code: "in_trash" });
        docs.set(d.id, { ...d, deletedAt: "2026-10-22T08:00:00Z", purgeAt: "2026-11-21T08:00:00Z" });
        return new Response(null, { status: 204 });
      }
      if (method === "PATCH") {
        if (patchHold) await patchHold;
        if (patchAnswer) return json(patchAnswer.status, patchAnswer.body);
        const { expectedModifiedAt, ...fields } = body as Partial<DocumentDetail> & { expectedModifiedAt?: string };
        const now = docs.get(d.id)!;
        // The server's precondition: the string compared as sent.
        if (expectedModifiedAt !== undefined && expectedModifiedAt !== now.modifiedAt) {
          return json(409, { error: "changed", code: "document_changed", modifiedAt: now.modifiedAt });
        }
        stamp++;
        const next = { ...now, ...fields, modifiedAt: `2026-10-22T08:00:${String(stamp).padStart(2, "0")}.123456Z` };
        docs.set(d.id, next);
        return json(200, next);
      }
      return json(200, d);
    }
    return json(404, { error: "not found" });
  });
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

/** layout.css, whose rules jsdom does not apply: tests pin the ones a layout depends on (Vitest runs in web/). */
const layoutCSS = () => readFileSync("src/layout.css", "utf8");
/** The declarations of the first rule whose selector list is exactly selector. */
function rule(css: string, selector: string): string {
  const m = new RegExp(`(?:^|\\n)${selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\s*\\{([^}]*)\\}`).exec(css);
  if (!m) throw new Error(`no rule for ${selector} in layout.css`);
  return m[1]!;
}
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
/** The "Discard changes?" question (a Carbon danger modal), if open. */
const discardQuestion = () => screen.queryByRole("dialog", { name: t.discardChangesTitle });
const answerQuestion = async (button: string) => {
  const ask = await screen.findByRole("dialog", { name: t.discardChangesTitle });
  fireEvent.click(within(ask).getByRole("button", { name: button }));
};
/** Answers the open question with "Discard changes". */
const discard = () => answerQuestion(t.discardChanges);
/** Answers the open question with "Keep editing". */
const keepEditing = () => answerQuestion(t.keepEditing);

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
    expect(detail().getByTestId("notes-rendered").textContent).toBe("Bezahlt");
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
    // The pages render before the viewer's effect observes them: wait for the observer too.
    await waitFor(() => expect(viewerIO()?.els).toHaveLength(5));
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
    expect(sent.body).toEqual({ title: "Rechnung März", archiveSerialNumber: null, documentDate: want, expectedModifiedAt: "2026-10-21T10:00:00Z" });
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
    patchAnswer = { status: 409, body: { error: "taken", code: "asn_taken", documentId: "d9" } };
    fireEvent.change(detail().getByLabelText(t.fieldASN), { target: { value: "12" } });
    fireEvent.click(detail().getByRole("button", { name: t.save }));
    fireEvent.click(await detail().findByRole("link", { name: t.openThatDocument }));
    expect(docParam()).toBe("d1");
    await discard();
    await waitFor(() => expect(docParam()).toBe("d9"));
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
    expect(patches()[0]!.body).toEqual({ tagIds: ["t1", "new-Arzt"], expectedModifiedAt: "2026-10-21T10:00:00Z" });
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
    // The ComboBox shows the choice in its field, with a clear button.
    expect((box as HTMLInputElement).value).toBe("Finanzamt");
    expect(detail().getByRole("button", { name: t.carbon.ids["clear.selection"] })).toBeTruthy();
    // Escape in the picker belongs to the picker (its list, its text), never closes the document:
    // with the list closed (after Enter)...
    fireEvent.keyDown(box, { key: "Escape" });
    expect(screen.getByTestId("document-detail")).toBeTruthy();
    expect(docParam()).toBe("d1");
    // ...and with it open (typed text: Carbon closes the list; the document stays).
    fireEvent.change(box, { target: { value: "x" } });
    expect(detail().getByRole("listbox", { name: /Correspondent|Korrespondent/ })).toBeTruthy();
    fireEvent.keyDown(box, { key: "Escape" });
    expect(screen.getByTestId("document-detail")).toBeTruthy();
    expect(docParam()).toBe("d1");
    await waitFor(() => expect(box.closest(".cds--list-box")?.classList.contains("cds--list-box--expanded")).toBe(false));
    fireEvent.click(detail().getByRole("button", { name: t.save }));
    await waitFor(() => expect(patches()[0]?.body).toEqual({ correspondentId: "c1", expectedModifiedAt: "2026-10-21T10:00:00Z" }));
  });

  it("loads the owner's metadata for an admin and creates rows for the owner", async () => {
    user = { id: 9, name: "root", admin: true };
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
    expect(patches()[0]!.body).toEqual({ title: "A2", isInbox: false, expectedModifiedAt: "2026-10-21T10:00:00Z" });
    await waitFor(() => expect(titleField().value).toBe("B"));
    // The inbox list no longer has it; no question about unsaved changes.
    expect(screen.queryByRole("link", { name: "A2" })).toBeNull();
    expect(discardQuestion()).toBeNull();
  });

  it("outside the inbox: saves without the inbox and closes", async () => {
    serve(doc("d1", "A"), doc("d2", "B"));
    render(<App />);
    fireEvent.click(await screen.findByRole("link", { name: "A" }));
    await waitFor(() => expect(titleField().value).toBe("A"));
    fireEvent.click(detail().getByRole("button", { name: t.done }));
    await waitFor(() => expect(screen.queryByTestId("document-detail")).toBeNull());
    expect(patches()[0]!.body).toEqual({ isInbox: false, expectedModifiedAt: "2026-10-21T10:00:00Z" });
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

    // Next: the question, with the text; "Keep editing" stays.
    fireEvent.click(detail().getByRole("button", { name: t.nextDocument }));
    const ask = await screen.findByRole("dialog", { name: t.discardChangesTitle });
    expect(within(ask).getByText(t.unsavedChanges)).toBeTruthy();
    expect(docParam()).toBe("d1");
    await keepEditing();
    await waitFor(() => expect(discardQuestion()).toBeNull());
    expect(docParam()).toBe("d1");
    expect(titleField().value).toBe("changed");

    // Leaving the page: the browser asks.
    const unload = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(unload);
    expect(unload.defaultPrevented).toBe(true);

    // Back: the document's URL comes back at once, then the question; refused, it stays.
    act(() => window.history.back());
    await screen.findByRole("dialog", { name: t.discardChangesTitle });
    expect(docParam()).toBe("d1");
    await keepEditing();
    await waitFor(() => expect(discardQuestion()).toBeNull());
    expect(docParam()).toBe("d1");
    expect(titleField().value).toBe("changed");

    // Close, accepted: gone without saving.
    fireEvent.click(detail().getByRole("button", { name: t.closeDocument }));
    expect(docParam()).toBe("d1");
    await discard();
    await waitFor(() => expect(docParam()).toBeNull());
    await waitFor(() => expect(screen.queryByTestId("document-detail")).toBeNull());
    expect(discardQuestion()).toBeNull();
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
    expect(discardQuestion()).toBeNull();
  });

  it("Escape means Keep editing: the modal is gone and does not reappear, the document stays open", async () => {
    serve(doc("d1", "A"), doc("d2", "B"));
    await openAt("/?doc=d1");
    fireEvent.change(titleField(), { target: { value: "changed" } });
    fireEvent.click(detail().getByRole("button", { name: t.nextDocument }));
    const ask = await screen.findByRole("dialog", { name: t.discardChangesTitle });
    // The question is a dialog to the detail's key handler: Escape reaches the modal only.
    expect(dialogOpen()).toBe(true);
    fireEvent.keyDown(within(ask).getByRole("button", { name: t.keepEditing }), { key: "Escape" });
    await waitFor(() => expect(discardQuestion()).toBeNull());
    expect(docParam()).toBe("d1");
    expect(titleField().value).toBe("changed");
    expect(screen.getByTestId("document-detail")).toBeTruthy();
    // No second question: a later render leaves it closed, and the next navigation asks anew.
    fireEvent.change(titleField(), { target: { value: "changed again" } });
    expect(discardQuestion()).toBeNull();
    fireEvent.click(detail().getByRole("button", { name: t.nextDocument }));
    await screen.findByRole("dialog", { name: t.discardChangesTitle });
    await discard();
    await waitFor(() => expect(docParam()).toBe("d2"));
    await waitFor(() => expect(titleField().value).toBe("B"));
  });

  it("returns the focus to the detail's heading after a question opened by Back", async () => {
    serve(doc("d1", "A"), doc("d2", "B"));
    render(<App />);
    fireEvent.click(await screen.findByRole("link", { name: "A" }));
    await waitFor(() => expect(titleField().value).toBe("A"));
    fireEvent.change(titleField(), { target: { value: "changed" } });
    // A Back has no opener: the focus is nowhere in particular.
    (document.activeElement as HTMLElement | null)?.blur();
    expect(document.activeElement).toBe(document.body);
    act(() => window.history.back());
    await screen.findByRole("dialog", { name: t.discardChangesTitle });
    await keepEditing();
    await waitFor(() => expect(discardQuestion()).toBeNull());
    expect(document.activeElement).toBe(detail().getByRole("heading", { level: 2 }));
    expect(docParam()).toBe("d1");
  });

  it("asks one question at a time: Back while the Delete question is open stays without a second question", async () => {
    serve(doc("d1", "A"), doc("d2", "B"));
    render(<App />);
    fireEvent.click(await screen.findByRole("link", { name: "A" }));
    await waitFor(() => expect(titleField().value).toBe("A"));
    fireEvent.change(titleField(), { target: { value: "changed" } });
    fireEvent.click(detail().getByRole("button", { name: t.deleteDocumentLabel("changed") }));
    await screen.findByRole("dialog", { name: t.discardChangesTitle });
    act(() => window.history.back());
    // The document's URL comes back; still the one question.
    await waitFor(() => expect(docParam()).toBe("d1"));
    await act(async () => {});
    expect(screen.getAllByRole("dialog", { name: t.discardChangesTitle })).toHaveLength(1);
    // Its answer is still the Delete's: the document goes to the trash.
    await discard();
    await waitFor(() => expect(requests.filter((r) => r.method === "DELETE").map((r) => r.url)).toEqual(["/api/documents/d1"]));
    await waitFor(() => expect(screen.queryByTestId("document-detail")).toBeNull());
    // Nothing locked: the next navigation goes through without a question.
    fireEvent.click(await screen.findByRole("link", { name: "B" }));
    expect(docParam()).toBe("d2");
    expect(discardQuestion()).toBeNull();
  });
});

describe("a save that answers late (#105)", () => {
  // A (d1) is open, reached from B (d2), so Back goes to B. A's save is
  // held back; the user goes Back to B and confirms leaving; then A's
  // answer comes.
  async function saveAThenBackToB(button: string) {
    serve(doc("d1", "A"), doc("d2", "B"));
    const calls = watchPatches();
    await openAt("/?doc=d2");
    fireEvent.click(detail().getByRole("button", { name: t.previousDocument }));
    await waitFor(() => expect(titleField().value).toBe("A"));
    fireEvent.change(titleField(), { target: { value: "A2" } });
    const release = holdPatches();
    fireEvent.click(detail().getByRole("button", { name: button }));
    await waitFor(() => expect(patches()).toHaveLength(1));
    act(() => window.history.back());
    await discard();
    await waitFor(() => expect(docParam()).toBe("d2"));
    await waitFor(() => expect(titleField().value).toBe("B"));
    return { calls, release };
  }

  it("Save: the answer for A never lands in B's form", async () => {
    const { calls, release } = await saveAThenBackToB(t.save);
    await act(async () => {
      release();
      await calls[0];
    });
    expect(titleField().value).toBe("B");
    expect(docParam()).toBe("d2");
    // The save did happen: the list row shows it.
    expect(await screen.findByRole("link", { name: "A2" })).toBeTruthy();
    expect(detail().queryByText(t.saved)).toBeNull();
  });

  it("Done: the late answer neither fills B's form nor navigates", async () => {
    const { calls, release } = await saveAThenBackToB(t.done);
    await act(async () => {
      release();
      await calls[0];
    });
    expect(docParam()).toBe("d2");
    expect(titleField().value).toBe("B");
    expect(screen.getByTestId("document-detail")).toBeTruthy();
  });

  it("a late conflict answer for A neither rebases into B's form nor reloads A", async () => {
    const { calls, release } = await saveAThenBackToB(t.save);
    patchAnswer = { status: 409, body: { error: "changed", code: "document_changed", modifiedAt: "2026-10-22T09:00:00.000001Z" } };
    const gets = () => requests.filter((r) => r.method === "GET" && r.url === "/api/documents/d1").length;
    const before = gets();
    await act(async () => {
      release();
      await calls[0];
    });
    expect(gets()).toBe(before);
    expect(titleField().value).toBe("B");
    expect(detail().queryByText(t.documentChanged)).toBeNull();
  });

  it("A, then B, then A again: the late answer of the first visit does not overwrite the new form", async () => {
    serve(doc("d1", "A"), doc("d2", "B"));
    const calls = watchPatches();
    await openAt("/?doc=d2");
    fireEvent.click(detail().getByRole("button", { name: t.previousDocument }));
    await waitFor(() => expect(titleField().value).toBe("A"));
    fireEvent.change(titleField(), { target: { value: "A2" } });
    const release = holdPatches();
    fireEvent.click(detail().getByRole("button", { name: t.done }));
    await waitFor(() => expect(patches()).toHaveLength(1));
    act(() => window.history.back());
    await discard();
    await waitFor(() => expect(docParam()).toBe("d2"));
    await waitFor(() => expect(titleField().value).toBe("B"));
    act(() => window.history.forward());
    await waitFor(() => expect(docParam()).toBe("d1"));
    // A is open again (the held save has not changed it yet); the user edits anew.
    await waitFor(() => expect(titleField().value).toBe("A"));
    fireEvent.change(titleField(), { target: { value: "A-new" } });
    await act(async () => {
      release();
      await calls[0];
    });
    expect(titleField().value).toBe("A-new");
    expect(docParam()).toBe("d1");
    expect(detail().queryByText(t.saved)).toBeNull();
    // The list row shows the save that did happen.
    expect(await screen.findByRole("link", { name: "A2" })).toBeTruthy();
  });

  it("no previous or next while saving: buttons disabled, arrow keys do nothing", async () => {
    serve(doc("d1", "A"), doc("d2", "B"), doc("d3", "C"));
    const calls = watchPatches();
    await openAt("/?doc=d2");
    fireEvent.change(titleField(), { target: { value: "B2" } });
    const release = holdPatches();
    fireEvent.click(detail().getByRole("button", { name: t.save }));
    await waitFor(() => expect(patches()).toHaveLength(1));
    expect((detail().getByRole("button", { name: t.nextDocument }) as HTMLButtonElement).disabled).toBe(true);
    expect((detail().getByRole("button", { name: t.previousDocument }) as HTMLButtonElement).disabled).toBe(true);
    // Leaving would be allowed: only the save in flight keeps the keys and
    // the swipe from navigating.
    fireEvent.keyDown(document.body, { key: "ArrowRight" });
    fireEvent.keyDown(document.body, { key: "ArrowLeft" });
    const section = screen.getByTestId("document-detail");
    fireEvent.touchStart(section, { touches: [{ clientX: 300, clientY: 400 }] });
    fireEvent.touchEnd(section, { changedTouches: [{ clientX: 100, clientY: 410 }] });
    expect(docParam()).toBe("d2");
    expect(discardQuestion()).toBeNull();
    await act(async () => {
      release();
      await calls[0];
    });
    expect(await detail().findByText(t.saved)).toBeTruthy();
    expect((detail().getByRole("button", { name: t.nextDocument }) as HTMLButtonElement).disabled).toBe(false);
    fireEvent.keyDown(document.body, { key: "ArrowRight" });
    expect(docParam()).toBe("d3");
  });
});

describe("changed elsewhere meanwhile (#105)", () => {
  it("sends modifiedAt exactly as the server answered it, microseconds included", async () => {
    serve(doc("d1", "A", { modifiedAt: "2026-10-21T10:00:00.123456Z" }));
    await openAt("/?doc=d1");
    fireEvent.change(titleField(), { target: { value: "A2" } });
    fireEvent.click(detail().getByRole("button", { name: t.save }));
    expect(await detail().findByText(t.saved)).toBeTruthy();
    expect(patches()[0]!.body).toEqual({ title: "A2", expectedModifiedAt: "2026-10-21T10:00:00.123456Z" });
    // The answer's modifiedAt is the next precondition.
    fireEvent.change(titleField(), { target: { value: "A3" } });
    fireEvent.click(detail().getByRole("button", { name: t.save }));
    await waitFor(() => expect(patches()).toHaveLength(2));
    expect(patches()[1]!.body).toEqual({ title: "A3", expectedModifiedAt: "2026-10-22T08:00:01.123456Z" });
  });

  it("on a conflict loads the document again, keeps the user's edits on top, and saves against the new state", async () => {
    tags = [tag("t1", "Eins"), tag("t2", "Zwei"), tag("t3", "Drei")];
    serve(doc("d1", "A", { tagIds: ["t1", "t3"], notes: "alt" }));
    await openAt("/?doc=d1");
    const box = detail().getByRole("combobox", { name: t.tags });
    await waitFor(() => expect((box as HTMLInputElement).disabled).toBe(false));
    // The user: a new title, and tag t3 removed.
    fireEvent.change(titleField(), { target: { value: "A2" } });
    fireEvent.click(detail().getByRole("button", { name: t.removeChoice("Drei") }));
    // Meanwhile another change (a bulk change, another tab) adds t2 and
    // rewrites the notes.
    docs.set("d1", { ...docs.get("d1")!, tagIds: ["t1", "t2", "t3"], notes: "neu", modifiedAt: "2026-10-21T11:00:00.654321Z" });
    const getsBefore = requests.filter((r) => r.method === "GET" && r.url === "/api/documents/d1").length;

    // Done: refused; no navigation, nothing saved by itself.
    fireEvent.click(detail().getByRole("button", { name: t.done }));
    expect(await detail().findByText(t.documentChanged)).toBeTruthy();
    expect(docParam()).toBe("d1");
    expect(patches()).toHaveLength(1);
    expect(requests.filter((r) => r.method === "GET" && r.url === "/api/documents/d1").length).toBe(getsBefore + 1);
    // The form: the user's title, the other change's notes, and t2 kept
    // with t3 still removed.
    await waitFor(() => expect(detail().getByTestId("notes-rendered").textContent).toBe("neu"));
    expect(titleField().value).toBe("A2");
    expect(detail().getByRole("button", { name: t.removeChoice("Zwei") })).toBeTruthy();
    expect(detail().getByRole("button", { name: t.removeChoice("Eins") })).toBeTruthy();
    expect(detail().queryByRole("button", { name: t.removeChoice("Drei") })).toBeNull();
    expect((detail().getByRole("button", { name: t.save }) as HTMLButtonElement).disabled).toBe(false);

    // Saved again: only the user's fields, against the reloaded state.
    fireEvent.click(detail().getByRole("button", { name: t.save }));
    expect(await detail().findByText(t.saved)).toBeTruthy();
    expect(patches()[1]!.body).toEqual({ title: "A2", tagIds: ["t1", "t2"], expectedModifiedAt: "2026-10-21T11:00:00.654321Z" });
    const stored = docs.get("d1")!;
    expect(stored.notes).toBe("neu");
    expect(stored.tagIds).toEqual(["t1", "t2"]);
    expect(detail().queryByText(t.documentChanged)).toBeNull();
  });
});

describe("notes in Markdown (#12)", () => {
  const notesArea = () => detail().queryByRole("textbox", { name: t.fieldNotes }) as HTMLTextAreaElement | null;
  const rendered = () => detail().getByTestId("notes-rendered");

  it("shows the notes rendered, with an Edit toggle to the textarea and back", async () => {
    const notes = "# Termin\n- [x] bezahlt\n- [ ] abgelegt\n**wichtig**\n[Portal](https://portal.example/a)\n[böse](javascript:alert(1))";
    serve(doc("d1", "A", { notes }));
    await openAt("/?doc=d1");
    expect(notesArea()).toBeNull();
    expect(within(rendered()).getByRole("heading", { level: 3 }).textContent).toBe("Termin");
    const boxes = within(rendered()).getAllByRole("checkbox") as HTMLInputElement[];
    expect(boxes.map((b) => [b.checked, b.disabled])).toEqual([
      [true, true],
      [false, true],
    ]);
    expect(rendered().querySelector("strong")?.textContent).toBe("wichtig");
    const links = within(rendered()).getAllByRole("link");
    expect(links).toHaveLength(1);
    expect(links[0]!.textContent).toBe("Portal");
    expect(links[0]!.getAttribute("href")).toBe("https://portal.example/a");
    expect(links[0]!.getAttribute("rel")).toBe("noopener noreferrer");
    expect(rendered().textContent).toContain("böse");

    fireEvent.click(detail().getByRole("tab", { name: t.notesEditLabel }));
    expect(notesArea()!.value).toBe(notes);
    expect(document.activeElement).toBe(notesArea());
    // The preview shows the text as edited, unsaved.
    fireEvent.change(notesArea()!, { target: { value: "*neu*" } });
    fireEvent.click(detail().getByRole("tab", { name: t.notesPreviewLabel }));
    expect(notesArea()).toBeNull();
    expect(rendered().querySelector("em")?.textContent).toBe("neu");
    expect(detail().getByText(t.unsaved)).toBeTruthy();
    expect(patches()).toHaveLength(0);
  });

  it("opens empty notes in the textarea, and a saved edit renders", async () => {
    serve(doc("d1", "A", { notes: "" }));
    await openAt("/?doc=d1");
    expect(detail().queryByTestId("notes-rendered")).toBeNull();
    // Nothing to preview yet.
    expect(detail().queryByRole("tab", { name: t.notesPreviewLabel })).toBeNull();
    fireEvent.change(notesArea()!, { target: { value: "- eins\n  - zwei" } });
    // Typing keeps the textarea.
    expect(notesArea()!.value).toBe("- eins\n  - zwei");
    fireEvent.click(detail().getByRole("button", { name: t.save }));
    expect(await detail().findByText(t.saved)).toBeTruthy();
    expect(patches()[0]!.body).toEqual({ notes: "- eins\n  - zwei", expectedModifiedAt: "2026-10-21T10:00:00Z" });
    expect(notesArea()).toBeNull();
    expect(within(rendered()).getAllByRole("listitem").map((li) => li.textContent)).toEqual(["•eins", "•zwei"]);
  });

  it("links bare URLs and e-mail addresses and shows entities as characters (#131)", async () => {
    const notes = "Portal: https://portal.example/login, Fragen an müller@example.com\n`https://code.example` &amp; M&uuml;ller\n&#106;avascript:alert(1) javascript:alert(1)";
    serve(doc("d1", "A", { notes }));
    await openAt("/?doc=d1");
    const links = within(rendered()).getAllByRole("link");
    expect(links.map((a) => [a.getAttribute("href"), a.textContent, a.getAttribute("target"), a.getAttribute("rel")])).toEqual([
      ["https://portal.example/login", "https://portal.example/login", "_blank", "noopener noreferrer"],
      ["mailto:m%C3%BCller@example.com", "müller@example.com", "_blank", "noopener noreferrer"],
    ]);
    expect(rendered().querySelector("code")?.textContent).toBe("https://code.example");
    // The named entities load on demand (#134); then the note renders again.
    await waitFor(() =>
      expect(rendered().textContent).toBe("Portal: https://portal.example/login, Fragen an müller@example.com\nhttps://code.example & Müller\njavascript:alert(1) javascript:alert(1)"),
    );
    // Edit shows the text as written.
    fireEvent.click(detail().getByRole("tab", { name: t.notesEditLabel }));
    expect(notesArea()!.value).toBe(notes);
  });

  it("opens a note nested thousands deep, and Edit opens it as text", async () => {
    const notes = "*a ".repeat(20_000) + "b*".repeat(20_000);
    serve(doc("d1", "A", { notes }));
    await openAt("/?doc=d1");
    expect(rendered().querySelector("em")).not.toBeNull();
    fireEvent.click(detail().getByRole("tab", { name: t.notesEditLabel }));
    expect(notesArea()!.value).toBe(notes);
    expect(detail().queryByTestId("notes-rendered")).toBeNull();
  });

  it("keeps a long code line inside the form's column: the code scrolls, the form does not widen", async () => {
    serve(doc("d1", "A", { notes: "```\n" + "x".repeat(200) + "\n```" }));
    await openAt("/?doc=d1");
    const pre = rendered().querySelector("pre")!;
    expect(pre.textContent).toBe("x".repeat(200));
    // The rendered notes and the fieldset above them carry the layout
    // classes whose rules (layout.css) let the code scroll and keep the
    // fieldset as narrow as its column: a fieldset is otherwise at least
    // as wide as its content.
    expect(rendered().classList.contains("notes-rendered")).toBe(true);
    let fieldset: HTMLElement | null = null;
    for (let el: HTMLElement | null = pre; el && el.tagName !== "FORM"; el = el.parentElement) {
      if (el.tagName === "FIELDSET") fieldset = el;
    }
    expect(fieldset?.classList.contains("doc-form__fields")).toBe(true);
    const css = layoutCSS();
    expect(rule(css, ".notes-rendered pre")).toContain("overflow-x: auto");
    expect(rule(css, ".doc-form__fields")).toContain("min-inline-size: 0");
    expect(rule(css, ".notes-rendered")).toContain("min-inline-size: 0");
  });

  it("shows the textarea again when the server refuses the notes", async () => {
    serve(doc("d1", "A", { notes: "alt" }));
    await openAt("/?doc=d1");
    fireEvent.click(detail().getByRole("tab", { name: t.notesEditLabel }));
    fireEvent.change(notesArea()!, { target: { value: "neu" } });
    fireEvent.click(detail().getByRole("tab", { name: t.notesPreviewLabel }));
    patchAnswer = { status: 400, body: { error: "bad", code: "invalid_notes" } };
    fireEvent.click(detail().getByRole("button", { name: t.save }));
    // Carbon's TextArea names its error text as aria-errormessage.
    expect((await detail().findByText(t.invalidNotes)).id).toBe(notesArea()!.getAttribute("aria-errormessage"));
    expect(notesArea()!.value).toBe("neu");
    expect(notesArea()!.getAttribute("aria-invalid")).toBe("true");
  });
});

describe("logging out (#105)", () => {
  const logouts = () => requests.filter((r) => r.method === "DELETE" && r.url === "/api/session");

  it("asks about unsaved changes; refused, the session and the edits stay", async () => {
    serve(doc("d1", "A"));
    await openAt("/?doc=d1");
    fireEvent.change(titleField(), { target: { value: "changed" } });
    fireEvent.click(within(screen.getByRole("banner", { name: t.appName })).getByRole("button", { name: t.logOut }));
    await screen.findByRole("dialog", { name: t.discardChangesTitle });
    await keepEditing();
    await waitFor(() => expect(discardQuestion()).toBeNull());
    expect(logouts()).toHaveLength(0);
    expect(titleField().value).toBe("changed");

    fireEvent.click(within(screen.getByRole("banner", { name: t.appName })).getByRole("button", { name: t.logOut }));
    await discard();
    await waitFor(() => expect(logouts()).toHaveLength(1));
    expect(patches()).toHaveLength(0);
  });

  it("does not ask without changes", async () => {
    serve(doc("d1", "A"));
    await openAt("/?doc=d1");
    fireEvent.click(within(screen.getByRole("banner", { name: t.appName })).getByRole("button", { name: t.logOut }));
    await waitFor(() => expect(logouts()).toHaveLength(1));
    expect(discardQuestion()).toBeNull();
  });
});

describe("the owner note (#105)", () => {
  it("compares owners by ID: the admin's own document with a stale name has none, another user of the same name has one", async () => {
    user = { id: 9, name: "root", admin: true };
    serve(doc("d1", "Mine", { ownerId: 9, ownerName: "root-old" }), doc("d2", "Theirs", { ownerId: 2, ownerName: "root" }));
    await openAt("/?doc=d1");
    expect(detail().queryByText(t.ownedBy("root-old"))).toBeNull();
    fireEvent.click(detail().getByRole("button", { name: t.nextDocument }));
    await waitFor(() => expect(titleField().value).toBe("Theirs"));
    expect(detail().getByText(t.ownedBy("root"))).toBeTruthy();
  });
});

describe("the trash (#12)", () => {
  const trashed = { deletedAt: "2026-10-01T08:00:00Z", purgeAt: "2026-10-31T08:00:00Z" };
  const posts = (suffix: string) => requests.filter((r) => r.method === "POST" && r.url.endsWith(suffix));

  it("shows a trashed document read-only with a banner, and Restore brings the form back", async () => {
    serve(doc("d1", "Rechnung", trashed));
    await openAt("/?doc=d1&trash=1");
    const banner = within(detail().getByRole("region", { name: t.trash }));
    expect(banner.getByText(t.trashBanner(trashDay(trashed.deletedAt)))).toBeTruthy();
    expect(banner.getByText(t.trashBannerRemoved(trashDay(trashed.purgeAt)))).toBeTruthy();
    expect(banner.getByRole("button", { name: t.purge })).toBeTruthy();
    expect(titleField().matches(":disabled")).toBe(true);
    expect(detail().queryByRole("button", { name: t.save })).toBeNull();
    expect(detail().queryByRole("button", { name: t.deleteDocumentLabel("Rechnung") })).toBeNull();

    fireEvent.click(banner.getByRole("button", { name: t.restore }));
    await waitFor(() => expect(titleField().matches(":disabled")).toBe(false));
    expect(posts("/api/documents/d1/restore")).toHaveLength(1);
    expect(detail().queryByRole("region", { name: t.trash })).toBeNull();
    expect(detail().getByRole("button", { name: t.save })).toBeTruthy();
  });

  it("keeps the focus in the detail after its Restore, on the title, and Escape still closes the detail (#138)", async () => {
    serve(doc("d1", "Rechnung", trashed));
    await openAt("/?doc=d1&trash=1");
    const restore = within(detail().getByRole("region", { name: t.trash })).getByRole("button", { name: t.restore });
    restore.focus();
    fireEvent.click(restore);
    expect((await screen.findByTestId("notice")).textContent).toContain(t.restoredDocument("Rechnung"));
    await waitFor(() => expect(document.activeElement).toBe(titleField()));
    expect(screen.getByTestId("notice").contains(document.activeElement)).toBe(false);
    // Escape goes to the detail, which closes; the notice stays.
    fireEvent.keyDown(document.activeElement!, { key: "Escape" });
    await waitFor(() => expect(screen.queryByTestId("document-detail")).toBeNull());
    expect(screen.getByTestId("notice").textContent).toContain(t.restoredDocument("Rechnung"));
  });

  it("dims the notes of a trashed document like its other fields (#138)", async () => {
    // Rendered notes: a dimmed box, and Edit disabled and dimmed.
    serve(doc("d1", "Rechnung", { ...trashed, notes: "Bezahlt" }), doc("d2", "Brief", trashed));
    await openAt("/?doc=d1&trash=1");
    const rendered = detail().getByTestId("notes-rendered");
    expect(rendered.classList.contains("notes-rendered--disabled")).toBe(true);
    expect(rule(layoutCSS(), ".notes-rendered--disabled")).toContain("var(--cds-text-disabled)");
    // The switcher's tabs are in the disabled fieldset.
    const edit = detail().getByRole("tab", { name: t.notesEditLabel });
    expect(edit.matches(":disabled")).toBe(true);
    // Empty notes: Carbon's textarea, disabled (its disabled style is Carbon's).
    fireEvent.click(detail().getByRole("button", { name: t.nextDocument }));
    await waitFor(() => expect(titleField().value).toBe("Brief"));
    const area = detail().getByRole("textbox", { name: t.fieldNotes });
    expect(area.matches(":disabled")).toBe(true);
    // A live document's rendered notes are not dimmed.
    cleanup();
    serve(doc("d3", "Lebt", { notes: "Offen" }));
    await openAt("/?doc=d3");
    expect(detail().getByTestId("notes-rendered").classList.contains("notes-rendered--disabled")).toBe(false);
  });

  it("asks before Delete permanently, and closes once it is done", async () => {
    serve(doc("d1", "Rechnung", trashed));
    await openAt("/?doc=d1&trash=1");
    fireEvent.click(within(detail().getByRole("region", { name: t.trash })).getByRole("button", { name: t.purge }));
    const ask = within(await screen.findByRole("dialog", { name: t.purgeTitle("Rechnung") }));
    expect(ask.getByText(t.purgeText(1))).toBeTruthy();
    // Cancel sends nothing.
    fireEvent.click(ask.getByRole("button", { name: t.cancel }));
    expect(posts("/purge")).toHaveLength(0);

    fireEvent.click(within(detail().getByRole("region", { name: t.trash })).getByRole("button", { name: t.purge }));
    fireEvent.click(within(await screen.findByRole("dialog", { name: t.purgeTitle("Rechnung") })).getByRole("button", { name: t.purgeConfirm(1) }));
    await waitFor(() => expect(screen.queryByTestId("document-detail")).toBeNull());
    expect(posts("/api/documents/d1/purge")).toHaveLength(1);
    expect((await screen.findByTestId("notice")).textContent).toContain(t.purgedDocument("Rechnung"));
  });

  it("names another user's document's owner in an admin's purge confirmation", async () => {
    user = { id: 1, name: "root", admin: true };
    serve(doc("d1", "Rechnung", { ...trashed, ownerId: 2, ownerName: "bob" }));
    await openAt("/?doc=d1&trash=1&all=1");
    fireEvent.click(within(detail().getByRole("region", { name: t.trash })).getByRole("button", { name: t.purge }));
    const ask = within(await screen.findByRole("dialog", { name: t.purgeTitle("Rechnung") }));
    expect(ask.getByText(t.purgeOwners("bob"))).toBeTruthy();
  });

  it("deletes a live document without a question, closes, and offers Undo", async () => {
    serve(doc("d1", "Rechnung"));
    await openAt("/?doc=d1");
    fireEvent.click(detail().getByRole("button", { name: t.deleteDocumentLabel("Rechnung") }));
    await waitFor(() => expect(screen.queryByTestId("document-detail")).toBeNull());
    expect(requests.filter((r) => r.method === "DELETE").map((r) => r.url)).toEqual(["/api/documents/d1"]);
    expect(discardQuestion()).toBeNull();
    const notice = within(await screen.findByTestId("notice"));
    fireEvent.click(notice.getByRole("button", { name: t.undo }));
    await waitFor(() => expect(posts("/api/documents/d1/restore")).toHaveLength(1));
    expect(await screen.findByRole("link", { name: "Rechnung" })).toBeTruthy();
  });

  it("asks about unsaved changes before a delete, and keeps them on a refusal", async () => {
    serve(doc("d1", "Rechnung"));
    await openAt("/?doc=d1");
    fireEvent.change(titleField(), { target: { value: "Neu" } });
    fireEvent.click(detail().getByRole("button", { name: t.deleteDocumentLabel("Neu") }));
    await screen.findByRole("dialog", { name: t.discardChangesTitle });
    expect(requests.filter((r) => r.method === "DELETE")).toHaveLength(0);
    await keepEditing();
    await waitFor(() => expect(discardQuestion()).toBeNull());
    expect(requests.filter((r) => r.method === "DELETE")).toHaveLength(0);
    expect(titleField().value).toBe("Neu");
    expect(screen.getByTestId("document-detail")).toBeTruthy();
    // Accepted: the delete happens and the detail closes.
    fireEvent.click(detail().getByRole("button", { name: t.deleteDocumentLabel("Neu") }));
    await discard();
    await waitFor(() => expect(requests.filter((r) => r.method === "DELETE").map((r) => r.url)).toEqual(["/api/documents/d1"]));
    await waitFor(() => expect(screen.queryByTestId("document-detail")).toBeNull());
  });

  it("says so when a save meets a document trashed elsewhere, and shows its banner", async () => {
    serve(doc("d1", "Rechnung"));
    await openAt("/?doc=d1");
    fireEvent.change(titleField(), { target: { value: "Neu" } });
    // Trashed by someone else meanwhile.
    docs.set("d1", { ...docs.get("d1")!, ...trashed });
    patchAnswer = { status: 409, body: { error: "the document is in the trash", code: "in_trash" } };
    fireEvent.click(detail().getByRole("button", { name: t.save }));
    const countsBefore = requests.filter((r) => r.url.startsWith("/api/documents/counts")).length;
    expect((await detail().findByText(t.inTrashEditsKept)).closest("[role=alert]")).not.toBeNull();
    await waitFor(() => expect(detail().getByRole("region", { name: t.trash })).toBeTruthy());
    expect(titleField().matches(":disabled")).toBe(true);
    // The edit is kept, not dropped for the server's state.
    expect(titleField().value).toBe("Neu");
    // The counts load again: the document left the list elsewhere.
    await waitFor(() => expect(requests.filter((r) => r.url.startsWith("/api/documents/counts")).length).toBeGreaterThan(countsBefore));

    // After Restore the kept edit can be saved.
    patchAnswer = null;
    fireEvent.click(within(detail().getByRole("region", { name: t.trash })).getByRole("button", { name: t.restore }));
    await waitFor(() => expect(titleField().matches(":disabled")).toBe(false));
    expect(titleField().value).toBe("Neu");
    fireEvent.click(detail().getByRole("button", { name: t.save }));
    await waitFor(() => expect(patches()).toHaveLength(2));
    expect(patches()[1]!.body).toMatchObject({ title: "Neu" });
  });

  it("shows a document trashed elsewhere when its Delete meets in_trash, with the text for one document", async () => {
    serve(doc("d1", "Rechnung"));
    await openAt("/?doc=d1");
    docs.set("d1", { ...docs.get("d1")!, ...trashed });
    fireEvent.click(detail().getByRole("button", { name: t.deleteDocumentLabel("Rechnung") }));
    expect(await detail().findByText(t.alreadyInTrash)).toBeTruthy();
    await waitFor(() => expect(detail().getByRole("region", { name: t.trash })).toBeTruthy());
  });

  it("makes the read-only fields of a trashed document look disabled", async () => {
    serve(doc("d1", "Rechnung", trashed));
    await openAt("/?doc=d1&trash=1");
    // Carbon's fields in a disabled fieldset: disabled, with Carbon's disabled style.
    expect(titleField().matches(":disabled")).toBe(true);
    expect(titleField().classList.contains("cds--text-input")).toBe(true);
    expect(detail().getByRole("combobox", { name: t.tags }).matches(":disabled")).toBe(true);
    expect(detail().getByRole("checkbox", { name: t.inInbox }).matches(":disabled")).toBe(true);
  });

  it("says when the archive serial number is taken by a document in the trash", async () => {
    serve(doc("d1", "Rechnung"));
    await openAt("/?doc=d1");
    fireEvent.change(detail().getByLabelText(t.fieldASN), { target: { value: "7" } });
    patchAnswer = { status: 409, body: { error: "taken", code: "asn_taken", documentId: "d9", trashed: true } };
    fireEvent.click(detail().getByRole("button", { name: t.save }));
    expect(await detail().findByText(t.asnTakenTrashed, { exact: false })).toBeTruthy();
  });

  it("walks the trash list with previous and next", async () => {
    serve(doc("d1", "Eins", trashed), doc("d2", "Zwei", trashed), doc("d3", "Lebt"));
    window.history.replaceState(null, "", "/?trash=1");
    render(<App />);
    fireEvent.click(await screen.findByRole("link", { name: "Eins" }));
    await waitFor(() => expect(titleField().value).toBe("Eins"));
    fireEvent.click(detail().getByRole("button", { name: t.nextDocument }));
    await waitFor(() => expect(titleField().value).toBe("Zwei"));
    expect((detail().getByRole("button", { name: t.nextDocument }) as HTMLButtonElement).disabled).toBe(true);
  });
});

describe("a delete or purge answered after the detail was left (#135 review B)", () => {
  const trashed = { deletedAt: "2026-10-01T08:00:00Z", purgeAt: "2026-10-31T08:00:00Z" };
  function holdTrash(): () => Promise<void> {
    let release: () => void = () => {};
    trashHold = new Promise<void>((r) => (release = r));
    return async () => {
      trashHold = null;
      await act(async () => release());
    };
  }
  const press = (key: string) => fireEvent.keyDown(document, { key });

  it("still drops the row and offers Undo when Escape closed the detail before the DELETE answered", async () => {
    serve(doc("d1", "Rechnung"), doc("d2", "Brief"));
    window.history.replaceState(null, "", "/");
    render(<App />);
    fireEvent.click(await screen.findByRole("link", { name: "Rechnung" }));
    await waitFor(() => expect(titleField().value).toBe("Rechnung"));
    const release = holdTrash();
    fireEvent.click(detail().getByRole("button", { name: t.deleteDocumentLabel("Rechnung") }));
    await waitFor(() => expect(requests.some((r) => r.method === "DELETE")).toBe(true));
    // While it runs, previous and next wait.
    expect((detail().getByRole("button", { name: t.nextDocument }) as HTMLButtonElement).disabled).toBe(true);
    press("ArrowRight");
    expect(docParam()).toBe("d1");
    press("Escape");
    await waitFor(() => expect(screen.queryByTestId("document-detail")).toBeNull());
    const countsBefore = requests.filter((r) => r.url.startsWith("/api/documents/counts")).length;
    await release();
    const notice = within(await screen.findByTestId("notice"));
    expect(notice.getByText(t.movedToTrash("Rechnung"))).toBeTruthy();
    expect(notice.getByRole("button", { name: t.undo })).toBeTruthy();
    await waitFor(() => expect(screen.queryByRole("link", { name: "Rechnung" })).toBeNull());
    await waitFor(() => expect(requests.filter((r) => r.url.startsWith("/api/documents/counts")).length).toBeGreaterThan(countsBefore));
  });

  it("does not close another document opened meanwhile", async () => {
    serve(doc("d1", "Rechnung"), doc("d2", "Brief"));
    await openAt("/?doc=d1");
    const release = holdTrash();
    fireEvent.click(detail().getByRole("button", { name: t.deleteDocumentLabel("Rechnung") }));
    await waitFor(() => expect(requests.some((r) => r.method === "DELETE")).toBe(true));
    press("Escape");
    await waitFor(() => expect(screen.queryByTestId("document-detail")).toBeNull());
    fireEvent.click(await screen.findByRole("link", { name: "Brief" }));
    await waitFor(() => expect(titleField().value).toBe("Brief"));
    await release();
    await screen.findByTestId("notice");
    expect(docParam()).toBe("d2");
    expect(screen.getByTestId("document-detail")).toBeTruthy();
  });

  it("still drops a purged row when the detail was left before the purge answered", async () => {
    serve(doc("d1", "Rechnung", trashed), doc("d2", "Bleibt", trashed));
    window.history.replaceState(null, "", "/?trash=1");
    render(<App />);
    fireEvent.click(await screen.findByRole("link", { name: "Rechnung" }));
    await waitFor(() => expect(titleField().value).toBe("Rechnung"));
    fireEvent.click(within(detail().getByRole("region", { name: t.trash })).getByRole("button", { name: t.purge }));
    const release = holdTrash();
    fireEvent.click(within(await screen.findByRole("dialog", { name: t.purgeTitle("Rechnung") })).getByRole("button", { name: t.purgeConfirm(1) }));
    await waitFor(() => expect(requests.some((r) => r.url.endsWith("/purge"))).toBe(true));
    // Escape closes the question, then the detail.
    fireEvent.click(within(screen.getByRole("dialog", { name: t.purgeTitle("Rechnung") })).getByRole("button", { name: t.cancel }));
    press("Escape");
    await waitFor(() => expect(screen.queryByTestId("document-detail")).toBeNull());
    await release();
    expect((await screen.findByTestId("notice")).textContent).toContain(t.purgedDocument("Rechnung"));
    await waitFor(() => expect(screen.queryByRole("link", { name: "Rechnung" })).toBeNull());
    expect(screen.getByRole("link", { name: "Bleibt" })).toBeTruthy();
  });
});

describe("the Carbon pickers (#150 part 4)", () => {
  const tagBox = async () => {
    const box = detail().getByRole("combobox", { name: t.tags }) as HTMLInputElement;
    await waitFor(() => expect(box.disabled).toBe(false));
    return box;
  };
  const optionNames = () => detail().getAllByRole("option").map((o) => o.getAttribute("aria-label"));

  it("lists the tags in tree order with their path, and finds a nested tag by its own name and by its parent's", async () => {
    tags = [tag("t3", "Home"), tag("t1", "Finance"), tag("t2", "Bills", { parentId: "t1" })];
    serve(doc("d1", "A"));
    await openAt("/?doc=d1");
    const box = await tagBox();
    fireEvent.click(box);
    expect(optionNames()).toEqual(["Finance", "Finance / Bills", "Home"]);
    // Its own name.
    fireEvent.change(box, { target: { value: "bills" } });
    expect(optionNames()).toEqual(["Finance / Bills"]);
    // Its parent's name shows the parent and the child.
    fireEvent.change(box, { target: { value: "Finance" } });
    expect(optionNames()).toEqual(["Finance", "Finance / Bills"]);
    fireEvent.click(detail().getByRole("option", { name: "Finance / Bills" }));
    // Chosen: the tag by its own name, as a chip with its remove button.
    const chosen = within(await detail().findByRole("list", { name: t.chosenIn(t.tags) }));
    expect(chosen.getAllByRole("listitem")).toHaveLength(1);
    expect(chosen.getByText("Bills")).toBeTruthy();
    expect(chosen.getByRole("button", { name: t.removeChoice("Bills") })).toBeTruthy();
    fireEvent.click(detail().getByRole("button", { name: t.save }));
    await waitFor(() => expect(patches()).toHaveLength(1));
    expect(patches()[0]!.body).toEqual({ tagIds: ["t2"], expectedModifiedAt: "2026-10-21T10:00:00Z" });
    // The chip removes it again.
    fireEvent.click(chosen.getByRole("button", { name: t.removeChoice("Bills") }));
    await waitFor(() => expect(detail().queryByRole("list", { name: t.chosenIn(t.tags) })).toBeNull());
    expect((detail().getByRole("button", { name: t.save }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("offers Create only for a name that is no tag yet, and a created tag is chosen at once", async () => {
    tags = [tag("t1", "Steuer")];
    serve(doc("d1", "A"));
    await openAt("/?doc=d1");
    const box = await tagBox();
    fireEvent.change(box, { target: { value: "steuer" } });
    expect(optionNames()).toEqual(["Steuer"]);
    fireEvent.change(box, { target: { value: "Steuern" } });
    expect(optionNames()).toEqual([t.createNamed("Steuern")]);
    fireEvent.click(detail().getByRole("option", { name: t.createNamed("Steuern") }));
    expect(await detail().findByRole("button", { name: t.removeChoice("Steuern") })).toBeTruthy();
    expect(requests.find((r) => r.method === "POST" && r.url === "/api/tags")!.body).toEqual({ name: "Steuern" });
    // Created, it is a tag like any other: no Create for it any more.
    fireEvent.change(box, { target: { value: "Steuern" } });
    expect(optionNames()).toEqual(["Steuern"]);
  });

  it("shows why a tag could not be created, at the field", async () => {
    tags = [tag("t1", "Steuer")];
    serve(doc("d1", "A"));
    await openAt("/?doc=d1");
    vi.spyOn(documentsApi, "get");
    const realFetch = fetch;
    vi.stubGlobal("fetch", async (url: string, init?: RequestInit) => {
      if (url === "/api/tags" && init?.method === "POST") return json(409, { error: "taken", code: "name_taken" });
      return realFetch(url, init);
    });
    const box = await tagBox();
    fireEvent.change(box, { target: { value: "Neu" } });
    fireEvent.click(detail().getByRole("option", { name: t.createNamed("Neu") }));
    expect(await detail().findByText(t.nameTaken)).toBeTruthy();
    expect(detail().queryByRole("button", { name: t.removeChoice("Neu") })).toBeNull();
  });

  it("keeps the arrow keys inside an open list box and the notes switcher: they never change the document", async () => {
    tags = [tag("t1", "Steuer"), tag("t2", "Arzt")];
    serve(doc("d1", "A", { notes: "x" }), doc("d2", "B"), doc("d3", "C"));
    render(<App />);
    fireEvent.click(await screen.findByRole("link", { name: "B" }));
    await waitFor(() => expect(titleField().value).toBe("B"));
    const box = await tagBox();
    fireEvent.click(box);
    const list = detail().getByRole("listbox", { name: /Tags/ });
    // In the field with the list open, and on an option of the list.
    fireEvent.keyDown(box, { key: "ArrowRight" });
    fireEvent.keyDown(box, { key: "ArrowLeft" });
    fireEvent.keyDown(within(list).getAllByRole("option")[0]!, { key: "ArrowRight" });
    expect(docParam()).toBe("d2");
    // Escape in the picker: the list closes, the document stays open.
    fireEvent.keyDown(box, { key: "Escape" });
    expect(docParam()).toBe("d2");
    expect(screen.getByTestId("document-detail")).toBeTruthy();
    // The notes switcher moves between Edit and Preview with the arrows.
    fireEvent.click(detail().getByRole("button", { name: t.previousDocument }));
    await waitFor(() => expect(titleField().value).toBe("A"));
    const preview = detail().getByRole("tab", { name: t.notesPreviewLabel });
    fireEvent.keyDown(preview, { key: "ArrowLeft" });
    expect(docParam()).toBe("d1");
    await waitFor(() => expect(detail().getByRole("textbox", { name: t.fieldNotes })).toBeTruthy());
    // Outside, the keys still move between documents.
    fireEvent.keyDown(document.body, { key: "ArrowRight" });
    expect(docParam()).toBe("d2");
  });

  it("chooses and clears the correspondent with the ComboBox, with Create for a new name", async () => {
    correspondents = [named("c1", "Finanzamt")];
    serve(doc("d1", "A", { correspondentId: "c1" }));
    await openAt("/?doc=d1");
    const box = detail().getByRole("combobox", { name: t.fieldCorrespondent }) as HTMLInputElement;
    await waitFor(() => expect(box.value).toBe("Finanzamt"));
    fireEvent.click(detail().getByRole("button", { name: t.carbon.ids["clear.selection"] }));
    await waitFor(() => expect(box.value).toBe(""));
    fireEvent.change(box, { target: { value: "Arzt" } });
    expect(detail().getAllByRole("option").map((o) => o.textContent)).toEqual([t.createNamed("Arzt")]);
    fireEvent.click(detail().getByRole("option", { name: t.createNamed("Arzt") }));
    await waitFor(() => expect(box.value).toBe("Arzt"));
    expect(requests.find((r) => r.method === "POST" && r.url === "/api/correspondents")!.body).toEqual({ name: "Arzt" });
    fireEvent.click(detail().getByRole("button", { name: t.save }));
    await waitFor(() => expect(patches()).toHaveLength(1));
    expect(patches()[0]!.body).toEqual({ correspondentId: "new-Arzt", expectedModifiedAt: "2026-10-21T10:00:00Z" });
  });
});

describe("the Carbon pickers (#150 part 4): the keys that would clear a choice (review A, blocking 1)", () => {
  const chips = () => within(detail().getByRole("list", { name: t.chosenIn(t.tags) })).getAllByRole("listitem").length;

  it("Escape on the focused tag field with its list closed keeps the chips, changes nothing and closes the document", async () => {
    tags = [tag("t1", "Steuer"), tag("t2", "Arzt")];
    serve(doc("d1", "A", { tagIds: ["t1", "t2"] }), doc("d2", "B"));
    render(<App />);
    fireEvent.click(await screen.findByRole("link", { name: "A" }));
    await waitFor(() => expect(titleField().value).toBe("A"));
    const box = detail().getByRole("combobox", { name: t.tags }) as HTMLInputElement;
    await waitFor(() => expect(box.disabled).toBe(false));
    expect(chips()).toBe(2);
    box.focus();
    fireEvent.keyDown(box, { key: "Escape" });
    // Nothing unsaved: no question; the document closes, as from any other field.
    await waitFor(() => expect(screen.queryByTestId("document-detail")).toBeNull());
    expect(docParam()).toBeNull();
    expect(discardQuestion()).toBeNull();
    expect(patches()).toHaveLength(0);
    // Opened again, both tags are still there.
    fireEvent.click(await screen.findByRole("link", { name: "A" }));
    await waitFor(() => expect(titleField().value).toBe("A"));
    await waitFor(() => expect(chips()).toBe(2));
  });

  it("Backspace or Delete in the empty tag field removes the last chosen tag only, and the save sends the rest", async () => {
    tags = [tag("t1", "Steuer"), tag("t2", "Arzt"), tag("t3", "Bank")];
    serve(doc("d1", "A", { tagIds: ["t1", "t2", "t3"] }));
    await openAt("/?doc=d1");
    const box = detail().getByRole("combobox", { name: t.tags }) as HTMLInputElement;
    await waitFor(() => expect(box.disabled).toBe(false));
    box.focus();
    fireEvent.keyDown(box, { key: "Backspace" });
    await waitFor(() => expect(chips()).toBe(2));
    fireEvent.keyDown(box, { key: "Delete" });
    await waitFor(() => expect(chips()).toBe(1));
    expect(detail().getByRole("button", { name: t.removeChoice("Steuer") })).toBeTruthy();
    expect(screen.getByTestId("document-detail")).toBeTruthy();
    fireEvent.click(detail().getByRole("button", { name: t.save }));
    await waitFor(() => expect(patches()).toHaveLength(1));
    expect(patches()[0]!.body).toEqual({ tagIds: ["t1"], expectedModifiedAt: "2026-10-21T10:00:00Z" });
    // With text in the field, Delete edits the text and keeps the chip.
    fireEvent.change(box, { target: { value: "x" } });
    fireEvent.keyDown(box, { key: "Delete" });
    expect(chips()).toBe(1);
  });

  it("Escape on a closed correspondent field keeps its text and its value, and closes the document (review A, follow-up 1)", async () => {
    correspondents = [named("c1", "Finanzamt")];
    serve(doc("d1", "A", { correspondentId: "c1" }));
    await openAt("/?doc=d1");
    const box = detail().getByRole("combobox", { name: t.fieldCorrespondent }) as HTMLInputElement;
    await waitFor(() => expect(box.value).toBe("Finanzamt"));
    box.focus();
    fireEvent.keyDown(box, { key: "Escape" });
    await waitFor(() => expect(screen.queryByTestId("document-detail")).toBeNull());
    expect(discardQuestion()).toBeNull();
    expect(patches()).toHaveLength(0);
    await openAt("/?doc=d1");
    const again = detail().getByRole("combobox", { name: t.fieldCorrespondent }) as HTMLInputElement;
    await waitFor(() => expect(again.value).toBe("Finanzamt"));
  });

  it("greys the chosen tags' chips of a document in the trash (review A, follow-up 3)", async () => {
    tags = [tag("t1", "Steuer", { color: "#ff0000" })];
    serve(doc("d1", "A", { tagIds: ["t1"], deletedAt: "2026-10-01T08:00:00Z", purgeAt: "2026-10-31T08:00:00Z" }));
    await openAt("/?doc=d1&trash=1");
    await waitFor(() => expect(chips()).toBe(1));
    const chip = within(detail().getByRole("list", { name: t.chosenIn(t.tags) })).getByRole("listitem").firstElementChild as HTMLElement;
    expect(chip.getAttribute("style")).toBeNull();
    expect(chip.className).toContain("cds--tag--gray");
  });
});

describe("the Carbon pickers (#150 part 4): the ComboBox's field and the form agree (review B, blocking 1)", () => {
  const corrBox = async () => {
    const box = detail().getByRole("combobox", { name: t.fieldCorrespondent }) as HTMLInputElement;
    await waitFor(() => expect(box.value).toBe("Finanzamt"));
    return box;
  };

  // The field was emptied and left: the choice is cleared, the form is dirty, the save sends null.
  for (const [how, leave] of [
    ["with its list still open", (box: HTMLInputElement) => fireEvent.blur(box)],
    [
      "with its list closed",
      (box: HTMLInputElement) => {
        fireEvent.keyDown(box, { key: "Escape" });
        fireEvent.blur(box);
      },
    ],
  ] as const) {
    it(`an emptied correspondent field, left ${how}, clears the choice: the form is dirty and the save sends null`, async () => {
      correspondents = [named("c1", "Finanzamt")];
      serve(doc("d1", "A", { correspondentId: "c1" }));
      await openAt("/?doc=d1");
      const box = await corrBox();
      box.focus();
      fireEvent.change(box, { target: { value: "" } });
      leave(box);
      await waitFor(() => expect((detail().getByRole("button", { name: t.save }) as HTMLButtonElement).disabled).toBe(false));
      await waitFor(() => expect(box.value).toBe(""));
      expect(detail().getByText(t.unsaved)).toBeTruthy();
      fireEvent.click(detail().getByRole("button", { name: t.save }));
      await waitFor(() => expect(patches()).toHaveLength(1));
      expect(patches()[0]!.body).toEqual({ correspondentId: null, expectedModifiedAt: "2026-10-21T10:00:00Z" });
    });
  }

  it("other text left in the field with its list closed gives way to the choice's name again, nothing changes", async () => {
    correspondents = [named("c1", "Finanzamt"), named("c2", "Arzt")];
    serve(doc("d1", "A", { correspondentId: "c1" }));
    await openAt("/?doc=d1");
    const box = await corrBox();
    box.focus();
    fireEvent.change(box, { target: { value: "Arzt" } });
    fireEvent.keyDown(box, { key: "Escape" });
    fireEvent.blur(box);
    await waitFor(() => expect(box.value).toBe("Finanzamt"));
    expect((detail().getByRole("button", { name: t.save }) as HTMLButtonElement).disabled).toBe(true);
    expect(detail().queryByText(t.unsaved)).toBeNull();
  });
});

describe("the Carbon pickers (#150 part 4): a creation still pending (CI on 0a3a96a)", () => {
  function holdCreates(): () => Promise<void> {
    let release: () => void = () => {};
    createHold = new Promise<void>((r) => (release = r));
    return async () => {
      createHold = null;
      await act(async () => release());
    };
  }
  const typeBox = async () => {
    const box = detail().getByRole("combobox", { name: t.fieldType }) as HTMLInputElement;
    await waitFor(() => expect(box.disabled).toBe(false));
    return box;
  };

  it("neither Save nor Done nor previous/next go on while a type is being created; the save then has the new type", async () => {
    serve(doc("d1", "A"), doc("d2", "B"));
    await openAt("/?doc=d1");
    fireEvent.change(titleField(), { target: { value: "A2" } });
    const box = await typeBox();
    const release = holdCreates();
    fireEvent.change(box, { target: { value: "Neu" } });
    fireEvent.click(detail().getByRole("option", { name: t.createNamed("Neu") }));
    await waitFor(() => expect(requests.some((r) => r.method === "POST" && r.url === "/api/document-types")).toBe(true));
    const save = detail().getByRole("button", { name: t.save }) as HTMLButtonElement;
    const done = detail().getByRole("button", { name: t.done }) as HTMLButtonElement;
    await waitFor(() => expect(save.disabled).toBe(true));
    expect(done.disabled).toBe(true);
    expect((detail().getByRole("button", { name: t.nextDocument }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(save);
    fireEvent.click(done);
    fireEvent.submit(detail().getByRole("form", { name: t.editDocument }));
    fireEvent.keyDown(document.body, { key: "ArrowRight" });
    expect(patches()).toHaveLength(0);
    expect(docParam()).toBe("d1");
    await release();
    await waitFor(() => expect(box.value).toBe("Neu"));
    await waitFor(() => expect(save.disabled).toBe(false));
    fireEvent.click(save);
    await waitFor(() => expect(patches()).toHaveLength(1));
    expect(patches()[0]!.body).toEqual({ title: "A2", documentTypeId: "new-Neu", expectedModifiedAt: "2026-10-21T10:00:00Z" });
  });

  it("a creation that answers after another document was opened changes nothing there", async () => {
    serve(doc("d1", "A"), doc("d2", "B"));
    render(<App />);
    fireEvent.click(await screen.findByRole("link", { name: "A" }));
    await waitFor(() => expect(titleField().value).toBe("A"));
    const box = await typeBox();
    const release = holdCreates();
    fireEvent.change(box, { target: { value: "Neu" } });
    fireEvent.click(detail().getByRole("option", { name: t.createNamed("Neu") }));
    await waitFor(() => expect(requests.some((r) => r.method === "POST" && r.url === "/api/document-types")).toBe(true));
    // Nothing is unsaved yet: a Forward to B (history) goes without a question; the detail stays mounted and shows B.
    act(() => {
      window.history.pushState(null, "", "/?doc=d2");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    await waitFor(() => expect(titleField().value).toBe("B"));
    expect(docParam()).toBe("d2");
    await release();
    await act(async () => {});
    expect((detail().getByRole("combobox", { name: t.fieldType }) as HTMLInputElement).value).toBe("");
    expect((detail().getByRole("button", { name: t.save }) as HTMLButtonElement).disabled).toBe(true);
    expect(detail().queryByText(t.unsaved)).toBeNull();
    expect(patches()).toHaveLength(0);
  });
});

describe("the Carbon pickers (#150 part 4): review A's delta follow-ups", () => {
  it("a held Backspace (auto-repeat) in the empty tag field removes no chip", async () => {
    tags = [tag("t1", "Steuer"), tag("t2", "Arzt")];
    serve(doc("d1", "A", { tagIds: ["t1", "t2"] }));
    await openAt("/?doc=d1");
    const box = detail().getByRole("combobox", { name: t.tags }) as HTMLInputElement;
    await waitFor(() => expect(box.disabled).toBe(false));
    const chips = () => within(detail().getByRole("list", { name: t.chosenIn(t.tags) })).getAllByRole("listitem").length;
    box.focus();
    fireEvent.keyDown(box, { key: "Backspace", repeat: true });
    fireEvent.keyDown(box, { key: "Backspace", repeat: true });
    await act(async () => {});
    expect(chips()).toBe(2);
    fireEvent.keyDown(box, { key: "Backspace" });
    await waitFor(() => expect(chips()).toBe(1));
  });

  it("typing in the next picker while a creation settles keeps focus and text there, and both choices are saved (review B)", async () => {
    correspondents = [];
    serve(doc("d1", "A"));
    await openAt("/?doc=d1");
    const corr = detail().getByRole("combobox", { name: t.fieldCorrespondent }) as HTMLInputElement;
    await waitFor(() => expect(corr.disabled).toBe(false));
    let release: () => void = () => {};
    createHold = new Promise<void>((r) => (release = r));
    corr.focus();
    fireEvent.change(corr, { target: { value: "Amt" } });
    fireEvent.click(detail().getByRole("option", { name: t.createNamed("Amt") }));
    await waitFor(() => expect(corr.disabled).toBe(true));
    // Meanwhile the user is in the type field, typing a new type.
    const type = detail().getByRole("combobox", { name: t.fieldType }) as HTMLInputElement;
    type.focus();
    fireEvent.change(type, { target: { value: "Ver" } });
    await act(async () => {
      createHold = null;
      release();
    });
    await waitFor(() => expect(corr.value).toBe("Amt"));
    expect(document.activeElement).toBe(type);
    expect(type.value).toBe("Ver");
    fireEvent.change(type, { target: { value: "Vertrag" } });
    fireEvent.click(detail().getByRole("option", { name: t.createNamed("Vertrag") }));
    await waitFor(() => expect(type.value).toBe("Vertrag"));
    const save = detail().getByRole("button", { name: t.save }) as HTMLButtonElement;
    await waitFor(() => expect(save.disabled).toBe(false));
    fireEvent.click(save);
    await waitFor(() => expect(patches()).toHaveLength(1));
    expect(patches()[0]!.body).toEqual({ correspondentId: "new-Amt", documentTypeId: "new-Vertrag", expectedModifiedAt: "2026-10-21T10:00:00Z" });
  });

  it("the field takes the focus back after a creation only when the user has not moved on", async () => {
    serve(doc("d1", "A"));
    await openAt("/?doc=d1");
    const box = detail().getByRole("combobox", { name: t.fieldType }) as HTMLInputElement;
    await waitFor(() => expect(box.disabled).toBe(false));
    let release: () => void = () => {};
    createHold = new Promise<void>((r) => (release = r));
    box.focus();
    fireEvent.change(box, { target: { value: "Neu" } });
    fireEvent.click(detail().getByRole("option", { name: t.createNamed("Neu") }));
    await waitFor(() => expect(box.disabled).toBe(true));
    // Meanwhile the user is in the title.
    titleField().focus();
    expect(document.activeElement).toBe(titleField());
    await act(async () => {
      createHold = null;
      release();
    });
    await waitFor(() => expect(box.value).toBe("Neu"));
    expect(document.activeElement).toBe(titleField());
  });
});

describe("the Carbon pickers (#150 part 4): review B's follow-ups", () => {
  it("the tag field's clear button removes every chosen tag at once, as a deliberate clear (follow-up 4)", async () => {
    tags = [tag("t1", "Steuer"), tag("t2", "Arzt")];
    serve(doc("d1", "A", { tagIds: ["t1", "t2"] }));
    await openAt("/?doc=d1");
    const box = detail().getByRole("combobox", { name: t.tags }) as HTMLInputElement;
    await waitFor(() => expect(box.disabled).toBe(false));
    fireEvent.click(detail().getByRole("button", { name: t.carbon.ids["clear.all"] }));
    await waitFor(() => expect(detail().queryByRole("list", { name: t.chosenIn(t.tags) })).toBeNull());
    expect(detail().getByText(t.unsaved)).toBeTruthy();
    fireEvent.click(detail().getByRole("button", { name: t.save }));
    await waitFor(() => expect(patches()).toHaveLength(1));
    expect(patches()[0]!.body).toEqual({ tagIds: [], expectedModifiedAt: "2026-10-21T10:00:00Z" });
  });

  it("the tag field takes the focus back after a tag was created from it by keyboard (follow-up 2)", async () => {
    tags = [tag("t1", "Steuer")];
    serve(doc("d1", "A"));
    await openAt("/?doc=d1");
    const box = detail().getByRole("combobox", { name: t.tags }) as HTMLInputElement;
    await waitFor(() => expect(box.disabled).toBe(false));
    let release: () => void = () => {};
    createHold = new Promise<void>((r) => (release = r));
    box.focus();
    fireEvent.change(box, { target: { value: "Neu" } });
    // Enter takes the highlighted item: the only one, "Create “Neu”".
    fireEvent.keyDown(box, { key: "Enter" });
    await waitFor(() => expect(requests.some((r) => r.method === "POST" && r.url === "/api/tags")).toBe(true));
    await waitFor(() => expect(box.disabled).toBe(true));
    // A browser drops the focus from the disabled field (jsdom keeps it, and ignores blur()): the body takes it.
    document.body.tabIndex = -1;
    document.body.focus();
    document.body.removeAttribute("tabindex");
    expect(document.activeElement).toBe(document.body);
    await act(async () => {
      createHold = null;
      release();
    });
    await detail().findByRole("button", { name: t.removeChoice("Neu") });
    await waitFor(() => expect(document.activeElement).toBe(detail().getByRole("combobox", { name: t.tags })));
  });

  it("offers no Create item for a typed path of an existing nested tag (follow-up 3)", async () => {
    tags = [tag("t1", "Finance"), tag("t2", "Bills", { parentId: "t1" })];
    serve(doc("d1", "A"));
    await openAt("/?doc=d1");
    const box = detail().getByRole("combobox", { name: t.tags }) as HTMLInputElement;
    await waitFor(() => expect(box.disabled).toBe(false));
    fireEvent.change(box, { target: { value: "finance / bills" } });
    expect(detail().getAllByRole("option").map((o) => o.getAttribute("aria-label"))).toEqual(["Finance / Bills"]);
  });
});

describe("the Carbon pickers (#150 part 4): changing a choice", () => {
  it("keeps what is typed over a chosen correspondent, and Enter takes the match", async () => {
    correspondents = [named("c1", "Finanzamt"), named("c2", "Arzt")];
    serve(doc("d1", "A", { correspondentId: "c1" }));
    await openAt("/?doc=d1");
    const box = detail().getByRole("combobox", { name: t.fieldCorrespondent }) as HTMLInputElement;
    await waitFor(() => expect(box.value).toBe("Finanzamt"));
    fireEvent.change(box, { target: { value: "ar" } });
    fireEvent.change(box, { target: { value: "arz" } });
    await act(async () => {});
    expect(box.value).toBe("arz");
    // The match, and Create for the typed name (no row is named exactly so).
    expect(detail().getAllByRole("option").map((o) => o.textContent)).toEqual(["Arzt", t.createNamed("arz")]);
    fireEvent.keyDown(box, { key: "Enter" });
    await waitFor(() => expect(box.value).toBe("Arzt"));
    fireEvent.click(detail().getByRole("button", { name: t.save }));
    await waitFor(() => expect(patches()).toHaveLength(1));
    expect(patches()[0]!.body).toEqual({ correspondentId: "c2", expectedModifiedAt: "2026-10-21T10:00:00Z" });
  });
});

describe("the detail in German", () => {
  afterEach(() => {
    vi.resetModules();
  });

  it("names the header, the fields, the pickers, the notes switcher and the actions in German, nothing in English", async () => {
    vi.resetModules();
    vi.stubGlobal("navigator", { ...navigator, languages: ["de-DE", "en-US"], language: "de-DE" });
    const { App: GermanApp } = await import("./App");
    const { messagesFor } = await import("./i18n");
    const de = messagesFor("de");
    tags = [tag("t1", "Steuer")];
    correspondents = [named("c1", "Finanzamt")];
    serve(doc("d1", "Rechnung", { tagIds: ["t1"], correspondentId: "c1", notes: "Bezahlt" }));
    window.history.replaceState(null, "", "/?doc=d1");
    render(<GermanApp />);
    const d = within(await screen.findByTestId("document-detail"));
    await waitFor(() => expect(d.getByLabelText(de.fieldTitle)).toBeTruthy());
    // The header row and the viewer's toolbar.
    for (const name of [de.closeDocument, de.previousDocument, de.nextDocument, de.zoomFit, de.zoomIn, de.zoomOut]) expect(d.getByRole("button", { name })).toBeTruthy();
    expect(d.getByRole("toolbar", { name: de.viewerTools })).toBeTruthy();
    // The fields and the pickers, with Carbon's texts.
    for (const name of [de.fieldDate, de.fieldASN]) expect(d.getByLabelText(name)).toBeTruthy();
    const corr = d.getByRole("combobox", { name: de.fieldCorrespondent }) as HTMLInputElement;
    await waitFor(() => expect(corr.value).toBe("Finanzamt"));
    // The correspondent's clear button; the tag picker has one of its own.
    expect(d.getAllByRole("button", { name: de.carbon.ids["clear.selection"] }).length).toBeGreaterThanOrEqual(1);
    expect(d.getAllByRole("button", { name: de.carbon.ids["open.menu"] }).length).toBeGreaterThanOrEqual(2);
    expect(d.getByRole("combobox", { name: de.tags })).toBeTruthy();
    const chosen = within(d.getByRole("list", { name: de.chosenIn(de.tags) }));
    expect(chosen.getByRole("button", { name: de.removeChoice("Steuer") })).toBeTruthy();
    // The notes switcher, the flags, the actions and the facts.
    expect(d.getByRole("tab", { name: de.notesEditLabel })).toBeTruthy();
    expect(d.getByRole("tab", { name: de.notesPreviewLabel })).toBeTruthy();
    expect(d.getByRole("checkbox", { name: de.inInbox })).toBeTruthy();
    expect(d.getByRole("checkbox", { name: de.favorite })).toBeTruthy();
    expect(d.getByRole("button", { name: de.save })).toBeTruthy();
    expect(d.getByRole("button", { name: de.done })).toBeTruthy();
    expect(d.getByRole("button", { name: de.deleteDocumentLabel("Rechnung") })).toBeTruthy();
    expect(d.getByRole("region", { name: de.fileFacts })).toBeTruthy();
    expect(d.getByRole("link", { name: de.downloadOriginal })).toBeTruthy();
    const html = document.body.innerHTML;
    for (const s of ["Open menu", "Close menu", "Clear selected", "Total items selected", "To clear selection", "Zoom in", "Zoom out", "Fit width", "Close document", "Next document", ">Save<", ">Done<", "Edit notes", "Choose an item"]) {
      expect(html, s).not.toContain(s);
    }
  });
});
