import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { AuthGate } from "./Auth";
import { messagesFor, t } from "./i18n";
import { findAlert, theAlert } from "./test/alerts";

// The login page on Carbon (#150, part 1): the requests of the password,
// code and passkey logins are the same as before, pinned as method, path
// and body; the errors are announced; Carbon's texts are German in German.

interface Call {
  method: string;
  url: string;
  body: unknown;
}
type Reply = { status: number; body?: unknown };
let calls: Call[];

/** Answers each request through handler and records it. */
function serve(handler: (method: string, url: string, body: unknown) => Reply) {
  calls = [];
  vi.stubGlobal("fetch", async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ method, url, body });
    const r = handler(method, url, body);
    return new Response(r.body === undefined ? null : JSON.stringify(r.body), { status: r.status, headers: { "Content-Type": "application/json" } });
  });
}

const alice = { name: "alice", admin: false, mustChangePassword: false };
const loggedOut: Reply = { status: 401, body: { error: "login required", setupRequired: false } };

function renderGate() {
  return render(<AuthGate>{(user) => <p>app for {user.name}</p>}</AuthGate>);
}

/** The message of the one alert with text: the notification's subtitle, without its status icon's description ("Error"). */
const alertMessage = () => theAlert().querySelector(".cds--inline-notification__subtitle")?.textContent;
const findAlertMessage = async () => (await findAlert()).querySelector(".cds--inline-notification__subtitle")?.textContent;

/** The password field: the input, not Carbon's show-password button that the label names too. */
const password = () => screen.getByLabelText(t.fieldPassword, { selector: "input" }) as HTMLInputElement;

async function logIn(name: string, pw: string) {
  fireEvent.change(await screen.findByLabelText(t.userName), { target: { value: name } });
  fireEvent.change(password(), { target: { value: pw } });
  fireEvent.click(screen.getByRole("button", { name: t.logIn }));
}

beforeEach(() => {
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener: () => {}, removeEventListener: () => {} }));
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  Reflect.deleteProperty(navigator, "credentials");
});

describe("login", () => {
  it("sends the password, then the code, as before", async () => {
    let session: Reply = loggedOut;
    serve((method, url) => {
      if (url === "/api/session" && method === "POST") return { status: 200, body: { twoFactorRequired: true, challenge: "ch1" } };
      if (url === "/api/session/2fa") {
        session = { status: 200, body: { user: { ...alice, twoFactorEnabled: true } } };
        return session;
      }
      return session;
    });
    renderGate();
    await logIn("alice", "correct horse battery");
    expect(await screen.findByRole("heading", { name: t.twoFactorLogin })).toBeTruthy();
    const code = screen.getByLabelText(t.twoFactorCode) as HTMLInputElement;
    // The code field never shows the password; it takes recovery codes too.
    expect(code.value).toBe("");
    expect(code.type).toBe("text");
    expect(code.autocomplete).toBe("one-time-code");
    fireEvent.change(code, { target: { value: "abcd-efgh-jkmn-pqrs" } });
    fireEvent.click(screen.getByRole("button", { name: t.logIn }));
    expect(await screen.findByText("app for alice")).toBeTruthy();
    expect(calls).toEqual([
      { method: "GET", url: "/api/session", body: undefined },
      { method: "POST", url: "/api/session", body: { username: "alice", password: "correct horse battery" } },
      { method: "POST", url: "/api/session/2fa", body: { challenge: "ch1", code: "abcd-efgh-jkmn-pqrs" } },
    ]);
  });

  it("logs in without a second step when the server sends the user", async () => {
    serve((method, url) => (url === "/api/session" && method === "POST" ? { status: 200, body: { user: alice } } : loggedOut));
    renderGate();
    expect((await screen.findByLabelText(t.userName)).getAttribute("autocomplete")).toBe("username");
    expect(password().getAttribute("autocomplete")).toBe("current-password");
    expect(password().type).toBe("password");
    await logIn("alice", "pw");
    expect(await screen.findByText("app for alice")).toBeTruthy();
  });

  it("shows why a login failed and clears the password", async () => {
    serve((method) => (method === "POST" ? { status: 401, body: { error: "wrong user name or password" } } : loggedOut));
    renderGate();
    await logIn("alice", "wrong");
    expect(await findAlertMessage()).toBe(t.wrongLogin);
    expect(password().value).toBe("");
  });

  it("shows a locked user and the wait after too many attempts", async () => {
    const answers: Reply[] = [
      { status: 403, body: { error: "user is locked" } },
      { status: 429, body: { error: "too many", retryAfter: 60 } },
    ];
    serve((method) => (method === "POST" ? answers.shift()! : loggedOut));
    renderGate();
    await logIn("lee", "lee-password");
    await waitFor(() => expect(alertMessage()).toBe(t.userLocked));
    expect(screen.queryByRole("heading", { name: t.twoFactorLogin })).toBeNull();
    await logIn("alice", "x");
    await waitFor(() => expect(alertMessage()).toBe(t.tooManyAttempts(60)));
  });

  it("rejects a wrong code and returns to the password step when the login expired", async () => {
    let session: Reply = loggedOut;
    serve((method, url, body) => {
      if (url === "/api/session" && method === "POST") return { status: 200, body: { twoFactorRequired: true, challenge: "ch1" } };
      if (url === "/api/session/2fa") {
        const code = (body as { code: string }).code;
        if (code === "000000") return { status: 401, body: { error: "invalid two-factor code" } };
        if (code === "111111") return { status: 401, body: { error: "the login has expired; log in again" } };
        session = { status: 200, body: { user: { ...alice, twoFactorEnabled: true } } };
        return session;
      }
      return session;
    });
    renderGate();
    await logIn("alice", "correct horse battery");
    const code = (await screen.findByLabelText(t.twoFactorCode)) as HTMLInputElement;
    fireEvent.change(code, { target: { value: "000000" } });
    fireEvent.click(screen.getByRole("button", { name: t.logIn }));
    await waitFor(() => expect(alertMessage()).toBe(t.invalidTwoFactor));
    expect(code.value).toBe("");

    // An expired login goes back to the password.
    fireEvent.change(code, { target: { value: "111111" } });
    fireEvent.click(screen.getByRole("button", { name: t.logIn }));
    await waitFor(() => expect(alertMessage()).toBe(t.twoFactorExpired));
    expect(password().value).toBe("");

    await logIn("alice", "correct horse battery");
    fireEvent.change(await screen.findByLabelText(t.twoFactorCode), { target: { value: "222222" } });
    fireEvent.click(screen.getByRole("button", { name: t.logIn }));
    expect(await screen.findByText("app for alice")).toBeTruthy();
    expect(calls.filter((c) => c.url === "/api/session/2fa").map((c) => c.body)).toEqual([
      { challenge: "ch1", code: "000000" },
      { challenge: "ch1", code: "111111" },
      { challenge: "ch1", code: "222222" },
    ]);
  });

  it("explains how to create the first admin, with the command to copy", async () => {
    serve(() => ({ status: 401, body: { error: "login required", setupRequired: true } }));
    renderGate();
    expect(await screen.findByRole("heading", { name: t.setupTitle })).toBeTruthy();
    expect(screen.getByText("./pgo user add NAME --admin")).toBeTruthy();
    expect(screen.getByRole("button", { name: t.carbon.copy })).toBeTruthy();
    expect(screen.getByRole("button", { name: t.checkAgain })).toBeTruthy();
  });
});

describe("passkey login", () => {
  const buf = (...b: number[]) => new Uint8Array(b).buffer;
  const assertion = {
    id: "AQ",
    rawId: buf(1),
    type: "public-key",
    authenticatorAttachment: "platform",
    getClientExtensionResults: () => ({}),
    response: { clientDataJSON: buf(2), authenticatorData: buf(3), signature: buf(4), userHandle: buf(5) },
  };

  /** A browser with passkeys but without autofill; credentials.get answers with get. */
  function stubPasskeys(get: () => Promise<unknown>) {
    const signalUnknownCredential = vi.fn(async () => {});
    vi.stubGlobal("PublicKeyCredential", Object.assign(function PublicKeyCredential() {}, { signalUnknownCredential }));
    const credentials = { get: vi.fn((_options: CredentialRequestOptions) => get()), create: vi.fn() };
    Object.defineProperty(navigator, "credentials", { value: credentials, configurable: true });
    return { credentials, signalUnknownCredential };
  }

  const offered = (): Reply => ({ status: 401, body: { error: "login required", setupRequired: false, passkeyOrigin: window.location.origin } });
  const ceremony: Reply = { status: 200, body: { token: "tok", publicKey: { challenge: "CQo", rpId: "localhost", userVerification: "required" } } };

  it("is offered only at the public URL", async () => {
    stubPasskeys(async () => assertion);
    serve(() => ({ status: 401, body: { error: "login required", setupRequired: false, passkeyOrigin: "https://docs.example.test" } }));
    renderGate();
    await screen.findByLabelText(t.userName);
    expect(screen.queryByRole("button", { name: t.passkeySignIn })).toBeNull();
  });

  it("logs in with the browser's passkey, with the same requests as before", async () => {
    const { credentials } = stubPasskeys(async () => assertion);
    let session = offered();
    serve((_, url) => {
      if (url === "/api/session/passkey/begin") return ceremony;
      if (url === "/api/session/passkey/finish") {
        session = { status: 200, body: { user: alice } };
        return session;
      }
      return session;
    });
    renderGate();
    fireEvent.click(await screen.findByRole("button", { name: t.passkeySignIn }));
    expect(await screen.findByText("app for alice")).toBeTruthy();
    const options = credentials.get.mock.calls[0]![0] as { publicKey: PublicKeyCredentialRequestOptions };
    expect(Array.from(new Uint8Array(options.publicKey.challenge as ArrayBuffer))).toEqual([9, 10]);
    expect(calls.filter((c) => c.url.startsWith("/api/session/passkey"))).toEqual([
      { method: "POST", url: "/api/session/passkey/begin", body: undefined },
      {
        method: "POST",
        url: "/api/session/passkey/finish",
        body: {
          token: "tok",
          credential: {
            id: "AQ",
            rawId: "AQ",
            type: "public-key",
            authenticatorAttachment: "platform",
            clientExtensionResults: {},
            response: { clientDataJSON: "Ag", authenticatorData: "Aw", signature: "BA", userHandle: "BQ" },
          },
        },
      },
    ]);
  });

  it("forgets a passkey the server does not know", async () => {
    const { signalUnknownCredential } = stubPasskeys(async () => assertion);
    serve((_, url) => {
      if (url === "/api/session/passkey/begin") return ceremony;
      if (url === "/api/session/passkey/finish")
        return { status: 401, body: { error: "this passkey is not registered", unknownCredential: true, credentialId: "AQ", rpId: "localhost" } };
      return offered();
    });
    renderGate();
    fireEvent.click(await screen.findByRole("button", { name: t.passkeySignIn }));
    expect(await findAlertMessage()).toBe(t.passkeyUnknown);
    expect(signalUnknownCredential).toHaveBeenCalledWith({ rpId: "localhost", credentialId: "AQ" });
  });

  it("says when no passkey was chosen", async () => {
    stubPasskeys(async () => {
      throw new DOMException("cancelled", "NotAllowedError");
    });
    serve((_, url) => (url === "/api/session/passkey/begin" ? ceremony : offered()));
    renderGate();
    fireEvent.click(await screen.findByRole("button", { name: t.passkeySignIn }));
    expect(await findAlertMessage()).toBe(t.passkeyCancelled);
    expect(calls.some((c) => c.url === "/api/session/passkey/finish")).toBe(false);
  });

  it("renews an expired autofill offer once", async () => {
    const signalUnknownCredential = vi.fn(async () => {});
    vi.stubGlobal(
      "PublicKeyCredential",
      Object.assign(function PublicKeyCredential() {}, { signalUnknownCredential, isConditionalMediationAvailable: async () => true }),
    );
    // Each conditional request answers at once, as if the user picked the
    // passkey after the ceremony expired. A third request never answers,
    // so that a loop would stop there.
    const get = vi.fn((_options: CredentialRequestOptions) => (get.mock.calls.length <= 2 ? Promise.resolve(assertion) : new Promise(() => {})));
    Object.defineProperty(navigator, "credentials", { value: { get, create: vi.fn() }, configurable: true });
    serve((_, url) => {
      if (url === "/api/session/passkey/begin") return ceremony;
      if (url === "/api/session/passkey/finish") return { status: 401, body: { error: "the passkey login has expired; try again" } };
      return offered();
    });
    renderGate();
    expect(await findAlertMessage()).toBe(t.passkeyExpired);
    await waitFor(() => expect(calls.filter((c) => c.url === "/api/session/passkey/finish")).toHaveLength(2));
    // The first offer and one renewal, both conditional; then no more.
    await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
    // Let the second failure's handling finish: no third offer follows.
    await act(async () => {});
    expect(get).toHaveBeenCalledTimes(2);
    for (const [options] of get.mock.calls) expect((options as CredentialRequestOptions).mediation).toBe("conditional");
    expect(calls.filter((c) => c.url === "/api/session/passkey/begin")).toHaveLength(2);
  });
});

describe("in German", () => {
  // Carbon's English defaults that the login screens must not show.
  const english = ["Show password", "Hide password", "close notification", "error icon", "Copy to clipboard", "Copied", "Show more", "Focus sentinel"];
  const de = messagesFor("de");

  /** Renders a module afresh with the browser set to German: i18n.ts reads the language when it loads. */
  async function renderGerman(handler: Parameters<typeof serve>[0]) {
    vi.resetModules();
    vi.stubGlobal("navigator", { ...navigator, languages: ["de-DE", "en-US"], language: "de-DE" });
    serve(handler);
    const { AuthGate: Gate } = await import("./Auth");
    const { t: texts } = await import("./i18n");
    expect(texts.logIn).toBe(de.logIn);
    render(<Gate>{(user) => <p>app for {user.name}</p>}</Gate>);
  }

  it("shows the login page and Carbon's texts in German, nothing in English", async () => {
    await renderGerman((method) => (method === "POST" ? { status: 401, body: { error: "wrong" } } : loggedOut));
    fireEvent.change(await screen.findByLabelText(de.userName), { target: { value: "alice" } });
    fireEvent.change(screen.getByLabelText(de.fieldPassword, { selector: "input" }), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: de.logIn }));
    await waitFor(() => expect(alertMessage()).toBe(de.wrongLogin));
    expect(screen.getByRole("button", { name: de.carbon.showPassword })).toBeTruthy();
    expect(document.body.innerHTML).toContain(de.carbon.status.error);
    for (const s of english) expect(document.body.innerHTML, s).not.toContain(s);
  });

  it("shows the first-admin screen in German", async () => {
    await renderGerman(() => ({ status: 401, body: { error: "login required", setupRequired: true } }));
    await screen.findByRole("heading", { name: de.setupTitle });
    expect(screen.getByRole("button", { name: de.carbon.copy })).toBeTruthy();
    for (const s of english) expect(document.body.innerHTML, s).not.toContain(s);
  });
});
