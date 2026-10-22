// Types and calls for the JSON API served by `paperless-go serve`.

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
    /** The parsed JSON error body, for errors that carry more than a message. */
    readonly body: Record<string, unknown> = {},
  ) {
    super(message);
  }
}

/**
 * Dispatched on window when an API request answers 401 (the session ended
 * or was never there) or says the password must be changed or two-factor
 * login set up first. The login gate then checks the session again and
 * shows the login page or the setup.
 */
export const UNAUTHORIZED_EVENT = "paperless-go:unauthorized";

const sessionPath = "/api/session";

async function errorFrom(res: Response): Promise<ApiError> {
  let body: Record<string, unknown> = {};
  try {
    body = (await res.json()) as Record<string, unknown>;
  } catch {
    // not JSON
  }
  return new ApiError(res.status, typeof body.error === "string" ? body.error : res.statusText, body);
}

async function errorFor(path: string, res: Response): Promise<ApiError> {
  const err = await errorFrom(res);
  // A failed login is no reason to check the session: only other paths.
  if (
    (res.status === 401 && !path.startsWith(sessionPath)) ||
    err.body.passwordChangeRequired === true ||
    err.body.twoFactorSetupRequired === true
  ) {
    window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
  }
  return err;
}

async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  const res = await fetch(path, { signal, headers: { Accept: "application/json" } });
  if (!res.ok) throw await errorFor(path, res);
  return (await res.json()) as T;
}

// Write requests carry JSON; the server rejects anything else (CSRF guard).
async function send<T>(method: string, path: string, body?: unknown): Promise<T | null> {
  const res = await fetch(path, {
    method,
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await errorFor(path, res);
  return res.status === 204 ? null : ((await res.json()) as T);
}

export interface User {
  name: string;
  admin: boolean;
  /** Logged in with a generated password: must choose their own first. */
  mustChangePassword: boolean;
  /** Two-factor login is on for this user. */
  twoFactorEnabled: boolean;
  /** Two-factor login is required (admins, or everybody by configuration). */
  twoFactorRequired: boolean;
}

/** The answer to the password step: a user, or the challenge for the second step. */
export type LoginResult = { user: User } | { twoFactorRequired: true; challenge: string };

/**
 * The login state: a user, or none and whether the first admin is missing.
 * passkeyOrigin is where passkey logins work, if the server offers them.
 */
export type SessionState = { user: User } | { user: null; setupRequired: boolean; passkeyOrigin?: string };

export const sessionApi = {
  get: async (signal?: AbortSignal): Promise<SessionState> => {
    const res = await fetch(sessionPath, { signal, headers: { Accept: "application/json" } });
    if (res.status === 401) {
      const err = await errorFrom(res);
      const origin = err.body.passkeyOrigin;
      return { user: null, setupRequired: err.body.setupRequired === true, passkeyOrigin: typeof origin === "string" ? origin : undefined };
    }
    if (!res.ok) throw await errorFrom(res);
    return (await res.json()) as { user: User };
  },
  login: async (username: string, password: string): Promise<LoginResult> => (await send<LoginResult>("POST", sessionPath, { username, password }))!,
  /** The second step: a TOTP or recovery code for the challenge of the password step. */
  login2FA: async (challenge: string, code: string): Promise<User> =>
    (await send<{ user: User }>("POST", `${sessionPath}/2fa`, { challenge, code }))!.user,
  logout: () => send("DELETE", sessionPath),
};

/** Seconds to wait after too many failed attempts, from a 429 answer. */
export function retryAfter(err: unknown): number | null {
  if (!(err instanceof ApiError) || err.status !== 429) return null;
  const s = err.body.retryAfter;
  return typeof s === "number" && s > 0 ? s : 60;
}

/** The user's 2FA state; the secret, URI and QR code only right after a setup started. */
export interface TwoFactorState {
  enabled: boolean;
  required: boolean;
  admin: boolean;
  setupPending: boolean;
  recoveryCodesLeft: number;
  secret?: string;
  otpauthUri?: string;
  qrDataUrl?: string;
}

export const profileApi = {
  changePassword: (current: string, next: string) => send("PUT", "/api/profile/password", { current, new: next }),
  twoFactor: {
    get: () => getJSON<TwoFactorState>("/api/profile/2fa"),
    setup: async () => (await send<TwoFactorState>("POST", "/api/profile/2fa/setup"))!,
    confirm: async (code: string) => (await send<{ recoveryCodes: string[] }>("POST", "/api/profile/2fa/confirm", { code }))!,
    disable: (currentPassword: string, code: string) => send("DELETE", "/api/profile/2fa", { currentPassword, code }),
    regenerateRecoveryCodes: async (code: string) => (await send<{ recoveryCodes: string[] }>("POST", "/api/profile/2fa/recovery-codes", { code }))!,
  },
};

/** A ceremony the server started: publicKey goes to navigator.credentials, token back with the answer. */
export interface Ceremony {
  token: string;
  publicKey: Record<string, unknown>;
}

/** A registered passkey of the logged-in user. */
export interface Passkey {
  id: number;
  name: string;
  createdAt: string;
  lastUsedAt: string | null;
}

export interface PasskeyList {
  /** False without PAPERLESS_GO_PUBLIC_URL on the server. */
  available: boolean;
  /** Where passkeys work, like https://docs.example.ts.net. */
  origin?: string;
  max: number;
  passkeys: Passkey[];
}

/** The server's answer for a passkey it does not know. */
export function unknownPasskey(err: unknown): { rpId: string; credentialId: string } | null {
  if (!(err instanceof ApiError) || err.body.unknownCredential !== true) return null;
  const { rpId, credentialId } = err.body;
  return typeof rpId === "string" && typeof credentialId === "string" ? { rpId, credentialId } : null;
}

export const passkeyApi = {
  beginLogin: async () => (await send<Ceremony>("POST", `${sessionPath}/passkey/begin`))!,
  finishLogin: async (token: string, credential: unknown) =>
    (await send<{ user: User }>("POST", `${sessionPath}/passkey/finish`, { token, credential }))!.user,
  list: () => getJSON<PasskeyList>("/api/profile/passkeys"),
  beginRegistration: async (name: string, currentPassword: string, code: string) =>
    (await send<Ceremony>("POST", "/api/profile/passkeys/begin", { name, currentPassword, code }))!,
  finishRegistration: (token: string, credential: unknown) => send("POST", "/api/profile/passkeys/finish", { token, credential }),
  remove: async (id: number) => (await send<{ credentialId: string; rpId?: string }>("DELETE", `/api/profile/passkeys/${id}`))!,
};

export type ApiKeyScope = "read" | "write";

/** An API key as listed: never the key itself. */
export interface ApiKey {
  id: number;
  /** Only in the admin list. */
  user?: string;
  name: string;
  /** The last 4 characters of the key. */
  hint: string;
  scopes: ApiKeyScope[];
  createdAt: string;
  expiresAt: string | null;
  lastUsedAt: string | null;
  revokedAt: string | null;
  state: "active" | "expired" | "revoked";
}

export interface ApiKeyList {
  max: number;
  keys: ApiKey[];
}

/** A key just created: the key itself is in this answer only. */
export type NewApiKey = ApiKey & { key: string };

export interface ApiKeyInput {
  name: string;
  scopes: ApiKeyScope[];
  /** Days until the key expires; null for never. */
  expiresInDays: number | null;
  currentPassword: string;
  code: string;
}

export const apiKeyApi = {
  list: () => getJSON<ApiKeyList>("/api/profile/api-keys"),
  create: async (input: ApiKeyInput) => (await send<NewApiKey>("POST", "/api/profile/api-keys", input))!,
  revoke: (id: number) => send("DELETE", `/api/profile/api-keys/${id}`),
  /** Every user's keys, for admins. */
  listAll: async () => (await getJSON<{ keys: ApiKey[] }>("/api/api-keys")).keys,
  revokeAny: (id: number) => send("DELETE", `/api/api-keys/${id}`),
};

/** A user as admins see them on the users page. */
export interface ManagedUser {
  name: string;
  admin: boolean;
  locked: boolean;
  mustChangePassword: boolean;
  twoFactorEnabled: boolean;
  passkeys: number;
  /** Keys that are neither revoked nor expired. */
  apiKeys: number;
  /** The rows the user owns; any of them keeps the user from being removed. */
  owned: { documents: number; tags: number; correspondents: number; documentTypes: number; savedViews: number };
  createdAt: string;
  lastLoginAt: string | null;
  /** The logged-in admin, whose own row cannot be changed there. */
  self: boolean;
}

/** Whether a user owns anything that keeps them from being removed. */
export function ownsData(u: ManagedUser): boolean {
  return Object.values(u.owned).some((n) => n > 0);
}

/** A generated password, shown once. */
export interface GeneratedPassword {
  name: string;
  password: string;
}

const userPath = (name: string) => `/api/users/${encodeURIComponent(name)}`;

export const usersApi = {
  list: async (signal?: AbortSignal) => (await getJSON<{ users: ManagedUser[] }>("/api/users", signal)).users,
  create: async (name: string, admin: boolean) => (await send<GeneratedPassword>("POST", "/api/users", { name, admin }))!,
  /** A new password; the user's passkeys are removed and API keys revoked unless told otherwise. */
  resetPassword: async (name: string, removePasskeys: boolean, revokeApiKeys: boolean) =>
    (await send<GeneratedPassword>("POST", `${userPath(name)}/password`, { removePasskeys, revokeApiKeys }))!,
  setAdmin: (name: string, admin: boolean) => send("PATCH", userPath(name), { admin }),
  setLocked: (name: string, locked: boolean) => send("PATCH", userPath(name), { locked }),
  remove: (name: string) => send("DELETE", userPath(name)),
  reset2FA: (name: string) => send("POST", `${userPath(name)}/2fa/reset`),
  removePasskeys: (name: string) => send("DELETE", `${userPath(name)}/passkeys`),
};

/** One entry of the audit log, as GET /api/audit lists it. */
export interface AuditEntry {
  id: number;
  at: string;
  actor: "session" | "api_key" | "cli";
  /** The user's name at the time. */
  user?: string;
  /** The name of the API key, for actor api_key. */
  apiKey?: string;
  action: string;
  target: { type: "document" | "user" | "api_key"; id: string; name?: string } | null;
  clientIp?: string;
  userAgent?: string;
  details: Record<string, unknown>;
  /** Set for a user who is not an admin on an entry about them made by an admin or the command line. */
  byOther?: boolean;
}

/** The filter of the log page. who is a user name, "cli" or "api_key" (admins only). */
export interface AuditFilter {
  since: string;
  until: string;
  group: string;
  who: string;
}

/** The query string of GET /api/audit for a filter and a page cursor. */
export function auditQuery(f: AuditFilter, cursor = ""): string {
  const p = new URLSearchParams();
  if (f.since) p.set("since", f.since);
  if (f.until) p.set("until", f.until);
  if (f.group) p.set("action", f.group);
  if (f.who === "cli" || f.who === "api_key") p.set("actor", f.who);
  else if (f.who) p.set("user", f.who);
  if (cursor) p.set("cursor", cursor);
  return p.toString();
}

export const auditApi = {
  list: (f: AuditFilter, cursor: string, signal?: AbortSignal) =>
    getJSON<{ entries: AuditEntry[]; nextCursor?: string }>(`/api/audit?${auditQuery(f, cursor)}`, signal),
};

/** A document as GET /api/documents lists it (the fields the UI uses). */
export interface DocumentSummary {
  id: string;
  title: string;
  createdAt: string;
  documentDate: string;
  originalFileName: string;
  originalMime: string;
  originalSize: number;
  hasArchive: boolean;
  /** The processing status; missing only when the server could not read it after an upload. */
  processing?: Processing;
  /** The thumbnail's URL (with its version), or null until processing made one. */
  thumbnailUrl?: string | null;
  /**
   * Only in a search with words: up to two fragments of the text, each
   * match between SNIPPET_START and SNIPPET_STOP. Plain text, never HTML.
   */
  snippet?: string;
}

/**
 * A document's processing status: the state of its newest job. error is
 * the fixed message of the last failed attempt (a failed job's reason, or
 * why a queued one runs again at retryAt).
 */
export interface Processing {
  state: "queued" | "running" | "done" | "failed";
  error?: string;
  attempts: number;
  maxAttempts: number;
  retryAt?: string;
}

/** Whether processing of d has not finished yet (queued or running). */
export const processingPending = (d: DocumentSummary) => d.processing?.state === "queued" || d.processing?.state === "running";

/** A page of GET /api/documents; nextCursor is missing on the last page. */
export interface DocumentPage {
  documents: DocumentSummary[];
  nextCursor?: string;
  maxUploadSize: number;
}

/** The query string of a search: the query, the page size, the browser's time zone (for date: and added:) and the cursor. */
export function searchQuery(q: string, limit: number, cursor = "", timeZone = browserTimeZone()): string {
  const p = new URLSearchParams({ q, limit: String(limit) });
  if (timeZone) p.set("tz", timeZone);
  if (cursor) p.set("cursor", cursor);
  return p.toString();
}

/** The browser's IANA time zone, or "" if it has none (the server then uses UTC). */
export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone ?? "";
  } catch {
    return "";
  }
}

export const documentsApi = {
  /** The caller's newest documents (an admin's: everyone's), at most limit (the server caps it at 100), and the upload limit in bytes. */
  list: (limit: number, signal?: AbortSignal) => getJSON<DocumentPage>(`/api/documents?limit=${limit}`, signal),
  /** One page of a search (docs/guide/search.md), best match first; cursor continues after the previous page. */
  search: (q: string, limit: number, cursor = "", signal?: AbortSignal) => getJSON<DocumentPage>(`/api/documents?${searchQuery(q, limit, cursor)}`, signal),
  /** The download URL of a document's original file or archive PDF. */
  fileURL: (id: string, kind: "original" | "archive" = "original") => `/api/documents/${encodeURIComponent(id)}/file?kind=${kind}`,
  /** Queues a document for processing again (Retry); answers the document with its new status. */
  reprocess: async (id: string): Promise<DocumentSummary> =>
    (await send<DocumentSummary>("POST", `/api/documents/${encodeURIComponent(id)}/reprocess`, {}))!,
};
