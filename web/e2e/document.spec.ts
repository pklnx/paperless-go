import type { Page } from "@playwright/test";
import { seed, unique } from "./demo";
import { expect, test } from "./fixtures";
import { chosen, confirmDialog, detail, detailForm, documentLink, field, pdfPage, pick, pickTag } from "./ui";

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

  await page.getByRole("button", { name: "Close document" }).click();
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
  await pick(page, "Document type", "Contract");
  await pickTag(page, "Home");
  await field(page, "Notes").fill(`Bring the **insurance card**. [Practice website](${url})`);
  await expect(page.getByText("Unsaved changes")).toBeVisible();
  await detailForm(page).getByRole("button", { name: "Save", exact: true }).click();
  await expect(detailForm(page).getByRole("status")).toHaveText("Saved");

  await page.reload();
  await expect(detail(page, title)).toBeVisible();
  await expect(field(page, "Title")).toHaveValue(title);
  await expect(field(page, "Document date")).toHaveValue("2026-06-30");
  await expect(chosen(page, "Correspondent")).toHaveText(correspondent);
  await expect(chosen(page, "Document type")).toHaveText("Contract");
  await expect(chosen(page, "Tags")).toHaveText("Home");
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

  // An unsaved edit: leaving asks first (a native confirm), and the
  // answer "OK" goes on and drops the edit.
  await field(page, "Title").fill(unique("Not saved"));
  await confirmDialog(page, () => page.getByRole("button", { name: "Next document" }).click());
  await expect(page).not.toHaveURL(new RegExp(`doc=${first}`));
  await page.getByRole("button", { name: "Previous document" }).click();
  await expect(field(page, "Title")).toHaveValue(seed.warranty.title);

  await page.getByRole("button", { name: "Close document" }).click();
  await expect(page).not.toHaveURL(/[?&]doc=/);
  await expect(documentLink(page, seed.warranty.title)).toBeVisible();
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
  await expect(detailForm(page).getByRole("alert")).toHaveText(
    "This document was changed elsewhere meanwhile. Your changes are kept on top of the new state; check them and save again.",
  );
  await expect(field(page, "Title")).toHaveValue(title);
  await detailForm(page).getByRole("button", { name: "Save", exact: true }).click();
  await expect(detailForm(page).getByRole("status")).toHaveText("Saved");

  await page.reload();
  await expect(field(page, "Title")).toHaveValue(title);
  await expect(detailForm(page).getByText("Changed in another tab.")).toBeVisible();
});
