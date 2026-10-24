// The pklnx design system theme in the built app (#150, part 1): Purple for
// the interactive colour in both themes, and in dark mode Carbon G100's
// white focus and tertiary colours; the bundled IBM Plex is the font that
// renders. The login page has the only Carbon primary button of part 1.
import type { Page } from "@playwright/test";
import { expect, test } from "./fixtures";
import { logInButton } from "./ui";

// No session: the login page.
test.use({ storageState: { cookies: [], origins: [] } });
// Without a session, GET /api/session answers 401 by design.
test.beforeEach(({ guards }) => guards.allowHTTPError("/api/session", 401));

const purple = "rgb(138, 63, 252)"; // Purple 60, #8a3ffc

/** The tokens' values as the browser resolves them, as rgb(…) colours. */
function resolved(page: Page, tokens: string[]): Promise<Record<string, string>> {
  return page.evaluate((names) => {
    const probe = document.createElement("div");
    document.body.append(probe);
    const out: Record<string, string> = {};
    for (const name of names) {
      probe.style.color = `var(${name})`;
      out[name] = getComputedStyle(probe).color;
    }
    probe.remove();
    return out;
  }, tokens);
}

const tokens = ["--cds-focus", "--cds-icon-interactive", "--cds-button-tertiary", "--cds-button-tertiary-hover", "--cds-button-tertiary-active"];

const expected = {
  // The pklnx light theme: Purple 60, its hover and Purple 80.
  light: {
    "--cds-focus": purple,
    "--cds-icon-interactive": purple,
    "--cds-button-tertiary": purple,
    "--cds-button-tertiary-hover": "rgb(120, 34, 251)",
    "--cds-button-tertiary-active": "rgb(73, 29, 139)",
  },
  // Carbon G100: white, Gray 10 and Gray 30.
  dark: {
    "--cds-focus": "rgb(255, 255, 255)",
    "--cds-icon-interactive": "rgb(255, 255, 255)",
    "--cds-button-tertiary": "rgb(255, 255, 255)",
    "--cds-button-tertiary-hover": "rgb(244, 244, 244)",
    "--cds-button-tertiary-active": "rgb(198, 198, 198)",
  },
};

test("primary buttons are Purple in both themes; focus is Purple in light and white in dark", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "light" });
  await page.goto("/");
  const logIn = logInButton(page);
  const html = page.locator("html");

  await expect(html).toHaveClass(/(^|\s)cds--white(\s|$)/);
  await expect(logIn).toHaveCSS("background-color", purple);
  expect(await resolved(page, tokens)).toEqual(expected.light);
  // Carbon's type scale applies: heading-04 for the app name (28px), not body text.
  await expect(page.getByRole("heading", { name: "paperless-go", level: 1 })).toHaveCSS("font-size", "28px");

  // theme.ts follows the system: the class on <html> is the signal.
  await page.emulateMedia({ colorScheme: "dark" });
  await expect(html).toHaveClass(/(^|\s)cds--g100(\s|$)/);
  await expect(html).not.toHaveClass(/cds--white/);
  await expect(logIn).toHaveCSS("background-color", purple);
  expect(await resolved(page, tokens)).toEqual(expected.dark);
});

test("IBM Plex Sans is loaded from the bundle", async ({ page }) => {
  await page.goto("/");
  await expect(logInButton(page)).toBeVisible();
  // A loaded FontFace, not getComputedStyle: Carbon's own rule names the
  // family whether or not a font file is there. The foreign-request guard
  // of the fixture proves that the file came from the same origin.
  const loaded = await page.evaluate(async () => {
    await document.fonts.ready;
    return [...document.fonts].filter((f) => f.family.replace(/"/g, "") === "IBM Plex Sans" && f.status === "loaded").length;
  });
  expect(loaded).toBeGreaterThan(0);
});
