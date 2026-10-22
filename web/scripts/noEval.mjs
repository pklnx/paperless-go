// The rules of the no-eval check: the UI's CSP has no 'unsafe-eval', so
// shipped JavaScript must not compile strings at run time. Used by the
// check of the built output (check-dist.mjs, `pnpm check:dist`) and by the
// fast check of pdf.js's package sources (src/pdfViewer.test.ts).

const evalCall = /(^|[^\w$.])eval\s*\(/;
// The indirect call bundlers use: (0, eval)(…).
const indirectEval = /(^|[^\w$.])eval\s*\)\s*\(/;
const newFunction = /new\s+Function\s*\(/;
const functionCall = /(^|[^\w$.])Function\s*\([^)]*\)/g;
// core-js's polyfills (pdf.js's legacy build) fall back to
// Function("return this") only without globalThis, which every browser of
// the CSP has. The minifier may quote it with ' or `.
const allowedFunction = 'Function("return this")';

/**
 * The rules source (a file named name) breaks, as messages; empty when it
 * breaks none. Forbidden: a call of eval, direct or indirect ((0, eval)(),
 * but not a property called eval (obj.eval() is fine); new Function(; and
 * Function(…) other than Function("return this") in any quotes.
 */
export function findEval(name, source) {
  const out = [];
  if (evalCall.test(source) || indirectEval.test(source)) out.push(`${name}: eval(`);
  if (newFunction.test(source)) out.push(`${name}: new Function(`);
  for (const m of source.match(functionCall) ?? []) {
    const call = m.replace(/^[^F]*/, "").replace(/['`]/g, '"');
    if (call !== allowedFunction) out.push(`${name}: ${call}`);
  }
  return out;
}
