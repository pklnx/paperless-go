import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import type { ApiKey, ManagedUser } from "./api";
import { t } from "./i18n";
import { UsersPage } from "./UsersPage";

const none = { documents: 0, tags: 0, correspondents: 0, documentTypes: 0, savedViews: 0 };
const user = (name: string, extra: Partial<ManagedUser> = {}): ManagedUser => ({
  name,
  admin: false,
  locked: false,
  mustChangePassword: false,
  twoFactorEnabled: false,
  passkeys: 0,
  apiKeys: 0,
  owned: none,
  createdAt: "2026-10-21T10:00:00Z",
  lastLoginAt: null,
  self: false,
  ...extra,
});

let requests: { method: string; url: string; body: unknown }[];

function stubServer(users: ManagedUser[], keys: ApiKey[] = []) {
  requests = [];
  vi.stubGlobal("fetch", async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    requests.push({ method, url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
    if (url === "/api/users" && method === "GET") return json(200, { users });
    if (url === "/api/api-keys" && method === "GET") return json(200, { keys });
    if (url === "/api/users" && method === "POST") return json(201, { name: "new", password: "abcde-fghjk-mnpqr-stuvw" });
    return new Response(null, { status: 204 });
  });
  vi.stubGlobal("confirm", () => true);
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("UsersPage", () => {
  it("lists users; the own row has no actions, owners cannot be removed", async () => {
    stubServer([user("admin", { admin: true, self: true }), user("kim", { owned: { ...none, documents: 3 } }), user("lee", { locked: true })]);
    render(<UsersPage />);
    const rows = await screen.findAllByRole("listitem");
    expect(rows).toHaveLength(3);
    expect(within(rows[0]!).getByText(t.selfHint)).toBeTruthy();
    expect(within(rows[0]!).queryByRole("button")).toBeNull();

    expect((within(rows[1]!).getByRole("button", { name: t.removeUser }) as HTMLButtonElement).disabled).toBe(true);
    expect(within(rows[1]!).getByText(t.ownsData)).toBeTruthy();
    expect(within(rows[1]!).getByText(new RegExp(`${t.colDocuments}: 3`))).toBeTruthy();

    // A locked user can be unlocked and removed.
    fireEvent.click(within(rows[2]!).getByRole("button", { name: t.unlock }));
    await vi.waitFor(() => expect(requests.some((r) => r.method === "PATCH")).toBe(true));
    expect(requests.find((r) => r.method === "PATCH")).toEqual({ method: "PATCH", url: "/api/users/lee", body: { locked: false } });
    fireEvent.click(within(rows[2]!).getByRole("button", { name: t.removeUser }));
    await vi.waitFor(() => expect(requests.some((r) => r.method === "DELETE" && r.url === "/api/users/lee")).toBe(true));
  });

  it("shows 2FA and resets it after confirmation", async () => {
    stubServer([user("admin", { admin: true, self: true, twoFactorEnabled: true }), user("kim", { twoFactorEnabled: true }), user("lee")]);
    render(<UsersPage />);
    const rows = await screen.findAllByRole("listitem");
    expect(within(rows[1]!).getByText(t.twoFactorShortOn)).toBeTruthy();
    expect(within(rows[2]!).getByText(t.twoFactorShortOff)).toBeTruthy();
    expect(within(rows[2]!).queryByRole("button", { name: t.twoFactorReset })).toBeNull();
    fireEvent.click(within(rows[1]!).getByRole("button", { name: t.twoFactorReset }));
    await vi.waitFor(() => expect(requests.some((r) => r.method === "POST" && r.url === "/api/users/kim/2fa/reset")).toBe(true));
  });

  it("shows passkeys and removes them after confirmation", async () => {
    stubServer([user("admin", { admin: true, self: true }), user("kim", { passkeys: 2 }), user("lee")]);
    render(<UsersPage />);
    const rows = await screen.findAllByRole("listitem");
    expect(within(rows[1]!).getByText(t.passkeyCount(2))).toBeTruthy();
    expect(within(rows[2]!).queryByRole("button", { name: t.removePasskeys })).toBeNull();
    fireEvent.click(within(rows[1]!).getByRole("button", { name: t.removePasskeys }));
    await vi.waitFor(() => expect(requests.some((r) => r.method === "DELETE" && r.url === "/api/users/kim/passkeys")).toBe(true));
  });

  it("resets a password, removing passkeys unless unticked", async () => {
    stubServer([user("admin", { admin: true, self: true }), user("kim", { passkeys: 1 }), user("lee")]);
    render(<UsersPage />);
    const rows = await screen.findAllByRole("listitem");
    fireEvent.click(within(rows[1]!).getByRole("button", { name: t.resetPassword }));
    const box = within(rows[1]!).getByLabelText(t.resetAlsoPasskeys(1)) as HTMLInputElement;
    expect(box.checked).toBe(true);
    fireEvent.click(box);
    fireEvent.click(within(within(rows[1]!).getByRole("group")).getByRole("button", { name: t.confirmReset }));
    await vi.waitFor(() => expect(requests.some((r) => r.method === "POST" && r.url === "/api/users/kim/password")).toBe(true));
    expect(requests.find((r) => r.url === "/api/users/kim/password")?.body).toEqual({ removePasskeys: false, revokeApiKeys: true });

    // Without passkeys there is nothing to tick; the default stays.
    fireEvent.click(within(rows[2]!).getByRole("button", { name: t.resetPassword }));
    expect(within(rows[2]!).queryByRole("checkbox")).toBeNull();
    fireEvent.click(within(within(rows[2]!).getByRole("group")).getByRole("button", { name: t.confirmReset }));
    await vi.waitFor(() => expect(requests.some((r) => r.url === "/api/users/lee/password")).toBe(true));
    expect(requests.find((r) => r.url === "/api/users/lee/password")?.body).toEqual({ removePasskeys: true, revokeApiKeys: true });
  });

  it("revokes API keys on a password reset unless unticked", async () => {
    stubServer([user("admin", { admin: true, self: true }), user("kim", { apiKeys: 2 })]);
    render(<UsersPage />);
    const rows = await screen.findAllByRole("listitem");
    expect(within(rows[1]!).getByText(t.apiKeyCount(2))).toBeTruthy();
    fireEvent.click(within(rows[1]!).getByRole("button", { name: t.resetPassword }));
    const box = within(rows[1]!).getByLabelText(t.resetAlsoApiKeys(2)) as HTMLInputElement;
    expect(box.checked).toBe(true);
    fireEvent.click(box);
    fireEvent.click(within(within(rows[1]!).getByRole("group")).getByRole("button", { name: t.confirmReset }));
    await vi.waitFor(() => expect(requests.some((r) => r.url === "/api/users/kim/password")).toBe(true));
    expect(requests.find((r) => r.url === "/api/users/kim/password")?.body).toEqual({ removePasskeys: true, revokeApiKeys: false });
  });

  it("shows a user's API keys and revokes one", async () => {
    const key: ApiKey = {
      id: 7, user: "kim", name: "scanner", hint: "abcd", scopes: ["write"], createdAt: "2026-10-01T00:00:00Z",
      expiresAt: null, lastUsedAt: null, revokedAt: null, state: "active",
    };
    stubServer([user("admin", { admin: true, self: true }), user("kim", { apiKeys: 1 }), user("lee")], [key]);
    render(<UsersPage />);
    const rows = await screen.findAllByRole("listitem");
    expect(within(rows[2]!).queryByRole("button", { name: t.showApiKeys })).toBeNull();
    fireEvent.click(await within(rows[1]!).findByRole("button", { name: t.showApiKeys }));
    expect(within(rows[1]!).getByText("scanner")).toBeTruthy();
    fireEvent.click(within(rows[1]!).getByRole("button", { name: t.apiKeyRevoke }));
    await vi.waitFor(() => expect(requests.some((r) => r.method === "DELETE" && r.url === "/api/api-keys/7")).toBe(true));
  });

  it("adds a user and shows the generated password once", async () => {
    stubServer([user("admin", { admin: true, self: true })]);
    render(<UsersPage />);
    fireEvent.click(await screen.findByRole("button", { name: t.addUser }));
    fireEvent.change(screen.getByLabelText(t.userName), { target: { value: "new" } });
    fireEvent.click(screen.getByLabelText(t.userIsAdmin));
    fireEvent.click(screen.getByRole("button", { name: t.addUser }));
    expect(await screen.findByText("abcde-fghjk-mnpqr-stuvw")).toBeTruthy();
    expect(requests.find((r) => r.method === "POST")?.body).toEqual({ name: "new", admin: true });
    fireEvent.click(screen.getByRole("button", { name: t.done }));
    expect(screen.queryByText("abcde-fghjk-mnpqr-stuvw")).toBeNull();
  });
});
