// Self-checks of the guards in fixtures.ts: each of these tests breaks one
// rule and must fail, through the guard's check when the test ends.
// test.fail() turns that expected failure into a pass; a guard that let
// the problem through would make the test pass and so fail here. So
// nothing else in these tests may fail: no assertions in their bodies, and
// every wait is for something the page does whether or not a guard works.
import { port } from "./demo";
import { test } from "./fixtures";

test("a console error fails the test", async ({ page }) => {
  test.fail();
  await page.goto("/");
  const logged = page.waitForEvent("console", (m) => m.text() === "guards self-check: console error");
  await page.evaluate(() => console.error("guards self-check: console error"));
  await logged;
});

test("a request to another host is aborted and fails the test", async ({ page }) => {
  test.fail();
  // Another origin that the demo serves itself, so nothing leaves the
  // machine even when the guard is broken: 127.0.0.1 instead of localhost
  // (the server's Host check accepts both). A navigation, because the
  // page's CSP would stop a fetch before the guard sees it. Its error (the
  // guard aborts it) is no failure by itself.
  await page.goto(`http://127.0.0.1:${port}/healthz`).then(
    () => undefined,
    () => undefined,
  );
});

test("an inline script blocked by the CSP fails the test", async ({ page }) => {
  test.fail();
  await page.goto("/");
  // Waits in the page for the violation, or for the script having run
  // (a CSP that lets it run): either way the body goes on, and only the
  // guard decides.
  await page.evaluate(
    () =>
      new Promise<void>((resolve) => {
        document.addEventListener("securitypolicyviolation", () => resolve(), { once: true });
        window.addEventListener("guards-self-check-ran", () => resolve(), { once: true });
        const s = document.createElement("script");
        s.textContent = 'window.dispatchEvent(new Event("guards-self-check-ran"));';
        document.body.append(s);
      }),
  );
});
