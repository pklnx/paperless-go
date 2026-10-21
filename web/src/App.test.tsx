import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { App } from "./App";
import { t } from "./i18n";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function stubSystem(status: number) {
  vi.stubGlobal("fetch", async () => new Response("", { status }));
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener: () => {}, removeEventListener: () => {} }));
}

describe("App", () => {
  it("shows a healthy server", async () => {
    stubSystem(200);
    render(<App />);
    expect(screen.getByRole("status").textContent).toBe(t.statusChecking);
    await waitFor(() => expect(screen.getByRole("status").textContent).toBe(t.statusOk));
  });
  it("shows a server without database", async () => {
    stubSystem(503);
    render(<App />);
    await waitFor(() => expect(screen.getByRole("status").textContent).toBe(t.statusDown));
  });
});
