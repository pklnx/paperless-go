// Checks the built docs site (.vitepress/dist) after `vitepress build`: the
// pklnx theme's fonts are in it and Inter is not, the licences of its fonts
// and icons ship with it and are linked, and the logo and icon files it
// names exist. Also checks that no Carbon or IBM package entered the
// lockfile (the theme's @carbon/styles peer is optional). No network.
import { createHash } from "node:crypto";
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const docs = fileURLToPath(new URL("..", import.meta.url));
const dist = join(docs, ".vitepress/dist");
const base = "/paperless-go/";
const designSystem = dirname(createRequire(join(docs, "package.json")).resolve("@pklnx/design-system/package.json"));
// The same files as licenseFiles in .vitepress/config.mts.
const licenses = {
  "IBM-Plex-OFL.txt": "project/fonts/OFL.txt",
  "pklnx-design-system-notices.txt": "THIRD_PARTY_NOTICES.md",
};

const errors = [];
const fail = (msg) => errors.push(msg);
const sha256 = (data) => createHash("sha256").update(data).digest("hex");

// Licences: copied unchanged from the package; the notices hold Apache-2.0.
for (const [name, source] of Object.entries(licenses)) {
  const file = join(dist, "licenses", name);
  if (!existsSync(file)) {
    fail(`licenses/${name} is missing`);
    continue;
  }
  if (sha256(readFileSync(file)) !== sha256(readFileSync(join(designSystem, source)))) {
    fail(`licenses/${name} differs from the package's ${source}`);
  }
}
const notices = join(dist, "licenses/pklnx-design-system-notices.txt");
if (existsSync(notices) && !readFileSync(notices, "utf8").includes("Apache License")) {
  fail("licenses/pklnx-design-system-notices.txt lacks the Apache License");
}

// Fonts: IBM Plex from the theme, no Inter from the default theme.
const assets = existsSync(join(dist, "assets")) ? readdirSync(join(dist, "assets")) : [];
const plex = assets.some((name) => {
  if (!name.endsWith(".css")) return false;
  const css = readFileSync(join(dist, "assets", name), "utf8");
  return [...css.matchAll(/@font-face\s*{[^}]*}/g)].some((face) =>
    [...face[0].matchAll(/url\(["']?([^"')]+\.woff2)["']?\)/g)].some((m) => {
      const url = m[1];
      return url.startsWith(`${base}assets/`) && /IBMPlex/.test(url) && assets.includes(url.slice(`${base}assets/`.length));
    }),
  );
});
if (!plex) fail("no built CSS has an @font-face with an IBM Plex woff2 under assets/");
for (const name of assets) {
  if (/inter/i.test(name) && /\.(woff2?|ttf|otf)$/.test(name)) fail(`Inter font emitted: assets/${name}`);
}

// index.html: theme colours per scheme, the tile icon, the licence links,
// and every logo or icon file it names exists.
const indexFile = join(dist, "index.html");
const index = existsSync(indexFile) ? readFileSync(indexFile, "utf8") : "";
if (!index) fail("index.html is missing");
for (const [color, scheme] of [
  ["#ffffff", "light"],
  ["#161616", "dark"],
]) {
  const meta = [...index.matchAll(/<meta\b[^>]*>/g)].map((m) => m[0]);
  if (!meta.some((m) => m.includes('name="theme-color"') && m.includes(`content="${color}"`) && m.includes(`media="(prefers-color-scheme: ${scheme})"`))) {
    fail(`index.html lacks the ${scheme} theme-color ${color}`);
  }
}
if (!/<link\b[^>]*rel="icon"[^>]*href="\/paperless-go\/paperless-go-tile\.svg"/.test(index)) fail("index.html lacks the tile icon link");
for (const name of Object.keys(licenses)) {
  if (!index.includes(`href="${base}licenses/${name}"`)) fail(`index.html lacks the footer link to licenses/${name}`);
}
const named = new Set([...index.matchAll(/\/paperless-go\/(paperless-go-[\w-]+\.(?:svg|png))/g)].map((m) => m[1]));
for (const want of ["paperless-go-tile.svg", "paperless-go-favicon-32.png", "paperless-go-mark-light.svg", "paperless-go-mark-dark.svg"]) {
  if (!named.has(want)) fail(`index.html does not name ${want}`);
}
for (const name of named) {
  if (!existsSync(join(dist, name))) fail(`${name} is named in index.html but missing from the build`);
}

// Lockfile: no Carbon or IBM package.
const lock = readFileSync(join(docs, "pnpm-lock.yaml"), "utf8");
for (const m of lock.matchAll(/^ {2}'?(@(?:carbon|ibm)\/[^@'\s]+)/gm)) fail(`pnpm-lock.yaml has the package ${m[1]}`);

if (errors.length > 0) {
  for (const e of errors) console.error(`check-dist: ${e}`);
  process.exit(1);
}
console.log("check-dist: ok");
