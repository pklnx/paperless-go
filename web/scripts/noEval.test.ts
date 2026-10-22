import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { checkDist } from "./check-dist.mjs";
import { findEval } from "./noEval.mjs";

describe("findEval", () => {
  it("finds each forbidden form", () => {
    expect(findEval("a.js", "x=1;eval('2')")).toEqual(["a.js: eval("]);
    expect(findEval("a.js", "eval ('2')")).toEqual(["a.js: eval("]);
    expect(findEval("a.js", "(0,eval)('2')")).toEqual(["a.js: eval("]);
    expect(findEval("a.js", "f=new Function('a','return a')")).toContain("a.js: new Function(");
    expect(findEval("a.js", "f=new  Function (code)")).toContain("a.js: new Function(");
    expect(findEval("a.js", "f=Function(code)")).toEqual(["a.js: Function(code)"]);
    expect(findEval("a.js", 'f=Function("return 1")')).toEqual(['a.js: Function("return 1")']);
  });

  it("lets the allowed forms pass", () => {
    expect(findEval("a.js", "obj.eval(x); $eval(y); my_eval(z); evaluate(w)")).toEqual([]);
    expect(findEval("a.js", 'g=Function("return this")()')).toEqual([]);
    expect(findEval("a.js", "g=Function('return this')()")).toEqual([]);
    expect(findEval("a.js", "g=Function(`return this`)()")).toEqual([]);
    expect(findEval("a.js", "x instanceof Function; typeof f=='function'; obj.Function(1); MyFunction(2)")).toEqual([]);
  });
});

describe("checkDist", () => {
  const dirs: string[] = [];
  afterEach(() => {
    for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true });
  });

  /** A build in a temp folder with files (paths below dist/), each with harmless code. */
  function dist(files: Record<string, string>): string {
    const d = mkdtempSync(join(tmpdir(), "check-dist-"));
    dirs.push(d);
    for (const [path, src] of Object.entries(files)) {
      mkdirSync(dirname(join(d, path)), { recursive: true });
      writeFileSync(join(d, path), src);
    }
    return d;
  }
  const complete = {
    "index.html": "<!doctype html>",
    "assets/index-Ab12_c.js": "console.log(1)",
    "assets/pdf.worker.min-Xy9.mjs": 'var g=Function("return this")()',
    "assets/pdfjs-6.4.299/wasm/openjpeg_nowasm_fallback.js": "var a=1",
    "assets/pdfjs-6.4.299/wasm/jbig2_nowasm_fallback.js": "var b=1",
  };

  it("passes a complete build without eval", () => {
    expect(checkDist(dist(complete))).toEqual([]);
  });

  it("fails without a build, and on an empty one", () => {
    expect(checkDist(join(tmpdir(), "check-dist-does-not-exist"))).toHaveLength(1);
    expect(checkDist(dist({}))).toHaveLength(1);
    expect(checkDist(dist({ "assets/.keep": "" }))).toHaveLength(4);
  });

  it("fails when the main chunk, the worker or a fallback decoder is missing", () => {
    for (const missing of Object.keys(complete).filter((k) => k !== "index.html")) {
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
});
