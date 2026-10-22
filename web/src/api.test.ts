import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, UNAUTHORIZED_EVENT, ownsData, passkeyApi, profileApi, retryAfter, sessionApi, unknownPasskey, usersApi, type ManagedUser } from "./api";

const reply = (status: number, body?: unknown) =>
  vi
    .spyOn(globalThis, "fetch")
    .mockResolvedValue(new Response(body === undefined ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));

afterEach(() => vi.restoreAllMocks());

function listen() {
  const seen = vi.fn();
  window.addEventListener(UNAUTHORIZED_EVENT, seen);
  return { seen, stop: () => window.removeEventListener(UNAUTHORIZED_EVENT, seen) };
}

describe("session", () => {
  it("reads the logged-in user", async () => {
    reply(200, { user: { name: "patrick", admin: true, mustChangePassword: false } });
    expect(await sessionApi.get()).toEqual({ user: { name: "patrick", admin: true, mustChangePassword: false } });
  });
  it("reports a missing session and whether setup is needed", async () => {
    reply(401, { error: "login required", setupRequired: true });
    expect(await sessionApi.get()).toEqual({ user: null, setupRequired: true });
    reply(401, { error: "login required", setupRequired: false });
    expect(await sessionApi.get()).toEqual({ user: null, setupRequired: false });
  });
  it("reads where passkeys work", async () => {
    reply(401, { error: "login required", setupRequired: false, passkeyOrigin: "https://docs.example.test" });
    expect(await sessionApi.get()).toEqual({ user: null, setupRequired: false, passkeyOrigin: "https://docs.example.test" });
  });
  it("does not announce a failed passkey login", async () => {
    const { seen, stop } = listen();
    try {
      reply(401, { error: "the passkey was not accepted" });
      await expect(passkeyApi.finishLogin("t", {})).rejects.toBeInstanceOf(ApiError);
      expect(seen).not.toHaveBeenCalled();
    } finally {
      stop();
    }
  });
  it("recognizes an unknown passkey", () => {
    expect(unknownPasskey(new ApiError(401, "x", { unknownCredential: true, rpId: "a.test", credentialId: "AQ" }))).toEqual({ rpId: "a.test", credentialId: "AQ" });
    expect(unknownPasskey(new ApiError(401, "x", {}))).toBeNull();
    expect(unknownPasskey(new Error("x"))).toBeNull();
  });
  it("throws on other errors", async () => {
    reply(500, { error: "internal error" });
    await expect(sessionApi.get()).rejects.toThrow("internal error");
  });
  it("announces a 401 from other endpoints, but not a failed login", async () => {
    const { seen, stop } = listen();
    try {
      reply(401, { error: "login required" });
      await expect(usersApi.list()).rejects.toThrow("login required");
      expect(seen).toHaveBeenCalledTimes(1);
      reply(401, { error: "wrong user name or password" });
      await expect(sessionApi.login("a", "b")).rejects.toBeInstanceOf(ApiError);
      expect(seen).toHaveBeenCalledTimes(1);
    } finally {
      stop();
    }
  });
  it("announces a required password change", async () => {
    const { seen, stop } = listen();
    try {
      reply(403, { error: "choose your own password first", passwordChangeRequired: true });
      await expect(usersApi.list()).rejects.toBeInstanceOf(ApiError);
      expect(seen).toHaveBeenCalledTimes(1);
      reply(403, { error: "only admins can manage users" });
      await expect(usersApi.list()).rejects.toThrow("only admins");
      expect(seen).toHaveBeenCalledTimes(1);
    } finally {
      stop();
    }
  });
  it("announces a required 2FA setup", async () => {
    const { seen, stop } = listen();
    try {
      reply(403, { error: "set up two-factor authentication first", twoFactorSetupRequired: true });
      await expect(usersApi.list()).rejects.toBeInstanceOf(ApiError);
      expect(seen).toHaveBeenCalledTimes(1);
    } finally {
      stop();
    }
  });
  it("returns the challenge of the password step and sends the second step", async () => {
    reply(200, { twoFactorRequired: true, challenge: "c1" });
    expect(await sessionApi.login("a", "b")).toEqual({ twoFactorRequired: true, challenge: "c1" });
    const user = { name: "a", admin: false, mustChangePassword: false, twoFactorEnabled: true, twoFactorRequired: false };
    const fetch = reply(200, { user });
    expect(await sessionApi.login2FA("c1", "123456")).toEqual(user);
    expect(fetch.mock.calls.at(-1)![0]).toBe("/api/session/2fa");
    expect(JSON.parse(String(fetch.mock.calls.at(-1)![1]?.body))).toEqual({ challenge: "c1", code: "123456" });
  });
  it("reads the wait time after too many attempts", () => {
    expect(retryAfter(new ApiError(429, "x", { retryAfter: 120 }))).toBe(120);
    expect(retryAfter(new ApiError(429, "x", {}))).toBe(60);
    expect(retryAfter(new ApiError(401, "x", { retryAfter: 120 }))).toBeNull();
    expect(retryAfter(new Error("x"))).toBeNull();
  });
});

describe("users and profile", () => {
  it("creates a user and returns the generated password", async () => {
    const fetch = reply(201, { name: "kim", password: "abcde-fghjk-mnpqr-stuvw" });
    expect(await usersApi.create("kim", true)).toEqual({ name: "kim", password: "abcde-fghjk-mnpqr-stuvw" });
    const [path, init] = fetch.mock.calls[0]!;
    expect(path).toBe("/api/users");
    expect(init?.method).toBe("POST");
    expect(new Headers(init?.headers).get("Content-Type")).toBe("application/json");
    expect(JSON.parse(String(init?.body))).toEqual({ name: "kim", admin: true });
  });

  it("encodes user names in paths", async () => {
    const fetch = reply(204);
    await usersApi.setLocked("a.b", true);
    expect(fetch.mock.calls[0]![0]).toBe("/api/users/a.b");
    expect(JSON.parse(String(fetch.mock.calls[0]![1]?.body))).toEqual({ locked: true });
    await usersApi.remove("a/b");
    expect(fetch.mock.calls[1]![0]).toBe("/api/users/a%2Fb");
    expect(fetch.mock.calls[1]![1]?.method).toBe("DELETE");
  });

  it("resets a password, removing passkeys or not", async () => {
    const fetch = reply(200, { name: "kim", password: "p" });
    expect(await usersApi.resetPassword("kim", true)).toEqual({ name: "kim", password: "p" });
    expect(fetch.mock.calls[0]![0]).toBe("/api/users/kim/password");
    expect(JSON.parse(String(fetch.mock.calls[0]![1]?.body))).toEqual({ removePasskeys: true });
    reply(200, { name: "kim", password: "p" });
    await usersApi.resetPassword("kim", false);
    expect(JSON.parse(String(vi.mocked(globalThis.fetch).mock.calls.at(-1)![1]?.body))).toEqual({ removePasskeys: false });
  });

  it("removes a user's passkeys", async () => {
    const fetch = reply(204);
    await usersApi.removePasskeys("a b");
    expect(fetch.mock.calls[0]![0]).toBe("/api/users/a%20b/passkeys");
    expect(fetch.mock.calls[0]![1]?.method).toBe("DELETE");
  });

  it("sends the password change", async () => {
    const fetch = reply(204);
    await profileApi.changePassword("old", "new one");
    expect(fetch.mock.calls[0]![0]).toBe("/api/profile/password");
    expect(fetch.mock.calls[0]![1]?.method).toBe("PUT");
    expect(JSON.parse(String(fetch.mock.calls[0]![1]?.body))).toEqual({ current: "old", new: "new one" });
  });

  it("calls the 2FA endpoints", async () => {
    let fetch = reply(200, { recoveryCodes: ["a"] });
    expect(await profileApi.twoFactor.confirm("123456")).toEqual({ recoveryCodes: ["a"] });
    expect(fetch.mock.calls.at(-1)![0]).toBe("/api/profile/2fa/confirm");
    fetch = reply(204);
    await profileApi.twoFactor.disable("pw", "123456");
    expect(fetch.mock.calls.at(-1)![1]?.method).toBe("DELETE");
    expect(JSON.parse(String(fetch.mock.calls.at(-1)![1]?.body))).toEqual({ currentPassword: "pw", code: "123456" });
    fetch = reply(204);
    await usersApi.reset2FA("a b");
    expect(fetch.mock.calls.at(-1)![0]).toBe("/api/users/a%20b/2fa/reset");
  });

  it("tells whether a user owns data", () => {
    const u = (owned: Partial<ManagedUser["owned"]>): ManagedUser => ({
      name: "kim",
      admin: false,
      locked: false,
      mustChangePassword: false,
      twoFactorEnabled: false,
      passkeys: 0,
      owned: { documents: 0, tags: 0, correspondents: 0, documentTypes: 0, savedViews: 0, ...owned },
      createdAt: "",
      lastLoginAt: null,
      self: false,
    });
    expect(ownsData(u({}))).toBe(false);
    expect(ownsData(u({ savedViews: 1 }))).toBe(true);
  });
});
