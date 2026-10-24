import { readFileSync } from "node:fs";
import type { Page } from "@playwright/test";
import { adminWithout2FA, generatedPasswordUser, lockedUser, timeStep, totp, twoFactorUser, type Login } from "./demo";
import { expect, test } from "./fixtures";
import { documentList, logInThroughForm, searchBox, twoFactorRequiredNotice } from "./ui";

// No session: these tests use the login page.
test.use({ storageState: { cookies: [], origins: [] } });

// Without a session, GET /api/session answers 401 by design (on load and
// after logging out); the browser logs that as a console error.
test.beforeEach(({ guards }) => guards.allowHTTPError("/api/session", 401));

// The password field. Exact: other fields' names contain "password".
const passwordField = (page: Page) => page.getByLabel("Password", { exact: true });
// Exact: "Log in with a passkey" is a button too.
const logIn = (page: Page) => page.getByRole("button", { name: "Log in", exact: true });

/** Fills in the login form and submits it. */
const submitLogin = (page: Page, user: Login, password = user.password) => logInThroughForm(page, user.name, password);

test("login with password and authentication code, then log out", async ({ page }) => {
  await page.goto("/");
  await submitLogin(page, twoFactorUser);
  await expect(page.getByRole("heading", { name: "Enter your authentication code" })).toBeVisible();
  // The demo confirmed this user's 2FA with an hour-old code, so the
  // current step is unused. The test runs once (retries: 0).
  await page.getByLabel("Authentication code").fill(totp(twoFactorUser.totpSecret, timeStep()));
  await logIn(page).click();

  // kim has no documents: the list's empty state, with kim logged in.
  await expect(searchBox(page)).toBeVisible();
  await expect(page.getByText("No documents yet", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Profile" })).toHaveText(twoFactorUser.name);
  await page.getByRole("button", { name: "Log out" }).click();
  await expect(page.getByLabel("User name")).toBeVisible();
});

test("a wrong password is refused and the field is cleared", async ({ page }) => {
  await page.goto("/");
  const answer = page.waitForResponse((r) => r.url().endsWith("/api/session") && r.request().method() === "POST");
  await submitLogin(page, twoFactorUser, "not-the-password");
  expect((await answer).status()).toBe(401);
  await expect(page.getByRole("alert")).toHaveText("Wrong user name or password.");
  await expect(passwordField(page)).toHaveValue("");
});

test("a locked user is refused before the code step", async ({ page, guards }) => {
  guards.allowHTTPError("/api/session", 403);
  await page.goto("/");
  const answer = page.waitForResponse((r) => r.url().endsWith("/api/session") && r.request().method() === "POST");
  await submitLogin(page, lockedUser);
  expect((await answer).status()).toBe(403);
  await expect(page.getByRole("alert")).toHaveText("This user is locked. Ask an admin to unlock it.");
  await expect(page.getByRole("heading", { name: "Enter your authentication code" })).toHaveCount(0);
});

test("an admin without 2FA must set it up before anything else", async ({ page, guards }) => {
  // The guard answers 403 to everything else.
  guards.allowHTTPError("/api/documents", 403);
  await page.goto("/");
  await submitLogin(page, adminWithout2FA);
  const setup = page.getByRole("heading", { name: "Set up authenticator app" });
  await expect(setup).toBeVisible();
  await expect(twoFactorRequiredNotice(page)).toBeVisible();

  // Neither another page nor a document opens. Any well-formed ID proves it.
  for (const path of ["/?view=log", "/?doc=00000000-0000-4000-8000-000000000001"]) {
    await page.goto(path);
    await expect(setup).toBeVisible();
    await expect(searchBox(page)).toHaveCount(0);
    await expect(page.getByRole("form", { name: "Edit document" })).toHaveCount(0);
  }
  // The API refuses the documents too, from the page's own session.
  const status = await page.evaluate(async () => (await fetch("/api/documents", { headers: { Accept: "application/json" } })).status);
  expect(status).toBe(403);
  await page.goto("/");

  // The setup: the manual key from the page, a code for it, the recovery codes.
  await page.getByRole("button", { name: "Set up authenticator app" }).click();
  await expect(page.getByText("If you cannot scan it, enter this key in the app:")).toBeVisible();
  const secret = ((await page.getByRole("code").textContent()) ?? "").trim();
  expect(secret).toMatch(/^[A-Z2-7]+=*$/);
  await page.getByLabel("Authentication code").fill(totp(secret, timeStep()));
  await page.getByRole("button", { name: "Enable two-factor authentication" }).click();
  await expect(page.getByLabel("Recovery codes")).toBeVisible();
  await page.getByRole("button", { name: "I have saved the codes" }).click();

  // Now the app: neo owns no documents.
  await expect(searchBox(page)).toBeVisible();
  await expect(page.getByText("No documents yet", { exact: true })).toBeVisible();
  await expect(documentList(page)).toHaveCount(0);
});

test("a generated password must be replaced before anything else", async ({ page }) => {
  await page.goto("/");
  await submitLogin(page, generatedPasswordUser);
  await expect(page.getByRole("heading", { name: "Choose your own password" })).toBeVisible();
  await expect(searchBox(page)).toHaveCount(0);
  await page.getByLabel("Current password").fill(generatedPasswordUser.password);
  await page.getByLabel("New password", { exact: true }).fill("pat-own-password-1");
  await page.getByLabel("Repeat new password").fill("pat-own-password-1");
  await page.getByRole("button", { name: "Change password" }).click();
  await expect(searchBox(page)).toBeVisible();
  await expect(page.getByRole("heading", { name: "Choose your own password" })).toHaveCount(0);
});

test("the web app manifest and its icons are served without a session", async ({ page }) => {
  // The built binary: Vite copies web/public into the embedded build, and
  // the real route table lets these paths through without a login.
  const files: Record<string, string> = {
    "/manifest.webmanifest": "application/manifest+json",
    "/favicon-32.png": "image/png",
    "/apple-touch-icon.png": "image/png",
    "/icon-192.png": "image/png",
    "/icon-512.png": "image/png",
    "/icon-maskable-512.png": "image/png",
  };
  for (const [path, type] of Object.entries(files)) {
    const resp = await page.request.get(path);
    expect(resp.status(), path).toBe(200);
    expect(resp.headers()["content-type"], path).toBe(type);
    expect(resp.headers()["content-security-policy"], path).toContain("default-src 'self'");
    expect(Buffer.compare(await resp.body(), readFileSync(new URL(`../public${path}`, import.meta.url))), path).toBe(0);
  }
});

test("the product mark follows the colour scheme", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "light" });
  await page.goto("/");
  await expect(page.locator(".app-mark--light")).toBeVisible();
  await expect(page.locator(".app-mark--dark")).toBeHidden();
  await page.emulateMedia({ colorScheme: "dark" });
  await expect(page.locator(".app-mark--dark")).toBeVisible();
  await expect(page.locator(".app-mark--light")).toBeHidden();
});
