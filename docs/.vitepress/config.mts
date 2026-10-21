import { defineConfig } from "vitepress";

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
    ["link", { rel: "icon", type: "image/svg+xml", href: "/paperless-go/logo.svg" }],
    ["meta", { name: "theme-color", content: "#2563eb" }],
  ],
  themeConfig: {
    logo: "/logo.svg",
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
          { text: "Audit log", link: "/guide/audit-log" },
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
    },
  },
});
