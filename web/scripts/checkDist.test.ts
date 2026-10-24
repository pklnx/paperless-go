// @vitest-environment node
// The build check (check-dist.mjs) on temporary dist folders: a complete
// build with local fonts and no remote host passes; each rule fails alone
// and next to the others.
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { checkDist } from "./check-dist.mjs";

const dirs: string[] = [];
afterEach(() => {
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true });
});

/** A build in a temp folder with files (paths below dist/), each with harmless content. */
function dist(files: Record<string, string>): string {
  const d = mkdtempSync(join(tmpdir(), "check-dist-"));
  dirs.push(d);
  for (const [path, src] of Object.entries(files)) {
    mkdirSync(dirname(join(d, path)), { recursive: true });
    writeFileSync(join(d, path), src);
  }
  return d;
}

/** The bundled font: what src/fonts.css becomes in the build. */
const localFont = '@font-face{font-family:IBM Plex Sans;font-weight:400;src:url(/assets/IBMPlexSans-Regular-Latin1-B1c2.woff2) format("woff2")}';
const complete = {
  "index.html": '<!doctype html><html><head><style>body{margin:0}</style><link rel="icon" href="/favicon-32.png"><script type="module" src="/assets/index-Ab12_c.js"></script></head><body></body></html>',
  "assets/index-Ab12_c.css": `${localFont}.cds--btn{background-image:url("data:image/svg+xml;charset=utf-8,%3Csvg xmlns='http://www.w3.org/2000/svg'/%3E")}`,
  "assets/index-Ab12_c.js": "console.log(1)",
  "assets/pdf.worker.min-Xy9.mjs": 'var g=Function("return this")()',
  "assets/pdfjs-6.4.299/wasm/openjpeg_nowasm_fallback.js": "var a=1",
  "assets/pdfjs-6.4.299/wasm/jbig2_nowasm_fallback.js": "var b=1",
};

describe("checkDist", () => {
  it("passes a complete build with local fonts and no remote host", () => {
    expect(checkDist(dist(complete))).toEqual([]);
  });

  it("fails without a build, and on an empty one", () => {
    expect(checkDist(join(tmpdir(), "check-dist-does-not-exist"))).toHaveLength(1);
    expect(checkDist(dist({}))).toHaveLength(1);
    // The four required files, index.html and the local font.
    expect(checkDist(dist({ "assets/.keep": "" }))).toHaveLength(6);
  });

  it("fails when the main chunk, the worker or a fallback decoder is missing", () => {
    for (const missing of Object.keys(complete).filter((k) => /\.m?js$/.test(k))) {
      const files: Record<string, string> = { ...complete };
      delete files[missing];
      const problems = checkDist(dist(files));
      expect(problems, missing).toHaveLength(1);
      expect(problems[0], missing).toMatch(/is missing$/);
    }
  });

  it("fails on an eval( or new Function( in any script, also nested below assets/", () => {
    expect(checkDist(dist({ ...complete, "assets/index-Ab12_c.js": "eval(x)" }))).toEqual(["index-Ab12_c.js: eval("]);
    expect(checkDist(dist({ ...complete, "assets/pdfjs-6.4.299/wasm/jbig2_nowasm_fallback.js": "new Function(s)" }))).toContain(
      "pdfjs-6.4.299/wasm/jbig2_nowasm_fallback.js: new Function(",
    );
    expect(checkDist(dist({ ...complete, "assets/chunk-1.mjs": "x=1;eval(y)" }))).toEqual(["chunk-1.mjs: eval("]);
    // Other files are not scripts.
    expect(checkDist(dist({ ...complete, "assets/notes.txt": "eval(x)" }))).toEqual([]);
  });

  it("fails on a remote @font-face, @import, s81c.com, script or link", () => {
    // Carbon's CDN rule, protocol-relative, left in the CSS.
    const cdnFont = '@font-face{font-family:IBM Plex Mono;src:url(//1.www.s81c.com/common/carbon/plex/x.woff2) format("woff2")}';
    expect(checkDist(dist({ ...complete, "assets/index-Ab12_c.css": localFont + cdnFont }))).toEqual([
      "assets/index-Ab12_c.css: refers to s81c.com (IBM's font CDN)",
      "assets/index-Ab12_c.css: url() with an absolute URL: url(//…",
    ]);
    expect(checkDist(dist({ ...complete, "assets/index-Ab12_c.css": `${localFont}@import "https://fonts.example/plex.css";` }))).toEqual([
      "assets/index-Ab12_c.css: @import of an absolute URL",
    ]);
    // With url(): both rules see it.
    expect(checkDist(dist({ ...complete, "assets/index-Ab12_c.css": `${localFont}@import url('http://fonts.example/plex.css');` }))).toEqual([
      "assets/index-Ab12_c.css: url() with an absolute URL: url('http://…",
      "assets/index-Ab12_c.css: @import of an absolute URL",
    ]);
    // A protocol-relative font in a <style> of index.html.
    expect(checkDist(dist({ ...complete, "index.html": `<html><head><style>${cdnFont}</style></head></html>` }))).toEqual([
      "index.html: refers to s81c.com (IBM's font CDN)",
      "index.html <style>: url() with an absolute URL: url(//…",
    ]);
    // s81c.com in a script.
    expect(checkDist(dist({ ...complete, "assets/index-Ab12_c.js": 'fetch("https://1.www.s81c.com/x")' }))).toEqual([
      "assets/index-Ab12_c.js: refers to s81c.com (IBM's font CDN)",
    ]);
    // A remote script or stylesheet in index.html.
    expect(checkDist(dist({ ...complete, "index.html": '<html><head><script src="https://cdn.example/x.js"></script></head></html>' }))).toEqual([
      "index.html: <script src> with an absolute URL",
    ]);
    expect(checkDist(dist({ ...complete, "index.html": "<html><head><link rel=\"stylesheet\" href='//cdn.example/x.css'></head></html>" }))).toEqual([
      "index.html: <link href> with an absolute URL",
    ]);
  });

  it("fails without a local font and without index.html", () => {
    expect(checkDist(dist({ ...complete, "assets/index-Ab12_c.css": ".a{color:red}" }))).toEqual([expect.stringMatching(/no @font-face with a local \/assets\/\*\.woff2 file/)]);
    const files: Record<string, string> = { ...complete };
    delete files["index.html"];
    expect(checkDist(dist(files))).toEqual([expect.stringMatching(/index\.html is missing$/)]);
  });

  it("lets JavaScript's new URL(\"https://…\") pass in .js and .mjs", () => {
    // pdf.worker.min-*.mjs contains new URL("https://a@b") four times; a
    // case-insensitive url( rule would flag it.
    const js = 'const u = new URL("https://x"); const v = new URL(\'https://a@b\');';
    expect(checkDist(dist({ ...complete, "assets/index-Ab12_c.js": js }))).toEqual([]);
    expect(checkDist(dist({ ...complete, "assets/pdf.worker.min-Xy9.mjs": js }))).toEqual([]);
    // Local and same-origin URLs in CSS are fine too.
    expect(checkDist(dist({ ...complete, "assets/index-Ab12_c.css": `${localFont}.a{background:url(/assets/a.svg)}.b{background:url("./b.png")}` }))).toEqual([]);
  });

  it("reports the required and eval problems next to the remote-host ones", () => {
    const files: Record<string, string> = { ...complete, "assets/index-Ab12_c.js": "eval(x)", "assets/index-Ab12_c.css": '@import "https://x.example/a.css";' };
    delete files["assets/pdf.worker.min-Xy9.mjs"];
    const problems = checkDist(dist(files));
    expect(problems).toEqual([
      expect.stringMatching(/the pdf\.js worker is missing$/),
      "index-Ab12_c.js: eval(",
      "assets/index-Ab12_c.css: @import of an absolute URL",
      expect.stringMatching(/no @font-face with a local/),
    ]);
  });
});
