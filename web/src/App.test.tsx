import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { App } from "./App";
import { firstAdminCommand } from "./Auth";
import { lg } from "./breakpoints";
import { t } from "./i18n";
import { dialogOpen } from "./dialogs";
import { doc, named, page, savedView, stubServer as stubApi, tag, type Server } from "./testServer";

// The shell against a fake server: the login gate, the Carbon header, the
// two gate screens, the server-down notification and the navigation (the
// SideNav and the column, as overlays below lg and as a rail and a column
// from lg up). The login page itself is tested in Auth.test.tsx.

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
/** The shell's SideNav (the lists and the pages). */
const shellNav = () => within(screen.getByRole("navigation", { name: t.navigation }));
/** The server-down notification, if shown. */
const serverDown = () => screen.queryByText(t.serverDownTitle)?.closest<HTMLElement>('[role="status"]') ?? null;

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
  it("shows the shell after login, and nothing about a healthy server", async () => {
    loggedIn();
    render(<App />);
    await screen.findByRole("banner", { name: t.appName });
    // The health check has answered: no notification for a healthy server (decision 11 of #150).
    await waitFor(() => expect(requests.some((r) => r.url === "/healthz")).toBe(true));
    await waitFor(() => expect(requests.filter((r) => r.url.startsWith("/api/documents/counts"))).not.toHaveLength(0));
    expect(serverDown()).toBeNull();
    // The user's name in the header; the pages in the SideNav. Not an admin: no users page, but the own log.
    expect(header().getByText("alice")).toBeTruthy();
    expect(shellNav().getByRole("link", { name: t.profile })).toBeTruthy();
    expect(shellNav().queryByRole("link", { name: t.users })).toBeNull();
    expect(shellNav().getByRole("link", { name: t.log })).toBeTruthy();
  });

  it("shows a notification under the header while the server is down", async () => {
    stubServer((_, url) => (url === "/api/session" ? { status: 200, body: { user: alice } } : { status: 503 }));
    render(<App />);
    await screen.findByText(t.serverDownTitle);
    const notification = serverDown()!;
    expect(notification).not.toBeNull();
    expect(notification.textContent).toContain(t.serverDownText);
    // Not a dialog: it takes no focus and has no close button.
    expect(within(notification).queryByRole("button")).toBeNull();
    expect(document.activeElement).toBe(document.body);
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
    await screen.findByRole("link", { name: t.users });
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
    await screen.findByRole("link", { name: t.profile });
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
    expect(screen.queryByRole("link", { name: t.users })).toBeNull();
    fireEvent.click(await screen.findByRole("button", { name: t.twoFactorSetup }));
    expect(await screen.findByText("SECRETKEY")).toBeTruthy();
    expect(screen.getByAltText(t.twoFactorQR)).toBeTruthy();
    fireEvent.change(screen.getByLabelText(t.twoFactorCode), { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: t.twoFactorConfirm }));
    // The codes are shown once; only "saved" goes on.
    expect((await screen.findByLabelText(t.twoFactorRecovery)).textContent).toBe(codes.join("\n"));
    fireEvent.click(screen.getByRole("button", { name: t.twoFactorCodesSaved }));
    await screen.findByRole("link", { name: t.users });
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
    fireEvent.click(await screen.findByRole("link", { name: t.users }));
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
    // The menu button (below lg; Carbon hides it from lg) opens the SideNav as an overlay; a second click closes it.
    const menu = h.getByRole("button", { name: t.openMenu });
    expect(menu.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(menu);
    const nav = screen.getByRole("navigation", { name: t.navigation });
    expect(nav.className).toContain("cds--side-nav--expanded");
    expect(menu.getAttribute("aria-expanded")).toBe("true");
    expect(menu.getAttribute("aria-controls")).toBe(nav.id);
    fireEvent.click(menu);
    expect(nav.className).not.toContain("cds--side-nav--expanded");
    // A plain click on the name lists all documents in the app; a modified click is the browser's.
    window.history.replaceState(null, "", "/?view=log");
    const modified = fireEvent.click(name, { ctrlKey: true });
    expect(modified).toBe(true);
    expect(window.location.search).toBe("?view=log");
    const plain = fireEvent.click(name);
    expect(plain).toBe(false);
    expect(window.location.search).toBe("");
  });

  it("opens the profile from the user's name in the header, and marks it there", async () => {
    loggedIn();
    render(<App />);
    await screen.findByRole("banner", { name: t.appName });
    // Named by its visible text and the page it opens (label in name).
    const me = header().getByRole("button", { name: `alice, ${t.profile}` });
    expect(me.textContent).toBe("alice");
    expect(me.getAttribute("aria-current")).toBeNull();
    fireEvent.click(me);
    expect(window.location.search).toBe("?view=profile");
    await screen.findByRole("heading", { name: t.profile });
    expect(me.getAttribute("aria-current")).toBe("page");
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

/** The listeners of the stubbed matchMedia with their query, told by resize(). */
let mediaListeners = new Set<{ query: string; fn: (e: { matches: boolean; media: string }) => void }>();
let wideNow = false;

/** matchMedia that answers Carbon's lg query (66rem) with wide; everything else is false. resize() changes the answer and tells the listeners. */
function screenWidth(wide: boolean) {
  wideNow = wide;
  mediaListeners = new Set();
  vi.stubGlobal("matchMedia", (query: string) => ({
    get matches() {
      return query === lg && wideNow;
    },
    media: query,
    addEventListener: (_: string, fn: (e: { matches: boolean; media: string }) => void) => mediaListeners.add({ query, fn }),
    removeEventListener: (_: string, fn: (e: { matches: boolean; media: string }) => void) => {
      for (const l of mediaListeners) if (l.fn === fn) mediaListeners.delete(l);
    },
  }));
}

/** The window crosses lg: the media query answers wide from now on, and every listener hears it (Carbon's too). */
function resize(wide: boolean) {
  wideNow = wide;
  act(() => mediaListeners.forEach(({ query, fn }) => fn({ matches: query === lg && wideNow, media: query })));
}

let api: Server;
let apiRequests: { method: string; url: string; path: string; body: Record<string, unknown> | undefined }[];

/** The logged-in shell against testServer's fake API, with tags, a correspondent and documents. */
async function renderShell(wide: boolean, url = "/", admin = false, prepare: (s: Server) => void = () => {}) {
  ({ server: api, requests: apiRequests } = stubApi());
  if (admin) api.user = { id: 1, name: "root", admin: true };
  api.tags = [tag("t1", "Steuer", null, { subtreeCount: 2 }), tag("t2", "2024", "t1")];
  api.correspondents = [named("c1", "Amt", 3)];
  api.documents = () => page([doc("d1", "Rechnung"), doc("d2", "Brief")]);
  prepare(api);
  screenWidth(wide);
  window.history.replaceState(null, "", url);
  render(<App />);
  await screen.findByRole("treeitem", { name: "Steuer 2" });
  await screen.findByRole("link", { name: "Rechnung" });
}

const menuButton = () => screen.getByRole("button", { name: t.openMenu });
const sideNav = () => screen.getByRole("navigation", { name: t.navigation });
const browseButton = () => screen.getByRole("button", { name: t.browse });
const browseDialog = () => screen.queryByRole("dialog", { name: t.browse });
const key = (el: Element, k: string) => fireEvent.keyDown(el, { key: k });

describe("phone navigation (below lg)", () => {
  it("opens the SideNav from the menu button, with focus in it, and closes it with Escape", async () => {
    await renderShell(false);
    const button = menuButton();
    expect(sideNav().hasAttribute("inert")).toBe(true);
    button.focus();
    fireEvent.click(button);
    const nav = sideNav();
    expect(nav.className).toContain("cds--side-nav--expanded");
    expect(button.getAttribute("aria-expanded")).toBe("true");
    expect(nav.hasAttribute("inert")).toBe(false);
    expect(document.activeElement).toBe(within(nav).getByRole("link", { name: /^All documents/ }));
    // The page behind it is inert, the column too; the header is not, so the button closes it.
    expect(browseButton().closest("[inert]")).not.toBeNull();
    expect(screen.getByRole("navigation", { name: t.browse }).closest("[inert]")).not.toBeNull();
    expect(button.closest("[inert]")).toBeNull();
    // The column overlay cannot open from inside it.
    fireEvent.click(browseButton());
    expect(browseDialog()).toBeNull();
    key(document.activeElement!, "Escape");
    expect(nav.className).not.toContain("cds--side-nav--expanded");
    expect(document.activeElement).toBe(button);
    expect(browseButton().closest("[inert]")).toBeNull();
  });

  it("goes to a page from the SideNav and closes it, with the focus on the page's heading; a list leaves it on the menu button", async () => {
    await renderShell(false);
    // A list from the SideNav is no view change: the focus returns to the menu button.
    menuButton().focus();
    fireEvent.click(menuButton());
    fireEvent.click(within(sideNav()).getByRole("link", { name: /^Inbox/ }));
    expect(window.location.search).toBe("?inbox=1");
    expect(sideNav().className).not.toContain("cds--side-nav--expanded");
    expect(document.activeElement).toBe(menuButton());
    expect(browseButton()).toBeTruthy();
    // A page: the new page starts at its heading, not at the menu button.
    fireEvent.click(menuButton());
    fireEvent.click(within(sideNav()).getByRole("link", { name: t.log }));
    expect(window.location.search).toBe("?inbox=1&view=log");
    expect(sideNav().className).not.toContain("cds--side-nav--expanded");
    expect(document.activeElement).toBe(await screen.findByRole("heading", { name: t.logTitle }));
  });

  it("opens the column as a modal overlay from Browse: focus trapped, Escape closes, focus returns", async () => {
    await renderShell(false);
    const button = browseButton();
    expect(button.getAttribute("aria-haspopup")).toBe("dialog");
    button.focus();
    fireEvent.click(button);
    const dialog = browseDialog()!;
    expect(dialog.getAttribute("aria-modal")).toBe("true");
    expect(button.getAttribute("aria-expanded")).toBe("true");
    expect(button.getAttribute("aria-controls")).toBe(dialog.id);
    // Focus moves into it, on the first node.
    expect(document.activeElement).toBe(within(dialog).getByRole("treeitem", { name: t.tags }));
    // Everything else is inert, the header too: no other overlay opens from it.
    expect(menuButton().closest("[inert]")).not.toBeNull();
    expect(button.closest("[inert]")).not.toBeNull();
    expect(dialog.closest("[inert]")).toBeNull();
    expect(document.querySelector(".nav-column__scrim")).not.toBeNull();
    fireEvent.click(menuButton());
    expect(sideNav().className).not.toContain("cds--side-nav--expanded");
    expect(browseDialog()).not.toBeNull();
    // Escape on a tree node: Carbon's TreeView keeps the key from bubbling.
    key(document.activeElement!, "Escape");
    expect(browseDialog()).toBeNull();
    expect(document.activeElement).toBe(button);
    expect(menuButton().closest("[inert]")).toBeNull();
    expect(document.querySelector(".nav-column__scrim")).toBeNull();
  });

  it("closes the column overlay with the scrim and the close button, and after choosing a tag", async () => {
    await renderShell(false);
    fireEvent.click(browseButton());
    fireEvent.click(document.querySelector(".nav-column__scrim")!);
    expect(browseDialog()).toBeNull();
    fireEvent.click(browseButton());
    fireEvent.click(within(browseDialog()!).getByRole("button", { name: t.closeBrowse }));
    expect(browseDialog()).toBeNull();
    browseButton().focus();
    fireEvent.click(browseButton());
    fireEvent.click(within(browseDialog()!).getByRole("treeitem", { name: "2024 1" }));
    expect(window.location.search).toBe("?tag=t2");
    expect(browseDialog()).toBeNull();
    expect(document.activeElement).toBe(browseButton());
  });

  it("closes the column overlay with Escape also while the focus is on the body (a tap on a blank part)", async () => {
    await renderShell(false);
    browseButton().focus();
    fireEvent.click(browseButton());
    expect(browseDialog()).not.toBeNull();
    (document.activeElement as HTMLElement).blur();
    expect(document.activeElement).toBe(document.body);
    key(document.body, "Escape");
    expect(browseDialog()).toBeNull();
    expect(document.activeElement).toBe(browseButton());
  });

  it("leaves Escape to a view's menu and modal inside the column overlay, then closes the overlay", async () => {
    await renderShell(false, "/", false, (s) => {
      s.savedViews = [savedView("v1", "Steuern", { filter: { "paperless-go": { v: 1, q: "Steuer" } } })];
    });
    browseButton().focus();
    fireEvent.click(browseButton());
    const dialog = browseDialog()!;
    // (b) Escape on the open menu closes the menu; the overlay stays.
    const trigger = within(dialog).getByRole("button", { name: t.viewActions("Steuern") });
    fireEvent.click(trigger);
    const menu = await screen.findByRole("menu", { name: t.viewActions("Steuern") });
    key(menu, "Escape");
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
    expect(browseDialog()).not.toBeNull();
    // (a) Escape in the delete modal closes the modal only: the overlay stays, the focus is on the view's menu button, nothing was sent.
    fireEvent.click(trigger);
    fireEvent.click(within(await screen.findByRole("menu", { name: t.viewActions("Steuern") })).getByRole("menuitem", { name: t.deleteView }));
    const ask = await screen.findByRole("dialog", { name: t.deleteViewTitle("Steuern") });
    key(within(ask).getByRole("button", { name: t.cancel }), "Escape");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: t.deleteViewTitle("Steuern") })).toBeNull());
    expect(browseDialog()).not.toBeNull();
    expect(document.activeElement).toBe(within(browseDialog()!).getByRole("button", { name: t.viewActions("Steuern") }));
    expect(apiRequests.filter((r) => r.method !== "GET")).toHaveLength(0);
    // (c) A further Escape closes the overlay, and the focus is back on Browse.
    key(document.activeElement!, "Escape");
    expect(browseDialog()).toBeNull();
    expect(document.activeElement).toBe(browseButton());
  });

  it("leaves Escape to a Carbon Modal over the SideNav overlay too", async () => {
    await renderShell(false);
    fireEvent.click(screen.getByRole("link", { name: "Rechnung" }));
    await screen.findByTestId("document-detail");
    // The detail's Delete asks in a native dialog; a Carbon Modal elsewhere (the trash's purge) is the same case:
    // the key goes to the widget, not to the overlay.
    fireEvent.click(menuButton());
    expect(sideNav().className).toContain("cds--side-nav--expanded");
    const modal = document.createElement("div");
    modal.className = "cds--modal is-visible";
    modal.innerHTML = '<div role="dialog"><button id="in-modal">x</button></div>';
    document.body.appendChild(modal);
    try {
      key(modal.querySelector("#in-modal")!, "Escape");
      expect(sideNav().className).toContain("cds--side-nav--expanded");
    } finally {
      modal.remove();
    }
    key(menuButton(), "Escape");
    expect(sideNav().className).not.toContain("cds--side-nav--expanded");
  });

  it("focuses the selected node when the overlay opens", async () => {
    await renderShell(false, "/?corr=c1");
    fireEvent.click(browseButton());
    expect(document.activeElement).toBe(within(browseDialog()!).getByRole("treeitem", { name: "Amt 3" }));
  });

  it("switches an admin's Everyone's documents with the Toggle", async () => {
    await renderShell(false, "/", true);
    const sw = screen.getByRole("switch", { name: t.everyonesDocuments });
    expect(sw.getAttribute("aria-checked")).toBe("false");
    fireEvent.click(sw);
    expect(window.location.search).toBe("?all=1");
    await waitFor(() => expect(sw.getAttribute("aria-checked")).toBe("true"));
    await waitFor(() => expect(apiRequests.map((r) => r.url)).toContain("/api/documents/counts"));
    fireEvent.click(sw);
    expect(window.location.search).toBe("");
  });

  it("leaves the open document alone when Escape closes the SideNav overlay", async () => {
    await renderShell(false);
    fireEvent.click(screen.getByRole("link", { name: "Rechnung" }));
    await screen.findByTestId("document-detail");
    fireEvent.click(menuButton());
    expect(sideNav().className).toContain("cds--side-nav--expanded");
    key(document.activeElement!, "Escape");
    expect(sideNav().className).not.toContain("cds--side-nav--expanded");
    expect(screen.getByTestId("document-detail")).toBeTruthy();
    expect(new URLSearchParams(window.location.search).get("doc")).toBe("d1");
    // Without an overlay, Escape closes the document as before.
    key(document.body, "Escape");
    await waitFor(() => expect(screen.queryByTestId("document-detail")).toBeNull());
  });
});

describe("desktop navigation (lg and up)", () => {
  it("shows the SideNav as a rail and the column as a column, with no overlays and no Browse button", async () => {
    await renderShell(true);
    const nav = sideNav();
    expect(nav.className).toContain("cds--side-nav--rail");
    expect(nav.hasAttribute("inert")).toBe(false);
    expect(screen.getByRole("navigation", { name: t.browse }).closest("[role=dialog]")).toBeNull();
    expect(screen.queryByRole("button", { name: t.closeBrowse })).toBeNull();
    // The Browse button is in the page (layout.css hides it from lg); it opens no overlay on this width.
    fireEvent.click(browseButton());
    expect(browseDialog()).toBeNull();
  });

  it("drops the rail's hover expansion when the window narrows below lg, instead of leaving an inert overlay", async () => {
    await renderShell(true);
    const link = within(sideNav()).getByRole("link", { name: t.log });
    fireEvent.focus(link);
    await waitFor(() => expect(sideNav().className).toContain("cds--side-nav--expanded"));
    resize(false);
    const nav = sideNav();
    expect(nav.className).not.toContain("cds--side-nav--rail");
    expect(nav.className).not.toContain("cds--side-nav--expanded");
    expect(document.querySelector(".cds--side-nav__overlay-active")).toBeNull();
    expect(dialogOpen()).toBe(false);
    // The overlay works as usual afterwards.
    fireEvent.click(menuButton());
    expect(sideNav().className).toContain("cds--side-nav--expanded");
    key(document.activeElement!, "Escape");
    expect(sideNav().className).not.toContain("cds--side-nav--expanded");
  });

  it("reveals the selected tag's parents once, not again when the column remounts", async () => {
    localStorage.setItem("paperless-go.tags.alice", JSON.stringify({ v: 1, collapsed: ["t1"] }));
    await renderShell(true, "/?tag=t2");
    const steuer = () => screen.getByRole("treeitem", { name: "Steuer 2" });
    expect(steuer().getAttribute("aria-expanded")).toBe("true");
    // The user collapses the parent again, goes to a page (the URL keeps the tag) and comes back.
    fireEvent.click(steuer().querySelector<HTMLElement>(":scope > .cds--tree-node__label .cds--tree-parent-node__toggle")!);
    expect(steuer().getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(within(sideNav()).getByRole("link", { name: t.log }));
    await screen.findByRole("heading", { name: t.logTitle });
    expect(screen.queryByRole("tree")).toBeNull();
    act(() => window.history.back());
    await waitFor(() => expect(window.location.search).toBe("?tag=t2"));
    expect((await screen.findByRole("treeitem", { name: "Steuer 2" })).getAttribute("aria-expanded")).toBe("false");
    expect(JSON.parse(localStorage.getItem("paperless-go.tags.alice")!)).toEqual({ v: 1, collapsed: ["t1"] });
  });

  it("marks one place as current: a shown saved view's button, and no list of the SideNav", async () => {
    await renderShell(true, "/", false, (s) => {
      s.savedViews = [savedView("v1", "Steuern", { filter: { "paperless-go": { v: 1, q: "Steuer" } } })];
    });
    const current = () => Array.from(document.querySelectorAll('[aria-current="page"]'));
    expect(current().map((e) => e.textContent)).toEqual([expect.stringMatching(/^All documents/)]);
    fireEvent.click(await screen.findByRole("button", { name: "Steuern" }));
    await waitFor(() => expect(window.location.search).toContain("sv=v1"));
    const marked = current();
    expect(marked).toHaveLength(1);
    expect(marked[0]!.tagName).toBe("BUTTON");
    expect(marked[0]!.textContent).toBe("Steuern");
  });

  it("expands the rail on focus and collapses it again when focus leaves", async () => {
    await renderShell(true);
    const nav = sideNav();
    const link = within(nav).getByRole("link", { name: t.log });
    expect(link.tabIndex).toBe(0);
    fireEvent.focus(link);
    await waitFor(() => expect(nav.className).toContain("cds--side-nav--expanded"));
    fireEvent.blur(link, { relatedTarget: document.body });
    await waitFor(() => expect(nav.className).not.toContain("cds--side-nav--expanded"));
  });

  it("leaves the open document alone when Escape closes a Carbon Modal next to it", async () => {
    await renderShell(true, "/", false, (s) => {
      s.savedViews = [savedView("v1", "Steuern", { filter: { "paperless-go": { v: 1, q: "Steuer" } } })];
    });
    fireEvent.click(screen.getByRole("link", { name: "Rechnung" }));
    await screen.findByTestId("document-detail");
    // The column stays usable next to the open document (from lg); a view's Delete asks in a Modal.
    fireEvent.click(await screen.findByRole("button", { name: t.viewActions("Steuern") }));
    fireEvent.click(within(await screen.findByRole("menu", { name: t.viewActions("Steuern") })).getByRole("menuitem", { name: t.deleteView }));
    const ask = await screen.findByRole("dialog", { name: t.deleteViewTitle("Steuern") });
    expect(screen.getByTestId("document-detail")).toBeTruthy();
    // Escape goes to the Modal (dialogs.ts), which closes; the document stays open, the focus returns to the view's menu button.
    key(within(ask).getByRole("button", { name: t.cancel }), "Escape");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: t.deleteViewTitle("Steuern") })).toBeNull());
    expect(screen.getByTestId("document-detail")).toBeTruthy();
    expect(document.activeElement).toBe(screen.getByRole("button", { name: t.viewActions("Steuern") }));
    expect(new URLSearchParams(window.location.search).get("doc")).toBe("d1");
    expect(apiRequests.filter((r) => r.method === "DELETE")).toHaveLength(0);
  });

  it("moves the focus to the page heading on a view change", async () => {
    await renderShell(true);
    const log = within(sideNav()).getByRole("link", { name: t.log });
    log.focus();
    fireEvent.click(log);
    const heading = await screen.findByRole("heading", { name: t.logTitle });
    expect(document.activeElement).toBe(heading);
    fireEvent.click(within(sideNav()).getByRole("link", { name: /^All documents/ }));
    await screen.findByRole("link", { name: "Rechnung" });
    expect(document.activeElement).toBe(document.getElementById("list-title"));
    // A filter is no view change: the focus stays where the click was.
    const tagNode = screen.getByRole("treeitem", { name: "Steuer 2" });
    tagNode.focus();
    fireEvent.click(tagNode);
    expect(window.location.search).toBe("?tag=t1");
    expect(document.activeElement).toBe(tagNode);
  });
});

describe("the navigation in German", () => {
  afterEach(() => {
    vi.resetModules();
  });

  it("names the menu button, the SideNav, the column and its overlay in German, nothing in English", async () => {
    vi.resetModules();
    vi.stubGlobal("navigator", { ...navigator, languages: ["de-DE", "en-US"], language: "de-DE" });
    const { App: GermanApp } = await import("./App");
    const { messagesFor } = await import("./i18n");
    const de = messagesFor("de");
    ({ server: api, requests: apiRequests } = stubApi());
    api.user = { id: 1, name: "root", admin: true };
    api.tags = [tag("t1", "Steuer")];
    screenWidth(false);
    render(<GermanApp />);
    await screen.findByRole("treeitem", { name: "Steuer 1" });
    expect(screen.getByRole("button", { name: de.openMenu })).toBeTruthy();
    const nav = screen.getByRole("navigation", { name: de.navigation });
    expect(within(nav).getAllByRole("link").map((l) => l.textContent?.replace(/ \d+$/, ""))).toEqual([de.allDocuments, de.inbox, de.favorites, de.trash, de.manageMetadata, de.log, de.users, de.profile, de.documentation]);
    fireEvent.click(screen.getByRole("button", { name: de.browse }));
    expect(screen.getByRole("dialog", { name: de.browse })).toBeTruthy();
    expect(screen.getByRole("button", { name: de.closeBrowse })).toBeTruthy();
    expect(screen.getByRole("switch", { name: de.everyonesDocuments })).toBeTruthy();
    const html = document.body.innerHTML;
    for (const s of ["Open menu", "Browse", "All documents", "Favorites", "Log out", "Profile", "Everyone", ">Off<", ">On<"]) expect(html, s).not.toContain(s);
  });
});

describe("the tag reveal and the metadata (#150, part 3 follow-up)", () => {
  it("reveals the selected tag's parents once, not again when the metadata loads again", async () => {
    localStorage.setItem("paperless-go.tags.root", JSON.stringify({ v: 1, collapsed: ["t1"] }));
    await renderShell(true, "/?tag=t2", true);
    const steuer = () => screen.getByRole("treeitem", { name: "Steuer 2" });
    expect(steuer().getAttribute("aria-expanded")).toBe("true");
    // The user collapses the parent again, then the metadata loads again
    // (the admin's switch; a bulk change or an edit would do the same).
    fireEvent.click(steuer().querySelector<HTMLElement>(":scope > .cds--tree-node__label .cds--tree-parent-node__toggle")!);
    expect(steuer().getAttribute("aria-expanded")).toBe("false");
    const tagLoads = () => apiRequests.filter((r) => r.method === "GET" && r.path === "/api/tags").length;
    const before = tagLoads();
    fireEvent.click(screen.getByRole("switch", { name: t.everyonesDocuments }));
    await waitFor(() => expect(window.location.search).toBe("?tag=t2&all=1"));
    await waitFor(() => expect(tagLoads()).toBe(before + 1));
    await waitFor(() => expect(apiRequests.filter((r) => r.method === "GET" && r.path === "/api/documents").at(-1)!.url).not.toContain("owner=me"));
    expect(steuer().getAttribute("aria-expanded")).toBe("false");
    expect(JSON.parse(localStorage.getItem("paperless-go.tags.root")!)).toEqual({ v: 1, collapsed: ["t1"] });
  });
});
