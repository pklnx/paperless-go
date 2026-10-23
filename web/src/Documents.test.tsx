import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import type { DocumentSummary } from "./api";
import { DocumentsUpload, WindowDrop } from "./Documents";
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

beforeEach(() => {
  stubXHR();
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

function renderHome() {
  const queue = new UploadQueue();
  // The document list tells the queue the server's limit.
  queue.maxSize = 1000;
  render(<DocumentsUpload queue={queue} maxSize={1000} />);
  return queue;
}

const rows = () => within(screen.getByRole("list", { name: t.uploadsLabel })).getAllByRole("listitem");

describe("DocumentsUpload", () => {
  it("uploads picked files two at a time, with progress and results", async () => {
    renderHome();

    fireEvent.change(screen.getByTestId("file-picker"), { target: { files: [file("a.pdf"), file("b.pdf"), file("c.pdf")] } });
    expect(rows()).toHaveLength(3);
    expect(FakeXHR.all).toHaveLength(2);
    expect(within(rows()[2]!).getByRole("status").textContent).toBe(t.uploadWaiting);

    act(() => FakeXHR.all[0]!.progress(3, 10));
    expect(within(rows()[0]!).getByRole("status").textContent).toBe("30 %");
    expect((within(rows()[0]!).getByRole("progressbar") as HTMLProgressElement).value).toBe(30);

    await act(async () => FakeXHR.all[0]!.respond(201, doc("1", "a")));
    expect(within(rows()[0]!).getByRole("status").textContent).toBe(t.uploadImported);
    // The third file starts once a slot is free.
    expect(FakeXHR.all).toHaveLength(3);

    await act(async () => FakeXHR.all[1]!.respond(200, { duplicate: true, existing: { id: "0", title: "Old letter" } }));
    expect(within(rows()[1]!).getByRole("status").textContent).toBe(t.uploadDuplicateOf("Old letter"));
    await act(async () => FakeXHR.all[2]!.respond(422, { error: "the file cannot be read", reason: "unreadable" }));
    expect(within(rows()[2]!).getByRole("status").textContent).toBe(t.uploadUnreadable);

    fireEvent.click(screen.getByRole("button", { name: t.clearFinished }));
    expect(screen.queryByRole("list", { name: t.uploadsLabel })).toBeNull();
  });

  it("refuses a file over the limit before sending it", async () => {
    renderHome();
    fireEvent.change(screen.getByTestId("file-picker"), { target: { files: [file("big.pdf", 1001)] } });
    expect(FakeXHR.all).toHaveLength(0);
    expect(within(rows()[0]!).getByRole("status").textContent).toBe(t.uploadTooLarge("1,000 B"));
  });

  it("cancels an upload", async () => {
    renderHome();
    fireEvent.change(screen.getByTestId("file-picker"), { target: { files: [file("a.pdf")] } });
    fireEvent.click(screen.getByRole("button", { name: t.cancelUpload("a.pdf") }));
    await vi.waitFor(() => expect(within(rows()[0]!).getByRole("status").textContent).toBe(t.uploadCancelled));
    expect(FakeXHR.all[0]!.aborted).toBe(true);
    expect(screen.queryByRole("button", { name: t.cancelUpload("a.pdf") })).toBeNull();
  });
});

describe("a duplicate in the trash and a later retry (#12)", () => {
  it("offers Restore for a duplicate whose document is in the trash", async () => {
    const calls: { url: string; method: string }[] = [];
    let status = 200;
    vi.stubGlobal("fetch", async (url: string, init?: RequestInit) => {
      calls.push({ url, method: init?.method ?? "GET" });
      return new Response(JSON.stringify(status === 200 ? doc("9", "Old") : { error: "not found" }), { status, headers: { "Content-Type": "application/json" } });
    });
    const queue = new UploadQueue();
    const onRestored = vi.fn();
    render(<DocumentsUpload queue={queue} maxSize={1000} onRestored={onRestored} />);
    fireEvent.change(screen.getByTestId("file-picker"), { target: { files: [file("a.pdf"), file("b.pdf")] } });
    await act(async () => FakeXHR.all[0]!.respond(200, { duplicate: true, existing: { id: "9", title: "Old" }, trashed: true }));
    await act(async () => FakeXHR.all[1]!.respond(200, { duplicate: true, existing: { id: "8", title: "Other" }, trashed: true }));
    expect(within(rows()[0]!).getByRole("status").textContent).toBe(t.uploadInTrashOf("Old"));

    fireEvent.click(screen.getByRole("button", { name: t.uploadRestoreLabel("a.pdf") }));
    await vi.waitFor(() => expect(within(rows()[0]!).getByRole("status").textContent).toBe(t.uploadRestored));
    expect(calls).toEqual([{ url: "/api/documents/9/restore", method: "POST" }]);
    expect(onRestored).toHaveBeenCalledOnce();
    expect(screen.queryByRole("button", { name: t.uploadRestoreLabel("a.pdf") })).toBeNull();

    // Gone from the trash meanwhile: the row says so and stays as it was.
    status = 404;
    fireEvent.click(screen.getByRole("button", { name: t.uploadRestoreLabel("b.pdf") }));
    expect((await within(rows()[1]!).findByRole("alert")).textContent).toBe(t.notInTrashAnyMore);
    expect(within(rows()[1]!).getByRole("status").textContent).toBe(t.uploadInTrashOf("Other"));
    expect(onRestored).toHaveBeenCalledOnce();
  });

  it("offers no Restore for a plain duplicate", async () => {
    renderHome();
    fireEvent.change(screen.getByTestId("file-picker"), { target: { files: [file("a.pdf")] } });
    await act(async () => FakeXHR.all[0]!.respond(200, { duplicate: true, existing: { id: "9", title: "Old" } }));
    expect(screen.queryByRole("button", { name: t.uploadRestoreLabel("a.pdf") })).toBeNull();
  });

  it("says when to try again after a 503 with Retry-After, and sends the file again on request", async () => {
    renderHome();
    const f = file("a.pdf");
    fireEvent.change(screen.getByTestId("file-picker"), { target: { files: [f] } });
    await act(async () => FakeXHR.all[0]!.respond(503, { error: "being removed" }, { "Retry-After": "60" }));
    expect(within(rows()[0]!).getByRole("status").textContent).toBe(t.uploadRetryAfter(60));
    fireEvent.click(screen.getByRole("button", { name: t.retryUpload("a.pdf") }));
    expect(FakeXHR.all).toHaveLength(2);
    expect(FakeXHR.all[1]!.body).toBe(f);
    await act(async () => FakeXHR.all[1]!.respond(201, doc("1", "a")));
    expect(within(rows()[0]!).getByRole("status").textContent).toBe(t.uploadImported);
    expect(screen.queryByRole("button", { name: t.retryUpload("a.pdf") })).toBeNull();
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
