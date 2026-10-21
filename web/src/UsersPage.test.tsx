import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import type { ManagedUser } from "./api";
import { t } from "./i18n";
import { UsersPage } from "./UsersPage";

const none = { documents: 0, tags: 0, correspondents: 0, documentTypes: 0, savedViews: 0 };
const user = (name: string, extra: Partial<ManagedUser> = {}): ManagedUser => ({
  name,
  admin: false,
  locked: false,
  mustChangePassword: false,
  owned: none,
  createdAt: "2026-10-21T10:00:00Z",
  lastLoginAt: null,
  self: false,
  ...extra,
});

let requests: { method: string; url: string; body: unknown }[];

function stubServer(users: ManagedUser[]) {
  requests = [];
  vi.stubGlobal("fetch", async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    requests.push({ method, url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
    if (url === "/api/users" && method === "GET") return json(200, { users });
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
