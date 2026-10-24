import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { defineConfig, type Plugin } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { carbonStyles } from "./build/stripRemoteFonts.ts";

// pdf.js (the PDF viewer of the document detail) needs data files at run
// time: the character maps (cmaps), the 14 standard fonts, and the
// JavaScript image decoders for JPEG 2000 and JBIG2 (the viewer turns
// WebAssembly off, so the CSP needs no 'wasm-unsafe-eval'). They are served
// from the server's own /assets/, under a folder named by the pdf.js
// version: /assets/ is cached as immutable, so a new version needs new URLs.
const pdfjsDir = fileURLToPath(new URL("node_modules/pdfjs-dist/", import.meta.url));
const pdfjsVersion = (JSON.parse(readFileSync(`${pdfjsDir}package.json`, "utf8")) as { version: string }).version;
const pdfjsBase = `assets/pdfjs-${pdfjsVersion}/`;

/** The data files of pdf.js, as paths below node_modules/pdfjs-dist. */
function pdfjsFiles(): string[] {
  const dir = (d: string) => readdirSync(pdfjsDir + d).map((f) => `${d}/${f}`);
  return [
    ...dir("cmaps"),
    ...dir("standard_fonts"),
    "wasm/openjpeg_nowasm_fallback.js",
    "wasm/jbig2_nowasm_fallback.js",
    "wasm/LICENSE_OPENJPEG",
    "wasm/LICENSE_PDFJS_OPENJPEG",
    "wasm/LICENSE_JBIG2",
    "wasm/LICENSE_PDFJS_JBIG2",
  ];
}

/** Puts pdf.js's data files into the build, and serves them in `pnpm dev`. */
function pdfjsAssets(): Plugin {
  return {
    name: "pdfjs-assets",
    generateBundle() {
      for (const f of pdfjsFiles()) this.emitFile({ type: "asset", fileName: pdfjsBase + f, source: readFileSync(pdfjsDir + f) });
    },
    configureServer(server) {
      const files = new Set(pdfjsFiles());
      server.middlewares.use(`/${pdfjsBase}`, (req, res, next) => {
        const f = decodeURIComponent((req.url ?? "").split("?")[0]!.replace(/^\//, ""));
        // Only the listed files: nothing else of node_modules is reachable.
        if (!files.has(f)) return next();
        if (f.endsWith(".js")) res.setHeader("Content-Type", "text/javascript");
        res.end(readFileSync(pdfjsDir + f));
      });
    },
  };
}

// The build is embedded into the Go binary (internal/web/ui).
export default defineConfig({
  // carbonStyles writes src/vendor/carbon.css (Carbon without its CDN fonts)
  // before Vite inlines it into index.css.
  plugins: [carbonStyles(), react(), tailwindcss(), pdfjsAssets()],
  define: {
    __PDFJS_ASSETS__: JSON.stringify(`/${pdfjsBase}`),
  },
  build: {
    outDir: "../internal/web/ui/dist",
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    // `pnpm dev` talks to `paperless-go serve` on :8080.
    proxy: { "/api": "http://localhost:8080", "/healthz": "http://localhost:8080" },
  },
  test: {
    // The unit tests only: web/e2e/*.spec.ts are Playwright's (pnpm e2e).
    // scripts/ holds the tests of the build checks (check-dist, the
    // telemetry block), build/ the test of the Carbon font strip.
    include: ["src/**/*.test.{ts,tsx}", "scripts/**/*.test.ts", "build/**/*.test.ts"],
    environment: "jsdom",
    testTimeout: 5_000,
    setupFiles: ["src/testSetup.ts"],
  },
});
