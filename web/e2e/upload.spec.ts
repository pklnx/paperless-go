import { uniqueWord } from "./demo";
import { expect, test } from "./fixtures";
import { makePDF } from "./pdf";
import { documentRow, noThumbnail, processingStatus, uploadFiles, uploadRow } from "./ui";

// Processing runs in the demo's worker with stand-in tools. The list loads
// a pending document again every 5 s, so the end state shows within this
// bound; it is an assertion on what the page shows, not a sleep.
const processed = { timeout: 20_000 };

test("an uploaded PDF is imported, processed and shows a thumbnail; the same file again is a duplicate", async ({ page }) => {
  const word = uniqueWord("upload");
  const file = { name: `${word}.pdf`, mimeType: "application/pdf", buffer: makePDF([["An uploaded test document", `Reference word: ${word}`]]) };
  await page.goto("/");
  await uploadFiles(page, [file]);
  await expect(uploadRow(page, file.name).getByRole("status")).toHaveText("imported");

  // The title comes from the file name. Only the end state is asserted:
  // the job may finish before the list first shows the document, so a
  // "Queued" or "Processing" badge may never be seen.
  const row = documentRow(page, word);
  await expect(row).toBeVisible();
  await expect(noThumbnail(row)).toHaveCount(0, processed);
  await expect(processingStatus(row)).toHaveCount(0);
  await expect(row.getByText("Failed", { exact: true })).toHaveCount(0);

  await uploadFiles(page, [file]);
  await expect(page.getByRole("list", { name: "Uploads" }).getByRole("status").filter({ hasText: `already there → ${word}` })).toBeVisible();
  await expect(documentRow(page, word)).toHaveCount(1);
});

test("a damaged PDF is imported and its processing fails with a Retry", async ({ page }) => {
  const name = `${uniqueWord("broken")}.pdf`;
  const title = name.replace(/\.pdf$/, "");
  await page.goto("/");
  // It starts like a PDF, so it is accepted; the tools cannot read it.
  await uploadFiles(page, [{ name, mimeType: "application/pdf", buffer: Buffer.from("%PDF-1.4\nthis is not really a PDF\n") }]);
  await expect(uploadRow(page, name).getByRole("status")).toHaveText("imported");
  const row = documentRow(page, title);
  await expect(row.getByText("Failed", { exact: true })).toBeVisible(processed);
  await expect(row.getByText("The PDF cannot be read: it is damaged, or it needs a password to open.")).toBeVisible();
  await expect(row.getByRole("button", { name: `Retry processing ${title}` })).toBeVisible();
});

test("a text file named .pdf is refused", async ({ page, guards }) => {
  // The server answers 415 Unsupported Media Type.
  guards.allowHTTPError("/api/documents", 415);
  const name = `${uniqueWord("text")}.pdf`;
  await page.goto("/");
  await uploadFiles(page, [{ name, mimeType: "application/pdf", buffer: Buffer.from("just some text, not a PDF\n") }]);
  await expect(uploadRow(page, name).getByRole("status")).toHaveText("not a PDF, JPEG, PNG or HEIC file");
  await expect(documentRow(page, name.replace(/\.pdf$/, ""))).toHaveCount(0);
});
