import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { AuditEntry } from "./api";
import { t } from "./i18n";
import { LogPage } from "./LogPage";

const entries: AuditEntry[] = [
  {
    id: 3,
    at: "2026-10-21T10:02:00Z",
    actor: "cli",
    action: "user.lock",
    target: { type: "user", id: "7", name: "bob" },
    details: { operator: "patrick", os_user: "nonroot", command: "paperless-go user lock" },
  },
  {
    id: 2,
    at: "2026-10-21T10:01:00Z",
    actor: "session",
    action: "auth.login_failed",
    target: null,
    clientIp: "192.0.2.1",
    details: { method: "password", reason: "unknown_user", name: "nobody" },
  },
  {
    id: 1,
    at: "2026-10-21T10:00:00Z",
    actor: "session",
    user: "alice",
    action: "auth.login",
    target: null,
    clientIp: "192.0.2.1",
    userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:141.0) Gecko/20100101 Firefox/141.0",
    details: { method: "password" },
  },
];

let urls: string[];

function stubServer(pages: Record<string, { entries: AuditEntry[]; nextCursor?: string }>) {
  urls = [];
  vi.stubGlobal("fetch", async (url: string) => {
    urls.push(url);
    const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    if (url === "/api/users") return json({ users: [{ name: "alice" }, { name: "bob" }] });
    const cursor = new URL(url, "http://x").searchParams.get("cursor") ?? "";
    return json(pages[cursor] ?? { entries: [] });
  });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("LogPage", () => {
  it("shows the admin every entry with who, target and client, and pages on", async () => {
    stubServer({ "": { entries: entries.slice(0, 2), nextCursor: "c1" }, c1: { entries: entries.slice(2) } });
    render(<LogPage admin />);
    expect(await screen.findByText(t.auditAction("user.lock"))).toBeTruthy();
    expect(screen.getByText(new RegExp(t.logCLIBy("patrick").replace(/[()]/g, "\\$&")))).toBeTruthy();
    expect(screen.getByText(/bob/, { selector: "span" })).toBeTruthy();
    expect(screen.getByText(`${t.logTriedName("nobody")}, ${t.auditCode("password")}, ${t.auditCode("unknown_user")}`)).toBeTruthy();
    expect(screen.getByText(t.logWho)).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: t.logMore }));
    expect(await screen.findByText("192.0.2.1 · Firefox 141 · Windows")).toBeTruthy();
    expect(screen.getByText(/· alice/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: t.logMore })).toBeNull();
    expect(urls.some((u) => u.includes("cursor=c1"))).toBe(true);
  });

  it("filters by action and person", async () => {
    stubServer({ "": { entries: [] } });
    render(<LogPage admin />);
    expect(await screen.findByText(t.logEmpty)).toBeTruthy();
    fireEvent.change(screen.getByLabelText(t.logAction), { target: { value: "users" } });
    await vi.waitFor(() => expect(urls.some((u) => u.includes("action=users"))).toBe(true));
    await screen.findByRole("option", { name: "bob" });
    fireEvent.change(screen.getByLabelText(t.logWho), { target: { value: "bob" } });
    await vi.waitFor(() => expect(urls.some((u) => u.includes("user=bob"))).toBe(true));
    fireEvent.change(screen.getByLabelText(t.logWho), { target: { value: "cli" } });
    await vi.waitFor(() => expect(urls.some((u) => u.includes("actor=cli"))).toBe(true));
  });

  it("shows a user their own entries without the person filter", async () => {
    stubServer({ "": { entries: entries.slice(2) } });
    render(<LogPage admin={false} />);
    expect(await screen.findByText(t.auditAction("auth.login"))).toBeTruthy();
    expect(screen.getByText(t.logOwnNote)).toBeTruthy();
    expect(screen.queryByText(t.logWho)).toBeNull();
    // Their own name is not repeated on every row.
    expect(screen.queryByText(/· alice/)).toBeNull();
    expect(urls).not.toContain("/api/users");
  });
});
