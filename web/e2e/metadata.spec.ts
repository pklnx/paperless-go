import type { Page } from "@playwright/test";
import { unique, work } from "./demo";
import { expect, test } from "./fixtures";
import { closeDocument, createChoice, detail, detailForm, documentLink, field, listEntry, metadataRow, metadataTab, navigation, pageEntry, saveDocument, search, sheet, subTagsToggle } from "./ui";

const metadataPage = "Tags, correspondents & types";

/** Opens the sample document, which these tests give new tags, correspondents and types. */
async function openSample(page: Page): Promise<void> {
  await page.goto("/");
  await search(page, work.metadata.word);
  await documentLink(page, work.metadata.title).click();
  await expect(detail(page, work.metadata.title)).toBeVisible();
}

/** Back to the page of tags, correspondents and types from the open document. */
async function openMetadataPage(page: Page): Promise<void> {
  await closeDocument(page);
  await pageEntry(page, metadataPage).click();
  await expect(page).toHaveURL(/[?&]view=metadata/);
}

test("a tag is created with a document, put under another tag, renamed and deleted", async ({ page }) => {
  const tag = unique("Tag");
  const renamed = unique("Tag renamed");
  await openSample(page);
  await createChoice(page, "Tags", tag);
  await saveDocument(page);
  await openMetadataPage(page);

  const row = metadataRow(page, "Tags", tag);
  await expect(row).toContainText("1 document");
  // Under Home: the sidebar then shows Home with sub-tags, the new one among them.
  await row.getByRole("button", { name: `Edit ${tag}`, exact: true }).click();
  const edit = sheet(page, `Edit ${tag}`);
  await edit.getByLabel("Name", { exact: true }).fill(renamed);
  await edit.getByLabel(/^Parent tag/).selectOption({ label: "Home" });
  await edit.getByRole("button", { name: "Save", exact: true }).click();
  await expect(edit).toBeHidden();
  await expect(metadataRow(page, "Tags", tag)).toHaveCount(0);
  await expect(metadataRow(page, "Tags", renamed)).toContainText("1 document");
  // The tag tree is in the documents view's column (#150, part 2): Home has sub-tags now, the new one among them.
  await listEntry(navigation(page), "All documents").click();
  const nav = navigation(page);
  await expect(subTagsToggle(nav, "Home")).toHaveAttribute("aria-expanded", "true");
  await expect(listEntry(nav, renamed)).toBeVisible();
  await pageEntry(page, metadataPage).click();
  await expect(page).toHaveURL(/[?&]view=metadata/);

  // Delete says how many documents lose the tag; the document stays.
  await metadataRow(page, "Tags", renamed).getByRole("button", { name: `Delete ${renamed}`, exact: true }).click();
  const remove = sheet(page, `Delete “${renamed}”?`);
  await expect(remove).toContainText("The tag is removed from 1 document. The document stays.");
  await remove.getByRole("button", { name: "Delete", exact: true }).click();
  await expect(remove).toBeHidden();
  await expect(metadataRow(page, "Tags", renamed)).toHaveCount(0);

  await listEntry(navigation(page), "All documents").click();
  await expect(subTagsToggle(nav, "Home")).toHaveCount(0);
  await search(page, work.metadata.word);
  await documentLink(page, work.metadata.title).click();
  await expect(detail(page, work.metadata.title)).toBeVisible();
  // The form is loaded (its title field holds the document's title) before
  // the removed tag is expected to be missing from it.
  await expect(field(page, "Title")).toHaveValue(work.metadata.title);
  await expect(detailForm(page).getByText(renamed)).toHaveCount(0);
});

test("a correspondent and a document type are created with a document; one is renamed, both are deleted", async ({ page }) => {
  const correspondent = unique("Correspondent");
  const renamed = unique("Correspondent renamed");
  const type = unique("Type");
  await openSample(page);
  await createChoice(page, "Correspondent", correspondent);
  await createChoice(page, "Document type", type);
  await saveDocument(page);
  await openMetadataPage(page);

  await metadataTab(page, "Correspondents").click();
  await expect(metadataRow(page, "Correspondents", correspondent)).toContainText("1 document");
  await metadataRow(page, "Correspondents", correspondent).getByRole("button", { name: `Edit ${correspondent}`, exact: true }).click();
  const edit = sheet(page, `Edit ${correspondent}`);
  await edit.getByLabel("Name", { exact: true }).fill(renamed);
  await edit.getByRole("button", { name: "Save", exact: true }).click();
  await expect(edit).toBeHidden();
  await expect(metadataRow(page, "Correspondents", correspondent)).toHaveCount(0);
  await expect(metadataRow(page, "Correspondents", renamed)).toContainText("1 document");

  await metadataRow(page, "Correspondents", renamed).getByRole("button", { name: `Delete ${renamed}`, exact: true }).click();
  const removeCorrespondent = sheet(page, `Delete “${renamed}”?`);
  await expect(removeCorrespondent).toContainText("1 document loses this correspondent. The document stays.");
  await removeCorrespondent.getByRole("button", { name: "Delete", exact: true }).click();
  await expect(metadataRow(page, "Correspondents", renamed)).toHaveCount(0);

  await metadataTab(page, "Document types").click();
  await expect(metadataRow(page, "Document types", type)).toContainText("1 document");
  await metadataRow(page, "Document types", type).getByRole("button", { name: `Delete ${type}`, exact: true }).click();
  const removeType = sheet(page, `Delete “${type}”?`);
  await expect(removeType).toContainText("1 document loses this type. The document stays.");
  await removeType.getByRole("button", { name: "Delete", exact: true }).click();
  await expect(metadataRow(page, "Document types", type)).toHaveCount(0);
});
