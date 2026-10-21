// A stand-in for XMLHttpRequest in tests; the app never imports it.

import { vi } from "vitest";

/** A stand-in for XMLHttpRequest that the test drives. */
export class FakeXHR {
  static all: FakeXHR[] = [];
  method = "";
  url = "";
  headers: Record<string, string> = {};
  body: unknown;
  status = 0;
  statusText = "";
  responseText = "";
  aborted = false;
  upload: { onprogress: ((e: { lengthComputable: boolean; loaded: number; total: number }) => void) | null } = { onprogress: null };
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onabort: (() => void) | null = null;
  constructor() {
    FakeXHR.all.push(this);
  }
  open(method: string, url: string) {
    this.method = method;
    this.url = url;
  }
  setRequestHeader(name: string, value: string) {
    this.headers[name] = value;
  }
  send(body: unknown) {
    this.body = body;
  }
  abort() {
    this.aborted = true;
    this.onabort?.();
  }
  progress(loaded: number, total: number) {
    this.upload.onprogress?.({ lengthComputable: true, loaded, total });
  }
  respond(status: number, body: unknown) {
    this.status = status;
    this.responseText = typeof body === "string" ? body : JSON.stringify(body);
    this.onload?.();
  }
}

export function stubXHR() {
  FakeXHR.all = [];
  vi.stubGlobal("XMLHttpRequest", FakeXHR);
}
