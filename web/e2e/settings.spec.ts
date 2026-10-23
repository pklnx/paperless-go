import { passwordChangeUser, plainUser, unique } from "./demo";
import { expect, test } from "./fixtures";
import { confirmDialog, logInThroughForm, navigation, themeToggle } from "./ui";

test.describe("as a user without a stored session", () => {
  test.use({ storageState: { cookies: [], origins: [] } });
  // Without a session, GET /api/session answers 401 by design.
  test.beforeEach(({ guards }) => guards.allowHTTPError("/api/session", 401));

  test("an API key is created with the password, shown once, and revoked", async ({ page }) => {
    const name = unique("script");
    await page.goto("/");
    await logInThroughForm(page, plainUser.name, plainUser.password);
    await navigation(page).getByRole("button", { name: "Profile" }).click();
    await expect(page).toHaveURL(/[?&]view=profile/);

    await page.getByLabel("Name of the key").fill(name);
    await expect(page.getByRole("checkbox", { name: /^read:/ })).toBeChecked();
    // The last "Current password" of the profile: the API key form's.
    // max has no 2FA: the password alone confirms the key.
    await page.getByLabel("Current password").last().fill(plainUser.password);
    await page.getByRole("button", { name: "Create API key" }).click();

    const created = page.getByRole("status").filter({ hasText: `API key “${name}” created. Copy it now: it is shown only once.` });
    await expect(created).toBeVisible();
    await expect(created.getByRole("code")).toHaveText(/^pl_\S+$/);
    await created.getByRole("button", { name: "Done" }).click();
    await expect(created).toHaveCount(0);

    const key = page.getByRole("listitem").filter({ hasText: name });
    await expect(key).toContainText("read");
    await expect(key).toContainText("never used");
    // Revoke asks with a native confirm.
    await confirmDialog(page, () => key.getByRole("button", { name: "Revoke" }).click());
    await expect(key).toContainText("revoked");
    await expect(key.getByRole("button", { name: "Revoke" })).toHaveCount(0);
  });

  test("a password change: the old password stops working, the new one logs in", async ({ page }) => {
    const next = `${unique("ida")}-passphrase`;
    await page.goto("/");
    await logInThroughForm(page, passwordChangeUser.name, passwordChangeUser.password);
    await navigation(page).getByRole("button", { name: "Profile" }).click();
    // The first "Current password" of the profile: the password form's.
    await page.getByLabel("Current password").first().fill(passwordChangeUser.password);
    await page.getByLabel("New password", { exact: true }).fill(next);
    await page.getByLabel("Repeat new password").fill(next);
    await page.getByRole("button", { name: "Change password" }).click();
    await expect(page.getByRole("status").filter({ hasText: "Password changed." })).toBeVisible();

    await navigation(page).getByRole("button", { name: "Log out" }).click();
    await expect(page.getByLabel("User name")).toBeVisible();
    await logInThroughForm(page, passwordChangeUser.name, passwordChangeUser.password);
    await expect(page.getByRole("alert")).toHaveText("Wrong user name or password.");
    await logInThroughForm(page, passwordChangeUser.name, next);
    // Logged in again, on the page the URL still names.
    await expect(page).toHaveURL(/[?&]view=profile/);
    await expect(page.getByText(`Logged in as ${passwordChangeUser.name}`)).toBeVisible();
  });
});

test.describe("the theme", () => {
  test.use({ colorScheme: "light" });

  test("the theme switch flips and is kept after a reload", async ({ page }) => {
    await page.goto("/");
    const toggle = themeToggle(page);
    await expect(toggle).toHaveAccessibleName("Switch to dark mode");
    await toggle.click();
    await expect(toggle).toHaveAccessibleName("Switch to light mode");
    await page.reload();
    await expect(themeToggle(page)).toHaveAccessibleName("Switch to light mode");
  });
});

test.describe("in German", () => {
  test.use({ locale: "de-DE" });

  test("the UI follows the browser's language", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByRole("searchbox", { name: "Dokumente durchsuchen" })).toBeVisible();
    await expect(page.getByRole("searchbox", { name: "Search documents" })).toHaveCount(0);
  });
});
