// Uploading files: one XHR per file (fetch has no upload progress), at most
// two at a time, each with its own row state.

import { UNAUTHORIZED_EVENT, type DocumentSummary } from "./api";
import type { Messages } from "./i18n";

/** Why the server (or the size check before sending) refused a file. */
export type RefusalReason = "too_large" | "unsupported" | "unreadable" | "too_many_pixels";

export type UploadState =
  | { kind: "waiting" }
  | { kind: "uploading"; percent: number }
  | { kind: "imported"; document: DocumentSummary }
  | { kind: "duplicate"; existingTitle?: string }
  | { kind: "failed"; reason?: RefusalReason; message?: string }
  | { kind: "cancelled" };

export interface UploadItem {
  id: number;
  name: string;
  size: number;
  state: UploadState;
}

/** How many uploads run at once. */
export const parallelUploads = 2;

const fileTypes = new Set(["application/pdf", "image/jpeg", "image/png", "image/heic", "image/heif"]);

/** The Content-Type to send: the browser's type if the server takes it, else octet-stream. The server detects the type from the content either way. */
export function uploadContentType(file: File): string {
  return fileTypes.has(file.type) ? file.type : "application/octet-stream";
}

// Bidirectional control characters can make a name display differently
// from what it is (for example "invoice‮fdp.exe").
const bidiControls = /[؜‎‏‪-‮⁦-⁩]/g;

/** A file or document name made safe to display: without bidi control characters. */
export function displayName(name: string): string {
  return name.replace(bidiControls, "");
}

/** The result of one upload request. */
export type UploadOutcome = Exclude<UploadState, { kind: "waiting" } | { kind: "uploading" }>;

/** Sends one file as POST /api/documents and reports the upload progress in percent. */
export function sendFile(file: File, onProgress: (percent: number) => void): { done: Promise<UploadOutcome>; abort: () => void } {
  const xhr = new XMLHttpRequest();
  const done = new Promise<UploadOutcome>((resolve) => {
    xhr.open("POST", "/api/documents");
    xhr.setRequestHeader("Content-Type", uploadContentType(file));
    xhr.setRequestHeader("Accept", "application/json");
    const name = encodedName(file.name);
    if (name) xhr.setRequestHeader("X-File-Name", name);
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable && e.total > 0) onProgress(Math.min(100, Math.floor((e.loaded / e.total) * 100)));
    };
    xhr.onload = () => {
      let body: Record<string, unknown> = {};
      try {
        body = JSON.parse(xhr.responseText) as Record<string, unknown>;
      } catch {
        // not JSON (a refusal by the middleware is plain text)
      }
      if (xhr.status === 201) {
        resolve({ kind: "imported", document: body as unknown as DocumentSummary });
      } else if (xhr.status === 200 && body.duplicate === true) {
        const existing = body.existing as { title?: string } | undefined;
        resolve({ kind: "duplicate", existingTitle: existing?.title });
      } else {
        if (xhr.status === 401 || body.passwordChangeRequired === true || body.twoFactorSetupRequired === true) {
          window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
        }
        const reason = typeof body.reason === "string" ? (body.reason as RefusalReason) : xhr.status === 413 ? "too_large" : undefined;
        resolve({ kind: "failed", reason, message: typeof body.error === "string" ? body.error : xhr.statusText || String(xhr.status) });
      }
    };
    xhr.onerror = () => resolve({ kind: "failed" });
    xhr.onabort = () => resolve({ kind: "cancelled" });
    xhr.send(file);
  });
  return { done, abort: () => xhr.abort() };
}

/** The file name for X-File-Name, percent-encoded; empty when it cannot be encoded (an unpaired surrogate), so the server names the document itself. */
export function encodedName(name: string): string {
  try {
    return encodeURIComponent(name);
  } catch {
    return "";
  }
}

type Send = typeof sendFile;

/**
 * The upload queue: files wait until one of the parallel slots is free.
 * Files over the size limit fail at once without being sent.
 */
export class UploadQueue {
  private items: UploadItem[] = [];
  private files = new Map<number, File>();
  private aborts = new Map<number, () => void>();
  private nextId = 1;
  private listeners = new Set<(items: UploadItem[]) => void>();
  /** The server's size limit in bytes, once known; larger files are refused before sending. */
  maxSize: number | undefined;

  constructor(private readonly send: Send = sendFile) {}

  /** The current rows, oldest first. */
  get list(): UploadItem[] {
    return this.items;
  }

  subscribe(fn: (items: UploadItem[]) => void): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  /** Queues files; those over maxSize fail without being sent. */
  add(files: Iterable<File>) {
    const maxSize = this.maxSize;
    for (const file of files) {
      const id = this.nextId++;
      const tooLarge = maxSize !== undefined && maxSize > 0 && file.size > maxSize;
      this.items = [
        ...this.items,
        { id, name: file.name, size: file.size, state: tooLarge ? { kind: "failed", reason: "too_large" } : { kind: "waiting" } },
      ];
      if (!tooLarge) this.files.set(id, file);
    }
    this.changed();
    this.pump();
  }

  /** Cancels a waiting or running upload. */
  cancel(id: number) {
    const abort = this.aborts.get(id);
    if (abort) {
      abort();
      return;
    }
    if (this.files.delete(id)) {
      this.set(id, { kind: "cancelled" });
    }
  }

  /** Removes the rows that are finished. */
  clearFinished() {
    this.items = this.items.filter((i) => i.state.kind === "waiting" || i.state.kind === "uploading");
    this.changed();
  }

  private set(id: number, state: UploadState) {
    this.items = this.items.map((i) => (i.id === id ? { ...i, state } : i));
    this.changed();
  }

  private changed() {
    for (const fn of this.listeners) fn(this.items);
  }

  private pump() {
    while (this.aborts.size < parallelUploads) {
      const next = this.items.find((i) => i.state.kind === "waiting" && this.files.has(i.id));
      if (!next) return;
      const file = this.files.get(next.id)!;
      this.files.delete(next.id);
      this.set(next.id, { kind: "uploading", percent: 0 });
      let started: ReturnType<Send>;
      try {
        started = this.send(file, (percent) => this.set(next.id, { kind: "uploading", percent }));
      } catch {
        // The request could not even start: the row fails, the slot stays free.
        this.set(next.id, { kind: "failed" });
        continue;
      }
      this.aborts.set(next.id, started.abort);
      void started.done
        .catch((): UploadOutcome => ({ kind: "failed" }))
        .then((outcome) => {
          this.aborts.delete(next.id);
          this.set(next.id, outcome);
          this.pump();
        });
    }
  }
}

/** Formats a size in bytes for display (MB with one decimal from 1 MB). */
export function formatSize(bytes: number, locale: string): string {
  if (bytes >= 1024 * 1024) return `${(bytes / (1024 * 1024)).toLocaleString(locale, { maximumFractionDigits: 1 })} MB`;
  if (bytes >= 1024) return `${Math.round(bytes / 1024).toLocaleString(locale)} KB`;
  return `${bytes.toLocaleString(locale)} B`;
}

/** The text of a row's state. */
export function stateText(state: UploadState, m: Messages, maxSize: string): string {
  switch (state.kind) {
    case "waiting":
      return m.uploadWaiting;
    case "uploading":
      return state.percent >= 100 ? m.uploadProcessing : `${state.percent} %`;
    case "imported":
      return m.uploadImported;
    case "duplicate":
      return state.existingTitle ? m.uploadDuplicateOf(displayName(state.existingTitle)) : m.uploadDuplicate;
    case "cancelled":
      return m.uploadCancelled;
    case "failed":
      switch (state.reason) {
        case "too_large":
          return m.uploadTooLarge(maxSize);
        case "unsupported":
          return m.uploadUnsupported;
        case "unreadable":
          return m.uploadUnreadable;
        case "too_many_pixels":
          return m.uploadTooManyPixels;
      }
      // The server's own text is English; show a translated one.
      return state.message ? m.uploadFailed : m.uploadNetworkError;
  }
}
