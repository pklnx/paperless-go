// A fake server for tests of the logged-in shell: fetch is stubbed, every
// request is recorded with its body, and the answers come from `server`,
// which each test changes. Nothing reaches the network.
import { vi } from "vitest";
import type { DocumentSummary, NamedRow, Tag } from "./api";
import { stubXHR } from "./fakeXHR";

export const doc = (id: string, title: string, more: Partial<DocumentSummary> = {}): DocumentSummary => ({
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
  ownerId: 1,
  ...more,
});

export const tag = (id: string, name: string, parentId: string | null = null, more: Partial<Tag> = {}): Tag => ({
  id,
  name,
  parentId,
  ownerId: 1,
  color: "",
  documentCount: 1,
  subtreeCount: 1,
  ...more,
});

export const named = (id: string, name: string, documentCount = 1, ownerId = 1): NamedRow => ({ id, name, ownerId, documentCount });

export type Reply = { status?: number; body?: unknown };

export interface Server {
  user: { name: string; admin: boolean };
  tags: Tag[];
  correspondents: NamedRow[];
  types: NamedRow[];
  counts: { inbox: number; favorites: number; total: number };
  /** The answer to GET /api/documents for its parameters. */
  documents: (p: URLSearchParams) => Reply;
  /** The answer to POST /api/documents/bulk for its body. */
  bulk: (body: Record<string, unknown>) => Reply;
  /** The answer to PATCH or DELETE of a tag, correspondent or type. */
  metadata: (method: string, path: string, body: Record<string, unknown> | undefined) => Reply;
}

export interface Request {
  method: string;
  url: string;
  path: string;
  body: Record<string, unknown> | undefined;
}

export const json = (status: number, body: unknown) =>
  new Response(body === undefined ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

export const page = (documents: DocumentSummary[], nextCursor?: string): Reply => ({ body: { documents, nextCursor, maxUploadSize: 1000 } });

/** Stubs fetch with a fresh server; returns it and the list of requests. */
export function stubServer(): { server: Server; requests: Request[] } {
  window.history.replaceState(null, "", "/");
  localStorage.clear();
  stubXHR();
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener: () => {}, removeEventListener: () => {} }));
  const requests: Request[] = [];
  const server: Server = {
    user: { name: "alice", admin: false },
    tags: [],
    correspondents: [],
    types: [],
    counts: { inbox: 0, favorites: 0, total: 0 },
    documents: () => page([]),
    bulk: () => ({ body: { updated: 0 } }),
    metadata: (method) => ({ status: method === "DELETE" ? 204 : 200, body: method === "DELETE" ? undefined : {} }),
  };
  vi.stubGlobal("fetch", async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    const path = url.split("?")[0]!;
    const body = init?.body ? (JSON.parse(String(init.body)) as Record<string, unknown>) : undefined;
    requests.push({ method, url, path, body });
    const p = new URLSearchParams(url.includes("?") ? url.slice(url.indexOf("?") + 1) : "");
    const reply = (r: Reply) => json(r.status ?? 200, r.body);
    if (path === "/api/session") return json(200, { user: { mustChangePassword: false, twoFactorEnabled: false, twoFactorRequired: false, ...server.user } });
    if (path === "/healthz") return new Response("ok", { status: 200 });
    if (path === "/api/documents/counts") return json(200, server.counts);
    if (path === "/api/documents/bulk" && method === "POST") return reply(server.bulk(body ?? {}));
    if (method === "GET" && path === "/api/tags") return json(200, { tags: server.tags });
    if (method === "GET" && path === "/api/correspondents") return json(200, { correspondents: server.correspondents });
    if (method === "GET" && path === "/api/document-types") return json(200, { documentTypes: server.types });
    if (/^\/api\/(tags|correspondents|document-types)\/[^/]+$/.test(path) && (method === "PATCH" || method === "DELETE")) return reply(server.metadata(method, path, body));
    if (path === "/api/documents" && method === "GET") return reply(server.documents(p));
    return json(404, { error: "not found" });
  });
  return { server, requests };
}
