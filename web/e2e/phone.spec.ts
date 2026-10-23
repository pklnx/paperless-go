import { devices } from "@playwright/test";
import { seed } from "./demo";
import { expect, test } from "./fixtures";
import { detail, documentLink, documentList, menuButton, navigation, openNavigation } from "./ui";

// A phone with a touch screen: no sidebar; the navigation is a sheet
// behind the menu button, and a document opens over the whole screen.
const { defaultBrowserType: _, ...phone } = devices["Pixel 7"];
test.use(phone);

test("the navigation opens from the menu, closes on Escape and filters the list", async ({ page }) => {
  await page.goto("/");
  await expect(documentList(page)).toBeVisible();
  // The sidebar is not shown: the only navigation is behind the menu.
  await expect(navigation(page)).toBeHidden();

  const dialog = await openNavigation(page);
  await expect(dialog.getByRole("navigation", { name: "Navigation" })).toBeInViewport();
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  await expect(menuButton(page)).toBeFocused();

  // Choosing a list closes the sheet.
  const again = await openNavigation(page);
  await again.getByRole("button", { name: /^Inbox/ }).click();
  await expect(again).toBeHidden();
  await expect(page).toHaveURL(/[?&]inbox=1/);
  await expect(documentList(page, "Inbox")).toBeVisible();
});

test("a document opens over the whole screen and Close returns to the list", async ({ page }) => {
  await page.goto("/");
  await documentLink(page, seed.incomeTax.title).click();
  const doc = detail(page, seed.incomeTax.title);
  await expect(doc).toBeInViewport({ ratio: 0.9 });
  await page.getByRole("button", { name: "Close document" }).click();
  await expect(doc).toHaveCount(0);
  await expect(documentLink(page, seed.incomeTax.title)).toBeInViewport();
});

test("the upload button is reachable and the page does not scroll sideways", async ({ page }) => {
  await page.goto("/");
  const choose = page.getByRole("button", { name: "Choose files" });
  await expect(choose).toBeInViewport();
  await expect(choose).toBeEnabled();
  await expect(documentList(page)).toBeVisible();
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  expect(overflow).toBeLessThanOrEqual(0);
});
