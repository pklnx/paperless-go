import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App } from "./App";
import { firstAdminCommand } from "./Auth";
import { t } from "./i18n";

type Reply = { status: number; body?: unknown };
type Handler = (method: string, url: string, body: unknown) => Reply;

let requests: { method: string; url: string; body: unknown }[];

function stubServer(handler: Handler) {
  requests = [];
  vi.stubGlobal("fetch", async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    requests.push({ method, url, body });
    const r = handler(method, url, body);
    return new Response(r.body === undefined ? null : JSON.stringify(r.body), {
      status: r.status,
      headers: { "Content-Type": "application/json" },
    });
  });
}

const alice = { name: "alice", admin: false, mustChangePassword: false };
const loggedOut: Reply = { status: 401, body: { error: "login required", setupRequired: false } };

beforeEach(() => {
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener: () => {}, removeEventListener: () => {} }));
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("App", () => {
  it("shows a healthy server after login", async () => {
    stubServer((_, url) => (url === "/api/session" ? { status: 200, body: { user: alice } } : { status: 200 }));
    render(<App />);
    await waitFor(() => expect(screen.getByRole("status").textContent).toBe(t.statusOk));
    expect(screen.getByText("alice")).toBeTruthy();
    // Not an admin: no users page, but the own log.
    expect(screen.queryByRole("button", { name: t.users })).toBeNull();
    expect(screen.getByRole("button", { name: t.log })).toBeTruthy();
  });

  it("shows a server without database", async () => {
    stubServer((_, url) => (url === "/api/session" ? { status: 200, body: { user: alice } } : { status: 503 }));
    render(<App />);
    await waitFor(() => expect(screen.getByRole("status").textContent).toBe(t.statusDown));
  });

  it("explains how to create the first admin", async () => {
    stubServer(() => ({ status: 401, body: { error: "login required", setupRequired: true } }));
    render(<App />);
    await waitFor(() => expect(screen.getByText(t.setupTitle)).toBeTruthy());
    expect(screen.getByText(firstAdminCommand)).toBeTruthy();
    expect(firstAdminCommand).toBe("./pgo user add NAME --admin");
  });

  it("logs in and out", async () => {
    let session: Reply = loggedOut;
    stubServer((method, url) => {
      if (url === "/api/session" && method === "POST") {
        session = { status: 200, body: { user: { ...alice, admin: true } } };
        return session;
      }
      if (url === "/api/session" && method === "DELETE") {
        session = loggedOut;
        return { status: 204 };
      }
      if (url === "/api/session") return session;
      return { status: 200 };
    });
    render(<App />);
    fireEvent.change(await screen.findByLabelText(t.userName), { target: { value: "alice" } });
    fireEvent.change(screen.getByLabelText(t.fieldPassword), { target: { value: "correct horse battery" } });
    fireEvent.click(screen.getByRole("button", { name: t.logIn }));
    await screen.findByRole("button", { name: t.users });
    expect(requests.find((r) => r.method === "POST")?.body).toEqual({ username: "alice", password: "correct horse battery" });

    fireEvent.click(screen.getByRole("button", { name: t.logOut }));
    await screen.findByLabelText(t.userName);
  });

  it("shows why a login failed", async () => {
    stubServer((method) => (method === "POST" ? { status: 401, body: { error: "wrong user name or password" } } : loggedOut));
    render(<App />);
    fireEvent.change(await screen.findByLabelText(t.userName), { target: { value: "alice" } });
    fireEvent.change(screen.getByLabelText(t.fieldPassword), { target: { value: "wrong" } });
    fireEvent.click(screen.getByRole("button", { name: t.logIn }));
    expect((await screen.findByRole("alert")).textContent).toBe(t.wrongLogin);
    expect((screen.getByLabelText(t.fieldPassword) as HTMLInputElement).value).toBe("");
  });

  it("shows the wait after too many attempts", async () => {
    stubServer((method) => (method === "POST" ? { status: 429, body: { error: "too many", retryAfter: 60 } } : loggedOut));
    render(<App />);
    fireEvent.change(await screen.findByLabelText(t.userName), { target: { value: "alice" } });
    fireEvent.change(screen.getByLabelText(t.fieldPassword), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: t.logIn }));
    expect((await screen.findByRole("alert")).textContent).toBe(t.tooManyAttempts(60));
  });

  it("asks for an own password first", async () => {
    stubServer((method, url) => {
      if (url === "/api/session") return { status: 200, body: { user: { ...alice, mustChangePassword: true } } };
      if (method === "PUT") return { status: 204 };
      return { status: 200 };
    });
    render(<App />);
    await screen.findByText(t.chooseOwnPassword);
    fireEvent.change(screen.getByLabelText(t.currentPassword), { target: { value: "generated-password" } });
    fireEvent.change(screen.getByLabelText(t.newPassword), { target: { value: "my own passphrase" } });
    fireEvent.change(screen.getByLabelText(t.repeatPassword), { target: { value: "my own passphrase" } });
    fireEvent.click(screen.getByRole("button", { name: t.changePassword }));
    await screen.findByRole("button", { name: t.profile });
    expect(requests.find((r) => r.method === "PUT")).toEqual({
      method: "PUT",
      url: "/api/profile/password",
      body: { current: "generated-password", new: "my own passphrase" },
    });
  });

  it("asks for the second factor after the password", async () => {
    let session: Reply = loggedOut;
    stubServer((method, url, body) => {
      if (url === "/api/session" && method === "POST") return { status: 200, body: { twoFactorRequired: true, challenge: "ch1" } };
      if (url === "/api/session/2fa") {
        const code = (body as { code: string }).code;
        if (code === "000000") return { status: 401, body: { error: "invalid two-factor code" } };
        if (code === "111111") return { status: 401, body: { error: "the login has expired; log in again" } };
        session = { status: 200, body: { user: { ...alice, twoFactorEnabled: true } } };
        return session;
      }
      if (url === "/api/session") return session;
      return { status: 200 };
    });
    render(<App />);
    const logIn = async () => {
      fireEvent.change(await screen.findByLabelText(t.userName), { target: { value: "alice" } });
      fireEvent.change(screen.getByLabelText(t.fieldPassword), { target: { value: "correct horse battery" } });
      fireEvent.click(screen.getByRole("button", { name: t.logIn }));
      await screen.findByText(t.twoFactorLogin);
    };
    await logIn();
    // The code field never shows the password.
    expect((screen.getByLabelText(t.twoFactorCode) as HTMLInputElement).value).toBe("");
    fireEvent.change(screen.getByLabelText(t.twoFactorCode), { target: { value: "000000" } });
    fireEvent.click(screen.getByRole("button", { name: t.logIn }));
    expect((await screen.findByRole("alert")).textContent).toBe(t.invalidTwoFactor);

    // An expired login goes back to the password.
    fireEvent.change(screen.getByLabelText(t.twoFactorCode), { target: { value: "111111" } });
    fireEvent.click(screen.getByRole("button", { name: t.logIn }));
    expect((await screen.findByRole("alert")).textContent).toBe(t.twoFactorExpired);
    expect(screen.getByLabelText(t.fieldPassword)).toBeTruthy();

    await logIn();
    fireEvent.change(screen.getByLabelText(t.twoFactorCode), { target: { value: "abcd-efgh-jkmn-pqrs" } });
    fireEvent.click(screen.getByRole("button", { name: t.logIn }));
    await screen.findByRole("button", { name: t.profile });
    expect(requests.filter((r) => r.url === "/api/session/2fa").at(-1)?.body).toEqual({ challenge: "ch1", code: "abcd-efgh-jkmn-pqrs" });
  });

  it("makes an admin without 2FA set it up first", async () => {
    const admin = { ...alice, admin: true, twoFactorRequired: true, twoFactorEnabled: false };
    let session: Reply = { status: 200, body: { user: admin } };
    const codes = Array.from({ length: 10 }, (_, i) => `aaaa-bbbb-cccc-ddd${i}`);
    stubServer((_, url) => {
      if (url === "/api/session") return session;
      if (url === "/api/profile/2fa") return { status: 200, body: { enabled: false, required: true, admin: true, setupPending: false, recoveryCodesLeft: 0 } };
      if (url === "/api/profile/2fa/setup")
        return { status: 200, body: { enabled: false, required: true, admin: true, setupPending: true, recoveryCodesLeft: 0, secret: "SECRETKEY", qrDataUrl: "data:image/png;base64,AA" } };
      if (url === "/api/profile/2fa/confirm") {
        session = { status: 200, body: { user: { ...admin, twoFactorEnabled: true } } };
        return { status: 200, body: { recoveryCodes: codes } };
      }
      return { status: 200 };
    });
    render(<App />);
    await screen.findByText(t.twoFactorRequired);
    expect(screen.queryByRole("button", { name: t.users })).toBeNull();
    fireEvent.click(await screen.findByRole("button", { name: t.twoFactorSetup }));
    expect(await screen.findByText("SECRETKEY")).toBeTruthy();
    expect(screen.getByAltText(t.twoFactorQR)).toBeTruthy();
    fireEvent.change(screen.getByLabelText(t.twoFactorCode), { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: t.twoFactorConfirm }));
    // The codes are shown once; only "saved" goes on.
    expect((await screen.findByLabelText(t.twoFactorRecovery)).textContent).toBe(codes.join("\n"));
    fireEvent.click(screen.getByRole("button", { name: t.twoFactorCodesSaved }));
    await screen.findByRole("button", { name: t.users });
    expect(requests.find((r) => r.url === "/api/profile/2fa/confirm")?.body).toEqual({ code: "123456" });
  });

  it("returns to the login page when the session ends", async () => {
    let session: Reply = { status: 200, body: { user: { ...alice, admin: true } } };
    stubServer((_, url) => {
      if (url === "/api/session") return session;
      if (url === "/api/users") {
        session = loggedOut;
        return { status: 401, body: { error: "login required" } };
      }
      return { status: 200 };
    });
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: t.users }));
    await screen.findByLabelText(t.userName);
  });
});
