import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { t } from "./i18n";
import { PasswordForm, ProfilePage, TwoFactorSettings } from "./Profile";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function fill(current: string, next: string, repeat = next) {
  fireEvent.change(screen.getByLabelText(t.currentPassword), { target: { value: current } });
  fireEvent.change(screen.getByLabelText(t.newPassword), { target: { value: next } });
  fireEvent.change(screen.getByLabelText(t.repeatPassword), { target: { value: repeat } });
  fireEvent.click(screen.getByRole("button", { name: t.changePassword }));
}

describe("PasswordForm", () => {
  it("checks length and repetition before sending", async () => {
    const fetch = vi.spyOn(globalThis, "fetch");
    render(<PasswordForm done={() => {}} />);
    fill("old one", "short");
    expect((await screen.findByRole("alert")).textContent).toBe(t.passwordRules);
    fill("old one", "a long passphrase", "another passphrase");
    expect((await screen.findByRole("alert")).textContent).toBe(t.passwordsDiffer);
    expect(fetch).not.toHaveBeenCalled();
  });

  it("names a wrong current password", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ error: "the current password is wrong" }), { status: 401 }));
    const done = vi.fn();
    render(<PasswordForm done={done} />);
    fill("wrong", "a long passphrase");
    expect((await screen.findByRole("alert")).textContent).toBe(t.wrongCurrentPassword);
    expect(done).not.toHaveBeenCalled();
  });
});

describe("ProfilePage", () => {
  it("confirms a changed password", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (url) =>
      String(url) === "/api/profile/2fa"
        ? new Response(JSON.stringify({ enabled: false, required: false, admin: false, setupPending: false, recoveryCodesLeft: 0 }), { status: 200 })
        : new Response(null, { status: 204 }),
    );
    render(<ProfilePage user={{ name: "kim", admin: false, mustChangePassword: false, twoFactorEnabled: false, twoFactorRequired: false }} />);
    expect(screen.getByText(t.loggedInAs("kim"))).toBeTruthy();
    fill("old passphrase", "a long passphrase");
    expect((await screen.findByRole("status")).textContent).toBe(t.passwordChanged);
    expect((screen.getByLabelText(t.newPassword) as HTMLInputElement).value).toBe("");
  });
});

type State = { enabled: boolean; required: boolean; admin: boolean; setupPending: boolean; recoveryCodesLeft: number };

function stubTwoFactor(state: State, other: (method: string, url: string) => Response = () => new Response(null, { status: 204 })) {
  const calls: { method: string; url: string; body: unknown }[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (url, init) => {
    const method = init?.method ?? "GET";
    calls.push({ method, url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined });
    if (method === "GET" && String(url) === "/api/profile/2fa") return new Response(JSON.stringify(state), { status: 200 });
    return other(method, String(url));
  });
  return calls;
}

describe("TwoFactorSettings", () => {
  it("offers the setup to a user without 2FA", async () => {
    stubTwoFactor({ enabled: false, required: false, admin: false, setupPending: false, recoveryCodesLeft: 0 });
    render(<TwoFactorSettings user="kim" />);
    expect(await screen.findByText(t.twoFactorOffText)).toBeTruthy();
    expect(screen.getByRole("button", { name: t.twoFactorSetup })).toBeTruthy();
  });

  it("turns 2FA off with the password and a code", async () => {
    const calls = stubTwoFactor({ enabled: true, required: false, admin: false, setupPending: false, recoveryCodesLeft: 7 });
    render(<TwoFactorSettings user="kim" />);
    expect(await screen.findByText(t.recoveryCodesLeft(7))).toBeTruthy();
    fireEvent.change(screen.getByLabelText(t.currentPassword), { target: { value: "my passphrase" } });
    const codeFields = screen.getAllByLabelText(t.twoFactorCode);
    fireEvent.change(codeFields[1]!, { target: { value: "123 456" } });
    fireEvent.click(screen.getByRole("button", { name: t.twoFactorDisable }));
    await vi.waitFor(() => expect(calls.some((c) => c.method === "DELETE")).toBe(true));
    expect(calls.find((c) => c.method === "DELETE")).toEqual({
      method: "DELETE",
      url: "/api/profile/2fa",
      body: { currentPassword: "my passphrase", code: "123 456" },
    });
  });

  it("names a wrong code, and does not offer turning it off to admins", async () => {
    stubTwoFactor({ enabled: true, required: true, admin: true, setupPending: false, recoveryCodesLeft: 10 }, () =>
      new Response(JSON.stringify({ error: "invalid two-factor code" }), { status: 401 }),
    );
    render(<TwoFactorSettings user="root" />);
    expect(await screen.findByText(t.twoFactorAdminRequired)).toBeTruthy();
    expect(screen.queryByRole("button", { name: t.twoFactorDisable })).toBeNull();
    fireEvent.change(screen.getByLabelText(t.twoFactorCode), { target: { value: "000000" } });
    fireEvent.click(screen.getByRole("button", { name: t.twoFactorRegenerate }));
    expect((await screen.findByRole("alert")).textContent).toBe(t.invalidTwoFactor);
  });
});
