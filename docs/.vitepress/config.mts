import { copyFileSync, mkdirSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { defineConfig } from "vitepress";

// The pklnx design system's package (the docs theme). THIRD_PARTY_NOTICES.md
// is in the package but not in its exports, so the files are found from its
// package.json.
const designSystem = dirname(createRequire(import.meta.url).resolve("@pklnx/design-system/package.json"));

/** The licence files the built site links to (fonts and icons of the theme); scripts/check-dist.mjs names them too. */
const licenseFiles = {
  "IBM-Plex-OFL.txt": "project/fonts/OFL.txt",
  "pklnx-design-system-notices.txt": "THIRD_PARTY_NOTICES.md",
};

export default defineConfig({
  title: "paperless-go",
  description: "A self-hosted document archive with OCR, full-text search and a web UI.",
  lang: "en",
  base: "/paperless-go/",
  cleanUrls: true,
  lastUpdated: true,
  // Fail the build on broken internal links; links to the local UI are fine.
  ignoreDeadLinks: "localhostLinks",
  head: [
    // Browsers take the last suitable icon: the SVG tile where supported,
    // the PNG as the fallback.
    ["link", { rel: "icon", type: "image/png", sizes: "32x32", href: "/paperless-go/paperless-go-favicon-32.png" }],
    ["link", { rel: "icon", type: "image/svg+xml", href: "/paperless-go/paperless-go-tile.svg" }],
    // `media` comes first: VitePress keys meta tags by their first attribute
    // other than content and keeps only the last of a key, so with `name`
    // first the dark one would replace the light one.
    ["meta", { media: "(prefers-color-scheme: light)", name: "theme-color", content: "#ffffff" }],
    ["meta", { media: "(prefers-color-scheme: dark)", name: "theme-color", content: "#161616" }],
  ],
  themeConfig: {
    // Decorative: the site title next to it is the name.
    logo: { light: "/paperless-go-mark-light.svg", dark: "/paperless-go-mark-dark.svg", alt: "" },
    nav: [
      { text: "Guide", link: "/guide/getting-started" },
      { text: "Reference", link: "/reference/configuration" },
      { text: "Development", link: "/development/" },
    ],
    sidebar: [
      {
        text: "Guide",
        items: [
          { text: "Getting started", link: "/guide/getting-started" },
          { text: "Backups and upgrades", link: "/guide/operations" },
          { text: "Importing files", link: "/guide/import" },
          { text: "Documents", link: "/guide/documents" },
          { text: "Search", link: "/guide/search" },
          { text: "Audit log", link: "/guide/audit-log" },
          { text: "Scanning with the iPhone", link: "/guide/iphone" },
          { text: "Running on the internet", link: "/guide/internet" },
        ],
      },
      {
        text: "Reference",
        items: [
          { text: "Configuration", link: "/reference/configuration" },
          { text: "Command line", link: "/reference/cli" },
          { text: "Security", link: "/reference/security" },
        ],
      },
      {
        text: "Development",
        items: [
          { text: "Contributing", link: "/development/" },
          { text: "Issues, plans and releases", link: "/development/process" },
          { text: "Architecture", link: "/development/architecture" },
          { text: "Roadmap", link: "/development/roadmap" },
          { text: "Flaky tests", link: "/development/flaky-tests" },
        ],
      },
    ],
    search: { provider: "local" },
    outline: { level: [2, 3] },
    footer: {
      message: "Entirely vibe-coded: written by an AI under human direction. Review before trusting it with important data.",
      copyright:
        'Fonts: IBM Plex, <a href="/paperless-go/licenses/IBM-Plex-OFL.txt">SIL Open Font License 1.1</a>. ' +
        'Icons: Carbon, <a href="/paperless-go/licenses/pklnx-design-system-notices.txt">Apache-2.0</a>.',
    },
  },
  // The theme's fonts and icons ship with their licences (scripts/check-dist.mjs checks them).
  buildEnd(site) {
    const dir = join(site.outDir, "licenses");
    mkdirSync(dir, { recursive: true });
    for (const [name, source] of Object.entries(licenseFiles)) copyFileSync(join(designSystem, source), join(dir, name));
  },
});
