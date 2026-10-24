// Which key presses belong to a component, so the app's key handling leaves them alone.
import { afterEach, describe, expect, it } from "vitest";
import { dialogOpen, ownedByWidget } from "./dialogs";

afterEach(() => {
  document.body.innerHTML = "";
});

/** A key press on the element that matches selector in html. */
function press(html: string, selector: string, key: string): boolean {
  document.body.innerHTML = html;
  const target = document.querySelector(selector)!;
  const e = new KeyboardEvent("keydown", { key, bubbles: true });
  Object.defineProperty(e, "target", { value: target });
  return ownedByWidget(e);
}

describe("ownedByWidget", () => {
  it("leaves plain buttons, links and tree nodes to the app", () => {
    for (const key of ["j", "k", "?", "/", "Escape"]) {
      expect(press('<button id="b">x</button>', "#b", key), key).toBe(false);
      expect(press('<a id="a" href="/">x</a>', "#a", key), key).toBe(false);
      expect(press('<ul role="tree"><li role="treeitem" id="n">Finance</li></ul>', "#n", key), key).toBe(false);
      // A tooltip's trigger (closed tooltip) is a plain button too.
      expect(press('<span class="cds--popover-container"><button id="t">x</button><span class="cds--popover"><span class="cds--popover-content">hint</span></span></span>', "#t", key), key).toBe(false);
    }
  });

  it("gives every key to fields, Carbon's Search included", () => {
    for (const key of ["j", "?", "/", "Escape"]) {
      expect(press('<div class="cds--search"><input id="s" type="search"></div>', "#s", key), key).toBe(true);
      expect(press('<textarea id="t"></textarea>', "#t", key), key).toBe(true);
      expect(press('<select id="s"><option>a</option></select>', "#s", key), key).toBe(true);
      expect(press('<div id="c" contenteditable="true">x</div>', "#c", key), key).toBe(true);
    }
  });

  it("gives every key to open menus, list boxes, popover content and dialogs", () => {
    for (const key of ["j", "k", "u", "Escape"]) {
      expect(press('<ul role="menu"><li><button role="menuitem" id="m">Copy</button></li></ul>', "#m", key), key).toBe(true);
      expect(press('<div role="listbox"><div role="option" id="o">a</div></div>', "#o", key), key).toBe(true);
      expect(press('<span class="cds--popover--open"><span class="cds--popover-content"><button id="p">x</button></span></span>', "#p", key), key).toBe(true);
      expect(press('<div class="cds--modal is-visible"><div role="dialog"><button id="d">x</button></div></div>', "#d", key), key).toBe(true);
    }
  });

  it("gives Escape to an open tooltip anywhere and to the SideNav, other keys not", () => {
    const tooltip = '<span class="cds--popover-container cds--popover--open"><button id="t">x</button></span><button id="b">y</button>';
    expect(press(tooltip, "#b", "Escape")).toBe(true);
    expect(press(tooltip, "#t", "Escape")).toBe(true);
    expect(press(tooltip, "#t", "j")).toBe(false);
    const nav = '<nav class="cds--side-nav"><a id="l" href="/">Log</a></nav>';
    expect(press(nav, "#l", "Escape")).toBe(true);
    expect(press(nav, "#l", "j")).toBe(false);
  });
});

describe("dialogOpen", () => {
  it("sees a visible Carbon Modal and an open native dialog, not a closed one", () => {
    document.body.innerHTML = '<div class="cds--modal"></div><dialog class="sheet"></dialog>';
    expect(dialogOpen()).toBe(false);
    document.body.innerHTML = '<div class="cds--modal is-visible"></div>';
    expect(dialogOpen()).toBe(true);
    document.body.innerHTML = '<dialog class="sheet" open></dialog>';
    expect(dialogOpen()).toBe(true);
  });

  it("sees the SideNav overlay and the column overlay, not the closed ones or the rail expanded on hover", () => {
    // Below lg: Carbon's SideNav closed, then open.
    document.body.innerHTML = '<nav class="cds--side-nav cds--side-nav__navigation"></nav><aside class="nav-column"></aside>';
    expect(dialogOpen()).toBe(false);
    document.body.innerHTML = '<nav class="cds--side-nav cds--side-nav__navigation cds--side-nav--expanded"></nav>';
    expect(dialogOpen()).toBe(true);
    // From lg: the rail, expanded by hover or focus, is no overlay.
    document.body.innerHTML = '<nav class="cds--side-nav cds--side-nav--rail cds--side-nav--expanded"></nav>';
    expect(dialogOpen()).toBe(false);
    // The column as an overlay (App), not as the page's column.
    document.body.innerHTML = '<aside class="nav-column nav-column--overlay" role="dialog"></aside>';
    expect(dialogOpen()).toBe(true);
  });
});
