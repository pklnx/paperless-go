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

/** The login state: a user, or none and whether the first admin is missing. */
export type SessionState = { user: User } | { user: null; setupRequired: boolean };

export const sessionApi = {
  get: async (signal?: AbortSignal): Promise<SessionState> => {
    const res = await fetch(sessionPath, { signal, headers: { Accept: "application/json" } });
    if (res.status === 401) {
      const err = await errorFrom(res);
      return { user: null, setupRequired: err.body.setupRequired === true };
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

/** A user as admins see them on the users page. */
export interface ManagedUser {
  name: string;
  admin: boolean;
  locked: boolean;
  mustChangePassword: boolean;
  twoFactorEnabled: boolean;
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
  resetPassword: async (name: string) => (await send<GeneratedPassword>("POST", `${userPath(name)}/password`))!,
  setAdmin: (name: string, admin: boolean) => send("PATCH", userPath(name), { admin }),
  setLocked: (name: string, locked: boolean) => send("PATCH", userPath(name), { locked }),
  remove: (name: string) => send("DELETE", userPath(name)),
  reset2FA: (name: string) => send("POST", `${userPath(name)}/2fa/reset`),
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
}

export const documentsApi = {
  /** The caller's newest documents (an admin's: everyone's), at most limit (the server caps it at 100), and the upload limit in bytes. */
  list: (limit: number, signal?: AbortSignal) =>
    getJSON<{ documents: DocumentSummary[]; maxUploadSize: number }>(`/api/documents?limit=${limit}`, signal),
  /** The download URL of a document's original file or archive PDF. */
  fileURL: (id: string, kind: "original" | "archive" = "original") => `/api/documents/${encodeURIComponent(id)}/file?kind=${kind}`,
};
