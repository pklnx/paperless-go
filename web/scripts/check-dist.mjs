// `pnpm check:dist`: scans the built UI (internal/web/ui/dist) for eval(
// and new Function( (noEval.mjs), so a new dependency or a Vite transform
// that needs 'unsafe-eval' fails the build in CI. A missing or incomplete
// build fails too, so an empty folder cannot pass.
import { readdirSync, readFileSync, statSync } from "node:fs";
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

function walk(dir) {
  return readdirSync(dir).flatMap((f) => {
    const p = join(dir, f);
    return statSync(p).isDirectory() ? walk(p) : [p];
  });
}

/**
 * The problems of the build in dist, as messages; empty when it is
 * complete and no script breaks a rule. Every .js and .mjs file below
 * dist/assets is scanned.
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
  return out;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const dist = fileURLToPath(new URL("../../internal/web/ui/dist", import.meta.url));
  const problems = checkDist(dist);
  if (problems.length > 0) {
    console.error(`check:dist: ${problems.length} problem(s):\n${problems.join("\n")}`);
    process.exit(1);
  }
  console.log("check:dist: no eval( or new Function( in the build");
}
