// Self-checks of the guards in fixtures.ts: each of these tests breaks one
// rule and must fail, through the guard's check when the test ends.
// test.fail() turns that expected failure into a pass; a guard that let
// the problem through would make the test pass and so fail here.
import { expect, test } from "./fixtures";

test("a console error fails the test", async ({ page }) => {
  test.fail();
  await page.goto("/");
  const logged = page.waitForEvent("console", (m) => m.type() === "error");
  await page.evaluate(() => console.error("guards self-check: console error"));
  await logged;
});

test("a request to another host is aborted and fails the test", async ({ page }) => {
  test.fail();
  // A navigation: the page's CSP (connect-src 'self') would stop a fetch
  // before the guard sees it, but it does not apply to navigations.
  const error = await page.goto("https://example.com/guards-self-check").then(
    () => null,
    (e: unknown) => (e instanceof Error ? e.message : String(e)),
  );
  // The guard aborted it: the browser never reached the network.
  expect(error).toContain("ERR_BLOCKED_BY_CLIENT");
});

test("an inline script blocked by the CSP fails the test", async ({ page }) => {
  test.fail();
  await page.goto("/");
  const violation = page.waitForEvent("console", (m) => m.text().startsWith("CSP violation"));
  await page.evaluate(() => {
    const s = document.createElement("script");
    s.textContent = "window.guardsSelfCheck = true;";
    document.body.append(s);
  });
  await violation;
  expect(await page.evaluate(() => "guardsSelfCheck" in window)).toBe(false);
});
