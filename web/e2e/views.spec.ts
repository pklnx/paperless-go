import { seed, unique } from "./demo";
import { expect, test } from "./fixtures";
import { documentList, documentRow, documentRows, notice, openList, openSavedViewMenu, savedViewEntry, search, sheet } from "./ui";

test("a search is saved as a view, opened from the sidebar, renamed and deleted", async ({ page }) => {
  const name = unique("Water");
  const renamed = unique("Water renamed");
  await page.goto("/");
  await search(page, seed.water.word);
  await expect(documentRow(page, seed.water.title)).toBeVisible();

  // "Save view…" names what the list shows; the new view is open at once.
  await page.getByRole("button", { name: "Save view…", exact: true }).click();
  const save = sheet(page, "Save this view");
  await save.getByLabel("Name", { exact: true }).fill(name);
  await save.getByRole("button", { name: "Save", exact: true }).click();
  await expect(notice(page, `View “${name}” saved.`)).toBeVisible();
  await expect(savedViewEntry(page, name)).toHaveAttribute("aria-current", "page");
  await expect(page).toHaveURL(/[?&]sv=[0-9a-f-]{36}/);

  // Another list, then the view from the sidebar: its search comes back.
  await openList(page, "All documents");
  await expect(page).not.toHaveURL(/[?&]sv=/);
  await expect(documentRow(page, seed.incomeTax.title)).toBeVisible();
  await savedViewEntry(page, name).click();
  await expect(page).toHaveURL(/[?&]sv=[0-9a-f-]{36}/);
  await expect(page).toHaveURL(new RegExp(`[?&]q=${seed.water.word}`));
  await expect(documentRows(documentList(page, "Search results"))).toHaveCount(1);
  await expect(documentRow(page, seed.water.title)).toBeVisible();
  await expect(documentRow(page, seed.incomeTax.title)).toHaveCount(0);

  // Rename: kept on the server, so it survives a reload.
  await (await openSavedViewMenu(page, name)).getByRole("menuitem", { name: "Rename", exact: true }).click();
  const rename = sheet(page, "Rename");
  await rename.getByLabel("Name", { exact: true }).fill(renamed);
  await rename.getByRole("button", { name: "Save", exact: true }).click();
  await expect(savedViewEntry(page, renamed)).toBeVisible();
  await expect(savedViewEntry(page, name)).toHaveCount(0);
  await page.reload();
  await expect(savedViewEntry(page, renamed)).toBeVisible();

  // Delete asks first, and the documents stay.
  await (await openSavedViewMenu(page, renamed)).getByRole("menuitem", { name: "Delete", exact: true }).click();
  const remove = sheet(page, `Delete “${renamed}”?`);
  await expect(remove).toContainText("Only the saved view is deleted; the documents stay.");
  await remove.getByRole("button", { name: "Delete", exact: true }).click();
  await expect(notice(page, `View “${renamed}” deleted.`)).toBeVisible();
  await expect(savedViewEntry(page, renamed)).toHaveCount(0);
  await openList(page, "All documents");
  await expect(documentRow(page, seed.water.title)).toBeVisible();
});
