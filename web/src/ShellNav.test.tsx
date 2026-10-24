import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { t } from "./i18n";
import { docsURL, ShellNav } from "./ShellNav";
import { parseState, type ViewState } from "./urlState";

const view = (search = ""): ViewState => parseState(search);
const counts = { total: 42, inbox: 3, favorites: 2, trash: 1 };

function nav(props: Partial<Parameters<typeof ShellNav>[0]> = {}) {
  const go = vi.fn();
  const onClose = vi.fn();
  render(<ShellNav id="nav" state={view()} admin={false} counts={null} rail expanded={false} onClose={onClose} go={go} {...props} />);
  return { go, onClose, nav: screen.getByRole("navigation", { name: t.navigation }) };
}

afterEach(cleanup);

const names = (el: HTMLElement) => within(el).getAllByRole("link").map((l) => l.textContent);

describe("ShellNav", () => {
  it("lists the documents, the pages and the documentation; Users only for admins", () => {
    const user = nav().nav;
    expect(names(user)).toEqual([t.allDocuments, t.inbox, t.favorites, t.trash, t.manageMetadata, t.log, t.profile, t.documentation]);
    cleanup();
    const admin = nav({ admin: true }).nav;
    expect(names(admin)).toEqual([t.allDocuments, t.inbox, t.favorites, t.trash, t.manageMetadata, t.log, t.users, t.profile, t.documentation]);
    expect(within(admin).getByRole("link", { name: t.documentation }).getAttribute("href")).toBe(docsURL);
  });

  it("shows each list's count once the counts are there", () => {
    const { nav: el } = nav({ counts });
    for (const [label, n] of [
      [t.allDocuments, 42],
      [t.inbox, 3],
      [t.favorites, 2],
      [t.trash, 1],
    ] as const) {
      const link = within(el).getByRole("link", { name: `${label} ${n}` });
      expect(link.querySelector(".shell-nav__count")?.textContent).toBe(String(n));
    }
    cleanup();
    // Without counts the names stand alone.
    expect(within(nav().nav).getByRole("link", { name: t.allDocuments }).textContent).toBe(t.allDocuments);
  });

  it("is Carbon's rail from lg up and an overlay below", () => {
    const rail = nav().nav;
    expect(rail.className).toContain("cds--side-nav--rail");
    cleanup();
    const closed = nav({ rail: false }).nav;
    expect(closed.className).not.toContain("cds--side-nav--rail");
    expect(closed.className).not.toContain("cds--side-nav--expanded");
    // Carbon keeps a closed overlay out of the tab order.
    expect(closed.hasAttribute("inert")).toBe(true);
    cleanup();
    const open = nav({ rail: false, expanded: true }).nav;
    expect(open.className).toContain("cds--side-nav--expanded");
    expect(open.hasAttribute("inert")).toBe(false);
  });

  it("closes the overlay with a click on Carbon's scrim", () => {
    const { onClose } = nav({ rail: false, expanded: true });
    fireEvent.click(document.querySelector(".cds--side-nav__overlay-active")!);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("links each place with a real href and goes there on a plain click", () => {
    const { go, onClose, nav: el } = nav({ admin: true, state: view("?q=tax&sort=date&view=log") });
    const inbox = within(el).getByRole("link", { name: t.inbox });
    // A list keeps the search and the sort; All documents clears the search too.
    expect(inbox.getAttribute("href")).toBe("?q=tax&inbox=1&sort=date");
    expect(within(el).getByRole("link", { name: t.allDocuments }).getAttribute("href")).toBe("?sort=date");
    expect(within(el).getByRole("link", { name: t.users }).getAttribute("href")).toBe("?q=tax&sort=date&view=users");
    // A plain click stays in the app.
    expect(fireEvent.click(inbox)).toBe(false);
    expect(go).toHaveBeenCalledWith({ inbox: "1", fav: "", trash: "", tag: "", corr: "", type: "", view: "", doc: "", sv: "" });
    expect(onClose).toHaveBeenCalledTimes(1);
    fireEvent.click(within(el).getByRole("link", { name: t.allDocuments }));
    expect(go).toHaveBeenLastCalledWith({ inbox: "", fav: "", trash: "", tag: "", corr: "", type: "", q: "", view: "", doc: "", sv: "" });
    fireEvent.click(within(el).getByRole("link", { name: t.users }));
    expect(go).toHaveBeenLastCalledWith({ view: "users", doc: "", sv: "" });
    // Ctrl-click is left to the browser (a new tab).
    expect(fireEvent.click(inbox, { ctrlKey: true })).toBe(true);
    expect(go).toHaveBeenCalledTimes(3);
    cleanup();
    expect(within(nav().nav).getByRole("link", { name: t.allDocuments }).getAttribute("href")).toBe("/");
  });

  it("marks the current list or page", () => {
    const { nav: el } = nav({ state: view("?inbox=1&tag=t1") });
    // The first filter in the sidebar's order is the one marked.
    expect(within(el).getByRole("link", { name: t.inbox }).getAttribute("aria-current")).toBe("page");
    expect(within(el).getByRole("link", { name: t.allDocuments }).getAttribute("aria-current")).toBeNull();
    cleanup();
    const page = nav({ state: view("?view=log&inbox=1") }).nav;
    expect(within(page).getByRole("link", { name: t.log }).getAttribute("aria-current")).toBe("page");
    // On a page no list is current, whatever the URL still holds.
    expect(within(page).getByRole("link", { name: t.inbox }).getAttribute("aria-current")).toBeNull();
    cleanup();
    const all = nav().nav;
    expect(within(all).getByRole("link", { name: t.allDocuments }).getAttribute("aria-current")).toBe("page");
  });
});
