import { bills, seed } from "./demo";
import { expect, test } from "./fixtures";
import { documentList, documentRow, documentRows, navigation, search, searchBox, tagEntry } from "./ui";

test("a word finds exactly its document, with the word highlighted", async ({ page }) => {
  await page.goto("/");
  await search(page, seed.incomeTax.word);
  await expect(page).toHaveURL(new RegExp(`[?&]q=${seed.incomeTax.word}`));
  const results = documentList(page, "Search results");
  await expect(documentRows(results)).toHaveCount(1);
  const row = documentRow(page, seed.incomeTax.title);
  // The snippet marks the match (role mark, which getByRole does not take).
  await expect(row).toMatchAriaSnapshot(`- mark: ${seed.incomeTax.word}`);
});

test("a word in no document shows the empty state", async ({ page }) => {
  await page.goto("/");
  await search(page, "nowhereword");
  await expect(page.getByText("Nothing found", { exact: true })).toBeVisible();
  await expect(documentList(page, "Search results")).toHaveCount(0);
  await page.getByRole("button", { name: "Clear search" }).click();
  await expect(searchBox(page)).toHaveValue("");
  await expect(documentRow(page, seed.incomeTax.title)).toBeVisible();
});

test("the German document is found by Kündigung", async ({ page }) => {
  await page.goto("/");
  await search(page, "Kündigung");
  await expect(documentRows(documentList(page, "Search results"))).toHaveCount(1);
  await expect(documentRow(page, seed.termination.title)).toBeVisible();
});

test("a tag in the sidebar filters the list; the chip removes the filter", async ({ page }) => {
  await page.goto("/");
  const nav = navigation(page);
  // Bills is a sub-tag of Finance, shown below it.
  await expect(nav.getByRole("button", { name: "Sub-tags of Finance" })).toHaveAttribute("aria-expanded", "true");
  await tagEntry(nav, "Bills").click();
  await expect(page).toHaveURL(/[?&]tag=[0-9a-f-]{36}/);
  const list = documentList(page, "Bills");
  await expect(documentRows(list)).toHaveCount(bills.length);
  for (const title of bills) await expect(documentRow(page, title)).toBeVisible();

  await page.getByRole("button", { name: "Remove filter Tag: Bills" }).click();
  await expect(page).not.toHaveURL(/[?&]tag=/);
  await expect(documentRow(page, seed.incomeTax.title)).toBeVisible();
  await expect(documentList(page, "All documents")).toBeVisible();
});

test("back and forward restore the search", async ({ page }) => {
  await page.goto("/");
  await search(page, seed.water.word);
  await expect(documentRow(page, seed.water.title)).toBeVisible();
  await search(page, seed.lease.word);
  await expect(documentRow(page, seed.lease.title)).toBeVisible();
  await expect(documentRow(page, seed.water.title)).toHaveCount(0);

  await page.goBack();
  await expect(searchBox(page)).toHaveValue(seed.water.word);
  await expect(documentRow(page, seed.water.title)).toBeVisible();
  await expect(documentRow(page, seed.lease.title)).toHaveCount(0);

  await page.goForward();
  await expect(searchBox(page)).toHaveValue(seed.lease.word);
  await expect(documentRow(page, seed.lease.title)).toBeVisible();
  await expect(documentRow(page, seed.water.title)).toHaveCount(0);
});
