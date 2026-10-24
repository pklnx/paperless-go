import { describe, expect, it } from "vitest";
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
