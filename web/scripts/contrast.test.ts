// @vitest-environment node
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// The text on the document list's rows must keep WCAG AA contrast (4.5:1)
// against the row's background in both themes: the title link and the
// ghost buttons use --cds-link-primary, which on Carbon's darker selected
// layer falls to 3.79:1 in the light theme (#190 review). The pair is read
// from layout.css and the theme files, so a changed token or background
// fails here instead of in a reviewer's browser. Under scripts/ for the Node types (tsconfig.node.json), like telemetryBlock.test.ts.

const here = dirname(fileURLToPath(import.meta.url));
const require = createRequire(import.meta.url);

/** The custom properties the first CSS block sets whose selector list holds selector (".cds--white" also in ":root, .cds--white { … }"). */
function tokens(css: string, selector: string): Map<string, string> {
  const m = new RegExp(`^[^{}]*(?:^|[,\\s])${selector.replace(/[.-]/g, "\\$&")}(?:[,\\s][^{}]*)?\\{`, "m").exec(css);
  if (!m) throw new Error(`no block ${selector}`);
  const start = m.index + m[0].length;
  const block = css.slice(start, css.indexOf("}", start));
  const out = new Map<string, string>();
  for (const m of block.matchAll(/(--cds-[a-z0-9-]+):\s*([^;]+);/g)) out.set(m[1]!, m[2]!.trim());
  return out;
}

/** The theme's tokens for a class, Carbon's values under the pklnx theme's. */
function theme(cls: ".cds--white" | ".cds--g100"): Map<string, string> {
  const carbon = readFileSync(require.resolve("@carbon/styles/css/styles.css"), "utf8");
  const pklnx = readFileSync(require.resolve("@pklnx/design-system/theme/pklnx-carbon.css"), "utf8");
  return new Map([...tokens(carbon, cls), ...tokens(pklnx, cls)]);
}

/** The background token a layout.css rule sets, following var() through the theme. */
function background(css: string, selector: string, t: Map<string, string>): string {
  const start = css.indexOf(`${selector} {`);
  if (start < 0) throw new Error(`no rule ${selector}`);
  const block = css.slice(start, css.indexOf("}", start));
  const m = /background-color:\s*var\((--cds-[a-z0-9-]+)\)/.exec(block);
  if (!m) throw new Error(`${selector} sets no token background`);
  const v = t.get(m[1]!);
  if (!v) throw new Error(`theme has no ${m[1]}`);
  return v;
}

/** WCAG 2 contrast ratio of two #rrggbb colours. */
export function contrast(a: string, b: string): number {
  const lum = (hex: string) => {
    const m = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(hex.trim());
    if (!m) throw new Error(`not a colour: ${hex}`);
    const f = (h: string) => {
      const c = parseInt(h, 16) / 255;
      return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
    };
    return 0.2126 * f(m[1]!) + 0.7152 * f(m[2]!) + 0.0722 * f(m[3]!);
  };
  const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x) as [number, number];
  return (hi + 0.05) / (lo + 0.05);
};

describe("contrast of the document rows", () => {
  const layout = readFileSync(join(here, "..", "src", "layout.css"), "utf8");

  it("computes WCAG ratios", () => {
    expect(contrast("#000000", "#ffffff")).toBeCloseTo(21, 1);
    expect(contrast("#8a3ffc", "#e0e0e0")).toBeCloseTo(3.79, 2);
  });

  it.each([".cds--white", ".cds--g100"] as const)("keeps the link colour at 4.5:1 or better on a row, checked or not, in %s", (cls) => {
    const t = theme(cls);
    const link = t.get("--cds-link-primary")!;
    expect(link).toBeTruthy();
    const row = background(layout, ".doc-row", t);
    expect(contrast(link, row)).toBeGreaterThanOrEqual(4.5);
    // A checked row keeps the row's background (it sets none of its own), so the same pair holds.
    const checked = layout.slice(layout.indexOf(".doc-row--checked {"));
    expect(checked.slice(0, checked.indexOf("}"))).not.toMatch(/background-color/);
    // The selected layer, which the checked row must not use, fails in the light theme: the reason for the rule above.
    if (cls === ".cds--white") expect(contrast(link, t.get("--cds-layer-selected-01")!)).toBeLessThan(4.5);
  });
});
