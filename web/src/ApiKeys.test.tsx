import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, type ApiKey } from "./api";
import { apiKeyDetails, apiKeyError, ApiKeyItems, ApiKeySection, NewKeyPanel } from "./ApiKeys";
import { t } from "./i18n";

const key: ApiKey = {
  id: 1, name: "backup", hint: "abcd", scopes: ["read", "write"], createdAt: "2026-10-01T00:00:00Z",
  expiresAt: null, lastUsedAt: null, revokedAt: null, state: "active",
};

afterEach(cleanup);

describe("NewKeyPanel", () => {
  it("shows the key once, copies it and hides it when done", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const done = vi.fn();
    const secret = "pl_" + "a".repeat(52);
    render(<NewKeyPanel created={{ ...key, key: secret }} done={done} />);
    expect(screen.getByText(secret)).toBeTruthy();
    expect(screen.getByRole("status").textContent).toContain(t.apiKeyCreated("backup"));
    fireEvent.click(screen.getByText(t.copy));
    expect(writeText).toHaveBeenCalledWith(secret);
    expect(await screen.findByText(t.copied)).toBeTruthy();
    fireEvent.click(screen.getByText(t.done));
    expect(done).toHaveBeenCalled();
  });
});

describe("ApiKeyItems", () => {
  it("offers revoking only active keys and never shows more than the hint", () => {
    const revoke = vi.fn();
    vi.spyOn(window, "confirm").mockReturnValue(true);
    render(<ApiKeyItems keys={[key, { ...key, id: 2, name: "old", state: "revoked" }]} revoke={revoke} />);
    expect(screen.getAllByText(t.apiKeyRevoke)).toHaveLength(1);
    expect(screen.getAllByText(/pl_…abcd/)).toHaveLength(2);
    fireEvent.click(screen.getByText(t.apiKeyRevoke));
    expect(revoke).toHaveBeenCalledWith(key);
  });
});

describe("apiKeyDetails", () => {
  it("names the state of keys that do not work", () => {
    expect(apiKeyDetails(key)).toContain("read, write");
    expect(apiKeyDetails({ ...key, state: "expired" })).toContain(t.apiKeyState("expired"));
    expect(apiKeyDetails(key)).toContain(t.apiKeyNeverUsed);
  });
});

describe("apiKeyError", () => {
  it("maps the server's answers", () => {
    expect(apiKeyError(new ApiError(401, "the current password is wrong"), 20)).toBe(t.wrongCurrentPassword);
    expect(apiKeyError(new ApiError(401, "invalid code"), 20)).toBe(t.invalidTwoFactor);
    expect(apiKeyError(new ApiError(409, "you already have an API key with this name"), 20)).toBe(t.apiKeyNameTaken);
    expect(apiKeyError(new ApiError(409, "you already have 20 API keys; revoke one first"), 20)).toBe(t.apiKeyLimit(20));
    expect(apiKeyError(new ApiError(429, "too many", { retryAfter: 30 }), 20)).toBe(t.tooManyAttempts(30));
  });
});

describe("ApiKeySection", () => {
  it("creates a key with the password, shows it once and lists only its hint", async () => {
    const secret = "pl_" + "b".repeat(48) + "wxyz";
    const created = { ...key, id: 2, name: "scanner", hint: "wxyz", scopes: ["read", "write"] };
    let keys: ApiKey[] = [];
    const requests: { method: string; url: string; body: unknown }[] = [];
    vi.stubGlobal("fetch", async (url: string, init?: RequestInit) => {
      const method = init?.method ?? "GET";
      requests.push({ method, url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
      const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
      if (url === "/api/profile/2fa") return json(200, { enabled: false });
      if (url === "/api/profile/api-keys" && method === "GET") return json(200, { max: 20, keys });
      if (url === "/api/profile/api-keys" && method === "POST") {
        keys = [created as ApiKey];
        return json(201, { ...created, key: secret });
      }
      return new Response(null, { status: 404 });
    });
    try {
      render(<ApiKeySection />);
      expect(await screen.findByText(t.apiKeyNone)).toBeTruthy();
      fireEvent.change(screen.getByLabelText(t.apiKeyName), { target: { value: " scanner " } });
      fireEvent.click(screen.getByLabelText(t.apiKeyScopeWrite));
      fireEvent.change(screen.getByLabelText(t.currentPassword), { target: { value: "correct horse battery" } });
      fireEvent.click(screen.getByRole("button", { name: t.apiKeyCreate }));
      expect(await screen.findByText(secret)).toBeTruthy();
      expect(requests.find((r) => r.method === "POST")?.body).toEqual({
        name: "scanner", scopes: ["read", "write"], expiresInDays: 90, currentPassword: "correct horse battery", code: "",
      });
      expect(await screen.findByText(/pl_…wxyz/)).toBeTruthy();
      fireEvent.click(screen.getByRole("button", { name: t.done }));
      expect(screen.queryByText(secret)).toBeNull();
      expect(document.body.textContent).not.toContain(secret);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("sends upload alone, and only write when write is ticked: write includes upload", async () => {
    const posted: unknown[] = [];
    vi.stubGlobal("fetch", async (url: string, init?: RequestInit) => {
      const method = init?.method ?? "GET";
      const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
      if (url === "/api/profile/2fa") return json(200, { enabled: false });
      if (url === "/api/profile/api-keys" && method === "GET") return json(200, { max: 20, keys: [] });
      if (url === "/api/profile/api-keys" && method === "POST") {
        const body = JSON.parse(String(init?.body)) as { name: string; scopes: string[] };
        posted.push(body.scopes);
        return json(201, { ...key, name: body.name, scopes: body.scopes, key: "pl_" + "c".repeat(52) });
      }
      return new Response(null, { status: 404 });
    });
    try {
      render(<ApiKeySection />);
      await screen.findByText(t.apiKeyNone);
      const box = (label: string) => screen.getByLabelText(label) as HTMLInputElement;
      const submit = async (name: string) => {
        fireEvent.change(screen.getByLabelText(t.apiKeyName), { target: { value: name } });
        fireEvent.change(screen.getByLabelText(t.currentPassword), { target: { value: "correct horse battery" } });
        fireEvent.click(screen.getByRole("button", { name: t.apiKeyCreate }));
        await screen.findByText(t.apiKeyCreated(name));
        fireEvent.click(screen.getByRole("button", { name: t.done }));
      };
      // An upload-only key for a phone.
      fireEvent.click(box(t.apiKeyScopeRead));
      fireEvent.click(box(t.apiKeyScopeUpload));
      expect(box(t.apiKeyScopeUpload).checked).toBe(true);
      expect(box(t.apiKeyScopeUpload).disabled).toBe(false);
      await submit("iPhone");
      // write ticks and fixes upload, and only write is sent.
      fireEvent.click(box(t.apiKeyScopeWrite));
      expect(box(t.apiKeyScopeUpload).checked).toBe(true);
      expect(box(t.apiKeyScopeUpload).disabled).toBe(true);
      await submit("script");
      // Unticking write leaves upload as it was ticked by hand.
      fireEvent.click(box(t.apiKeyScopeWrite));
      expect(box(t.apiKeyScopeUpload).checked).toBe(true);
      expect(box(t.apiKeyScopeUpload).disabled).toBe(false);
      fireEvent.click(box(t.apiKeyScopeUpload));
      expect(box(t.apiKeyScopeUpload).checked).toBe(false);
      fireEvent.click(box(t.apiKeyScopeWrite));
      expect(box(t.apiKeyScopeUpload).checked).toBe(true);
      fireEvent.click(box(t.apiKeyScopeWrite));
      expect(box(t.apiKeyScopeUpload).checked).toBe(false);
      expect(posted).toEqual([["upload"], ["write"]]);
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
