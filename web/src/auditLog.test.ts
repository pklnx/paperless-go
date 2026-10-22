import { describe, expect, it } from "vitest";
import { auditQuery, type AuditEntry } from "./api";
import { commandOperator, defaultFilter, entryDetail, shortUserAgent, targetLabel } from "./auditLog";
import { messagesFor } from "./i18n";

const entry = (action: string, more: Partial<AuditEntry> = {}): AuditEntry => ({
  id: 1,
  at: "2026-10-21T10:00:00Z",
  actor: "session",
  user: "alice",
  action,
  target: null,
  details: {},
  ...more,
});

describe("auditQuery", () => {
  it("maps the filter to the API's parameters", () => {
    expect(auditQuery({ since: "2026-10-01", until: "", group: "login", who: "" })).toBe("since=2026-10-01&action=login");
    expect(auditQuery({ since: "", until: "2026-10-31", group: "", who: "cli" }, "abc")).toBe("until=2026-10-31&actor=cli&cursor=abc");
    expect(auditQuery({ since: "", until: "", group: "", who: "bob" })).toBe("user=bob");
  });
});

describe("defaultFilter", () => {
  it("covers the last 30 days", () => {
    expect(defaultFilter(new Date("2026-10-21T12:00:00Z"))).toEqual({ since: "2026-09-21", until: "", group: "", who: "" });
  });
});

describe("entry texts", () => {
  it("names the target by name, else by ID", () => {
    expect(targetLabel(entry("user.lock", { target: { type: "user", id: "7", name: "bob" } }))).toBe("bob");
    expect(targetLabel(entry("user.lock", { target: { type: "user", id: "7" } }))).toBe("7");
    expect(targetLabel(entry("auth.login"))).toBe("");
  });

  it("shows an API key's scopes, owner and count", () => {
    const en = messagesFor("en");
    expect(entryDetail(entry("api_key.create", { details: { scopes: ["read", "write"] } }), en)).toBe("read, write");
    expect(entryDetail(entry("api_key.revoke", { details: { owner: "alice", reason: "user" } }), en)).toBe("user");
    expect(entryDetail(entry("api_key.revoke", { user: "root", details: { owner: "alice", reason: "admin" } }), en)).toBe("“alice”, admin");
    expect(entryDetail(entry("api_key.revoke", { actor: "cli", user: undefined, details: { apiKeysRevoked: 2, reason: "cli" } }), en)).toBe("2 API keys, command line");
    expect(entryDetail(entry("auth.login_failed", { details: { method: "api_key", reason: "revoked" } }), en)).toBe("API key, revoked");
  });

  it("shows a login's method, reason and tried name, translated", () => {
    const en = messagesFor("en");
    const de = messagesFor("de");
    const failed = entry("auth.login_failed", { details: { method: "password", reason: "unknown_user", name: "bob" } });
    expect(entryDetail(failed, en)).toBe("“bob”, password, unknown user");
    expect(entryDetail(failed, de)).toBe("„bob“, Passwort, unbekannter Benutzer");
    expect(entryDetail(entry("auth.login_failed", { details: { reason: "wrong_password" } }), de)).toBe("falsches Passwort");
    expect(entryDetail(entry("auth.login_failed", { details: { reason: "locked" } }), en)).toBe("user locked");
    expect(entryDetail(entry("auth.login", { details: { method: "password" } }), en)).toBe("password");
    expect(entryDetail(entry("user.add", { details: { admin: true } }), de)).toBe("Admin");
    expect(entryDetail(entry("user.add", { details: { admin: false } }), en)).toBe("");
    expect(entryDetail(entry("auth.login", { details: { method: "2fa", factor: "recovery_code" } }), en)).toBe("password and code, recovery code");
    expect(entryDetail(entry("auth.login_failed", { details: { method: "2fa", reason: "second_factor" } }), de)).toBe("Passwort und Code, falscher Code");
    expect(entryDetail(entry("profile.2fa_disable", { details: { factor: "totp" } }), en)).toBe("authenticator app");
    expect(entryDetail(entry("profile.2fa_enable"), en)).toBe("");
    // A code the UI does not know yet is shown as it is.
    expect(entryDetail(entry("auth.login", { details: { method: "fido-v9" } }), de)).toBe("fido-v9");
  });

  it("names passkeys", () => {
    const en = messagesFor("en");
    expect(entryDetail(entry("auth.login", { details: { method: "passkey", passkey: "Phone" } }), en)).toBe("passkey, “Phone”");
    expect(entryDetail(entry("auth.login_failed", { details: { method: "passkey", reason: "unknown_passkey" } }), en)).toBe("passkey, unknown passkey");
    expect(entryDetail(entry("profile.passkey_add", { details: { name: "Key" } }), en)).toBe("“Key”");
    expect(entryDetail(entry("user.passkeys_removed", { details: { count: 2 } }), en)).toBe("2 passkeys");
    expect(entryDetail(entry("user.password_reset", { details: { passkeysRemoved: 1 } }), en)).toBe("Remove passkeys: 1");
    expect(entryDetail(entry("user.password_reset", { details: { passkeysRemoved: 0 } }), en)).toBe("");
  });

  it("names the refused 2FA change and why", () => {
    const failed = entry("profile.2fa_failed", { details: { attempted: "profile.2fa_disable", reason: "wrong_password" } });
    expect(entryDetail(failed, messagesFor("en"))).toBe("Disabled 2FA, wrong password");
    expect(entryDetail(failed, messagesFor("de"))).toBe(`${messagesFor("de").auditAction("profile.2fa_disable")}, falsches Passwort`);
  });

  it("names the operator of a command", () => {
    expect(commandOperator(entry("user.add", { actor: "cli", details: { operator: "patrick", os_user: "nonroot" } }))).toBe("patrick");
    expect(commandOperator(entry("user.add", { actor: "cli", details: { os_user: "nonroot" } }))).toBe("nonroot");
    expect(commandOperator(entry("user.add", { actor: "cli" }))).toBe("");
  });
});

describe("shortUserAgent", () => {
  it("names browser and platform", () => {
    expect(shortUserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:141.0) Gecko/20100101 Firefox/141.0")).toBe("Firefox 141 · Windows");
    expect(
      shortUserAgent("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1"),
    ).toBe("Safari 18 · iPhone");
    expect(shortUserAgent("Go-http-client/1.1")).toBe("Go-http-client/1.1");
  });
});
