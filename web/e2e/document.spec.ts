import type { Page } from "@playwright/test";
import { origin, seed, unique } from "./demo";
import { expect, test } from "./fixtures";
import { chosenNames, closeDocument, detail, detailForm, discardChanges, discardQuestion, documentLink, field, formError, keepEditing, logOut, notice, openSavedViewMenu, pdfPage, pick, pickTag, rowTags, saveDocument, search, sheet } from "./ui";

/** The open document's ID, from the URL. */
const openID = (page: Page) => new URL(page.url()).searchParams.get("doc") ?? "";

test("a document opens from the list and its PDF renders in the viewer", async ({ page }) => {
  await page.goto("/");
  await documentLink(page, seed.lease.title).click();
  await expect(page).toHaveURL(/[?&]doc=[0-9a-f-]{36}/);
  await expect(detail(page, seed.lease.title)).toBeVisible();
  // pdf.js's text layer holds the page's text: its worker, the standard
  // fonts and the cmaps loaded from the server itself (the guards abort
  // any other host and fail on a CSP violation).
  await expect(pdfPage(page, 1)).toContainText(seed.lease.word);
  await expect(page.getByText("Page 1 / 2")).toBeVisible();

  await closeDocument(page);
  await expect(page).not.toHaveURL(/[?&]doc=/);
  await expect(detail(page, seed.lease.title)).toHaveCount(0);
});

test("edit every field, save, and the values are kept after a reload", async ({ page }) => {
  const title = unique("Dentist reminder");
  const correspondent = unique("Dental practice");
  const url = "https://example.com/dentist";
  await page.goto("/");
  await documentLink(page, seed.dentist.title).click();
  await expect(detail(page, seed.dentist.title)).toBeVisible();

  await field(page, "Title").fill(title);
  await field(page, "Document date").fill("2026-06-30");
  // A correspondent that does not exist yet is created from the picker.
  const box = detailForm(page).getByRole("combobox", { name: "Correspondent", exact: true });
  await box.fill(correspondent);
  await page.getByRole("option", { name: `Create “${correspondent}”` }).click();
  await expect(box).toHaveValue(correspondent);
  await pick(page, "Document type", "Contract");
  await pickTag(page, "Home");
  await expect(chosenNames(page)).toHaveText(["Home"]);
  await field(page, "Notes").fill(`Bring the **insurance card**. [Practice website](${url})`);
  await expect(page.getByText("Unsaved changes")).toBeVisible();
  await detailForm(page).getByRole("button", { name: "Save", exact: true }).click();
  await expect(detailForm(page).getByRole("status")).toHaveText("Saved");

  await page.reload();
  await expect(detail(page, title)).toBeVisible();
  await expect(field(page, "Title")).toHaveValue(title);
  await expect(field(page, "Document date")).toHaveValue("2026-06-30");
  await expect(field(page, "Correspondent")).toHaveValue(correspondent);
  await expect(field(page, "Document type")).toHaveValue("Contract");
  await expect(chosenNames(page)).toHaveText(["Home"]);
  // The notes show rendered: the link is a link.
  await expect(detailForm(page).getByRole("link", { name: "Practice website" })).toHaveAttribute("href", url);
  await expect(detailForm(page).getByText("insurance card")).toBeVisible();
});

test("next and previous move through the list; unsaved changes are asked about", async ({ page }) => {
  await page.goto("/");
  await documentLink(page, seed.warranty.title).click();
  await expect(detail(page, seed.warranty.title)).toBeVisible();
  const first = openID(page);

  await page.getByRole("button", { name: "Next document" }).click();
  await expect(page).not.toHaveURL(new RegExp(`doc=${first}`));
  await expect(detail(page, seed.warranty.title)).toHaveCount(0);
  await page.getByRole("button", { name: "Previous document" }).click();
  await expect(detail(page, seed.warranty.title)).toBeVisible();
  expect(openID(page)).toBe(first);

  // An unsaved edit: leaving asks first (a Carbon modal). "Keep editing"
  // keeps the edit and the URL, and so does Escape; the dialog is gone
  // then and stays gone (the detail's own Escape does not close the
  // document behind it). "Discard changes" goes on and drops the edit.
  const edited = unique("Not saved");
  const next = page.getByRole("button", { name: "Next document" });
  await field(page, "Title").fill(edited);
  await next.click();
  await expect(discardQuestion(page)).toBeVisible();
  await expect(discardQuestion(page)).toContainText("This document has unsaved changes. Leave it and discard them?");
  await keepEditing(page);
  await expect(discardQuestion(page)).toHaveCount(0);
  await expect(next).toBeFocused();
  expect(openID(page)).toBe(first);
  await expect(field(page, "Title")).toHaveValue(edited);

  await next.click();
  await expect(discardQuestion(page)).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(discardQuestion(page)).toHaveCount(0);
  await expect(next).toBeFocused();
  expect(openID(page)).toBe(first);
  await expect(field(page, "Title")).toHaveValue(edited);
  // The detail is named by its edited title meanwhile.
  await expect(detail(page, edited)).toBeVisible();
  expect(await discardQuestion(page).count()).toBe(0);

  await next.click();
  await discardChanges(page);
  await expect(page).not.toHaveURL(new RegExp(`doc=${first}`));
  await expect(discardQuestion(page)).toHaveCount(0);
  await page.getByRole("button", { name: "Previous document" }).click();
  await expect(field(page, "Title")).toHaveValue(seed.warranty.title);

  await closeDocument(page);
  await expect(page).not.toHaveURL(/[?&]doc=/);
  await expect(documentLink(page, seed.warranty.title)).toBeVisible();
});

test("Back with unsaved changes asks; Keep editing keeps the URL, Discard changes shows the previous entry without a duplicate", async ({ page }) => {
  await page.goto("/");
  await documentLink(page, seed.warranty.title).click();
  await expect(detail(page, seed.warranty.title)).toBeVisible();
  const first = openID(page);
  // The history: the list, the first document, the second (Next).
  await page.getByRole("button", { name: "Next document" }).click();
  await expect(page).not.toHaveURL(new RegExp(`doc=${first}`));
  const second = openID(page);
  await expect(field(page, "Title")).not.toHaveValue(seed.warranty.title);
  await field(page, "Title").fill(unique("Not saved"));
  // The focus is nowhere in particular (a tap on a blank part): the question has no opener.
  await field(page, "Title").blur();

  // Back: the second document's URL stays while it asks, and after "Keep editing".
  await page.goBack();
  await expect(discardQuestion(page)).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`doc=${second}`));
  await keepEditing(page);
  await expect(discardQuestion(page)).toHaveCount(0);
  await expect(page).toHaveURL(new RegExp(`doc=${second}`));
  // The question had no opener: the focus is on the detail's heading.
  await expect(page.getByTestId("document-detail").getByRole("heading", { level: 2 })).toBeFocused();

  // Back again, "Discard changes": the first document, with its own title.
  await page.goBack();
  await discardChanges(page);
  await expect(page).toHaveURL(new RegExp(`doc=${first}`));
  await expect(field(page, "Title")).toHaveValue(seed.warranty.title);
  await expect(discardQuestion(page)).toHaveCount(0);
  // No duplicate entry: one more Back leaves the detail for the list.
  await page.goBack();
  await expect(page).not.toHaveURL(/[?&]doc=/);
  await expect(detail(page, seed.warranty.title)).toHaveCount(0);
  await expect(documentLink(page, seed.warranty.title)).toBeVisible();
});

test("the question over another open modal (a view's Rename): the keyboard stays in the question, Escape answers it, the focus returns below", async ({ page }) => {
  // A saved view, so that its Rename modal can be open next to the document (the column sits beside the detail from lg).
  const name = unique("Lease view");
  await page.goto("/");
  await search(page, seed.lease.word);
  await page.getByRole("button", { name: "Save view…", exact: true }).click();
  await sheet(page, "Save this view").getByLabel("Name", { exact: true }).fill(name);
  await sheet(page, "Save this view").getByRole("button", { name: "Save", exact: true }).click();
  await expect(notice(page, `View “${name}” saved.`)).toBeVisible();
  await documentLink(page, seed.lease.title).click();
  await expect(detail(page, seed.lease.title)).toBeVisible();
  await field(page, "Title").fill("Not saved");

  // Rename open with the focus in its field, then Back: the question opens over it.
  await (await openSavedViewMenu(page, name)).getByRole("menuitem", { name: "Rename", exact: true }).click();
  const rename = sheet(page, "Rename");
  const nameField = rename.getByLabel("Name", { exact: true });
  await expect(nameField).toBeFocused();
  await page.goBack();
  const question = discardQuestion(page);
  const keep = question.getByRole("button", { name: "Keep editing", exact: true });
  await expect(keep).toBeFocused();
  // Still there after the frames in which the lower modal's own focus wrap would have moved it.
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
  await expect(keep).toBeFocused();
  // Tab and Shift+Tab stay inside the question.
  for (const key of ["Tab", "Tab", "Tab", "Tab", "Shift+Tab", "Shift+Tab", "Shift+Tab", "Shift+Tab"]) {
    await page.keyboard.press(key);
    await expect(question.locator(":focus")).toHaveCount(1);
  }
  // The first Escape answers the question (Keep editing); the focus returns into Rename, to its field.
  await page.keyboard.press("Escape");
  await expect(question).toHaveCount(0);
  await expect(field(page, "Title")).toHaveValue("Not saved");
  await expect(rename).toBeVisible();
  await expect(nameField).toBeFocused();
  // The next Escape closes Rename, with the focus back on its opener.
  await page.keyboard.press("Escape");
  await expect(rename).toHaveCount(0);
  await expect(page.getByRole("button", { name: `Actions for ${name}`, exact: true })).toBeFocused();

  // Leave nothing behind: the edit dropped, the view deleted.
  await closeDocument(page);
  await discardChanges(page);
  await expect(page).not.toHaveURL(/[?&]doc=/);
  await (await openSavedViewMenu(page, name)).getByRole("menuitem", { name: "Delete", exact: true }).click();
  await sheet(page, `Delete “${name}”?`).getByRole("button", { name: "Delete", exact: true }).click();
  await expect(notice(page, `View “${name}” deleted.`)).toBeVisible();
});

test("logging out with unsaved changes asks; Keep editing keeps the session and the edit", async ({ page }) => {
  await page.goto("/");
  await documentLink(page, seed.internet.title).click();
  await expect(detail(page, seed.internet.title)).toBeVisible();
  const edited = unique("Not saved");
  await field(page, "Title").fill(edited);
  await logOut(page);
  await expect(discardQuestion(page)).toBeVisible();
  await keepEditing(page);
  await expect(discardQuestion(page)).toHaveCount(0);
  await expect(page.getByRole("banner").getByRole("button", { name: "Log out", exact: true })).toBeVisible();
  await expect(field(page, "Title")).toHaveValue(edited);
  await expect(page).toHaveURL(/[?&]doc=/);
  expect(await page.getByLabel("User name", { exact: true }).count()).toBe(0);
  // Leave nothing unsaved behind: the edit is dropped.
  await closeDocument(page);
  await discardChanges(page);
  await expect(page).not.toHaveURL(/[?&]doc=/);
});

test("a change made elsewhere meanwhile: the save is refused and the user's edits are kept", async ({ page, guards }) => {
  await page.goto("/");
  await documentLink(page, seed.bank.title).click();
  await expect(detail(page, seed.bank.title)).toBeVisible();
  const id = openID(page);
  // The next save answers 409 document_changed; the browser logs it.
  guards.allowHTTPError(`/api/documents/${id}`, 409);

  const title = unique("Bank letter");
  await field(page, "Title").fill(title);
  // Another tab changes the notes. From inside the page, so the browser
  // sends its Origin header (the server refuses writes without it).
  const status = await page.evaluate(async (path) => {
    const res = await fetch(path, {
      method: "PATCH",
      headers: { "Content-Type": "application/json", Accept: "application/json" },
      body: JSON.stringify({ notes: "Changed in another tab." }),
    });
    return res.status;
  }, `/api/documents/${id}`);
  expect(status).toBe(200);

  await detailForm(page).getByRole("button", { name: "Save", exact: true }).click();
  await expect(formError(page)).toHaveText("This document was changed elsewhere meanwhile. Your changes are kept on top of the new state; check them and save again.");
  await expect(field(page, "Title")).toHaveValue(title);
  await detailForm(page).getByRole("button", { name: "Save", exact: true }).click();
  await expect(detailForm(page).getByRole("status")).toHaveText("Saved");

  await page.reload();
  await expect(field(page, "Title")).toHaveValue(title);
  await expect(detailForm(page).getByText("Changed in another tab.")).toBeVisible();
});

test.describe("the tag picker on the seeded lease", () => {
  // The lease is seed data other specs count on: whatever the test did
  // (also on a failure or a timeout), the Bills tag comes off again through
  // the API, with the document's modifiedAt as the precondition.
  test.afterEach(async ({ request }) => {
    const headers = { Origin: origin };
    const tags = ((await (await request.get("/api/tags")).json()) as { tags: { id: string; name: string }[] }).tags;
    const bills = tags.find((x) => x.name === "Bills");
    const found = ((await (await request.get(`/api/documents?q=${seed.lease.word}`)).json()) as { documents: { id: string; title: string }[] }).documents;
    const lease = found.find((d) => d.title === seed.lease.title);
    if (!bills || !lease) return;
    const doc = (await (await request.get(`/api/documents/${lease.id}`)).json()) as { tagIds: string[]; modifiedAt: string };
    if (!doc.tagIds.includes(bills.id)) return;
    const res = await request.patch(`/api/documents/${lease.id}`, { headers, data: { tagIds: doc.tagIds.filter((id) => id !== bills.id), expectedModifiedAt: doc.modifiedAt } });
    expect(res.ok()).toBe(true);
  });

  test("a nested tag is picked by typing its own name, and the row shows it", async ({ page }) => {
    // Bills is a sub-tag of Finance: the picker lists it as "Finance / Bills" and finds it by "Bills".
    const { title } = seed.lease;
    await page.goto("/");
    await documentLink(page, title).click();
    await expect(detail(page, title)).toBeVisible();
    await expect(chosenNames(page)).toHaveText(["Home"]);
    await pickTag(page, "Bills", "Finance / Bills");
    // The chips in the order chosen.
    await expect(chosenNames(page)).toHaveText(["Home", "Bills"]);
    // Escape on the closed tag field is the document's Escape: it asks about the unsaved tag and keeps the chips.
    await detailForm(page).getByRole("combobox", { name: "Tags", exact: true }).press("Escape");
    await expect(discardQuestion(page)).toBeVisible();
    await keepEditing(page);
    await expect(chosenNames(page)).toHaveText(["Home", "Bills"]);
    await saveDocument(page);
    await closeDocument(page);
    // The row shows both; the server lists a document's tags by their id, so their order is not the chips'.
    const tags = rowTags(page, title).getByRole("listitem");
    await expect(tags).toHaveCount(2);
    expect((await tags.allTextContents()).sort()).toEqual(["Bills", "Home"]);
  });
});
