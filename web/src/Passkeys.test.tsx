import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { t } from "./i18n";
import { PasskeySection } from "./Passkeys";

type Call = { method: string; url: string; body: unknown };
let calls: Call[];

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

function stubServer(list: Record<string, unknown>, twoFactor = false, other: (c: Call) => Response | undefined = () => undefined) {
  calls = [];
  vi.stubGlobal("fetch", async (url: string, init?: RequestInit) => {
    const c = { method: init?.method ?? "GET", url, body: init?.body ? JSON.parse(String(init.body)) : undefined };
    calls.push(c);
    const answer = other(c);
    if (answer) return answer;
    if (url === "/api/profile/passkeys" && c.method === "GET") return json(200, list);
    if (url === "/api/profile/2fa") return json(200, { enabled: twoFactor, required: false, admin: false, setupPending: false, recoveryCodesLeft: 0 });
    if (url === "/api/profile/passkeys/begin")
      return json(200, {
        token: "tok",
        publicKey: { challenge: "AQID", rp: { id: "localhost", name: "paperless-go" }, user: { id: "BAUG", name: "kim", displayName: "kim" }, pubKeyCredParams: [] },
      });
    if (url === "/api/profile/passkeys/finish") return json(201, { name: "Phone" });
    if (url.startsWith("/api/profile/passkeys/") && c.method === "DELETE") return json(200, { credentialId: "AQ", rpId: "localhost" });
    return new Response(null, { status: 404 });
  });
}

const buf = (...b: number[]) => new Uint8Array(b).buffer;

/** A browser with passkeys: navigator.credentials and PublicKeyCredential are mocks. */
function stubBrowser(create: () => Promise<unknown>) {
  const signalUnknownCredential = vi.fn(async () => {});
  vi.stubGlobal("PublicKeyCredential", Object.assign(function PublicKeyCredential() {}, { signalUnknownCredential }));
  const credentials = { create: vi.fn((_options: CredentialCreationOptions) => create()), get: vi.fn() };
  Object.defineProperty(navigator, "credentials", { value: credentials, configurable: true });
  return { credentials, signalUnknownCredential };
}

const here = () => ({ available: true, origin: window.location.origin, max: 10, passkeys: [] as unknown[] });

beforeEach(() => vi.stubGlobal("confirm", () => true));

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  Reflect.deleteProperty(navigator, "credentials");
});

describe("PasskeySection", () => {
  it("explains a server without a public URL", async () => {
    stubServer({ available: false, max: 10, passkeys: [] });
    render(<PasskeySection />);
    expect(await screen.findByText(t.passkeysUnavailable)).toBeTruthy();
  });

  it("names the address where passkeys work", async () => {
    stubBrowser(async () => null);
    stubServer({ ...here(), origin: "https://docs.example.test" });
    render(<PasskeySection />);
    expect(await screen.findByText(t.passkeysOtherOrigin("https://docs.example.test"))).toBeTruthy();
    expect(screen.queryByRole("button", { name: t.passkeyAdd })).toBeNull();
  });

  it("registers a passkey with the password and the browser's answer", async () => {
    const { credentials } = stubBrowser(async () => ({
      id: "AQ",
      rawId: buf(1),
      type: "public-key",
      authenticatorAttachment: "platform",
      getClientExtensionResults: () => ({}),
      response: { clientDataJSON: buf(2), attestationObject: buf(3), getTransports: () => ["internal"] },
    }));
    stubServer(here());
    render(<PasskeySection />);
    expect(await screen.findByText(t.passkeyNone)).toBeTruthy();
    fireEvent.change(screen.getByLabelText(t.passkeyName), { target: { value: " Phone " } });
    fireEvent.change(screen.getByLabelText(t.currentPassword), { target: { value: "my passphrase" } });
    fireEvent.click(screen.getByRole("button", { name: t.passkeyAdd }));
    expect((await screen.findByRole("status")).textContent).toBe(t.passkeyAdded("Phone"));

    expect(calls.find((c) => c.url === "/api/profile/passkeys/begin")?.body).toEqual({ name: "Phone", currentPassword: "my passphrase", code: "" });
    const options = credentials.create.mock.calls[0]![0] as { publicKey: PublicKeyCredentialCreationOptions };
    expect(Array.from(new Uint8Array(options.publicKey.challenge as ArrayBuffer))).toEqual([1, 2, 3]);
    expect(calls.find((c) => c.url === "/api/profile/passkeys/finish")?.body).toEqual({
      token: "tok",
      credential: {
        id: "AQ",
        rawId: "AQ",
        type: "public-key",
        authenticatorAttachment: "platform",
        clientExtensionResults: {},
        response: { clientDataJSON: "Ag", attestationObject: "Aw", transports: ["internal"] },
      },
    });
  });

  it("asks for a code with 2FA on and explains a wrong one", async () => {
    const { credentials } = stubBrowser(async () => null);
    stubServer(here(), true, (c) => (c.url === "/api/profile/passkeys/begin" ? json(401, { error: "invalid two-factor code" }) : undefined));
    render(<PasskeySection />);
    expect(await screen.findByText(t.passkeyConfirm2FA)).toBeTruthy();
    fireEvent.change(screen.getByLabelText(t.passkeyName), { target: { value: "Phone" } });
    fireEvent.change(screen.getByLabelText(t.currentPassword), { target: { value: "my passphrase" } });
    fireEvent.change(screen.getByLabelText(t.twoFactorCode), { target: { value: "000000" } });
    fireEvent.click(screen.getByRole("button", { name: t.passkeyAdd }));
    expect((await screen.findByRole("alert")).textContent).toBe(t.invalidTwoFactor);
    expect(credentials.create).not.toHaveBeenCalled();
    expect((screen.getByLabelText(t.currentPassword) as HTMLInputElement).value).toBe("");
  });

  it("says when the browser dialog was cancelled", async () => {
    stubBrowser(async () => {
      throw new DOMException("cancelled", "NotAllowedError");
    });
    stubServer(here());
    render(<PasskeySection />);
    fireEvent.change(await screen.findByLabelText(t.passkeyName), { target: { value: "Phone" } });
    fireEvent.change(screen.getByLabelText(t.currentPassword), { target: { value: "my passphrase" } });
    fireEvent.click(screen.getByRole("button", { name: t.passkeyAdd }));
    expect((await screen.findByRole("alert")).textContent).toBe(t.passkeyCancelled);
    expect(calls.some((c) => c.url === "/api/profile/passkeys/finish")).toBe(false);
  });

  it("removes a passkey and tells the browser", async () => {
    const { signalUnknownCredential } = stubBrowser(async () => null);
    stubServer({ ...here(), passkeys: [{ id: 7, name: "Phone", createdAt: "2026-10-21T10:00:00Z", lastUsedAt: null }] });
    render(<PasskeySection />);
    expect(await screen.findByText(t.passkeyNeverUsed)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: t.passkeyRemove }));
    await vi.waitFor(() => expect(signalUnknownCredential).toHaveBeenCalledWith({ rpId: "localhost", credentialId: "AQ" }));
    expect(calls.some((c) => c.method === "DELETE" && c.url === "/api/profile/passkeys/7")).toBe(true);
  });
});
