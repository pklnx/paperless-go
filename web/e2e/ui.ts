// The one place that knows how the UI is built. Specs find things through
// these helpers, by role, label and text only (no CSS classes, element
// types or structure), so a rewrite of the UI (#150, Carbon) changes these
// helpers and the accessible names it renames, not the specs.
import type { Dialog, Locator, Page } from "@playwright/test";

/** The documents of the list, a list named by the list's heading ("All documents", "Search results", a tag's name…). */
export function documentList(page: Page, name: string | RegExp = "All documents"): Locator {
  return page.getByRole("main").getByRole("list", { name, exact: true });
}

/** A document's row in the list: the list item with the document's title as its link. */
export function documentRow(page: Page, title: string): Locator {
  return page
    .getByRole("main")
    .getByRole("listitem")
    .filter({ has: page.getByRole("link", { name: title, exact: true }) });
}

/** Every document row of a list (the rows with a download link, not their tag chips). */
export function documentRows(list: Locator): Locator {
  return list.getByRole("listitem").filter({ has: list.page().getByRole("link", { name: "Download", exact: true }) });
}

/** A document's title link in its row: opens the detail. */
export function documentLink(page: Page, title: string): Locator {
  return documentRow(page, title).getByRole("link", { name: title, exact: true });
}

/**
 * The placeholder a row shows until processing made a thumbnail. The
 * thumbnail itself is decoration (alt="") and has no role, so the
 * placeholder's test id is the one way to tell them apart: #150 must keep
 * data-testid="no-thumbnail" on it.
 */
export function noThumbnail(row: Locator): Locator {
  return row.getByTestId("no-thumbnail");
}

/** The processing badge of a row ("Queued", "Processing"). */
export function processingStatus(row: Locator): Locator {
  return row.getByRole("status").filter({ hasText: /^(Queued|Queued again|Processing)$/ });
}

/** The search field. */
export function searchBox(page: Page): Locator {
  return page.getByRole("searchbox", { name: "Search documents" });
}

/** Searches for q (a search runs on Enter). */
export async function search(page: Page, q: string): Promise<void> {
  await searchBox(page).fill(q);
  await searchBox(page).press("Enter");
}

/** The sidebar, or the navigation sheet on a phone. */
export function navigation(page: Page): Locator {
  return page.getByRole("navigation", { name: "Navigation" });
}

/** A tag's entry in the navigation: its name, then its count. */
export function tagEntry(nav: Locator, name: string): Locator {
  const escaped = name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return nav.getByRole("button", { name: new RegExp(`^${escaped}(?: \\d+)?$`) });
}

/** The phone's menu button. */
export function menuButton(page: Page): Locator {
  return page.getByRole("button", { name: "Open menu" });
}

/** Opens the navigation on a phone and returns its dialog. */
export async function openNavigation(page: Page): Promise<Locator> {
  await menuButton(page).click();
  const dialog = page.getByRole("dialog", { name: "Navigation" });
  await dialog.waitFor();
  return dialog;
}

/** A file for uploadFiles. */
export interface UploadFile {
  name: string;
  mimeType: string;
  buffer: Buffer;
}

/** Uploads files through "Choose files" and its file chooser. */
export async function uploadFiles(page: Page, files: UploadFile[]): Promise<void> {
  const chooser = page.waitForEvent("filechooser");
  await page.getByRole("button", { name: "Choose files" }).click();
  await (await chooser).setFiles(files);
}

/** The row of an upload in the upload panel, by its file name. */
export function uploadRow(page: Page, name: string): Locator {
  return page.getByRole("list", { name: "Uploads" }).getByRole("listitem").filter({ hasText: name });
}

/** The open document's detail: a region named by its title. */
export function detail(page: Page, title: string | RegExp): Locator {
  return page.getByRole("region", { name: title, exact: true });
}

/** The detail's form. */
export function detailForm(page: Page): Locator {
  return page.getByRole("form", { name: "Edit document" });
}

/** A field of the detail's form by its label. */
export function field(page: Page, label: string): Locator {
  return detailForm(page).getByLabel(label, { exact: true });
}

/** The chosen entries of a picker ("Chosen: Tags"). */
export function chosen(page: Page, label: string): Locator {
  return detailForm(page).getByRole("list", { name: `Chosen: ${label}` });
}

/** Chooses name in the picker labelled label (correspondent, type or tags). */
export async function pick(page: Page, label: string, name: string): Promise<void> {
  const box = detailForm(page).getByRole("combobox", { name: label, exact: true });
  await box.fill(name);
  await page.getByRole("listbox", { name: label }).getByRole("option", { name, exact: true }).click();
}

/** Chooses a tag in the detail's tag picker. */
export function pickTag(page: Page, name: string): Promise<void> {
  return pick(page, "Tags", name);
}

/** The PDF viewer's page n: a group named "Page n" with pdf.js's text layer. */
export function pdfPage(page: Page, n: number): Locator {
  return page.getByRole("group", { name: `Page ${n}`, exact: true });
}

/**
 * Runs action (the click that asks) and accepts the confirmation, whichever
 * kind it is:
 * - a native window.confirm (API key revoke, unsaved changes, lock, reset
 *   2FA and remove user), which Playwright would otherwise dismiss, so the
 *   action would silently not happen;
 * - an in-DOM confirmation, when confirm names its button: a Sheet
 *   ("Delete permanently") or an inline panel (role="group", the admin's
 *   "Reset password"). The button is found by its name only, without
 *   assuming a dialog role.
 * It fails when no confirmation shows. #150's Carbon modals change only
 * this helper.
 */
export async function confirmDialog(page: Page, action: () => Promise<void>, confirm?: string | RegExp): Promise<void> {
  let native = false;
  const accept = (d: Dialog) => {
    native = true;
    void d.accept();
  };
  page.once("dialog", accept);
  try {
    await action();
  } finally {
    page.off("dialog", accept);
  }
  if (native) return;
  if (confirm === undefined) throw new Error("confirmDialog: no confirmation was shown");
  await page.getByRole("button", { name: confirm, exact: true }).click();
}

/** The theme switch: named after the theme it switches to. */
export function themeToggle(page: Page): Locator {
  return page.getByRole("button", { name: /^Switch to (dark|light) mode$/ });
}

/**
 * The form whose submit button is named button. The profile's forms have no
 * accessible name, so the button tells them apart (each has a "Current
 * password"); #150 can name them, and then this becomes
 * getByRole("form", { name }).
 */
export function formWithButton(page: Page, button: string): Locator {
  return page.locator("form").filter({ has: page.getByRole("button", { name: button, exact: true }) });
}

/**
 * The 2FA guard's notice that 2FA is required. The setup page shows the
 * sentence twice (the gate and the setup's own text), so the first is
 * taken.
 */
export function twoFactorRequiredNotice(page: Page): Locator {
  return page.getByText("Two-factor authentication is required before you can use paperless-go.").first();
}

/** Logs in through the login page as a user without 2FA. */
export async function logInThroughForm(page: Page, name: string, password: string): Promise<void> {
  await page.getByLabel("User name").fill(name);
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByRole("button", { name: "Log in", exact: true }).click();
}
