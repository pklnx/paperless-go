import type { Page } from "@playwright/test";
import { work } from "./demo";
import { expect, test } from "./fixtures";
import { makePDF } from "./pdf";
import { confirmDialog, detail, documentLink, documentList, documentRow, field, listEntry, navigation, notice, search, trashBanner, undoButton, uploadFiles, uploadRow } from "./ui";

/** Finds the document by its word and moves it to the trash from its row. */
async function deleteFromList(page: Page, title: string, word: string): Promise<void> {
  await search(page, word);
  await documentRow(page, title).getByRole("button", { name: `Move ${title} to the trash`, exact: true }).click();
  await expect(notice(page, `“${title}” moved to the trash.`)).toBeVisible();
  await expect(documentRow(page, title)).toHaveCount(0);
}

/** Opens the trash from the navigation. */
async function openTrash(page: Page): Promise<void> {
  await listEntry(navigation(page), "Trash").click();
  await expect(page).toHaveURL(/[?&]trash=1/);
  await expect(documentList(page, "Trash")).toBeVisible();
}

test("a deleted document comes back with Undo; in the trash it is read-only until it is restored", async ({ page }) => {
  const { title, word } = work.trashUndo;
  await page.goto("/");
  await deleteFromList(page, title, word);

  // Undo puts it back.
  await undoButton(page).click();
  await expect(notice(page, `“${title}” restored.`)).toBeVisible();
  await expect(documentRow(page, title)).toBeVisible();

  // Deleted again, it is in the trash view, with the date it is deleted.
  await documentRow(page, title).getByRole("button", { name: `Move ${title} to the trash`, exact: true }).click();
  await expect(documentRow(page, title)).toHaveCount(0);
  await openTrash(page);
  await expect(documentRow(page, title)).toContainText("Deleted on");

  // Its detail shows the trash banner, and the form cannot be edited.
  await documentLink(page, title).click();
  await expect(detail(page, title)).toBeVisible();
  const banner = trashBanner(page, title);
  await expect(banner).toContainText("In the trash since");
  await expect(field(page, "Title")).toBeDisabled();
  await expect(page.getByRole("button", { name: "Save", exact: true })).toHaveCount(0);

  // Restore from the banner: editable again, and gone from the trash.
  await banner.getByRole("button", { name: "Restore", exact: true }).click();
  await expect(banner).toHaveCount(0);
  await expect(field(page, "Title")).toBeEnabled();
  await page.getByRole("button", { name: "Close document" }).click();
  await expect(documentRow(page, title)).toHaveCount(0);
  await listEntry(navigation(page), "All documents").click();
  await search(page, word);
  await expect(documentRow(page, title)).toBeVisible();
});

test("Delete permanently asks first and removes the document for good", async ({ page }) => {
  const { title, word } = work.trashPurge;
  await page.goto("/");
  await deleteFromList(page, title, word);
  await openTrash(page);
  await documentLink(page, title).click();
  await expect(detail(page, title)).toBeVisible();
  const id = new URL(page.url()).searchParams.get("doc") ?? "";
  expect(id).not.toBe("");

  const banner = trashBanner(page, title);
  await confirmDialog(page, () => banner.getByRole("button", { name: "Delete permanently", exact: true }).click(), "Delete 1 document permanently");
  await expect(notice(page, `“${title}” deleted permanently.`)).toBeVisible();
  await expect(detail(page, title)).toHaveCount(0);

  // The server removes the rest (its files) after it is woken, in the
  // background: poll until the document is gone, never one look.
  await expect
    .poll(async () => (await page.request.get(`/api/documents/${id}`, { headers: { Accept: "application/json" } })).status(), { timeout: 20_000 })
    .toBe(404);
  await expect(async () => {
    await page.reload();
    // The trash keeps the search for the word: nothing is found any more.
    await expect(page.getByText("Nothing found", { exact: true })).toBeVisible();
    await expect(documentRow(page, title)).toHaveCount(0);
  }).toPass({ timeout: 20_000 });
});

test("a trashed file uploaded again is found in the trash and restored", async ({ page }) => {
  const { title, word, lines } = work.trashAgain;
  // The file of the seed document: the demo and this writer make the same bytes.
  const file = { name: `${word}.pdf`, mimeType: "application/pdf", buffer: makePDF([[...lines, `Reference word: ${word}`]]) };
  await page.goto("/");
  await deleteFromList(page, title, word);

  await uploadFiles(page, [file]);
  const row = uploadRow(page, file.name);
  await expect(row.getByRole("status")).toHaveText(`already there, in the trash → ${title}`);
  await row.getByRole("button", { name: /^Restore .* from the trash$/ }).click();
  await expect(row.getByRole("status")).toHaveText("restored from the trash");

  // A typed search is not loaded again after a restore: load the list afresh.
  await page.reload();
  await expect(documentRow(page, title)).toBeVisible();
});
