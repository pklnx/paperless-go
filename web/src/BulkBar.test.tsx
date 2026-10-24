import { afterEach, beforeEach, describe, expect, it, onTestFinished, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { ApiError, bulkApi, type BulkCount } from "./api";
import { App } from "./App";
import { bulkErrorText } from "./BulkBar";
import { t } from "./i18n";
import { longPressTimer, maxSelection, selectionLimit } from "./selection";
import { doc, held, named, page, stubServer, tag, type Request, type Server } from "./testServer";

// Selecting documents in the list and changing them together, against a
// fake server: what the UI sends, and what it shows for each answer.

let server: Server;
let requests: Request[];

beforeEach(() => {
  ({ server, requests } = stubServer());
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
const gets = (path: string) => requests.filter((r) => r.method === "GET" && r.path === path);
const bulks = () => requests.filter((r) => r.path === "/api/documents/bulk").map((r) => r.body);
// By attribute, not by role: the role queries are slow on long lists.
const byLabel = <T extends Element>(sel: string, label: string) => {
  const el = Array.from(document.querySelectorAll<T>(sel)).find((e) => e.getAttribute("aria-label") === label);
  if (!el) throw new Error(`no ${sel} labelled ${label}`);
  return el;
};
/** A row's Carbon checkbox: the input its (visually hidden) label names. */
const box = (name: string) => {
  const label = Array.from(document.querySelectorAll("label")).find((l) => l.textContent === t.selectDocument(name));
  const el = label && document.getElementById(label.htmlFor);
  if (!(el instanceof HTMLInputElement)) throw new Error(`no checkbox labelled ${t.selectDocument(name)}`);
  return el;
};
const bar = () => within(byLabel<HTMLElement>("[role=region]", t.bulkActions));

/** What a browser does when the focused button is disabled: the focus falls to the body (jsdom keeps it on the button and ignores blur()). */
function dropFocus() {
  document.body.tabIndex = -1;
  document.body.focus();
  document.body.removeAttribute("tabindex");
}

async function start() {
  render(<App />);
  await waitFor(() => expect(gets("/api/documents").length).toBeGreaterThan(0));
  await waitFor(() => expect(screen.queryByText(t.loading)).toBeNull());
}

const five = () => page(["1", "2", "3", "4", "5"].map((id) => doc(id, `Doc ${id}`, { isInbox: true })));

describe("selection", () => {
  it("selects a range with shift, and a bulk change sends those IDs, reloads list and counts, and keeps the selection", async () => {
    server.documents = five;
    server.bulk = () => ({ body: { updated: 3 } });
    await start();
    await screen.findByText("Doc 1");
    expect(screen.queryByRole("region", { name: t.bulkActions })).toBeNull();

    fireEvent.click(box("Doc 2"));
    fireEvent.click(box("Doc 4"), { shiftKey: true });
    expect(["Doc 1", "Doc 2", "Doc 3", "Doc 4", "Doc 5"].map((n) => box(n).checked)).toEqual([false, true, true, true, false]);
    expect(bar().getByText(t.selectedCount(3))).toBeTruthy();
    // Shift-click back down to 3 unchecks 2 to 3.
    fireEvent.click(box("Doc 3"), { shiftKey: true });
    expect(["Doc 2", "Doc 3", "Doc 4"].map((n) => box(n).checked)).toEqual([false, false, true]);
    fireEvent.click(box("Doc 2"));
    fireEvent.click(box("Doc 3"));
    expect(["Doc 2", "Doc 3", "Doc 4"].map((n) => box(n).checked)).toEqual([true, true, true]);

    const listsBefore = gets("/api/documents").length;
    const countsBefore = gets("/api/documents/counts").length;
    fireEvent.click(bar().getByRole("button", { name: t.bulkRemoveFromInbox }));
    expect((await bar().findByRole("status")).textContent).toBe(t.bulkUpdated(3));
    expect(bulks()).toEqual([{ ids: ["2", "3", "4"], inbox: false }]);
    await waitFor(() => expect(gets("/api/documents").length).toBe(listsBefore + 1));
    await waitFor(() => expect(gets("/api/documents/counts").length).toBe(countsBefore + 1));
    expect(gets("/api/tags").length).toBeGreaterThan(1);
    expect(["Doc 2", "Doc 3", "Doc 4"].map((n) => box(n).checked)).toEqual([true, true, true]);
  });

  it("reloads every loaded page after a change, and drops documents that left the list from the selection", async () => {
    let inbox = new Set(["1", "2", "3"]);
    server.documents = (p) => {
      const all = ["1", "2", "3"].filter((id) => inbox.has(id)).map((id) => doc(id, `Doc ${id}`, { isInbox: true }));
      // Two pages: the first holds one document.
      return p.get("cursor") ? page(all.slice(1)) : page(all.slice(0, 1), all.length > 1 ? "c1" : undefined);
    };
    server.bulk = (b) => {
      inbox = new Set([...inbox].filter((id) => !(b.ids as string[]).includes(id)));
      return { body: { updated: 1 } };
    };
    window.history.replaceState(null, "", "/?inbox=1");
    await start();
    fireEvent.click(await screen.findByRole("button", { name: t.loadMore }));
    await screen.findByText("Doc 3");
    fireEvent.click(box("Doc 2"));
    fireEvent.click(box("Doc 3"));
    fireEvent.click(bar().getByRole("button", { name: t.bulkRemoveFromInbox }));
    await waitFor(() => expect(screen.queryByText("Doc 2")).toBeNull());
    // Doc 3 was on the second page: it is loaded again.
    expect(screen.queryByText("Doc 3")).toBeNull();
    expect(screen.getByText("Doc 1")).toBeTruthy();
    // Nothing selected any more: the bar is gone.
    expect(screen.queryByRole("region", { name: t.bulkActions })).toBeNull();
  });

  it("sends the right body for each action", async () => {
    server.documents = () => page([doc("1", "Doc 1")]);
    server.tags = [tag("t1", "Steuer"), tag("t2", "2024", "t1"), tag("t3", "Arzt")];
    server.correspondents = [named("c1", "Finanzamt")];
    server.types = [named("d1", "Rechnung")];
    server.bulk = () => ({ body: { updated: 1 } });
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectDocument("Doc 1") }));

    const simple: [string, Record<string, unknown>][] = [
      [t.bulkRemoveFromInbox, { inbox: false }],
      [t.bulkMoveToInbox, { inbox: true }],
      [t.bulkFavorite, { favorite: true }],
      [t.bulkUnfavorite, { favorite: false }],
    ];
    for (const [label, body] of simple) {
      const n = bulks().length;
      fireEvent.click(bar().getByRole("button", { name: label }));
      await waitFor(() => expect(bulks()).toHaveLength(n + 1));
      expect(bulks().at(-1)).toEqual({ ids: ["1"], ...body });
      await waitFor(() => expect(bar().getByRole("button", { name: label }).hasAttribute("disabled")).toBe(false));
    }

    // Tags: add two, remove one, in one request.
    fireEvent.click(bar().getByRole("button", { name: t.bulkTags }));
    const sheet = within(screen.getByRole("dialog", { name: t.bulkTagsTitle }));
    const [add, remove] = sheet.getAllByRole("group");
    fireEvent.click(within(add!).getByRole("checkbox", { name: "Steuer" }));
    fireEvent.click(within(add!).getByRole("checkbox", { name: "2024" }));
    fireEvent.click(within(remove!).getByRole("checkbox", { name: "Arzt" }));
    fireEvent.click(sheet.getByRole("button", { name: t.apply }));
    await waitFor(() => expect(bulks().at(-1)).toEqual({ ids: ["1"], addTags: ["t1", "t2"], removeTags: ["t3"] }));
    expect(screen.queryByRole("dialog", { name: t.bulkTagsTitle })).toBeNull();

    const pickNamed = async (button: string, title: string, choice: string, body: Record<string, unknown>) => {
      await waitFor(() => expect(bar().getByRole("button", { name: button }).hasAttribute("disabled")).toBe(false));
      fireEvent.click(bar().getByRole("button", { name: button }));
      const s = within(screen.getByRole("dialog", { name: title }));
      expect(s.getByRole("button", { name: t.apply }).hasAttribute("disabled")).toBe(true);
      fireEvent.click(s.getByRole("radio", { name: choice }));
      fireEvent.click(s.getByRole("button", { name: t.apply }));
      await waitFor(() => expect(bulks().at(-1)).toEqual({ ids: ["1"], ...body }));
    };
    await pickNamed(t.bulkCorrespondent, t.bulkCorrespondentTitle, "Finanzamt", { correspondent: "c1" });
    await pickNamed(t.bulkCorrespondent, t.bulkCorrespondentTitle, t.bulkClearCorrespondent, { correspondent: null });
    await pickNamed(t.bulkType, t.bulkTypeTitle, "Rechnung", { documentType: "d1" });
    await pickNamed(t.bulkType, t.bulkTypeTitle, t.bulkClearType, { documentType: null });
  });

  it("refuses a tag both added and removed before sending", async () => {
    server.documents = () => page([doc("1", "Doc 1")]);
    server.tags = [tag("t1", "Steuer")];
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectDocument("Doc 1") }));
    fireEvent.click(bar().getByRole("button", { name: t.bulkTags }));
    const sheet = within(screen.getByRole("dialog", { name: t.bulkTagsTitle }));
    const [add, remove] = sheet.getAllByRole("group");
    fireEvent.click(within(add!).getByRole("checkbox", { name: "Steuer" }));
    fireEvent.click(within(remove!).getByRole("checkbox", { name: "Steuer" }));
    expect(sheet.getByRole("alert").textContent).toBe(t.bulkTagAddAndRemove);
    const apply = sheet.getByRole("button", { name: t.apply });
    expect(apply.hasAttribute("disabled")).toBe(true);
    // Enter in the form sends nothing either.
    fireEvent.submit(add!.closest("form")!);
    expect(bulks()).toEqual([]);
  });

  it("offers only the owner's names when an admin selected another user's documents", async () => {
    server.user = { id: 1, name: "root", admin: true };
    window.history.replaceState(null, "", "/?all=1");
    server.documents = () => page([doc("1", "Bobs", { ownerId: 2, ownerName: "bob" }), doc("2", "Mine", { ownerId: 1, ownerName: "root" })]);
    server.tags = [tag("t1", "Root tag", null, { ownerId: 1 }), tag("t2", "Bob tag", null, { ownerId: 2 })];
    server.correspondents = [named("c1", "Root corr", 1, 1), named("c2", "Bob corr", 1, 2)];
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectDocument("Bobs") }));
    fireEvent.click(bar().getByRole("button", { name: t.bulkTags }));
    let sheet = within(screen.getByRole("dialog", { name: t.bulkTagsTitle }));
    expect(within(sheet.getAllByRole("group")[0]!).getAllByRole("checkbox").map((c) => c.getAttribute("aria-label") ?? c.parentElement?.textContent)).toEqual(["Bob tag"]);
    fireEvent.click(sheet.getByRole("button", { name: t.carbon.closeModal }));
    fireEvent.click(bar().getByRole("button", { name: t.bulkCorrespondent }));
    sheet = within(screen.getByRole("dialog", { name: t.bulkCorrespondentTitle }));
    expect(sheet.getAllByRole("radio").map((r) => r.parentElement?.textContent)).toEqual([t.bulkClearCorrespondent, "Bob corr"]);
  });

  it("selects all loaded up to the limit and says so", async () => {
    // The list's limit is the server's 500; a lowered one checks the same
    // cap with a few rows (hundreds of rows made the test slow).
    expect(selectionLimit.max).toBe(maxSelection);
    expect(maxSelection).toBe(500);
    selectionLimit.max = 5;
    onTestFinished(() => {
      selectionLimit.max = maxSelection;
    });
    // More than the limit, in two pages.
    const all = Array.from({ length: 10 }, (_, i) => doc(`d${i}`, `Doc ${i}`));
    server.documents = (p) => (p.get("cursor") ? page(all.slice(4)) : page(all.slice(0, 4), "c1"));
    await start();
    fireEvent.click(screen.getByRole("button", { name: t.loadMore }));
    await screen.findByText("Doc 9");
    fireEvent.click(screen.getByRole("checkbox", { name: t.selectAllLoaded }));
    expect(screen.getByText(t.selectionCapped(5))).toBeTruthy();
    expect(bar().getByText(t.selectedCount(5))).toBeTruthy();
    expect(box("Doc 4").checked).toBe(true);
    expect(box("Doc 5").checked).toBe(false);
    // Another one past the limit is refused the same way.
    fireEvent.click(box("Doc 7"));
    expect(box("Doc 7").checked).toBe(false);
    fireEvent.click(bar().getByRole("button", { name: t.bulkFavorite }));
    await waitFor(() => expect(bulks()).toHaveLength(1));
    expect((bulks()[0]!.ids as string[]).length).toBe(5);
    // Again: everything is selected, so it clears.
    fireEvent.click(screen.getByRole("checkbox", { name: t.selectAllLoaded }));
    expect(screen.queryByRole("region", { name: t.bulkActions })).toBeNull();
  });

  it("starts selecting with a long press on phones", async () => {
    const fire: (() => void)[] = [];
    vi.spyOn(longPressTimer, "set").mockImplementation((fn) => fire.push(fn));
    server.documents = five;
    await start();
    const row = (await screen.findByText("Doc 2")).closest("li")!;
    // A mouse does not start it.
    fireEvent.pointerDown(row, { pointerType: "mouse" });
    expect(fire).toHaveLength(0);
    // A touch that moves away (scrolling) does not either.
    fireEvent.pointerDown(row, { pointerType: "touch", clientX: 10, clientY: 10 });
    fireEvent.pointerMove(row, { pointerType: "touch", clientX: 10, clientY: 60 });
    expect(fire).toHaveLength(1);
    fireEvent.pointerDown(row, { pointerType: "touch", clientX: 10, clientY: 10 });
    expect(fire).toHaveLength(2);
    act(() => fire[1]!());
    expect(box("Doc 2").checked).toBe(true);
    expect(bar().getByText(t.selectedCount(1))).toBeTruthy();
  });
});

describe("selection next to the detail link", () => {
  it("checks a row by its checkbox without opening the document", async () => {
    server.documents = five;
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectDocument("Doc 2") }));
    fireEvent.click(box("Doc 4"), { shiftKey: true });
    expect(window.location.search).toBe("");
    expect(bar().getByText(t.selectedCount(3))).toBeTruthy();
  });

  it("selects with a long press on the title link, and the click that ends it does not open the document", async () => {
    const fire: (() => void)[] = [];
    vi.spyOn(longPressTimer, "set").mockImplementation((fn) => fire.push(fn));
    server.documents = five;
    await start();
    const link = await screen.findByRole("link", { name: "Doc 2" });
    fireEvent.pointerDown(link, { pointerType: "touch", clientX: 5, clientY: 5 });
    expect(fire).toHaveLength(1);
    act(() => fire[0]!());
    fireEvent.pointerUp(link, { pointerType: "touch" });
    fireEvent.click(link);
    expect(window.location.search).toBe("");
    expect(box("Doc 2").checked).toBe(true);
    // A plain tap opens it as before.
    fireEvent.pointerDown(link, { pointerType: "touch", clientX: 5, clientY: 5 });
    fireEvent.pointerUp(link, { pointerType: "touch" });
    fireEvent.click(link);
    expect(window.location.search).toBe("?doc=2");
    // The open document covers the list: the selection and its bar stay, but cannot be used.
    expect(box("Doc 2").checked).toBe(true);
    expect(byLabel<HTMLElement>("[role=region]", t.bulkActions).closest("[inert]")).not.toBeNull();
  });
});

describe("errors", () => {
  it("explains several owners before sending: no tags, only clearing a correspondent or type, and keeps the selection", async () => {
    server.user = { id: 1, name: "root", admin: true };
    window.history.replaceState(null, "", "/?all=1");
    server.documents = () => page([doc("1", "Bobs", { ownerId: 2 }), doc("2", "Mine", { ownerId: 1 })]);
    server.tags = [tag("t1", "Steuer", null, { ownerId: 1 }), tag("t2", "Steuer", null, { ownerId: 2 })];
    server.correspondents = [named("c1", "Amt", 1, 1), named("c2", "Amt", 1, 2)];
    server.types = [named("d1", "Rechnung", 1, 1)];
    server.bulk = () => ({ body: { updated: 2 } });
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectDocument("Bobs") }));
    fireEvent.click(box("Mine"));
    // Tags: the one-owner rule right away, no sheet, nothing sent.
    fireEvent.click(bar().getByRole("button", { name: t.bulkTags }));
    expect((await bar().findByRole("alert")).textContent).toBe(t.bulkSeveralOwners);
    expect(screen.queryByRole("dialog", { name: t.bulkTagsTitle })).toBeNull();
    // Type: only clearing, and why; no owner's names.
    fireEvent.click(bar().getByRole("button", { name: t.bulkType }));
    let s = within(screen.getByRole("dialog", { name: t.bulkTypeTitle }));
    expect(s.getAllByRole("radio").map((r) => r.parentElement?.textContent)).toEqual([t.bulkClearType]);
    fireEvent.click(s.getByRole("button", { name: t.carbon.closeModal }));
    // Correspondent: only clearing, which goes through for both owners.
    fireEvent.click(bar().getByRole("button", { name: t.bulkCorrespondent }));
    s = within(screen.getByRole("dialog", { name: t.bulkCorrespondentTitle }));
    expect(s.getByText(t.bulkOnlyClearSeveralOwners)).toBeTruthy();
    expect(s.getAllByRole("radio").map((r) => r.parentElement?.textContent)).toEqual([t.bulkClearCorrespondent]);
    expect(bulks()).toEqual([]);
    fireEvent.click(s.getByRole("radio", { name: t.bulkClearCorrespondent }));
    fireEvent.click(s.getByRole("button", { name: t.apply }));
    expect((await bar().findByRole("status")).textContent).toBe(t.bulkUpdated(2));
    expect(bulks()).toEqual([{ ids: ["1", "2"], correspondent: null }]);
    expect(box("Bobs").checked && box("Mine").checked).toBe(true);
  });

  it("offers to try again after a 503, and the retry sends the same change", async () => {
    server.documents = () => page([doc("1", "Doc 1")]);
    let fail = true;
    server.bulk = () => (fail ? { status: 503, body: { error: "a concurrent change got in the way; try again" } } : { body: { updated: 1 } });
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectDocument("Doc 1") }));
    fireEvent.click(bar().getByRole("button", { name: t.bulkFavorite }));
    const alert = await bar().findByRole("alert");
    expect(alert.textContent).toContain(t.serverBusy);
    expect(box("Doc 1").checked).toBe(true);
    fail = false;
    fireEvent.click(within(alert).getByRole("button", { name: t.tryAgain }));
    expect((await bar().findByRole("status")).textContent).toBe(t.bulkUpdated(1));
    expect(bulks()).toEqual([
      { ids: ["1"], favorite: true },
      { ids: ["1"], favorite: true },
    ]);
  });

  it("drops an error and its Try again when the selection changes", async () => {
    server.documents = () => page([doc("1", "Doc 1"), doc("2", "Doc 2")]);
    server.bulk = () => ({ status: 503, body: { error: "a concurrent change got in the way; try again" } });
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectDocument("Doc 1") }));
    fireEvent.click(bar().getByRole("button", { name: t.bulkFavorite }));
    expect((await bar().findByRole("alert")).textContent).toContain(t.serverBusy);
    fireEvent.click(box("Doc 2"));
    expect(bar().queryByRole("alert")).toBeNull();
    expect(bar().queryByRole("button", { name: t.tryAgain })).toBeNull();
  });

  it("counts and asks again before retrying a change of all matches", async () => {
    server.documents = (p) => (p.get("cursor") ? page([doc("3", "Doc 3")]) : page([doc("1", "Doc 1"), doc("2", "Doc 2")], "c1"));
    let fail = true;
    server.bulk = (b) => (b.dryRun ? { body: { matched: 3, owners: 1, limit: 5000, fingerprint: "v1.fp" } } : fail ? { status: 503, body: { error: "busy" } } : { body: { updated: 3 } });
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectAllLoaded }));
    fireEvent.click(screen.getByRole("button", { name: t.selectAllMatches }));
    await screen.findByText(t.allMatchesSelected(3));
    fireEvent.click(bar().getByRole("button", { name: t.bulkFavorite }));
    fireEvent.click(within(await screen.findByRole("dialog", { name: t.bulkConfirmTitle })).getByRole("button", { name: t.bulkConfirm(3) }));
    const alert = await bar().findByRole("alert");
    fail = false;
    fireEvent.click(within(alert).getByRole("button", { name: t.tryAgain }));
    fireEvent.click(within(await screen.findByRole("dialog", { name: t.bulkConfirmTitle })).getByRole("button", { name: t.bulkConfirm(3) }));
    expect((await bar().findByRole("status")).textContent).toBe(t.bulkUpdated(3));
    const match = { tz };
    expect(bulks()).toEqual([
      { match, dryRun: true },
      { match, dryRun: true },
      { match, fingerprint: "v1.fp", favorite: true },
      { match, dryRun: true },
      { match, fingerprint: "v1.fp", favorite: true },
    ]);
  });

  it("names each refusal", () => {
    const err = (status: number, body: Record<string, unknown>) => new ApiError(status, String(body.error ?? ""), body);
    expect(bulkErrorText(err(400, { code: "too_many_documents", matched: 5001, limit: 5000 }))).toEqual({ text: `${t.bulkMatchCount(5001)} ${t.bulkTooMany(5000)}`, retry: false });
    expect(bulkErrorText(err(400, { code: "too_many_documents", limit: 500 })).text).toBe(t.bulkTooMany(500));
    expect(bulkErrorText(err(400, { code: "several_owners" })).text).toBe(t.bulkSeveralOwners);
    expect(bulkErrorText(err(400, { code: "owner_mismatch" })).text).toBe(t.bulkOwnerMismatch);
    expect(bulkErrorText(err(400, { code: "tag_add_and_remove" })).text).toBe(t.bulkTagAddAndRemove);
    expect(bulkErrorText(err(400, { code: "nothing_to_change" })).text).toBe(t.bulkNothingToChange);
    expect(bulkErrorText(err(400, { code: "empty_selection" })).text).toBe(t.bulkEmptySelection);
    expect(bulkErrorText(err(409, { code: "match_changed", matched: 0 }))).toEqual({ text: t.bulkMatchChangedError, retry: true });
    expect(bulkErrorText(err(404, { error: "not found" }))).toEqual({ text: t.bulkNotFound, retry: false });
    expect(bulkErrorText(err(503, { error: "audit log unavailable" }))).toEqual({ text: t.serverBusy, retry: true });
    expect(bulkErrorText(err(500, { error: "boom" })).text).toBe(t.failed("boom"));
    expect(bulkErrorText(new Error("offline")).text).toBe(t.failed("offline"));
  });
});

describe("all matches of a search", () => {
  const twoPages = () => (p: URLSearchParams) => (p.get("cursor") ? page([doc("3", "Doc 3")]) : page([doc("1", "Doc 1"), doc("2", "Doc 2")], "c1"));

  it("counts the matches, asks with the count, and sends the search, owner=me for an admin without the switch", async () => {
    server.user = { id: 1, name: "root", admin: true };
    server.tags = [tag("t1", "Steuer")];
    window.history.replaceState(null, "", "/?tag=t1&q=Rechnung");
    server.documents = twoPages();
    let matched = 120;
    server.bulk = (b) => (b.dryRun ? { body: { matched, owners: 1, limit: 5000, fingerprint: `v1.${matched}` } } : { body: { updated: 121 } });
    await start();
    await screen.findByText("Doc 1");
    // Only once every loaded one is checked.
    expect(screen.queryByRole("button", { name: t.selectAllMatches })).toBeNull();
    fireEvent.click(screen.getByRole("checkbox", { name: t.selectAllLoaded }));
    fireEvent.click(screen.getByRole("button", { name: t.selectAllMatches }));
    expect(await screen.findByText(t.allMatchesSelected(120))).toBeTruthy();
    const match = { q: "Rechnung", tag: ["t1"], owner: "me", tz };
    expect(bulks()).toEqual([{ match, dryRun: true }]);
    expect(bar().getByText(t.selectedCount(120))).toBeTruthy();

    // The action counts again, and the confirmation names the new count.
    matched = 121;
    fireEvent.click(bar().getByRole("button", { name: t.bulkRemoveFromInbox }));
    const confirm = within(await screen.findByRole("dialog", { name: t.bulkConfirmTitle }));
    expect(confirm.getByText(t.bulkConfirmText(121))).toBeTruthy();
    expect(bulks()).toHaveLength(2);
    // Cancel sends nothing.
    fireEvent.click(confirm.getByRole("button", { name: t.cancel }));
    expect(bulks()).toHaveLength(2);

    fireEvent.click(bar().getByRole("button", { name: t.bulkRemoveFromInbox }));
    fireEvent.click(within(await screen.findByRole("dialog", { name: t.bulkConfirmTitle })).getByRole("button", { name: t.bulkConfirm(121) }));
    expect((await bar().findByRole("status")).textContent).toBe(t.bulkUpdated(121));
    // The change sends the fingerprint of the dry run whose count the dialog showed.
    expect(bulks()).toEqual([{ match, dryRun: true }, { match, dryRun: true }, { match, dryRun: true }, { match, fingerprint: "v1.121", inbox: false }]);
    // The count is stale now: back to the checked rows.
    await waitFor(() => expect(screen.queryByText(t.allMatchesSelected(120))).toBeNull());
    expect(bar().getByText(t.selectedCount(2))).toBeTruthy();
    fireEvent.click(bar().getByRole("button", { name: t.bulkFavorite }));
    await waitFor(() => expect(bulks().at(-1)).toEqual({ ids: ["1", "2"], favorite: true }));
  });

  it("offers one owner's names when all matches belong to that owner", async () => {
    server.user = { id: 1, name: "root", admin: true };
    window.history.replaceState(null, "", "/?all=1");
    server.documents = (p) => (p.get("cursor") ? page([doc("3", "B3", { ownerId: 2 })]) : page([doc("1", "B1", { ownerId: 2 }), doc("2", "B2", { ownerId: 2 })], "c1"));
    server.tags = [tag("t1", "Root tag", null, { ownerId: 1 }), tag("t2", "Bob tag", null, { ownerId: 2 })];
    let owners = 1;
    server.bulk = () => ({ body: { matched: 3, owners, limit: 5000, fingerprint: "v1.fp" } });
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectAllLoaded }));
    fireEvent.click(screen.getByRole("button", { name: t.selectAllMatches }));
    await screen.findByText(t.allMatchesSelected(3));
    const tagChoices = () => {
      fireEvent.click(bar().getByRole("button", { name: t.bulkTags }));
      const sheet = within(screen.getByRole("dialog", { name: t.bulkTagsTitle }));
      const names = within(sheet.getAllByRole("group")[0]!).getAllByRole("checkbox").map((c) => c.parentElement?.textContent);
      fireEvent.click(sheet.getByRole("button", { name: t.carbon.closeModal }));
      return names;
    };
    expect(tagChoices()).toEqual(["Bob tag"]);
    // Several owners: no names at all, the one-owner rule instead, before anything is sent.
    owners = 2;
    fireEvent.click(screen.getByRole("button", { name: t.clearSelection }));
    fireEvent.click(screen.getByRole("checkbox", { name: t.selectAllLoaded }));
    fireEvent.click(screen.getByRole("button", { name: t.selectAllMatches }));
    await screen.findByText(t.allMatchesSelected(3));
    const sent = bulks().length;
    fireEvent.click(bar().getByRole("button", { name: t.bulkTags }));
    expect((await bar().findByRole("alert")).textContent).toBe(t.bulkSeveralOwners);
    expect(screen.queryByRole("dialog", { name: t.bulkTagsTitle })).toBeNull();
    expect(bulks()).toHaveLength(sent);
  });

  it("shows the one-owner rule instead of the confirmation when the fresh count finds several owners", async () => {
    server.user = { id: 1, name: "root", admin: true };
    window.history.replaceState(null, "", "/?all=1");
    server.documents = (p) => (p.get("cursor") ? page([doc("3", "B3", { ownerId: 2 })]) : page([doc("1", "B1", { ownerId: 2 }), doc("2", "B2", { ownerId: 2 })], "c1"));
    server.tags = [tag("t2", "Bob tag", null, { ownerId: 2 })];
    let owners = 1;
    server.bulk = () => ({ body: { matched: 3, owners, limit: 5000, fingerprint: "v1.fp" } });
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectAllLoaded }));
    fireEvent.click(screen.getByRole("button", { name: t.selectAllMatches }));
    await screen.findByText(t.allMatchesSelected(3));
    fireEvent.click(bar().getByRole("button", { name: t.bulkTags }));
    const sheet = within(screen.getByRole("dialog", { name: t.bulkTagsTitle }));
    fireEvent.click(within(sheet.getAllByRole("group")[0]!).getByRole("checkbox", { name: "Bob tag" }));
    // Another user's document joined the matches since they were selected.
    owners = 2;
    fireEvent.click(sheet.getByRole("button", { name: t.apply }));
    expect((await bar().findByRole("alert")).textContent).toBe(t.bulkSeveralOwners);
    expect(screen.queryByRole("dialog", { name: t.bulkConfirmTitle })).toBeNull();
    expect(bulks().every((b) => b!.dryRun === true)).toBe(true);
  });

  it("counts and asks again after 409 match_changed, and never resends by itself", async () => {
    server.documents = twoPages();
    let matched = 230;
    let changes = 0;
    server.bulk = (b) => {
      if (b.dryRun) return { body: { matched, owners: 1, limit: 5000, fingerprint: `v1.${matched}` } };
      changes++;
      return b.fingerprint === `v1.${matched}`
        ? { body: { updated: matched } }
        : { status: 409, body: { error: "the matches of the search changed since they were counted", code: "match_changed", matched } };
    };
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectAllLoaded }));
    fireEvent.click(screen.getByRole("button", { name: t.selectAllMatches }));
    await screen.findByText(t.allMatchesSelected(230));
    fireEvent.click(bar().getByRole("button", { name: t.bulkRemoveFromInbox }));
    let dialog = within(await screen.findByRole("dialog", { name: t.bulkConfirmTitle }));
    expect(dialog.queryByText(t.bulkMatchChanged(230))).toBeNull();
    // One document is imported before the confirm.
    matched = 231;
    fireEvent.click(dialog.getByRole("button", { name: t.bulkConfirm(230) }));
    // Counted again; the dialog says so, with the new count.
    dialog = within(await screen.findByRole("dialog", { name: t.bulkConfirmTitle }));
    expect(dialog.getByText(t.bulkMatchChanged(231))).toBeTruthy();
    expect(dialog.getByRole("button", { name: t.bulkConfirm(231) })).toBeTruthy();
    const match = { tz };
    expect(bulks()).toEqual([
      { match, dryRun: true },
      { match, dryRun: true },
      { match, fingerprint: "v1.230", inbox: false },
      { match, dryRun: true },
    ]);
    expect(changes).toBe(1);
    // Cancel sends nothing.
    fireEvent.click(dialog.getByRole("button", { name: t.cancel }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: t.bulkConfirmTitle })).toBeNull());
    expect(bulks()).toHaveLength(4);
    expect(changes).toBe(1);
    // Asked again, confirmed again: the new fingerprint.
    fireEvent.click(bar().getByRole("button", { name: t.bulkRemoveFromInbox }));
    dialog = within(await screen.findByRole("dialog", { name: t.bulkConfirmTitle }));
    expect(dialog.queryByText(t.bulkMatchChanged(231))).toBeNull();
    fireEvent.click(dialog.getByRole("button", { name: t.bulkConfirm(231) }));
    expect((await bar().findByRole("status")).textContent).toBe(t.bulkUpdated(231));
    expect(bulks().at(-1)).toEqual({ match, fingerprint: "v1.231", inbox: false });
    expect(changes).toBe(2);
  });

  it("drops the bar's count when the checked rows changed meanwhile", async () => {
    server.documents = twoPages();
    let answer: ReturnType<typeof held> | null = null;
    server.bulk = (b) => {
      if (!b.dryRun) return { body: { updated: 1 } };
      if (answer) return answer.promise;
      return { body: { matched: 3, owners: 1, limit: 5000, fingerprint: "v1.fp" } };
    };
    let counted: Promise<BulkCount> | undefined;
    const count = bulkApi.count;
    vi.spyOn(bulkApi, "count").mockImplementation((m) => (counted = count(m)));
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectAllLoaded }));
    fireEvent.click(screen.getByRole("button", { name: t.selectAllMatches }));
    await screen.findByText(t.allMatchesSelected(3));
    answer = held();
    fireEvent.click(bar().getByRole("button", { name: t.bulkFavorite }));
    expect(bar().getByText(t.bulkCounting)).toBeTruthy();
    // The user checks one row instead: the bar is about that row now.
    fireEvent.click(box("Doc 1"));
    await act(async () => {
      answer!.release({ body: { matched: 3, owners: 1, limit: 5000, fingerprint: "v1.fp" } });
      await counted;
    });
    expect(screen.queryByRole("dialog", { name: t.bulkConfirmTitle })).toBeNull();
    expect(bar().queryByText(t.bulkCounting)).toBeNull();
    expect(bar().getByText(t.selectedCount(1))).toBeTruthy();
    // Not busy: the actions work.
    expect(bar().getByRole("button", { name: t.bulkFavorite }).hasAttribute("disabled")).toBe(false);
  });

  it("keeps a later choice when a held Select all matches count answers", async () => {
    server.documents = twoPages();
    const answer = held();
    server.bulk = () => answer.promise;
    let counted: Promise<BulkCount> | undefined;
    const count = bulkApi.count;
    vi.spyOn(bulkApi, "count").mockImplementation((m) => (counted = count(m)));
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectAllLoaded }));
    fireEvent.click(screen.getByRole("button", { name: t.selectAllMatches }));
    fireEvent.click(screen.getByRole("button", { name: t.clearSelection }));
    fireEvent.click(box("Doc 2"));
    await act(async () => {
      answer.release({ body: { matched: 3, owners: 1, limit: 5000, fingerprint: "v1.fp" } });
      await counted;
    });
    expect(screen.queryByText(t.allMatchesSelected(3))).toBeNull();
    expect(bar().getByText(t.selectedCount(1))).toBeTruthy();
    expect(box("Doc 2").checked && !box("Doc 1").checked).toBe(true);
  });

  it("sends no owner with Everyone's documents on, and none for a user", async () => {
    server.user = { id: 1, name: "root", admin: true };
    window.history.replaceState(null, "", "/?all=1&inbox=1");
    server.documents = twoPages();
    server.bulk = () => ({ body: { matched: 3, owners: 2, limit: 5000, fingerprint: "v1.fp" } });
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectAllLoaded }));
    fireEvent.click(screen.getByRole("button", { name: t.selectAllMatches }));
    await screen.findByText(t.allMatchesSelected(3));
    expect(bulks()).toEqual([{ match: { inbox: true, tz }, dryRun: true }]);
  });

  it("refuses a search with too many matches, and one without any", async () => {
    server.documents = twoPages();
    server.bulk = () => ({ body: { matched: 6000, owners: 0, limit: 5000 } });
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectAllLoaded }));
    fireEvent.click(screen.getByRole("button", { name: t.selectAllMatches }));
    expect((await screen.findByRole("alert")).textContent).toBe(`${t.bulkMatchCount(6000)} ${t.bulkTooMany(5000)}`);
    // Still the loaded ones only.
    expect(bar().getByText(t.selectedCount(2))).toBeTruthy();

    // The matches shrank to none between selecting and the action.
    let matched = 3;
    server.bulk = () => ({ body: { matched, owners: 1, limit: 5000, fingerprint: "v1.fp" } });
    fireEvent.click(screen.getByRole("button", { name: t.selectAllMatches }));
    await screen.findByText(t.allMatchesSelected(3));
    matched = 0;
    fireEvent.click(bar().getByRole("button", { name: t.bulkFavorite }));
    expect((await bar().findByRole("alert")).textContent).toBe(t.bulkNoMatches);
    expect(screen.queryByRole("dialog", { name: t.bulkConfirmTitle })).toBeNull();
    expect(bulks().every((b) => b!.dryRun === true)).toBe(true);
  });

  it("clears the selection for another list, checked rows and all matches", async () => {
    server.tags = [tag("t1", "Steuer"), tag("t2", "Arzt")];
    // Every list holds the same documents, so pruning alone would keep them checked.
    server.documents = twoPages();
    server.bulk = () => ({ body: { matched: 3, owners: 1, limit: 5000, fingerprint: "v1.fp" } });
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectDocument("Doc 1") }));
    expect(bar().getByText(t.selectedCount(1))).toBeTruthy();
    fireEvent.click(screen.getByRole("treeitem", { name: /^Steuer/ }));
    await waitFor(() => expect(screen.queryByRole("region", { name: t.bulkActions })).toBeNull());
    expect(box("Doc 1").checked).toBe(false);

    fireEvent.click(screen.getByRole("checkbox", { name: t.selectAllLoaded }));
    fireEvent.click(screen.getByRole("button", { name: t.selectAllMatches }));
    await screen.findByText(t.allMatchesSelected(3));
    fireEvent.click(screen.getByRole("treeitem", { name: /^Arzt/ }));
    await waitFor(() => expect(screen.queryByRole("region", { name: t.bulkActions })).toBeNull());
    expect(screen.queryByText(t.allMatchesSelected(3))).toBeNull();
    expect(box("Doc 1").checked || box("Doc 2").checked).toBe(false);

    // A refusal of "all matches" belongs to its list too.
    server.bulk = () => ({ body: { matched: 6000, owners: 0, limit: 5000 } });
    fireEvent.click(screen.getByRole("checkbox", { name: t.selectAllLoaded }));
    fireEvent.click(screen.getByRole("button", { name: t.selectAllMatches }));
    expect((await screen.findByRole("alert")).textContent).toBe(`${t.bulkMatchCount(6000)} ${t.bulkTooMany(5000)}`);
    fireEvent.click(screen.getByRole("treeitem", { name: /^Steuer/ }));
    await waitFor(() => expect(screen.queryByRole("region", { name: t.bulkActions })).toBeNull());
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("drops a count that answers after the list changed", async () => {
    server.tags = [tag("t1", "Steuer")];
    server.documents = twoPages();
    const answer = held();
    server.bulk = () => answer.promise;
    let counted: Promise<BulkCount> | undefined;
    const count = bulkApi.count;
    vi.spyOn(bulkApi, "count").mockImplementation((m) => (counted = count(m)));
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectAllLoaded }));
    fireEvent.click(screen.getByRole("button", { name: t.selectAllMatches }));
    expect(bulks()).toEqual([{ match: { tz }, dryRun: true }]);
    fireEvent.click(screen.getByRole("treeitem", { name: /^Steuer/ }));
    await waitFor(() => expect(gets("/api/documents").at(-1)!.url).toContain("tag=t1"));
    await screen.findByText("Doc 1");
    // The list's own handler of the count runs before this await resumes.
    await act(async () => {
      answer.release({ body: { matched: 3, owners: 1, limit: 5000, fingerprint: "v1.fp" } });
      await counted;
    });
    expect(screen.queryByText(t.allMatchesSelected(3))).toBeNull();
    expect(screen.queryByRole("region", { name: t.bulkActions })).toBeNull();
    expect(box("Doc 1").checked).toBe(false);
  });

  it("leaves a list that loaded after a slow change as it is", async () => {
    server.tags = [tag("t1", "Steuer")];
    server.documents = (p) => (p.get("tag") ? page([doc("b1", "B 1")]) : page([doc("1", "Doc 1")]));
    const answer = held();
    server.bulk = () => answer.promise;
    let applied: Promise<number> | undefined;
    const apply = bulkApi.apply;
    vi.spyOn(bulkApi, "apply").mockImplementation((sel, c) => (applied = apply(sel, c)));
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectDocument("Doc 1") }));
    fireEvent.click(bar().getByRole("button", { name: t.bulkFavorite }));
    fireEvent.click(screen.getByRole("treeitem", { name: /^Steuer/ }));
    await screen.findByText("B 1");
    const lists = gets("/api/documents").length;
    const counts = gets("/api/documents/counts").length;
    await act(async () => {
      answer.release({ body: { updated: 1 } });
      await applied;
    });
    // The change reloads the counts (after the list would have started loading again) ...
    await waitFor(() => expect(gets("/api/documents/counts").length).toBe(counts + 1));
    // ... but not the list, which belongs to the change's list, not this one.
    expect(gets("/api/documents")).toHaveLength(lists);
    expect(screen.getByText("B 1")).toBeTruthy();
  });
});

describe("the bar's Carbon modals (#150, part 3)", () => {
  const twoPages = () => (p: URLSearchParams) => (p.get("cursor") ? page([doc("3", "Doc 3")]) : page([doc("1", "Doc 1"), doc("2", "Doc 2")], "c1"));
  const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
  /** Every POST /api/documents/bulk as method, path and body. */
  const posts = () => requests.filter((r) => r.path === "/api/documents/bulk").map((r) => ({ method: r.method, path: r.path, body: r.body }));
  const dialog = (name: string) => screen.findByRole("dialog", { name });
  const gone = (name: string) => waitFor(() => expect(screen.queryByRole("dialog", { name })).toBeNull());

  it("sends nothing from the every-match confirmation on Cancel, Escape or the close button: the dry runs are the only requests", async () => {
    server.documents = twoPages();
    const counted = { body: { matched: 3, owners: 1, limit: 5000, fingerprint: "v1.fp" } };
    let hold: ReturnType<typeof held> | null = null;
    server.bulk = (b) => (b.dryRun ? (hold ? hold.promise : counted) : { body: { updated: 3 } });
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectAllLoaded }));
    fireEvent.click(screen.getByRole("button", { name: t.selectAllMatches }));
    await screen.findByText(t.allMatchesSelected(3));
    const favorite = () => bar().getByRole("button", { name: t.bulkFavorite });
    expect(bar().getByText(t.selectedCount(3))).toBeTruthy();

    // While the dry run counts, the bar's buttons are disabled; a browser
    // drops the focus from a disabled button (jsdom keeps it, so the blur
    // is simulated), and the modal mounts with the focus on the body.
    hold = held();
    favorite().focus();
    fireEvent.click(favorite());
    expect(favorite().hasAttribute("disabled")).toBe(true);
    act(() => dropFocus());
    expect(document.activeElement).toBe(document.body);
    await act(async () => hold!.release(counted));
    hold = null;
    let ask = within(await dialog(t.bulkConfirmTitle));
    expect(ask.getByText(t.bulkConfirmText(3))).toBeTruthy();
    // It opens with the focus on Cancel, not on "Change 3 documents".
    expect(document.activeElement).toBe(ask.getByRole("button", { name: t.cancel }));
    fireEvent.click(ask.getByRole("button", { name: t.cancel }));
    await gone(t.bulkConfirmTitle);
    // The focus is back on the button that asked.
    expect(document.activeElement).toBe(favorite());

    fireEvent.click(favorite());
    ask = within(await dialog(t.bulkConfirmTitle));
    fireEvent.keyDown(ask.getByRole("button", { name: t.cancel }), { key: "Escape" });
    await gone(t.bulkConfirmTitle);

    fireEvent.click(favorite());
    ask = within(await dialog(t.bulkConfirmTitle));
    fireEvent.click(ask.getByRole("button", { name: t.carbon.closeModal }));
    await gone(t.bulkConfirmTitle);

    const dryRun = { method: "POST", path: "/api/documents/bulk", body: { match: { tz }, dryRun: true } };
    expect(posts()).toEqual([dryRun, dryRun, dryRun, dryRun]);
    expect(requests.filter((r) => r.method !== "GET" && !(r.body && r.body.dryRun === true))).toHaveLength(0);
    // The bar is still there with its count, and the selection too.
    expect(bar().getByText(t.selectedCount(3))).toBeTruthy();
    expect(box("Doc 1").checked).toBe(true);
  });

  it("sends nothing from the question before checked documents go to the trash on Cancel or Escape", async () => {
    server.documents = () => page([doc("1", "Doc 1"), doc("2", "Doc 2")]);
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectDocument("Doc 1") }));
    fireEvent.click(box("Doc 2"));
    expect(bar().getByText(t.selectedCount(2))).toBeTruthy();
    fireEvent.click(bar().getByRole("button", { name: t.bulkDelete }));
    let ask = within(await dialog(t.bulkTrashTitle));
    expect(ask.getByText(t.bulkTrashText(2))).toBeTruthy();
    expect(ask.getByRole("button", { name: t.bulkTrashConfirm(2) })).toBeTruthy();
    fireEvent.click(ask.getByRole("button", { name: t.cancel }));
    await gone(t.bulkTrashTitle);
    fireEvent.click(bar().getByRole("button", { name: t.bulkDelete }));
    ask = within(await dialog(t.bulkTrashTitle));
    fireEvent.keyDown(ask.getByRole("button", { name: t.bulkTrashConfirm(2) }), { key: "Escape" });
    await gone(t.bulkTrashTitle);
    expect(posts()).toEqual([]);
    expect(requests.filter((r) => r.method !== "GET")).toHaveLength(0);
    expect(box("Doc 1").checked && box("Doc 2").checked).toBe(true);
  });

  it("sends nothing from the every-match restore question on Cancel or Escape", async () => {
    const trashed = { deletedAt: "2026-10-01T08:00:00Z", purgeAt: "2026-10-31T08:00:00Z" };
    server.documents = (p) => (p.get("cursor") ? page([doc("3", "Doc 3", trashed)]) : page([doc("1", "Doc 1", trashed), doc("2", "Doc 2", trashed)], "c1"));
    server.bulk = (b) => (b.dryRun ? { body: { matched: 3, owners: 1, limit: 5000, fingerprint: "v1.fp" } } : { body: { updated: 3 } });
    window.history.replaceState(null, "", "/?trash=1");
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectAllLoaded }));
    fireEvent.click(screen.getByRole("button", { name: t.selectAllMatches }));
    await screen.findByText(t.allMatchesSelected(3));
    fireEvent.click(bar().getByRole("button", { name: t.restore }));
    let ask = within(await dialog(t.bulkRestoreTitle));
    expect(ask.getByText(t.bulkRestoreText(3))).toBeTruthy();
    expect(ask.getByText(t.bulkEveryMatch)).toBeTruthy();
    fireEvent.click(ask.getByRole("button", { name: t.cancel }));
    await gone(t.bulkRestoreTitle);
    fireEvent.click(bar().getByRole("button", { name: t.restore }));
    ask = within(await dialog(t.bulkRestoreTitle));
    fireEvent.keyDown(ask.getByRole("button", { name: t.bulkRestoreConfirm(3) }), { key: "Escape" });
    await gone(t.bulkRestoreTitle);
    const match = { trashed: true, tz };
    const dryRun = { method: "POST", path: "/api/documents/bulk", body: { match, dryRun: true, action: "restore" } };
    expect(posts()).toEqual([dryRun, dryRun, dryRun]);
  });

  it("sends nothing from the permanent delete question on Cancel, Escape or the close button", async () => {
    const trashed = { deletedAt: "2026-10-01T08:00:00Z", purgeAt: "2026-10-31T08:00:00Z" };
    server.documents = () => page([doc("1", "Doc 1", trashed), doc("2", "Doc 2", trashed)]);
    window.history.replaceState(null, "", "/?trash=1");
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectDocument("Doc 1") }));
    fireEvent.click(box("Doc 2"));
    const purge = () => bar().getByRole("button", { name: t.purge });
    fireEvent.click(purge());
    let ask = within(await dialog(t.purgeBulkTitle));
    expect(ask.getByText(t.purgeText(2))).toBeTruthy();
    fireEvent.click(ask.getByRole("button", { name: t.cancel }));
    await gone(t.purgeBulkTitle);
    fireEvent.click(purge());
    ask = within(await dialog(t.purgeBulkTitle));
    fireEvent.keyDown(ask.getByRole("button", { name: t.purgeConfirm(2) }), { key: "Escape" });
    await gone(t.purgeBulkTitle);
    fireEvent.click(purge());
    ask = within(await dialog(t.purgeBulkTitle));
    fireEvent.click(ask.getByRole("button", { name: t.carbon.closeModal }));
    await gone(t.purgeBulkTitle);
    expect(posts()).toEqual([]);
    expect(requests.filter((r) => r.method !== "GET")).toHaveLength(0);
    expect(box("Doc 1").checked && box("Doc 2").checked).toBe(true);
  });

  it("closes the tag, correspondent and type forms on Cancel and Escape without sending anything", async () => {
    server.documents = () => page([doc("1", "Doc 1")]);
    server.tags = [tag("t1", "Steuer")];
    server.correspondents = [named("c1", "Finanzamt")];
    server.types = [named("d1", "Rechnung")];
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectDocument("Doc 1") }));
    for (const [button, title, choose] of [
      [t.bulkTags, t.bulkTagsTitle, (d: ReturnType<typeof within>) => fireEvent.click(within(d.getAllByRole("group")[0]!).getByRole("checkbox", { name: "Steuer" }))],
      [t.bulkCorrespondent, t.bulkCorrespondentTitle, (d: ReturnType<typeof within>) => fireEvent.click(d.getByRole("radio", { name: "Finanzamt" }))],
      [t.bulkType, t.bulkTypeTitle, (d: ReturnType<typeof within>) => fireEvent.click(d.getByRole("radio", { name: "Rechnung" }))],
    ] as const) {
      fireEvent.click(bar().getByRole("button", { name: button }));
      let d = within(await dialog(title));
      expect(d.getByRole("button", { name: t.apply }).hasAttribute("disabled")).toBe(true);
      // Something chosen, Apply enabled: Cancel still sends nothing.
      choose(d);
      expect(d.getByRole("button", { name: t.apply }).hasAttribute("disabled")).toBe(false);
      fireEvent.click(d.getByRole("button", { name: t.cancel }));
      await gone(title);
      fireEvent.click(bar().getByRole("button", { name: button }));
      d = within(await dialog(title));
      choose(d);
      fireEvent.keyDown(d.getByRole("button", { name: t.cancel }), { key: "Escape" });
      await gone(title);
    }
    expect(posts()).toEqual([]);
  });
});

describe("the list and the bar in German", () => {
  afterEach(() => {
    vi.resetModules();
  });

  it("names the search, the chips, the sort, the rows, the bar and its modals in German, nothing in English", async () => {
    vi.resetModules();
    vi.stubGlobal("navigator", { ...navigator, languages: ["de-DE", "en-US"], language: "de-DE" });
    const { App: GermanApp } = await import("./App");
    const { messagesFor } = await import("./i18n");
    const de = messagesFor("de");
    ({ server, requests } = stubServer());
    server.tags = [tag("t1", "Steuer"), tag("t2", "2024", "t1")];
    server.documents = () => page([doc("1", "Rechnung", { tagIds: ["t1"], isInbox: true, isFavorite: true, processing: { state: "queued", attempts: 0, maxAttempts: 3 } })]);
    window.history.replaceState(null, "", "/?tag=t1");
    render(<GermanApp />);
    await screen.findByText("Rechnung");
    await waitFor(() => expect(screen.queryByText(de.loading)).toBeNull());
    // The search field and its clear button, the filter chip, the sort and its direction.
    expect(screen.getByRole("searchbox", { name: de.searchLabel })).toBeTruthy();
    expect(screen.getByRole("button", { name: de.carbon.clearSearch })).toBeTruthy();
    expect(screen.getByRole("button", { name: de.removeFilter(de.tagFilter("Steuer")) })).toBeTruthy();
    expect(screen.getByRole("combobox", { name: de.sortBy })).toBeTruthy();
    expect(screen.getByRole("button", { name: de.sortNewestFirst })).toBeTruthy();
    // The row: its checkbox, flags, badge, actions and tag chip.
    const row = screen.getByText("Rechnung").closest("li")!;
    expect(within(row).getByRole("checkbox", { name: de.selectDocument("Rechnung") })).toBeTruthy();
    expect(within(row).getByText(de.inInbox)).toBeTruthy();
    expect(within(row).getByText(de.favorite)).toBeTruthy();
    expect(within(row).getByRole("status").textContent).toBe(de.processingQueued);
    expect(within(row).getByRole("link", { name: de.download })).toBeTruthy();
    expect(within(row).getByRole("button", { name: de.deleteDocumentLabel("Rechnung") })).toBeTruthy();
    expect(within(within(row).getByRole("list", { name: de.tags })).getAllByRole("listitem").map((c) => c.textContent)).toEqual(["Steuer"]);
    // The selection and the bar with its count.
    fireEvent.click(screen.getByRole("checkbox", { name: de.selectAllLoaded }));
    const bar = within(screen.getByRole("region", { name: de.bulkActions }));
    expect(bar.getByText(de.selectedCount(1))).toBeTruthy();
    expect(screen.getByRole("button", { name: de.clearSelection })).toBeTruthy();
    // A modal of the bar: Carbon's buttons in German.
    fireEvent.click(bar.getByRole("button", { name: de.bulkTags }));
    const ask = within(await screen.findByRole("dialog", { name: de.bulkTagsTitle }));
    expect(ask.getByRole("group", { name: de.bulkAddTags })).toBeTruthy();
    expect(ask.getByRole("button", { name: de.apply })).toBeTruthy();
    expect(ask.getByRole("button", { name: de.cancel })).toBeTruthy();
    expect(ask.getByRole("button", { name: de.carbon.closeModal })).toBeTruthy();
    const html = document.body.innerHTML;
    for (const s of ["Cancel", ">Close<", "Select all rows", "Dismiss", "Clear search input", "Clear filter", "Focus sentinel", "items selected", ">Apply<", "Load more", "Download"]) {
      expect(html, s).not.toContain(s);
    }
  });
});

describe("the focus of the bar's modals (#190 review round 1)", () => {
  const dialog = (name: string) => screen.findByRole("dialog", { name });
  const inside = (name: string) => screen.getByRole("dialog", { name }).contains(document.activeElement);

  it("moves the focus into the tag, correspondent and type forms when they open: the first choice, or Close without tags", async () => {
    server.documents = () => page([doc("1", "Doc 1")]);
    server.tags = [tag("t1", "Steuer"), tag("t2", "2024", "t1")];
    server.correspondents = [named("c1", "Finanzamt")];
    server.types = [named("d1", "Rechnung")];
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectDocument("Doc 1") }));
    const tags = () => bar().getByRole("button", { name: t.bulkTags });
    tags().focus();
    fireEvent.click(tags());
    let d = within(await dialog(t.bulkTagsTitle));
    // The first checkbox of "Add" (Apply is disabled and could take no focus).
    expect(document.activeElement).toBe(within(d.getAllByRole("group")[0]!).getByRole("checkbox", { name: "Steuer" }));
    expect(inside(t.bulkTagsTitle)).toBe(true);
    fireEvent.click(d.getByRole("button", { name: t.cancel }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: t.bulkTagsTitle })).toBeNull());
    expect(document.activeElement).toBe(tags());

    fireEvent.click(bar().getByRole("button", { name: t.bulkCorrespondent }));
    d = within(await dialog(t.bulkCorrespondentTitle));
    expect(document.activeElement).toBe(d.getByRole("radio", { name: t.bulkClearCorrespondent }));
    fireEvent.keyDown(d.getByRole("radio", { name: t.bulkClearCorrespondent }), { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: t.bulkCorrespondentTitle })).toBeNull());

    fireEvent.click(bar().getByRole("button", { name: t.bulkType }));
    d = within(await dialog(t.bulkTypeTitle));
    expect(document.activeElement).toBe(d.getByRole("radio", { name: t.bulkClearType }));
    fireEvent.click(d.getByRole("button", { name: t.cancel }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: t.bulkTypeTitle })).toBeNull());
    expect(requests.filter((r) => r.method !== "GET")).toHaveLength(0);
  });

  it("moves the focus to Close when the tag form has no tags to offer", async () => {
    server.documents = () => page([doc("1", "Doc 1")]);
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectDocument("Doc 1") }));
    fireEvent.click(bar().getByRole("button", { name: t.bulkTags }));
    const d = within(await dialog(t.bulkTagsTitle));
    expect(d.getByText(t.bulkNoTags)).toBeTruthy();
    expect(document.activeElement).toBe(d.getByRole("button", { name: t.carbon.closeModal }));
    expect(inside(t.bulkTagsTitle)).toBe(true);
  });

  it("opens the every-match restore question with the focus on Cancel, and returns it to the button that asked, which was disabled meanwhile", async () => {
    const trashed = { deletedAt: "2026-10-01T08:00:00Z", purgeAt: "2026-10-31T08:00:00Z" };
    server.documents = (p) => (p.get("cursor") ? page([doc("3", "Doc 3", trashed)]) : page([doc("1", "Doc 1", trashed), doc("2", "Doc 2", trashed)], "c1"));
    const counted = { body: { matched: 3, owners: 1, limit: 5000, fingerprint: "v1.fp" } };
    let hold: ReturnType<typeof held> | null = null;
    server.bulk = (b) => (b.dryRun ? (hold ? hold.promise : counted) : { body: { updated: 3 } });
    window.history.replaceState(null, "", "/?trash=1");
    await start();
    fireEvent.click(await screen.findByRole("checkbox", { name: t.selectAllLoaded }));
    fireEvent.click(screen.getByRole("button", { name: t.selectAllMatches }));
    await screen.findByText(t.allMatchesSelected(3));
    const restore = () => bar().getByRole("button", { name: t.restore });
    hold = held();
    restore().focus();
    fireEvent.click(restore());
    act(() => dropFocus());
    await act(async () => hold!.release(counted));
    hold = null;
    const ask = within(await dialog(t.bulkRestoreTitle));
    expect(document.activeElement).toBe(ask.getByRole("button", { name: t.cancel }));
    fireEvent.keyDown(ask.getByRole("button", { name: t.cancel }), { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: t.bulkRestoreTitle })).toBeNull());
    expect(document.activeElement).toBe(restore());
    expect(requests.filter((r) => r.method !== "GET" && !(r.body && r.body.dryRun === true))).toHaveLength(0);
  });
});
