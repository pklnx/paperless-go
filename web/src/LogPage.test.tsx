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

function stubServer(pages: Record<string, { entries: AuditEntry[]; nextCursor?: string }>, userNames = ["alice", "bob"]) {
  urls = [];
  vi.stubGlobal("fetch", async (url: string) => {
    urls.push(url);
    const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    if (url === "/api/users") return json({ users: userNames.map((name) => ({ name })) });
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

  it("lists the people after the fixed entries in collator order", async () => {
    stubServer({ "": { entries: [] } }, ["b", "a.b", "a"]);
    render(<LogPage admin />);
    await vi.waitFor(() => expect(screen.getAllByRole("option", { name: "a.b" })).toHaveLength(1));
    const who = (screen.getByText(t.logWho).querySelector("select") as HTMLSelectElement).options;
    const names = Array.from(who, (o) => o.textContent);
    expect(names.slice(0, 4)).toEqual([t.logEveryone, t.logCommandLine, t.logApiKeys, t.logServer]);
    expect(names.slice(4)).toEqual(["a", "a.b", "b"]);
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
    fireEvent.change(screen.getByLabelText(t.logWho), { target: { value: "@cli" } });
    await vi.waitFor(() => expect(urls.some((u) => u.includes("actor=cli"))).toBe(true));
    fireEvent.change(screen.getByLabelText(t.logWho), { target: { value: "@system" } });
    await vi.waitFor(() => expect(urls.some((u) => u.includes("actor=system"))).toBe(true));
  });

  it("selects a user named like an actor as a user", async () => {
    stubServer({ "": { entries: [] } }, ["cli", "system"]);
    render(<LogPage admin />);
    await screen.findByRole("option", { name: "cli" });
    fireEvent.change(screen.getByLabelText(t.logWho), { target: { value: "cli" } });
    await vi.waitFor(() => expect(urls.some((u) => u.includes("user=cli"))).toBe(true));
    expect(urls.some((u) => u.includes("actor=cli"))).toBe(false);
    fireEvent.change(screen.getByLabelText(t.logWho), { target: { value: "system" } });
    await vi.waitFor(() => expect(urls.some((u) => u.includes("user=system"))).toBe(true));
    expect(urls.some((u) => u.includes("actor=system"))).toBe(false);
  });

  const consumeEntry = (byOther: boolean, interrupted = false): AuditEntry => ({
    id: 9,
    at: "2026-10-22T10:00:00Z",
    actor: "system",
    action: "document.consume",
    target: { type: "user", id: "2", name: "alice" },
    details: { imported: 2, duplicates: 1, failed: 0, ...(interrupted ? { interrupted: true } : {}) },
    ...(byOther ? { byOther: true } : {}),
  });

  it("shows the admin the server's entries with the user they are about", async () => {
    stubServer({ "": { entries: [consumeEntry(false)] } });
    render(<LogPage admin />);
    expect(await screen.findByText(t.auditAction("document.consume"))).toBeTruthy();
    expect(screen.getByText(`· ${t.logServer}`, { exact: false })).toBeTruthy();
    expect(screen.getByText(/· alice/)).toBeTruthy();
    expect(screen.getByText(`${t.logImported(2)}, ${t.logDuplicates(1)}`)).toBeTruthy();
    expect(screen.queryByText(new RegExp(t.logByServer))).toBeNull();
  });

  it("shows an interrupted scan to the admin and to the user, and no hint otherwise", async () => {
    const hint = `${t.logImported(2)}, ${t.logDuplicates(1)} · ${t.logInterrupted}`;
    stubServer({ "": { entries: [consumeEntry(false, true)] } });
    const admin = render(<LogPage admin />);
    expect(await screen.findByText(hint)).toBeTruthy();
    admin.unmount();
    stubServer({ "": { entries: [consumeEntry(true, true)] } });
    const owner = render(<LogPage admin={false} />);
    expect(await screen.findByText(hint)).toBeTruthy();
    owner.unmount();
    stubServer({ "": { entries: [consumeEntry(true)] } });
    render(<LogPage admin={false} />);
    expect(await screen.findByText(`${t.logImported(2)}, ${t.logDuplicates(1)}`)).toBeTruthy();
    expect(screen.queryByText(new RegExp(t.logInterrupted))).toBeNull();
  });

  it("tells a user the server imported files into their account", async () => {
    stubServer({ "": { entries: [consumeEntry(true)] } });
    render(<LogPage admin={false} />);
    expect(await screen.findByText(t.auditAction("document.consume"))).toBeTruthy();
    expect(screen.getByText(`· ${t.logByServer}`, { exact: false })).toBeTruthy();
    expect(screen.getByText(`${t.logImported(2)}, ${t.logDuplicates(1)}`)).toBeTruthy();
    expect(screen.queryByText(/alice/)).toBeNull();
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

  it("names who changed a user's account, without the target", async () => {
    stubServer({
      "": {
        entries: [
          {
            id: 5,
            at: "2026-10-21T10:05:00Z",
            actor: "session",
            user: "root",
            action: "user.lock",
            target: { type: "user", id: "2", name: "alice" },
            details: {},
            byOther: true,
          },
          {
            id: 4,
            at: "2026-10-21T10:04:00Z",
            actor: "cli",
            action: "user.password_set",
            target: { type: "user", id: "2", name: "alice" },
            details: {},
            byOther: true,
          },
        ],
      },
    });
    render(<LogPage admin={false} />);
    expect(await screen.findByText(t.auditAction("user.lock"))).toBeTruthy();
    expect(screen.getByText(t.logOwnNote)).toBeTruthy();
    expect(screen.getByText(`· ${t.logByAdmin("root")}`, { exact: false })).toBeTruthy();
    // The command line without an operator, and no "by" for it.
    expect(screen.getByText(`· ${t.logCLI}`, { exact: false })).toBeTruthy();
    expect(screen.queryAllByText(/by |von /).length).toBe(1);
    // The target is the user themself and is not shown.
    expect(screen.queryByText(/alice/)).toBeNull();
  });
});
