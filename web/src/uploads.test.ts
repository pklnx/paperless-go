import { afterEach, describe, expect, it, vi } from "vitest";
import { UNAUTHORIZED_EVENT, type DocumentSummary } from "./api";
import { messagesFor } from "./i18n";
import { FakeXHR, stubXHR } from "./fakeXHR";
import { displayName, parallelUploads, sendFile, stateText, uploadContentType, UploadQueue, type UploadOutcome } from "./uploads";

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

const file = (name: string, size = 10, type = "application/pdf") => new File([new Uint8Array(size)], name, { type });

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("sendFile", () => {
  it("posts the raw file with its type and encoded name and reports progress", async () => {
    stubXHR();
    const progress: number[] = [];
    const f = file("Rechnung März.pdf");
    const { done } = sendFile(f, (p) => progress.push(p));
    const x = FakeXHR.all[0]!;
    expect(x.method).toBe("POST");
    expect(x.url).toBe("/api/documents");
    expect(x.headers["Content-Type"]).toBe("application/pdf");
    expect(x.headers["X-File-Name"]).toBe("Rechnung%20M%C3%A4rz.pdf");
    expect(x.body).toBe(f);
    x.progress(5, 10);
    x.progress(10, 10);
    x.respond(201, doc("1", "Rechnung März"));
    expect(await done).toEqual({ kind: "imported", document: doc("1", "Rechnung März") });
    expect(progress).toEqual([50, 100]);
  });

  it("maps duplicates, refusals, network errors and aborts", async () => {
    stubXHR();
    const outcome = (respond: (x: FakeXHR) => void): Promise<UploadOutcome> => {
      const { done } = sendFile(file("a.pdf"), () => {});
      respond(FakeXHR.all.at(-1)!);
      return done;
    };
    expect(await outcome((x) => x.respond(200, { duplicate: true, existing: { id: "9", title: "Old" } }))).toEqual({
      kind: "duplicate",
      existingTitle: "Old",
    });
    expect(await outcome((x) => x.respond(200, { duplicate: true }))).toEqual({ kind: "duplicate", existingTitle: undefined });
    expect(await outcome((x) => x.respond(415, { error: "not a PDF", reason: "unsupported" }))).toEqual({
      kind: "failed",
      reason: "unsupported",
      message: "not a PDF",
    });
    expect(await outcome((x) => x.respond(413, "too large"))).toMatchObject({ kind: "failed", reason: "too_large" });
    expect(await outcome((x) => x.onerror?.())).toEqual({ kind: "failed" });
    expect(await outcome((x) => x.abort())).toEqual({ kind: "cancelled" });
  });

  it("sends octet-stream for other types and asks for a login on 401", async () => {
    stubXHR();
    const seen = vi.fn();
    window.addEventListener(UNAUTHORIZED_EVENT, seen);
    const { done } = sendFile(file("notes.txt", 3, "text/plain"), () => {});
    const x = FakeXHR.all[0]!;
    expect(x.headers["Content-Type"]).toBe("application/octet-stream");
    x.respond(401, { error: "login required" });
    expect(await done).toMatchObject({ kind: "failed" });
    expect(seen).toHaveBeenCalledOnce();
    window.removeEventListener(UNAUTHORIZED_EVENT, seen);
  });
});

/** A send function whose uploads the test finishes by hand. */
function manualSend() {
  const calls: { name: string; progress: (p: number) => void; finish: (o: UploadOutcome) => void; aborted: boolean }[] = [];
  const send = (f: File, progress: (p: number) => void) => {
    let finish!: (o: UploadOutcome) => void;
    const done = new Promise<UploadOutcome>((r) => (finish = r));
    const call = { name: f.name, progress, finish, aborted: false };
    calls.push(call);
    return {
      done,
      abort: () => {
        call.aborted = true;
        finish({ kind: "cancelled" });
      },
    };
  };
  return { calls, send };
}

const flush = () => new Promise<void>((r) => queueMicrotask(r));

describe("UploadQueue", () => {
  it("runs two uploads at a time and starts the next when one ends", async () => {
    const { calls, send } = manualSend();
    const q = new UploadQueue(send);
    q.add([file("a.pdf"), file("b.pdf"), file("c.pdf")]);
    expect(parallelUploads).toBe(2);
    expect(calls.map((c) => c.name)).toEqual(["a.pdf", "b.pdf"]);
    expect(q.list.map((i) => i.state.kind)).toEqual(["uploading", "uploading", "waiting"]);
    calls[0]!.progress(40);
    expect(q.list[0]!.state).toEqual({ kind: "uploading", percent: 40 });
    calls[0]!.finish({ kind: "imported", document: doc("1", "a") });
    await flush();
    expect(calls.map((c) => c.name)).toEqual(["a.pdf", "b.pdf", "c.pdf"]);
    expect(q.list.map((i) => i.state.kind)).toEqual(["imported", "uploading", "uploading"]);
  });

  it("refuses files over the limit without sending them", () => {
    const { calls, send } = manualSend();
    const q = new UploadQueue(send);
    q.maxSize = 100;
    q.add([file("big.pdf", 101), file("exact.pdf", 100)]);
    expect(calls.map((c) => c.name)).toEqual(["exact.pdf"]);
    expect(q.list[0]!.state).toEqual({ kind: "failed", reason: "too_large" });
  });

  it("cancels waiting and running uploads", async () => {
    const { calls, send } = manualSend();
    const q = new UploadQueue(send);
    q.add([file("a.pdf"), file("b.pdf"), file("c.pdf")]);
    q.cancel(q.list[2]!.id);
    expect(q.list[2]!.state.kind).toBe("cancelled");
    q.cancel(q.list[0]!.id);
    expect(calls[0]!.aborted).toBe(true);
    await flush();
    expect(q.list.map((i) => i.state.kind)).toEqual(["cancelled", "uploading", "cancelled"]);
    // The cancelled waiting file is never sent.
    expect(calls.map((c) => c.name)).toEqual(["a.pdf", "b.pdf"]);
    q.clearFinished();
    expect(q.list.map((i) => i.name)).toEqual(["b.pdf"]);
  });

  it("tells subscribers about every change", () => {
    const { send } = manualSend();
    const q = new UploadQueue(send);
    const seen = vi.fn();
    const off = q.subscribe(seen);
    q.add([file("a.pdf")]);
    expect(seen).toHaveBeenCalled();
    off();
    seen.mockClear();
    q.add([file("b.pdf")]);
    expect(seen).not.toHaveBeenCalled();
  });
});

describe("display", () => {
  it("strips bidi control characters from names", () => {
    expect(displayName("invoice‮fdp.exe")).toBe("invoicefdp.exe");
    expect(displayName("⁦a⁩‎b‏؜")).toBe("ab");
    expect(displayName("Rechnung März")).toBe("Rechnung März");
  });

  it("describes each state in English and German", () => {
    const en = messagesFor("en");
    const de = messagesFor("de");
    expect(stateText({ kind: "waiting" }, en, "")).toBe("waiting");
    expect(stateText({ kind: "waiting" }, de, "")).toBe("wartet");
    expect(stateText({ kind: "uploading", percent: 42 }, en, "")).toBe("42 %");
    expect(stateText({ kind: "duplicate", existingTitle: "Alt‮" }, de, "")).toBe("schon vorhanden → Alt");
    expect(stateText({ kind: "duplicate" }, en, "")).toBe("already there");
    expect(stateText({ kind: "failed", reason: "too_large" }, en, "100 MB")).toBe("larger than 100 MB");
    expect(stateText({ kind: "failed", reason: "too_large" }, de, "100 MB")).toBe("größer als 100 MB");
    expect(stateText({ kind: "failed", reason: "unsupported" }, de, "")).toBe("keine PDF-, JPEG-, PNG- oder HEIC-Datei");
    expect(stateText({ kind: "failed", message: "boom" }, en, "")).toBe("Failed: boom");
    expect(stateText({ kind: "failed" }, en, "")).toBe("upload failed; check the connection");
  });

  it("sends the browser's type only when the server takes it", () => {
    expect(uploadContentType(file("a.heic", 1, "image/heic"))).toBe("image/heic");
    expect(uploadContentType(file("a", 1, ""))).toBe("application/octet-stream");
  });
});
