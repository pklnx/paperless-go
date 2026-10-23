// PROOF P3 (temporary, reverted before review): a failing assertion turns
// web-e2e red and uploads the traces artifact.
import { expect, test } from "./fixtures";

test("PROOF P3: a deliberately failing assertion", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "PROOF P3: this heading does not exist" })).toBeVisible();
});
