import { bulkNames, work } from "./demo";
import { expect, test } from "./fixtures";
import { bulkBar, documentList, documentRow, documentRows, listEntry, navigation, rowTags, search, selectAllLoaded, selectBox, sheet } from "./ui";

test("two checked documents get a tag and a correspondent through the bulk bar", async ({ page }) => {
  await page.goto("/");
  await search(page, work.pair.word);
  await expect(documentRows(documentList(page, "Search results"))).toHaveCount(work.pair.titles.length);
  for (const title of work.pair.titles) await selectBox(page, title).check();
  const bar = bulkBar(page);
  await expect(bar.getByText("2 selected", { exact: true })).toBeVisible();

  // A tag: its sheet lists every tag twice, to add and to remove.
  await bar.getByRole("button", { name: "Tags…" }).click();
  const tags = sheet(page, "Tags of the selected documents");
  await tags.getByRole("group", { name: "Add", exact: true }).getByRole("checkbox", { name: bulkNames.tag, exact: true }).check();
  await tags.getByRole("button", { name: "Apply", exact: true }).click();
  await expect(tags).toBeHidden();
  for (const title of work.pair.titles) await expect(rowTags(page, title)).toContainText(bulkNames.tag);

  // A correspondent.
  await bar.getByRole("button", { name: "Correspondent…" }).click();
  const correspondent = sheet(page, "Correspondent of the selected documents");
  await correspondent.getByRole("radio", { name: bulkNames.correspondent, exact: true }).check();
  await correspondent.getByRole("button", { name: "Apply", exact: true }).click();
  await expect(correspondent).toBeHidden();
  for (const title of work.pair.titles) await expect(documentRow(page, title)).toContainText(bulkNames.correspondent);
  await expect(bar.getByRole("status")).toHaveText("2 documents changed.");

  // The server has it, not only the page.
  await page.reload();
  for (const title of work.pair.titles) {
    await expect(rowTags(page, title)).toContainText(bulkNames.tag);
    await expect(documentRow(page, title)).toContainText(bulkNames.correspondent);
  }
});

test("every match of a search is counted, confirmed and then changed", async ({ page }) => {
  await page.goto("/");
  await search(page, work.batch.word);
  // The list loads one page; the other matches are not loaded yet.
  await expect(documentRows(documentList(page, "Search results"))).toHaveCount(50);
  await expect(page.getByRole("button", { name: "Load more", exact: true })).toBeVisible();

  await selectAllLoaded(page).check();
  const bar = bulkBar(page);
  await expect(bar.getByText("50 selected", { exact: true })).toBeVisible();
  // "All matches" counts the search (a dry run) and selects exactly that many.
  await page.getByRole("button", { name: "Select all matches of this search", exact: true }).click();
  await expect(page.getByText(`All ${work.batch.count} matches of this search are selected.`, { exact: true })).toBeVisible();
  await expect(bar.getByText(`${work.batch.count} selected`, { exact: true })).toBeVisible();

  await bar.getByRole("button", { name: "Type…" }).click();
  const type = sheet(page, "Type of the selected documents");
  await type.getByRole("radio", { name: bulkNames.type, exact: true }).check();
  await type.getByRole("button", { name: "Apply", exact: true }).click();

  // Nothing is sent before the count is shown and confirmed.
  const confirm = sheet(page, "Change all matches?");
  await expect(confirm).toContainText(`This changes ${work.batch.count} documents: every match of this search, also those not loaded yet.`);
  await confirm.getByRole("button", { name: `Change ${work.batch.count} documents`, exact: true }).click();
  await expect(bar.getByRole("status")).toHaveText(`${work.batch.count} documents changed.`);

  // All of them have the type, also those that were never loaded: the
  // sidebar counts them.
  await expect(listEntry(navigation(page), bulkNames.type)).toHaveAccessibleName(`${bulkNames.type} ${work.batch.count}`);
});
