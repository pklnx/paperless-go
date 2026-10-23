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
    expect(auditQuery({ since: "", until: "2026-10-31", group: "", who: "@cli" }, "abc")).toBe("until=2026-10-31&actor=cli&cursor=abc");
    expect(auditQuery({ since: "", until: "", group: "", who: "@api_key" })).toBe("actor=api_key");
    expect(auditQuery({ since: "", until: "", group: "", who: "@system" })).toBe("actor=system");
    expect(auditQuery({ since: "", until: "", group: "", who: "bob" })).toBe("user=bob");
  });

  it("selects users named like an actor by name", () => {
    expect(auditQuery({ since: "", until: "", group: "", who: "cli" })).toBe("user=cli");
    expect(auditQuery({ since: "", until: "", group: "", who: "system" })).toBe("user=system");
    expect(auditQuery({ since: "", until: "", group: "", who: "api_key" })).toBe("user=api_key");
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
    expect(entryDetail(entry("document.search", { details: { q: "Rechnung tag:Steuer" } }), en)).toBe("“Rechnung tag:Steuer”");
    expect(entryDetail(entry("document.search", { details: { tag: ["x"] } }), en)).toBe("");
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

  it("counts the files of an import from the consume folder or the command line", () => {
    const en = messagesFor("en");
    const de = messagesFor("de");
    const consume = entry("document.consume", { actor: "system", user: undefined, details: { imported: 2, duplicates: 1, failed: 0, interrupted: true } });
    expect(entryDetail(consume, en)).toBe("2 files imported, 1 duplicate · interrupted");
    expect(entryDetail(consume, de)).toBe("2 Dateien importiert, 1 Duplikat · abgebrochen");
    const one = entry("document.consume", { actor: "system", details: { imported: 1, duplicates: 2, failed: 3 } });
    expect(entryDetail(one, en)).toBe("1 file imported, 2 duplicates, 3 files failed");
    expect(entryDetail(one, de)).toBe("1 Datei importiert, 2 Duplikate, 3 Dateien fehlgeschlagen");
    expect(entryDetail(entry("document.import", { actor: "cli", details: { path: "/srv/x", imported: 3, duplicates: 0, failed: 1 } }), en)).toBe(
      "3 files imported, 1 file failed",
    );
    const restored = entry("document.consume", { actor: "system", details: { imported: 0, duplicates: 1, restored: 2, failed: 0 } });
    expect(entryDetail(restored, en)).toBe("1 duplicate, 2 documents restored from the trash");
    expect(entryDetail(restored, de)).toBe("1 Duplikat, 2 Dokumente aus dem Papierkorb wiederhergestellt");
    expect(entryDetail(entry("document.import", { actor: "cli", details: { imported: 0, restored: 1 } }), en)).toBe("1 document restored from the trash");
    expect(entryDetail(entry("document.consume", { details: {} }), en)).toBe("");
    const none = (interrupted: unknown) => entry("document.consume", { details: { imported: 0, duplicates: 0, failed: 0, interrupted } });
    expect(entryDetail(none(true), en)).toBe("interrupted");
    expect(entryDetail(none(true), de)).toBe("abgebrochen");
    for (const v of [false, undefined, "yes", 1]) {
      expect(entryDetail(none(v), en)).toBe("");
      expect(entryDetail(entry("document.consume", { details: { imported: 1, interrupted: v } }), en)).toBe("1 file imported");
    }
    const cli = entry("document.import", { actor: "cli", details: { path: "/srv/x", imported: 3, duplicates: 0, failed: 1, interrupted: true } });
    expect(entryDetail(cli, en)).toBe("3 files imported, 1 file failed · interrupted");
    expect(entryDetail(cli, de)).toBe("3 Dateien importiert, 1 Datei fehlgeschlagen · abgebrochen");
    expect(en.auditAction("document.import")).toBe("Imported");
    expect(de.auditAction("document.import")).toBe("Importiert");
    expect(en.auditAction("document.consume")).toBe("Imported from the consume folder");
    expect(de.auditAction("document.consume")).toBe("Aus dem Eingangsordner importiert");
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

describe("the server as an actor", () => {
  it("is named in both languages", () => {
    expect(messagesFor("en").logServer).toBe("Server");
    expect(messagesFor("en").logByServer).toBe("by the server");
    expect(messagesFor("de").logServer).toBe("Server");
    expect(messagesFor("de").logByServer).toBe("vom Server");
  });
});
