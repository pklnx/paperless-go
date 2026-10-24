import { devices } from "@playwright/test";
import { seed } from "./demo";
import { expect, test } from "./fixtures";
import { browseButton, browseColumn, bulkBar, closeDocument, collapseTag, detail, discardChanges, discardQuestion, documentLink, documentList, expandTag, field, keepEditing, menuButton, openBrowse, openNavigation, pageEntry, selectBox, shellNav, tagEntry } from "./ui";

// A phone with a touch screen (below Carbon's lg): no rail and no column;
// the SideNav is an overlay behind the menu button, the column (saved views,
// tags, correspondents, types) an overlay behind Browse, and a document
// opens below the header.
const { defaultBrowserType: _, ...phone } = devices["Pixel 7"];
test.use(phone);

/** The closed SideNav: inert (out of the tab order) and clipped off screen. */
async function expectNavigationClosed(page: Parameters<typeof shellNav>[0]) {
  const nav = shellNav(page);
  await expect(nav).not.toHaveClass(/cds--side-nav--expanded/);
  await expect(nav).toHaveAttribute("inert", "");
  // Clipped by the zero-wide SideNav (its own box is not empty).
  await expect(pageEntry(page, "Log")).not.toBeInViewport();
}

test("the navigation opens from the menu, closes on Escape with the focus back, and filters the list", async ({ page }) => {
  await page.goto("/");
  await expect(documentList(page)).toBeVisible();
  // No rail and no column: both are behind their buttons.
  await expectNavigationClosed(page);
  await expect(browseColumn(page)).toBeHidden();

  const nav = await openNavigation(page);
  await expect(nav.getByRole("link", { name: /^All documents/ })).toBeFocused();
  for (const name of ["Inbox", "Trash", "Log", "Profile"]) await expect(nav.getByRole("link", { name: new RegExp(`^${name}`) })).toBeInViewport();
  await page.keyboard.press("Escape");
  await expectNavigationClosed(page);
  await expect(menuButton(page)).toBeFocused();

  // Choosing a list closes the SideNav.
  const again = await openNavigation(page);
  await again.getByRole("link", { name: /^Inbox/ }).click();
  await expectNavigationClosed(page);
  await expect(page).toHaveURL(/[?&]inbox=1/);
  await expect(documentList(page, "Inbox")).toBeVisible();
});

test("the column opens from Browse as a dialog, closes on Escape with the focus back, and a tag filters the list", async ({ page }) => {
  await page.goto("/");
  await expect(browseButton(page)).toBeVisible();
  const dialog = await openBrowse(page);
  await expect(dialog).toBeVisible();
  // The focus is in the tree.
  await expect(dialog.getByRole("treeitem").first()).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(browseButton(page)).toBeFocused();

  const open = await openBrowse(page);
  await tagEntry(open, "Bills").click();
  await expect(open).toHaveCount(0);
  await expect(page).toHaveURL(/[?&]tag=[0-9a-f-]{36}/);
  await expect(documentList(page, "Bills")).toBeVisible();
});

test("neither overlay opens from the other", async ({ page }) => {
  await page.goto("/");
  await expect(documentList(page)).toBeVisible();
  // Behind the open SideNav the page is inert; a click that reaches the Browse button anyway opens nothing.
  await openNavigation(page);
  await browseButton(page).dispatchEvent("click");
  await expect(page.getByRole("dialog", { name: "Browse" })).toHaveCount(0);
  await expect(shellNav(page)).toHaveClass(/cds--side-nav--expanded/);
  await page.keyboard.press("Escape");
  await expectNavigationClosed(page);
  // Behind the column overlay the header is inert; a click that reaches the menu button opens nothing.
  const dialog = await openBrowse(page);
  await menuButton(page).dispatchEvent("click");
  await expectNavigationClosed(page);
  await expect(dialog).toBeVisible();
});

test("collapsed sub-tags are hidden inside the column overlay too", async ({ page }) => {
  await page.goto("/");
  const dialog = await openBrowse(page);
  await expect(tagEntry(dialog, "Bills")).toBeVisible();
  await collapseTag(dialog, "Finance");
  await expect(tagEntry(dialog, "Bills")).toBeHidden();
  await expect(tagEntry(dialog, "Finance")).toBeVisible();
  await expandTag(dialog, "Finance");
  await expect(tagEntry(dialog, "Bills")).toBeVisible();
});

test("a document opens below the header and Close returns to the list", async ({ page }) => {
  await page.goto("/");
  await documentLink(page, seed.incomeTax.title).click();
  const doc = detail(page, seed.incomeTax.title);
  await expect(doc).toBeInViewport({ ratio: 0.9 });
  await closeDocument(page);
  await expect(doc).toHaveCount(0);
  await expect(documentLink(page, seed.incomeTax.title)).toBeInViewport();
});

test("a question opened from the navigation keeps the focus on Keep editing, and Keep editing lands on the heading", async ({ page }) => {
  await page.goto("/");
  await documentLink(page, seed.incomeTax.title).click();
  await expect(detail(page, seed.incomeTax.title)).toBeVisible();
  await field(page, "Title").fill("Not saved");
  // A list chosen from the SideNav overlay: the overlay closes and would return the focus to the
  // menu button, outside the question; the question keeps it on its safe button.
  const nav = await openNavigation(page);
  await nav.getByRole("link", { name: /^Inbox/ }).click();
  await expect(discardQuestion(page)).toBeVisible();
  await expect(discardQuestion(page).getByRole("button", { name: "Keep editing", exact: true })).toBeFocused();
  await expectNavigationClosed(page);
  await expect(page).not.toHaveURL(/[?&]inbox=1/);
  // The link that asked is hidden in the closed SideNav: the focus goes to the detail's heading.
  await keepEditing(page);
  await expect(discardQuestion(page)).toHaveCount(0);
  await expect(page.getByTestId("document-detail").getByRole("heading", { level: 2 })).toBeFocused();
  await expect(field(page, "Title")).toHaveValue("Not saved");
  // Nothing unsaved is left behind.
  await closeDocument(page);
  await discardChanges(page);
  await expect(page).not.toHaveURL(/[?&]doc=/);
});

test("the upload button is reachable and the page does not scroll sideways or under the header", async ({ page }) => {
  await page.goto("/");
  const choose = page.getByRole("button", { name: "Choose files" });
  await expect(choose).toBeInViewport();
  await expect(choose).toBeEnabled();
  await expect(documentList(page)).toBeVisible();
  const overflow = await page.evaluate(() => ({ x: document.documentElement.scrollWidth - window.innerWidth, y: document.documentElement.scrollHeight - window.innerHeight }));
  expect(overflow.x).toBeLessThanOrEqual(0);
  // The page scrolls inside the shell, never as a whole under the fixed header.
  expect(overflow.y).toBeLessThanOrEqual(0);
});

test("a page (the profile) scrolls inside the shell too, not under the header", async ({ page }) => {
  await page.goto("/?view=profile");
  await expect(page.getByRole("heading", { name: "Profile", exact: true })).toBeVisible();
  // The profile is taller than a phone screen: its main scrolls, the document does not.
  const main = page.getByRole("main");
  await expect(main).toBeVisible();
  const sizes = await main.evaluate((el) => ({ scroll: el.scrollHeight, client: el.clientHeight, doc: document.documentElement.scrollHeight - window.innerHeight }));
  expect(sizes.scroll).toBeGreaterThan(sizes.client);
  expect(sizes.doc).toBeLessThanOrEqual(0);
});

// A narrow screen (390px, an iPhone's width): every action of the bulk bar
// is on screen without sideways scrolling (the actions wrap). With a fine
// pointer, so the row's checkbox is there to check (a touch screen hides it
// until a long press selects); the width is what this test is about.
test.describe("on a 390px screen", () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: false, isMobile: false });

  test("the bulk bar shows all of its actions without horizontal scrolling", async ({ page }) => {
    await page.goto("/");
    await expect(documentList(page)).toBeVisible();
    await selectBox(page, seed.incomeTax.title).check();
    const bar = bulkBar(page);
    await expect(bar.getByText("1 selected", { exact: true })).toBeVisible();
    for (const name of ["Tags…", "Correspondent…", "Type…", "Remove from inbox", "Move to inbox", "Favorite", "Unfavorite", "Delete…"]) {
      await expect(bar.getByRole("button", { name, exact: true })).toBeInViewport({ ratio: 1 });
    }
    const overflow = await bar.evaluate((el) => Array.from(el.querySelectorAll("*")).concat(el).some((e) => e.scrollWidth > e.clientWidth + 1));
    expect(overflow).toBe(false);
    const pageOverflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    expect(pageOverflow).toBeLessThanOrEqual(0);
  });
});

// Between Carbon's md (48rem) and lg (66rem): still below lg, so the
// SideNav and the column are overlays, as on a phone, with a mouse.
test.describe("on a desktop of 1024px", () => {
  const { defaultBrowserType: _d, ...desktop } = devices["Desktop Chrome"];
  test.use({ ...desktop, viewport: { width: 1024, height: 768 } });

  test("the menu button and Browse open their overlays; the header has the name and Log out", async ({ page }) => {
    await page.goto("/");
    await expect(documentList(page)).toBeVisible();
    await expectNavigationClosed(page);
    await expect(menuButton(page)).toBeVisible();
    await expect(browseColumn(page)).toBeHidden();
    const dialog = await openBrowse(page);
    await expect(tagEntry(dialog, "Finance")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
    // The header is there at every width, with the app name and Log out.
    const header = page.getByRole("banner");
    await expect(header.getByRole("link", { name: "paperless-go" })).toBeVisible();
    await expect(header.getByRole("button", { name: "Log out" })).toBeVisible();
  });
});

// From lg (66rem): the SideNav is Carbon's rail, the column is part of the page.
test.describe("on a desktop of 1280px", () => {
  const { defaultBrowserType: _d, ...desktop } = devices["Desktop Chrome"];
  test.use({ ...desktop, viewport: { width: 1280, height: 800 } });

  test("the SideNav is a rail and the column a column, with no menu and no Browse button", async ({ page }) => {
    await page.goto("/");
    await expect(documentList(page)).toBeVisible();
    const nav = shellNav(page);
    await expect(nav).toHaveClass(/cds--side-nav--rail/);
    await expect(nav).not.toHaveAttribute("inert", "");
    await expect(menuButton(page)).toBeHidden();
    await expect(browseButton(page)).toBeHidden();
    await expect(browseColumn(page)).toBeVisible();
    await expect(page.getByRole("dialog", { name: "Browse" })).toHaveCount(0);
    await expect(tagEntry(browseColumn(page), "Finance")).toBeVisible();
    // The rail expands on hover and shows the names.
    await expect(pageEntry(page, "Log")).toBeVisible();
    await nav.hover();
    await expect(nav).toHaveClass(/cds--side-nav--expanded/);
  });
});
