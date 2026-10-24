// @vitest-environment node
// The brand assets (logo and app icons) are copies of pklnx/design-system
// files: brand-assets.json records each one's source and sha256. This test
// keeps the copies, their licence file and the files that use them in step.
// It reads the repository only; no network.
import { createHash } from "node:crypto";
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const root = fileURLToPath(new URL("../../", import.meta.url));
const read = (path: string) => readFileSync(join(root, path));
const text = (path: string) => read(path).toString("utf8");
const sha256 = (data: Buffer | string) => createHash("sha256").update(data).digest("hex");

interface BrandAssets {
  repository: string;
  commit: string;
  files: { path: string; source: string; sha256: string }[];
}
const brand = JSON.parse(text("brand-assets.json")) as BrandAssets;
const listed = brand.files.map((f) => f.path).sort();

const REDISTRIBUTION =
  "You may redistribute these files unchanged as part of unmodified copies of this project; modified versions must replace them.";

/** Width and height from a PNG's IHDR chunk. */
function pngSize(data: Buffer): string {
  expect(data.subarray(0, 8).toString("hex")).toBe("89504e470d0a1a0a");
  expect(data.subarray(12, 16).toString("latin1")).toBe("IHDR");
  return `${data.readUInt32BE(16)}x${data.readUInt32BE(20)}`;
}

describe("brand-assets.json", () => {
  it("names the design-system commit in full", () => {
    expect(brand.repository).toBe("https://github.com/pklnx/design-system");
    expect(brand.commit).toMatch(/^[0-9a-f]{40}$/);
  });

  it("matches each file's sha256", () => {
    expect(brand.files.length).toBeGreaterThan(0);
    for (const f of brand.files) {
      expect(existsSync(join(root, f.path)), f.path).toBe(true);
      expect(sha256(read(f.path)), f.path).toBe(f.sha256);
    }
  });

  it("lists exactly the brand files in the repository", () => {
    const found = [
      ...readdirSync(join(root, "web/src/brand")).map((n) => `web/src/brand/${n}`),
      ...readdirSync(join(root, "web/public"))
        .filter((n) => n.endsWith(".png"))
        .map((n) => `web/public/${n}`),
      ...readdirSync(join(root, "docs/public"), { withFileTypes: true })
        .filter((e) => e.isFile() && /\.(svg|png)$/.test(e.name))
        .map((e) => `docs/public/${e.name}`),
    ].sort();
    expect(listed).toEqual(found);
  });
});

describe("BRAND-ASSETS.md", () => {
  const md = text("BRAND-ASSETS.md");

  it("lists exactly the brand-assets.json files", () => {
    const section = md.split(/^## Files$/m)[1] ?? "";
    const paths = [...section.matchAll(/^- `([^`]+)`$/gm)].map((m) => m[1]).sort();
    expect(paths).toEqual(listed);
  });

  it("has the redistribution sentence and links LICENSE and README.md", () => {
    expect(md.replace(/\s+/g, " ")).toContain(REDISTRIBUTION);
    expect(md).toContain("](LICENSE)");
    expect(md).toContain("](README.md)");
  });

  it("is linked from README.md", () => {
    expect(text("README.md")).toContain("](BRAND-ASSETS.md)");
  });
});

describe("LICENSE", () => {
  // LICENSE stays the plain MIT License; the brand terms are in
  // BRAND-ASSETS.md. Line endings are normalised, so the checkout does not
  // matter. The line checks come first, so a failure names what differs.
  const license = text("LICENSE").replace(/\r\n?/g, "\n");
  const lines = license.split("\n");

  it("is the plain MIT License", () => {
    expect(lines[0]).toBe("MIT License");
    expect(lines[2]).toBe("Copyright (c) 2026 Patrick Klein");
    // The warranty paragraph ends the file, followed by exactly one newline.
    expect(lines.slice(-3)).toEqual(["OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE", "SOFTWARE.", ""]);
    // A deliberate change to LICENSE updates this pin in the same PR.
    expect(sha256(license)).toBe("3ab472d18a36afd1b4960db218d4c7d23f5b46b297f04017b8323b3290f05363");
  });
});

describe("web/public/manifest.webmanifest", () => {
  interface Icon {
    src: string;
    sizes: string;
    type: string;
    purpose: string;
  }
  const manifest = JSON.parse(text("web/public/manifest.webmanifest")) as {
    background_color: string;
    theme_color: string;
    icons: Icon[];
  };

  it("names icons in web/public with their real sizes", () => {
    expect(manifest.icons.length).toBeGreaterThan(0);
    for (const icon of manifest.icons) {
      expect(icon.src).toMatch(/^\/[^/]+\.png$/);
      expect(icon.type).toBe("image/png");
      expect(pngSize(read(`web/public${icon.src}`)), icon.src).toBe(icon.sizes);
    }
    expect(manifest.icons.filter((i) => i.purpose === "maskable")).toHaveLength(1);
  });

  it("uses white for the background and theme colour", () => {
    expect(manifest.background_color).toBe("#ffffff");
    expect(manifest.theme_color).toBe("#ffffff");
  });
});

describe("web/index.html", () => {
  const links = [...text("web/index.html").matchAll(/<link\b[^>]*>/g)].map((m) => m[0]);
  const link = (rel: string, href: string) => links.some((l) => l.includes(`rel="${rel}"`) && l.includes(`href="${href}"`));

  it("links the icons and the manifest", () => {
    expect(link("icon", "/favicon-32.png")).toBe(true);
    expect(link("icon", "/src/brand/paperless-go-tile.svg")).toBe(true);
    expect(link("manifest", "/manifest.webmanifest")).toBe(true);
    expect(link("apple-touch-icon", "/apple-touch-icon.png")).toBe(true);
  });
});
