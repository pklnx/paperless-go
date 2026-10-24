import { readdirSync, readFileSync } from "node:fs";
import type { Page } from "@playwright/test";
import { adminWithout2FA, generatedPasswordUser, lockedUser, timeStep, totp, twoFactorUser, type Login } from "./demo";
import { expect, test } from "./fixtures";
import { documentList, logInButton, logInThroughForm, logOut, loginError, passwordField, productMark, searchBox, twoFactorRequiredNotice, userNameField } from "./ui";

// No session: these tests use the login page.
test.use({ storageState: { cookies: [], origins: [] } });

// Without a session, GET /api/session answers 401 by design (on load and
// after logging out); the browser logs that as a console error.
test.beforeEach(({ guards }) => guards.allowHTTPError("/api/session", 401));

/** Fills in the login form and submits it. */
const submitLogin = (page: Page, user: Login, password = user.password) => logInThroughForm(page, user.name, password);

test("login with password and authentication code, then log out", async ({ page }) => {
  await page.goto("/");
  await submitLogin(page, twoFactorUser);
  await expect(page.getByRole("heading", { name: "Enter your authentication code" })).toBeVisible();
  // The demo confirmed this user's 2FA with an hour-old code, so the
  // current step is unused. The test runs once (retries: 0).
  await page.getByLabel("Authentication code").fill(totp(twoFactorUser.totpSecret, timeStep()));
  await logInButton(page).click();

  // kim has no documents: the list's empty state, with kim logged in.
  await expect(searchBox(page)).toBeVisible();
  await expect(page.getByText("No documents yet", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Profile" })).toHaveText(twoFactorUser.name);
  await logOut(page);
  await expect(userNameField(page)).toBeVisible();
});

test("a wrong password is refused and the field is cleared", async ({ page }) => {
  await page.goto("/");
  const answer = page.waitForResponse((r) => r.url().endsWith("/api/session") && r.request().method() === "POST");
  await submitLogin(page, twoFactorUser, "not-the-password");
  expect((await answer).status()).toBe(401);
  await expect(loginError(page)).toHaveText("Wrong user name or password.");
  await expect(passwordField(page)).toHaveValue("");
});

test("a locked user is refused before the code step", async ({ page, guards }) => {
  guards.allowHTTPError("/api/session", 403);
  await page.goto("/");
  const answer = page.waitForResponse((r) => r.url().endsWith("/api/session") && r.request().method() === "POST");
  await submitLogin(page, lockedUser);
  expect((await answer).status()).toBe(403);
  await expect(loginError(page)).toHaveText("This user is locked. Ask an admin to unlock it.");
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
  // Exactly one mark, the one for the applied theme; it is decorative and its file loads.
  const mark = productMark(page);
  await expect(mark).toHaveCount(1);
  await expect(mark).toBeVisible();
  await expect(mark).toHaveClass(/(^|\s)app-mark--light(\s|$)/);
  await expect(mark).toHaveAttribute("alt", "");
  expect(await mark.evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth > 0)).toBe(true);
  await page.emulateMedia({ colorScheme: "dark" });
  await expect(mark).toHaveCount(1);
  await expect(mark).toHaveClass(/(^|\s)app-mark--dark(\s|$)/);
  await expect(mark).not.toHaveClass(/app-mark--light/);
  await expect.poll(() => mark.evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth > 0)).toBe(true);
});

test("the built assets are served as files only", async ({ request }) => {
  // The built binary, whose embedded build holds the real hashed files and
  // pdf.js's data files (the Go tests run without a build). The request
  // fixture, not a page navigation: a 404 is the expected answer here, and
  // the guards fail a test on the console error a page navigation to it
  // logs (pklnx/mail-archive#250).
  const html = await (await request.get("/")).text();
  const script = /\ssrc="(\/assets\/[^"]+\.js)"/.exec(html)?.[1];
  const style = /\shref="(\/assets\/[^"]+\.css)"/.exec(html)?.[1];
  expect(script, "index.html names a script under /assets/").toBeTruthy();
  expect(style, "index.html names a style sheet under /assets/").toBeTruthy();
  const immutable = "public, max-age=31536000, immutable";

  // The host's type table decides the exact JavaScript type string.
  const js = await request.get(script!);
  expect(js.status()).toBe(200);
  expect(js.headers()["content-type"]).toMatch(/^(text|application)\/javascript/);
  expect(js.headers()["cache-control"]).toBe(immutable);
  expect(js.headers()["cross-origin-resource-policy"]).toBe("same-origin");
  expect((await js.body()).length).toBeGreaterThan(0);
  const css = await request.get(style!);
  expect(css.status()).toBe(200);
  expect(css.headers()["content-type"]).toMatch(/^text\/css/);
  expect(css.headers()["cache-control"]).toBe(immutable);

  // pdf.js's data files sit in a folder named by its version (vite.config.ts):
  // the files are served, immutable, the folders are no listing.
  const pdfjs = new URL("../node_modules/pdfjs-dist/", import.meta.url);
  const { version } = JSON.parse(readFileSync(new URL("package.json", pdfjs), "utf8")) as { version: string };
  const base = `/assets/pdfjs-${version}`;
  const cmap = readdirSync(new URL("cmaps", pdfjs)).sort()[0]!;
  // An extensionless file in a folder below, and a map in another.
  for (const file of [`cmaps/${cmap}`, "wasm/LICENSE_OPENJPEG"]) {
    const resp = await request.get(`${base}/${file}`);
    expect(resp.status(), file).toBe(200);
    expect(resp.headers()["cache-control"], file).toBe(immutable);
    expect(Buffer.compare(await resp.body(), readFileSync(new URL(file, pdfjs))), file).toBe(0);
  }

  // No listing of any folder, and no redirect: a plain, uncached 404.
  const name = script!.slice("/assets/".length);
  for (const path of ["/assets/", "/assets/x/index.html", "/assets/nope.js", `${base}/`, `${base}/cmaps/`, `${base}/cmaps`, `${base}/wasm/`]) {
    const resp = await request.get(path, { maxRedirects: 0 });
    expect(resp.status(), path).toBe(404);
    expect(resp.headers()["cache-control"], path).toBe("no-cache");
    expect(resp.headers()["cross-origin-resource-policy"], path).toBe("same-origin");
    const body = await resp.text();
    expect(body, path).not.toContain(name);
    expect(body, path).not.toContain(cmap);
    expect(body, path).not.toContain("LICENSE_OPENJPEG");
  }

  // The mux routes on the decoded path: the folder under another spelling
  // is the same file.
  const encoded = await request.get(script!.replace("/assets/", "/%61ssets/"));
  expect(encoded.status()).toBe(200);
  expect(encoded.headers()["cache-control"]).toBe(immutable);
});
