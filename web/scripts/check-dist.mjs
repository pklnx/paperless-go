// `pnpm check:dist`: scans the built UI (internal/web/ui/dist) so that a
// new dependency or a Vite transform cannot quietly break the CSP, which
// allows only the server itself and no 'unsafe-eval'. A missing or
// incomplete build fails too, so an empty folder cannot pass. Fails on:
// - a missing main chunk, pdf.js worker or fallback decoder (required);
// - eval( or new Function( in any .js or .mjs below dist/assets (noEval.mjs);
// - "s81c.com" (IBM's font CDN, which Carbon's CSS points at) anywhere in
//   the build's .css, .js, .mjs and .html files;
// - a CSS url() or @import with an absolute URL (http:, https:, //host) in a
//   .css file or inside a <style> of index.html. Matched case-sensitively
//   and in CSS only: a JavaScript `new URL("https://…")` is no CSS url();
// - a <script src> or <link href> with an absolute URL in index.html;
// - no @font-face with a local /assets/*.woff2 file in the CSS (the
//   bundled IBM Plex, src/fonts.css).
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { findEval } from "./noEval.mjs";

/** The files the build must have, as patterns of their paths below assets/. */
const required = [
  ["the main chunk", /^index-[\w-]+\.js$/],
  ["the pdf.js worker", /^pdf\.worker[\w.-]*\.m?js$/],
  ["the JPEG 2000 fallback decoder", /^pdfjs-[^/]+\/wasm\/openjpeg_nowasm_fallback\.js$/],
  ["the JBIG2 fallback decoder", /^pdfjs-[^/]+\/wasm\/jbig2_nowasm_fallback\.js$/],
];

/** An absolute URL: a scheme (http:, https:) or protocol-relative (//host). */
const absolute = String.raw`(?:[a-z][a-z0-9+.-]*:)?\/\/`;
/** A CSS url() with an absolute URL. Case-sensitive: `new URL(` in JavaScript is not a CSS url(). */
const remoteURL = new RegExp(String.raw`url\(\s*["']?\s*${absolute}`);
/** A CSS @import of an absolute URL, with or without url(). */
const remoteImport = new RegExp(String.raw`@import\s+(?:url\(\s*)?["']?\s*${absolute}`);
/** A <script src> or <link href> in HTML with an absolute URL. */
const remoteScript = new RegExp(String.raw`<script\b[^>]*\bsrc\s*=\s*["']?\s*${absolute}`, "i");
const remoteLink = new RegExp(String.raw`<link\b[^>]*\bhref\s*=\s*["']?\s*${absolute}`, "i");
/** A @font-face whose src is a local font file of the build. */
const localFont = /@font-face\s*\{[^{}]*src:\s*url\(\s*["']?\/assets\/[^)"']+\.woff2/;

function walk(dir) {
  return readdirSync(dir).flatMap((f) => {
    const p = join(dir, f);
    return statSync(p).isDirectory() ? walk(p) : [p];
  });
}

/** The <style> blocks of an HTML document, joined. */
function styleBlocks(html) {
  return [...html.matchAll(/<style\b[^>]*>([\s\S]*?)<\/style>/gi)].map((m) => m[1]).join("\n");
}

/** The problems of one CSS text: remote url() and @import. */
function cssProblems(name, css) {
  const out = [];
  const url = css.match(remoteURL);
  if (url) out.push(`${name}: url() with an absolute URL: ${url[0]}…`);
  if (remoteImport.test(css)) out.push(`${name}: @import of an absolute URL`);
  return out;
}

/**
 * The problems of the build in dist, as messages; empty when it is
 * complete and no file breaks a rule. Every .js and .mjs file below
 * dist/assets is scanned for eval; every .css, .js, .mjs and .html file of
 * the build (index.html at the dist root included) for remote hosts.
 */
export function checkDist(dist) {
  const assets = join(dist, "assets");
  let files;
  try {
    files = walk(assets);
  } catch {
    return [`${assets}: no build (run pnpm build)`];
  }
  const names = files.map((f) => relative(assets, f).split("\\").join("/"));
  const out = [];
  for (const [what, pattern] of required) {
    if (!names.some((n) => pattern.test(n))) out.push(`${assets}: ${what} is missing`);
  }
  files.forEach((f, i) => {
    if (/\.m?js$/.test(f)) out.push(...findEval(names[i], readFileSync(f, "utf8")));
  });

  // Remote hosts: the whole build, with index.html from the dist root.
  const index = join(dist, "index.html");
  const scanned = walk(dist).filter((f) => /\.(css|m?js|html)$/.test(f));
  if (!existsSync(index)) out.push(`${dist}: index.html is missing`);
  let fonts = 0;
  for (const f of scanned) {
    const name = relative(dist, f).split("\\").join("/");
    const text = readFileSync(f, "utf8");
    if (/s81c\.com/i.test(text)) out.push(`${name}: refers to s81c.com (IBM's font CDN)`);
    if (f.endsWith(".css")) {
      out.push(...cssProblems(name, text));
      if (localFont.test(text)) fonts++;
    } else if (f.endsWith(".html")) {
      out.push(...cssProblems(`${name} <style>`, styleBlocks(text)));
      if (remoteScript.test(text)) out.push(`${name}: <script src> with an absolute URL`);
      if (remoteLink.test(text)) out.push(`${name}: <link href> with an absolute URL`);
    }
  }
  if (fonts === 0) out.push(`${dist}: no @font-face with a local /assets/*.woff2 file in the CSS`);
  return out;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const dist = fileURLToPath(new URL("../../internal/web/ui/dist", import.meta.url));
  const problems = checkDist(dist);
  if (problems.length > 0) {
    console.error(`check:dist: ${problems.length} problem(s):\n${problems.join("\n")}`);
    process.exit(1);
  }
  console.log("check:dist: complete build, no eval( or new Function(, no remote URLs, local fonts present");
}
