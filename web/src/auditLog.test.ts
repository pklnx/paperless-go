import { describe, expect, it } from "vitest";
import { auditQuery, type AuditEntry } from "./api";
import { commandOperator, defaultFilter, entryDetail, shortUserAgent, targetLabel } from "./auditLog";

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

  it("shows a login's method, reason and tried name", () => {
    expect(entryDetail(entry("auth.login_failed", { details: { method: "password", reason: "unknown_user", name: "bob" } }))).toBe(
      "“bob”, password, unknown_user",
    );
    expect(entryDetail(entry("auth.login", { details: { method: "password" } }))).toBe("password");
    expect(entryDetail(entry("user.add", { details: { admin: true } }))).toBe("admin");
    expect(entryDetail(entry("user.add", { details: { admin: false } }))).toBe("");
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
