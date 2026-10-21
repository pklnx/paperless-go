import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import type { DocumentSummary } from "./api";
import { DocumentsHome, WindowDrop } from "./Documents";
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

beforeEach(() => {
  docs = [];
  listCalls = 0;
  stubXHR();
  vi.stubGlobal("fetch", async (url: string) => {
    if (url === "/api/documents?limit=10") {
      listCalls++;
      return new Response(JSON.stringify({ documents: docs, maxUploadSize: 1000 }), { status: 200, headers: { "Content-Type": "application/json" } });
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
