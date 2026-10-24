import { devices } from "@playwright/test";
import { seed } from "./demo";
import { expect, test } from "./fixtures";
import { closeDocument, collapseTag, detail, documentLink, documentList, expandTag, menuButton, navigation, openNavigation, sidebar, tagEntry } from "./ui";

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

test("collapsed sub-tags are hidden inside the navigation sheet too", async ({ page }) => {
  await page.goto("/");
  const dialog = await openNavigation(page);
  const nav = dialog.getByRole("navigation", { name: "Navigation" });
  await expect(tagEntry(nav, "Bills")).toBeVisible();
  // The same Sidebar under the sheet's own .legacy root (index.css).
  await collapseTag(nav, "Finance");
  await expect(tagEntry(nav, "Bills")).toBeHidden();
  await expect(tagEntry(nav, "Finance")).toBeVisible();
  await expandTag(nav, "Finance");
  await expect(tagEntry(nav, "Bills")).toBeVisible();
});

test("a document opens over the whole screen and Close returns to the list", async ({ page }) => {
  await page.goto("/");
  await documentLink(page, seed.incomeTax.title).click();
  const doc = detail(page, seed.incomeTax.title);
  await expect(doc).toBeInViewport({ ratio: 0.9 });
  await closeDocument(page);
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

// Between Carbon's md (48rem) and lg (66rem), where the sidebar is
// permanent: the header's menu button is hidden, so no width shows both.
// Part 2 of #150 moves the shell to lg and replaces this block.
test.describe("on a desktop of 1024px", () => {
  const { defaultBrowserType: _d, ...desktop } = devices["Desktop Chrome"];
  test.use({ ...desktop, viewport: { width: 1024, height: 768 } });

  test("the sidebar is visible and the menu button is hidden", async ({ page }) => {
    await page.goto("/");
    await expect(documentList(page)).toBeVisible();
    await expect(sidebar(page)).toBeVisible();
    await expect(sidebar(page).getByRole("navigation", { name: "Navigation" })).toBeVisible();
    await expect(menuButton(page)).toBeHidden();
    // The header is there at every width, with the app name and Log out.
    const header = page.getByRole("banner");
    await expect(header.getByRole("link", { name: "paperless-go" })).toBeVisible();
    await expect(header.getByRole("button", { name: "Log out" })).toBeVisible();
  });
});
