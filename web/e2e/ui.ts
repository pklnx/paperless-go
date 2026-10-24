// The one place that knows how the UI is built. Specs find things through
// these helpers, by role, label and text only (no CSS classes, element
// types or structure), so a rewrite of the UI (#150, Carbon) changes these
// helpers and the accessible names it renames, not the specs.
import { expect, type Dialog, type Locator, type Page } from "@playwright/test";

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

/**
 * The app's navigation, in three places since #150 part 2: the shell's
 * SideNav ("Navigation": the lists with their counts and the pages; a rail
 * from lg, an overlay behind the menu button below), the documents view's
 * column ("Browse": saved views, tags, correspondents and types; a column
 * from lg, an overlay behind the Browse button below) and the header's
 * account controls (the user's name as the Profile button, Log out). The
 * helpers below look in all of them, so a spec needs no knowledge of which
 * holds what.
 */
export function navigation(page: Page): Locator {
  return shellNav(page).or(browseColumn(page)).or(page.getByRole("banner", { name: "paperless-go", exact: true }));
}

/**
 * Opens a list from the SideNav ("All documents", "Inbox", "Trash"). From lg
 * the SideNav is a rail that stays expanded while the pointer rests on it,
 * over the column: the pointer moves on, so the rail is out of the way.
 */
export async function openList(page: Page, name: string): Promise<void> {
  await listEntry(shellNav(page), name).click();
  await page.mouse.move(640, 400);
  await expect(shellNav(page)).not.toHaveClass(/cds--side-nav--expanded/);
}

/** The shell's SideNav. */
export function shellNav(page: Page): Locator {
  return page.getByRole("navigation", { name: "Navigation", exact: true });
}

/** The documents view's column (saved views, tags, correspondents, types), as a column or inside its overlay. */
export function browseColumn(page: Page): Locator {
  return page.getByRole("navigation", { name: "Browse", exact: true });
}

/** A list's entry in the navigation ("All documents", "Trash": a link of the SideNav; a tag, a correspondent or a type: a node of the column's tree): its name, then its count. */
export function listEntry(nav: Locator, name: string): Locator {
  const escaped = name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const named = new RegExp(`^${escaped}(?: \\d+)?$`);
  return nav.getByRole("link", { name: named }).or(nav.getByRole("treeitem", { name: named }));
}

/** A tag's entry in the navigation: its name, then its count. */
export const tagEntry = listEntry;

/** The tree node that holds the sub-tags of the tag named name (none once it has no sub-tags); aria-expanded says whether they show. */
export function subTagsToggle(nav: Locator, name: string): Locator {
  return tagEntry(nav, name).and(nav.page().locator("[aria-expanded]"));
}

/** Collapses the sub-tags of the tag named name (they must be expanded): Left on the node, as Carbon's TreeView takes it. */
export async function collapseTag(nav: Locator, name: string): Promise<void> {
  const toggle = subTagsToggle(nav, name);
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  await toggle.focus();
  await toggle.press("ArrowLeft");
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
}

/** Expands the sub-tags of the tag named name (they must be collapsed): Right on the node. */
export async function expandTag(nav: Locator, name: string): Promise<void> {
  const toggle = subTagsToggle(nav, name);
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await toggle.focus();
  await toggle.press("ArrowRight");
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
}

/** The header's menu button: below lg it opens the SideNav as an overlay; from lg it is hidden. */
export function menuButton(page: Page): Locator {
  return page.getByRole("button", { name: "Open menu", exact: true });
}

/** Opens the SideNav below lg and returns it (expanded, with the focus inside). */
export async function openNavigation(page: Page): Promise<Locator> {
  await menuButton(page).click();
  const nav = shellNav(page);
  await expect(nav).toHaveClass(/cds--side-nav--expanded/);
  return nav;
}

/** The list toolbar's Browse button: below lg it opens the column as an overlay; from lg it is hidden. */
export function browseButton(page: Page): Locator {
  return page.getByRole("button", { name: "Browse", exact: true });
}

/** Opens the column overlay below lg and returns its dialog. */
export async function openBrowse(page: Page): Promise<Locator> {
  await browseButton(page).click();
  const dialog = page.getByRole("dialog", { name: "Browse", exact: true });
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

/** Closes the open document (the detail's close button). */
export async function closeDocument(page: Page): Promise<void> {
  await page.getByRole("button", { name: "Close document", exact: true }).click();
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
 *   assuming a dialog role. When the page has another button of that name
 *   (the one that asked), confirm is the button itself, as a Locator.
 * It fails when no confirmation shows. #150's Carbon modals change only
 * this helper.
 */
export async function confirmDialog(page: Page, action: () => Promise<void>, confirm?: string | RegExp | Locator): Promise<void> {
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
  const button = typeof confirm === "string" || confirm instanceof RegExp ? page.getByRole("button", { name: confirm, exact: true }) : confirm;
  await button.click();
}

/** The theme switch (in the header, or on the login page): named after the theme it switches to. */
export function themeToggle(page: Page): Locator {
  return page.getByRole("button", { name: /^Switch to (dark|light) mode$/ });
}

/** Logs out through the header's button. */
export async function logOut(page: Page): Promise<void> {
  await page.getByRole("banner").getByRole("button", { name: "Log out", exact: true }).click();
}

/** The login page's user name field. */
export function userNameField(page: Page): Locator {
  return page.getByLabel("User name", { exact: true });
}

/** The login page's password field. Exact: Carbon's show-password button and other fields' names contain "password". */
export function passwordField(page: Page): Locator {
  return page.getByLabel("Password", { exact: true });
}

/** The "Log in" button. Exact: "Log in with a passkey" is a button too. */
export function logInButton(page: Page): Locator {
  return page.getByRole("button", { name: "Log in", exact: true });
}

/**
 * The login page's error: the message of its Carbon notification. The
 * notification is the one alert with text (Carbon's fields keep an empty
 * alert region of their own), and its message is the element below the
 * status icon, whose description is not part of the message. The class is
 * Carbon's; this is the one place that knows it.
 */
export function loginError(page: Page): Locator {
  return page
    .getByRole("alert")
    .filter({ hasText: /\S/ })
    .locator(".cds--inline-notification__subtitle");
}

/** The product mark (the logo) for the applied theme: exactly one img, with the theme's class (app-mark--light or app-mark--dark). */
export function productMark(page: Page): Locator {
  return page.locator(".app-mark");
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
  await userNameField(page).fill(name);
  await passwordField(page).fill(password);
  await logInButton(page).click();
}

/**
 * A dialog by its title: a native <dialog> opened with showModal (bulk
 * edit, Save view, the metadata page, the permanent delete) until its part
 * of #150, or a Carbon Modal (the saved-view menu's Rename and Delete since
 * part 2): both have the role and the name.
 */
export function sheet(page: Page, title: string | RegExp): Locator {
  return page.getByRole("dialog", { name: title, exact: true });
}

/** A notice at the bottom of the screen (it has no role: its text is its name). */
export function notice(page: Page, text: string): Locator {
  return page.getByText(text, { exact: true });
}

/** The Undo button of a notice. */
export function undoButton(page: Page): Locator {
  return page.getByRole("button", { name: "Undo", exact: true });
}

/** A document's checkbox in the list. */
export function selectBox(page: Page, title: string): Locator {
  return documentRow(page, title).getByRole("checkbox", { name: `Select ${title}`, exact: true });
}

/** "Select all loaded" above the list. */
export function selectAllLoaded(page: Page): Locator {
  return page.getByRole("checkbox", { name: "Select all loaded", exact: true });
}

/** The bar that changes the checked documents. */
export function bulkBar(page: Page): Locator {
  return page.getByRole("region", { name: "Change the selected documents", exact: true });
}

/** A document's tag chips in its row. */
export function rowTags(page: Page, title: string): Locator {
  return documentRow(page, title).getByRole("list", { name: "Tags", exact: true });
}

/** Creates name (it must not exist yet) in the picker labelled label (correspondent, type or tags) and chooses it. */
export async function createChoice(page: Page, label: string, name: string): Promise<void> {
  await detailForm(page).getByRole("combobox", { name: label, exact: true }).fill(name);
  await page.getByRole("option", { name: `Create “${name}”` }).click();
}

/** Saves the open document's form and waits for "Saved". */
export async function saveDocument(page: Page): Promise<void> {
  await detailForm(page).getByRole("button", { name: "Save", exact: true }).click();
  await expect(detailForm(page).getByRole("status")).toHaveText("Saved");
}

/** The entry of the "Saved views" list in the navigation: its button, which opens the view. */
export function savedViewEntry(page: Page, name: string): Locator {
  return savedViewsList(page).getByRole("button", { name, exact: true });
}

/** The "Saved views" list in the navigation. */
export function savedViewsList(page: Page): Locator {
  return browseColumn(page).getByRole("list", { name: "Saved views", exact: true });
}

/** A saved view's row (the list item that holds its button and its menu): aria-current marks the view the list shows. */
export function savedViewRow(page: Page, name: string): Locator {
  return savedViewsList(page)
    .getByRole("listitem")
    .filter({ has: page.getByRole("button", { name, exact: true }) });
}

/** Opens the menu of the saved view named name and returns it (its items: Rename, Update…, Move up, Move down, Delete). */
export async function openSavedViewMenu(page: Page, name: string): Promise<Locator> {
  await savedViewRow(page, name)
    .getByRole("button", { name: `Actions for ${name}`, exact: true })
    .click();
  const menu = page.getByRole("menu", { name: `Actions for ${name}`, exact: true });
  await menu.waitFor();
  return menu;
}

/** A page of the navigation ("Log", "Users", "Profile", "Tags, correspondents & types"): a link of the SideNav. */
export function pageEntry(page: Page, name: string): Locator {
  return shellNav(page).getByRole("link", { name, exact: true });
}

/** A tab of the metadata page ("Tags", "Correspondents", "Document types"). */
export function metadataTab(page: Page, name: string): Locator {
  return page.getByRole("tab", { name, exact: true });
}

/**
 * An entry of the metadata page's list (the list is named after its tab;
 * the navigation has lists with the same names, so the page's main area).
 */
export function metadataRow(page: Page, list: string, name: string): Locator {
  return page
    .getByRole("main")
    .getByRole("list", { name: list, exact: true })
    .getByRole("listitem")
    .filter({ has: page.getByRole("button", { name, exact: true }) });
}

/** A user's card on the users page: the list item that starts with the user's name. */
export function userRow(page: Page, name: string): Locator {
  return page
    .getByRole("main")
    .getByRole("listitem")
    .filter({ has: page.getByText(name, { exact: true }) });
}

/** The panel that asks before a password is reset: an inline group inside the user's card. */
export function resetPasswordPanel(row: Locator): Locator {
  return row.getByRole("group").filter({ hasText: "They are logged out everywhere and get a new password from you." });
}

/** An entry of the log page: a list item of the page's main area. */
export function logEntries(page: Page): Locator {
  return page.getByRole("main").getByRole("listitem");
}

/**
 * A filter of the log page by its label. The labels wrap their select, so
 * the accessible name continues with the select's options: the name is
 * matched from its start.
 */
export function logFilter(page: Page, label: string): Locator {
  return page.getByRole("combobox", { name: new RegExp(`^${label}\\b`) });
}

/**
 * The banner of a document in the trash: a region named "Trash" inside the
 * document's detail (the list's own region has the same name).
 */
export function trashBanner(page: Page, title: string): Locator {
  return detail(page, title).getByRole("region", { name: "Trash", exact: true });
}
