import { afterEach, describe, expect, it, vi } from "vitest";
import { UNAUTHORIZED_EVENT, type DocumentSummary } from "./api";
import { messagesFor } from "./i18n";
import { FakeXHR, stubXHR } from "./fakeXHR";
import { displayName, encodedName, parallelUploads, sendFile, stateText, uploadContentType, UploadQueue, type UploadOutcome } from "./uploads";

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
      existingId: "9",
    });
    // In the trash (#12): the row offers Restore.
    expect(await outcome((x) => x.respond(200, { duplicate: true, existing: { id: "9", title: "Old" }, trashed: true }))).toEqual({
      kind: "duplicate",
      existingTitle: "Old",
      existingId: "9",
      trashed: true,
    });
    // A 503 with Retry-After (busy, or the trashed copy is being deleted permanently): send again later.
    expect(await outcome((x) => x.respond(503, { error: "being removed" }, { "Retry-After": "60" }))).toEqual({
      kind: "failed",
      reason: undefined,
      message: "being removed",
      retryAfter: 60,
    });
    expect(await outcome((x) => x.respond(503, { error: "no audit log" }))).toEqual({ kind: "failed", reason: undefined, message: "no audit log" });
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

describe("Retry-After and restore", () => {
  it("keeps the file of a row the server asked to send later, and sends it again on retry", async () => {
    const { calls, send } = manualSend();
    const q = new UploadQueue(send);
    q.add([file("a.pdf")]);
    const id = q.list[0]!.id;
    expect(q.canRetry(id)).toBe(false);
    calls[0]!.finish({ kind: "failed", message: "busy", retryAfter: 60 });
    await flush();
    await flush();
    expect(q.list[0]!.state).toMatchObject({ kind: "failed", retryAfter: 60 });
    expect(q.canRetry(id)).toBe(true);
    q.retry(id);
    expect(calls).toHaveLength(2);
    expect(calls[1]!.name).toBe("a.pdf");
    expect(q.canRetry(id)).toBe(false);
    calls[1]!.finish({ kind: "failed", message: "boom" });
    await flush();
    await flush();
    // Another failure is not one to send again.
    expect(q.canRetry(id)).toBe(false);
  });

  it("marks a trashed duplicate restored, and nothing else", async () => {
    const { calls, send } = manualSend();
    const q = new UploadQueue(send);
    q.add([file("a.pdf"), file("b.pdf")]);
    calls[0]!.finish({ kind: "duplicate", existingId: "9", trashed: true });
    calls[1]!.finish({ kind: "failed" });
    await flush();
    await flush();
    q.markRestored(q.list[0]!.id);
    q.markRestored(q.list[1]!.id);
    expect(q.list[0]!.state).toEqual({ kind: "duplicate", existingId: "9", trashed: false, restored: true });
    expect(q.list[1]!.state).toEqual({ kind: "failed" });
  });
});

describe("failures to start", () => {
  it("fails a row whose send throws and frees its slot", async () => {
    const { calls, send } = manualSend();
    let first = true;
    const throwing = (f: File, p: (n: number) => void) => {
      if (first) {
        first = false;
        throw new Error("send failed");
      }
      return send(f, p);
    };
    const q = new UploadQueue(throwing);
    q.add([file("a.pdf"), file("b.pdf"), file("c.pdf")]);
    expect(q.list.map((i) => i.state.kind)).toEqual(["failed", "uploading", "uploading"]);
    expect(calls.map((c) => c.name)).toEqual(["b.pdf", "c.pdf"]);
  });

  it("fails a row whose request rejects and starts the next", async () => {
    const { calls, send } = manualSend();
    let first = true;
    const rejecting = (f: File, p: (n: number) => void) => {
      if (first) {
        first = false;
        return { done: Promise.reject(new Error("xhr.send threw")), abort: () => {} };
      }
      return send(f, p);
    };
    const q = new UploadQueue(rejecting);
    q.add([file("a.pdf"), file("b.pdf"), file("c.pdf")]);
    await vi.waitFor(() => expect(q.list[0]!.state.kind).toBe("failed"));
    expect(calls.map((c) => c.name)).toEqual(["b.pdf", "c.pdf"]);
    // Cancelling the failed row does nothing: its abort entry is gone.
    q.cancel(q.list[0]!.id);
    expect(q.list[0]!.state.kind).toBe("failed");
  });

  it("rejects when xhr.send throws, so the queue can fail the row", async () => {
    stubXHR();
    const spy = vi.spyOn(FakeXHR.prototype, "send").mockImplementation(() => {
      throw new Error("NetworkError");
    });
    const { done } = sendFile(file("a.pdf"), () => {});
    await expect(done).rejects.toThrow("NetworkError");
    spy.mockRestore();
  });

  it("leaves out a file name that cannot be encoded", () => {
    expect(encodedName("a\uD800.pdf")).toBe("");
    expect(encodedName("März.pdf")).toBe("M%C3%A4rz.pdf");
    stubXHR();
    // A real File replaces lone surrogates itself; a stand-in keeps one.
    sendFile({ name: "a\uD800.pdf", type: "application/pdf" } as File, () => {});
    expect(FakeXHR.all[0]!.headers["X-File-Name"]).toBeUndefined();
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
    // An unknown server reason shows a translated text, not the server's English one.
    expect(stateText({ kind: "failed", message: "boom" }, en, "")).toBe("could not be imported");
    expect(stateText({ kind: "failed", message: "boom" }, de, "")).toBe("konnte nicht importiert werden");
    expect(stateText({ kind: "failed" }, en, "")).toBe("upload failed; check the connection");
    expect(stateText({ kind: "duplicate", existingTitle: "Alt", existingId: "9", trashed: true }, en, "")).toBe("already there, in the trash → Alt");
    expect(stateText({ kind: "duplicate", existingId: "9", trashed: true }, de, "")).toBe("schon vorhanden, im Papierkorb");
    expect(stateText({ kind: "duplicate", existingId: "9", restored: true }, en, "")).toBe("restored from the trash");
    expect(stateText({ kind: "failed", message: "x", retryAfter: 60 }, en, "")).toBe("not imported yet; try again in 1 min");
    expect(stateText({ kind: "failed", message: "x", retryAfter: 5 }, en, "")).toBe("not imported yet; try again in 5 s");
  });

  it("sends the browser's type only when the server takes it", () => {
    expect(uploadContentType(file("a.heic", 1, "image/heic"))).toBe("image/heic");
    expect(uploadContentType(file("a", 1, ""))).toBe("application/octet-stream");
  });
});
