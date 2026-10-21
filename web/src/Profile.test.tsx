import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { t } from "./i18n";
import { PasswordForm, ProfilePage } from "./Profile";

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
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(null, { status: 204 }));
    render(<ProfilePage user={{ name: "kim", admin: false, mustChangePassword: false }} />);
    expect(screen.getByText(t.loggedInAs("kim"))).toBeTruthy();
    fill("old passphrase", "a long passphrase");
    expect((await screen.findByRole("status")).textContent).toBe(t.passwordChanged);
    expect((screen.getByLabelText(t.newPassword) as HTMLInputElement).value).toBe("");
  });
});
