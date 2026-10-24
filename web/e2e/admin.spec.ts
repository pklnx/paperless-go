import type { Page } from "@playwright/test";
import { origin, resetTwoFactorUser, seed, unique } from "./demo";
import { expect, test } from "./fixtures";
import { confirmDialog, documentRow, logEntries, logFilter, pageEntry, resetPasswordPanel, search, userRow } from "./ui";

/** Opens the users page. */
async function openUsers(page: Page): Promise<void> {
  await page.goto("/");
  await pageEntry(page, "Users").click();
  await expect(page).toHaveURL(/[?&]view=users/);
  await expect(page.getByRole("heading", { name: "Users", exact: true })).toBeVisible();
}

/** Adds a user (no admin); returns the generated password the page shows once, and closes that panel. */
async function addUser(page: Page, name: string): Promise<string> {
  await page.getByRole("button", { name: "Add user", exact: true }).click();
  await page.getByLabel("User name", { exact: true }).fill(name);
  await page.getByRole("button", { name: "Add user", exact: true }).click();
  return acknowledgePassword(page, name);
}

/** Reads the generated password in the panel that shows it once, then closes the panel. */
async function acknowledgePassword(page: Page, name: string): Promise<string> {
  const panel = page.getByRole("status").filter({ hasText: `Password for ${name}` });
  await expect(panel).toBeVisible();
  await expect(panel).toContainText("Shown only now.");
  const password = ((await panel.getByRole("code").textContent()) ?? "").trim();
  expect(password.length).toBeGreaterThan(8);
  await panel.getByRole("button", { name: "Done", exact: true }).click();
  await expect(panel).toHaveCount(0);
  return password;
}

test("a user is created with a generated password, locked, unlocked, given a new password and removed", async ({ page }) => {
  const name = unique("user");
  await openUsers(page);
  const first = await addUser(page, name);
  const row = userRow(page, name);
  await expect(row).toContainText("must change password");
  await expect(row).toContainText("no 2FA");

  // Lock asks with a native confirm; Unlock asks for nothing.
  await confirmDialog(page, () => row.getByRole("button", { name: "Lock", exact: true }).click());
  await expect(row).toContainText("locked");
  await row.getByRole("button", { name: "Unlock", exact: true }).click();
  await expect(row).toContainText("must change password");
  await expect(row.getByRole("button", { name: "Lock", exact: true })).toBeVisible();

  // Reset password asks in the card itself, then shows the new password once.
  const panel = resetPasswordPanel(row);
  await confirmDialog(page, () => row.getByRole("button", { name: "Reset password", exact: true }).click(), panel.getByRole("button", { name: "Reset password", exact: true }));
  const second = await acknowledgePassword(page, name);
  expect(second).not.toBe(first);

  // The user owns nothing, so they can be removed (a native confirm).
  await confirmDialog(page, () => row.getByRole("button", { name: "Remove", exact: true }).click());
  await expect(row).toHaveCount(0);
});

test("the admin resets another user's two-factor authentication", async ({ page }) => {
  await openUsers(page);
  const row = userRow(page, resetTwoFactorUser.name);
  await expect(row).toContainText("2FA on");
  await confirmDialog(page, () => row.getByRole("button", { name: "Reset 2FA", exact: true }).click());
  await expect(row).toContainText("no 2FA");
  await expect(row.getByRole("button", { name: "Reset 2FA", exact: true })).toHaveCount(0);
});

test("the log shows a login made in the test, and the action filter narrows it", async ({ page, playwright }) => {
  const name = unique("user");
  await openUsers(page);
  const password = await addUser(page, name);

  // A fresh login of that user, made here: the entry does not depend on
  // what other tests logged before (the list shows the newest first).
  // The server needs an Origin, which a browser sends and this client does not.
  // No stored session: a login with the admin's cookie would end the admin's session.
  const client = await playwright.request.newContext({ baseURL: origin, storageState: { cookies: [], origins: [] }, extraHTTPHeaders: { Origin: origin } });
  try {
    const login = await client.post("/api/session", { data: { username: name, password } });
    expect(login.ok()).toBe(true);
  } finally {
    await client.dispose();
  }

  await pageEntry(page, "Log").click();
  await expect(page).toHaveURL(/[?&]view=log/);
  const logins = logEntries(page).filter({ hasText: "Logged in" }).filter({ hasText: name });
  const added = logEntries(page).filter({ hasText: "Added user" }).filter({ hasText: name });
  await expect(logins).toHaveCount(1);
  await expect(added).toHaveCount(1);

  await logFilter(page, "Action").selectOption({ label: "Logins" });
  await expect(added).toHaveCount(0);
  await expect(logins).toHaveCount(1);

  await logFilter(page, "Action").selectOption({ label: "User management" });
  await expect(added).toHaveCount(1);
  await expect(logins).toHaveCount(0);
});

test("Everyone's documents shows another user's documents with their owner; switching it off hides them", async ({ page }) => {
  await page.goto("/");
  await search(page, seed.gym.word);
  await expect(page.getByText("Nothing found", { exact: true })).toBeVisible();

  const everyone = page.getByRole("switch", { name: "Everyone's documents", exact: true });
  await everyone.check();
  await expect(documentRow(page, seed.gym.title)).toContainText("Owner: max");

  await everyone.uncheck();
  await expect(page.getByText("Nothing found", { exact: true })).toBeVisible();
  await expect(documentRow(page, seed.gym.title)).toHaveCount(0);
});
