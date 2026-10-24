import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { App } from "./App";
import { firstAdminCommand } from "./Auth";
import { t } from "./i18n";

// The shell against a fake server: the login gate, the Carbon header and
// the two gate screens. The login page itself is tested in Auth.test.tsx.

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
/** A server with a session for alice that answers everything else with 200. */
const loggedIn = (user = alice) => stubServer((_, url) => (url === "/api/session" ? { status: 200, body: { user } } : { status: 200 }));

// Carbon's Header, named after the app (the detail's own <header> is a banner too).
const header = () => within(screen.getByRole("banner", { name: t.appName }));
const sidebar = () => within(screen.getByTestId("sidebar"));

beforeEach(() => {
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener: () => {}, removeEventListener: () => {} }));
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  localStorage.clear();
  document.documentElement.className = "";
});

describe("App", () => {
  it("shows a healthy server after login", async () => {
    loggedIn();
    render(<App />);
    await waitFor(() => expect(screen.getByRole("status").textContent).toBe(t.statusOk));
    // The user's name in the header and as the sidebar's profile button.
    expect(header().getByText("alice")).toBeTruthy();
    expect(sidebar().getByRole("button", { name: t.profile }).textContent).toBe("alice");
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

  it("logs in and out through the header", async () => {
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
    fireEvent.change(screen.getByLabelText(t.fieldPassword, { selector: "input" }), { target: { value: "correct horse battery" } });
    fireEvent.click(screen.getByRole("button", { name: t.logIn }));
    await screen.findByRole("button", { name: t.users });
    expect(requests.find((r) => r.method === "POST")?.body).toEqual({ username: "alice", password: "correct horse battery" });

    fireEvent.click(header().getByRole("button", { name: t.logOut }));
    await screen.findByLabelText(t.userName);
    expect(requests.filter((r) => r.method === "DELETE" && r.url === "/api/session")).toHaveLength(1);
  });

  it("asks for an own password first", async () => {
    stubServer((method, url) => {
      if (url === "/api/session") return { status: 200, body: { user: { ...alice, mustChangePassword: true } } };
      if (method === "PUT") return { status: 204 };
      return { status: 200 };
    });
    render(<App />);
    await screen.findByText(t.chooseOwnPassword);
    // No shell yet: the gate screen has no header.
    expect(screen.queryByRole("banner", { name: t.appName })).toBeNull();
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

describe("the header", () => {
  it("names the app with its mark, the menu button opens the navigation, and the name link lists all documents", async () => {
    loggedIn();
    render(<App />);
    await screen.findByRole("banner", { name: t.appName });
    const h = header();
    // The name links to "/" and carries the mark for the applied theme (light: no stored choice, no dark system).
    const name = h.getByRole("link", { name: t.appName });
    expect(name.getAttribute("href")).toBe("/");
    const mark = name.querySelector("img")!;
    expect(mark.getAttribute("alt")).toBe("");
    expect(mark.className).toContain("app-mark--light");
    // The menu button (hidden from md by layout.css) opens today's navigation sheet.
    const menu = h.getByRole("button", { name: t.openMenu });
    expect(menu.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(menu);
    expect(screen.getByRole("dialog", { name: t.navigation })).toBeTruthy();
    expect(menu.getAttribute("aria-expanded")).toBe("true");
    fireEvent.click(within(screen.getByRole("dialog", { name: t.navigation })).getByRole("button", { name: t.closeDialog }));
    expect(screen.queryByRole("dialog", { name: t.navigation })).toBeNull();
    // A plain click on the name lists all documents in the app; a modified click is the browser's.
    window.history.replaceState(null, "", "/?view=log");
    const modified = fireEvent.click(name, { ctrlKey: true });
    expect(modified).toBe(true);
    expect(window.location.search).toBe("?view=log");
    const plain = fireEvent.click(name);
    expect(plain).toBe(false);
    expect(window.location.search).toBe("");
  });

  it("switches the theme from the header, and the mark follows", async () => {
    loggedIn();
    render(<App />);
    await screen.findByRole("banner", { name: t.appName });
    const h = header();
    fireEvent.click(h.getByRole("button", { name: t.switchTheme("dark") }));
    expect(document.documentElement.classList.contains("cds--g100")).toBe(true);
    // The legacy views' dark: variants still need the dark class (until part 6 of #150).
    expect(document.documentElement.classList.contains("dark")).toBe(true);
    expect(h.getByRole("link", { name: t.appName }).querySelector("img")!.className).toContain("app-mark--dark");
    expect(localStorage.getItem("paperless-go.theme")).toBe("dark");
    fireEvent.click(h.getByRole("button", { name: t.switchTheme("light") }));
    expect(document.documentElement.classList.contains("cds--white")).toBe(true);
    expect(h.getByRole("link", { name: t.appName }).querySelector("img")!.className).toContain("app-mark--light");
  });

  it("logs out from the header after asking about unsaved changes", async () => {
    // confirmLeave() without an open document asks nothing; the logout request goes out.
    loggedIn();
    render(<App />);
    await screen.findByRole("banner", { name: t.appName });
    fireEvent.click(header().getByRole("button", { name: t.logOut }));
    await waitFor(() => expect(requests.filter((r) => r.method === "DELETE" && r.url === "/api/session")).toHaveLength(1));
  });
});
