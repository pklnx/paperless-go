import { describe, expect, it } from "vitest";
import { SNIPPET_START as S, SNIPPET_STOP as E, snippetParts } from "./snippet";

describe("snippetParts", () => {
  it("splits at the markers", () => {
    expect(snippetParts(`Ihre ${S}Rechnung${E} für ${S}März${E}.`)).toEqual([
      { text: "Ihre ", mark: false },
      { text: "Rechnung", mark: true },
      { text: " für ", mark: false },
      { text: "März", mark: true },
      { text: ".", mark: false },
    ]);
  });
  it("keeps markup as text", () => {
    expect(snippetParts(`<b>${S}x${E}</b>`)).toEqual([
      { text: "<b>", mark: false },
      { text: "x", mark: true },
      { text: "</b>", mark: false },
    ]);
  });
  it("drops stray and repeated markers and runs an open mark to the end", () => {
    expect(snippetParts(`${E}a${S}${S}b${E}${E}c${S}d`)).toEqual([
      { text: "a", mark: false },
      { text: "b", mark: true },
      { text: "c", mark: false },
      { text: "d", mark: true },
    ]);
    expect(snippetParts(`${S}${E}`)).toEqual([]);
    expect(snippetParts("")).toEqual([]);
  });
  it("removes bidi controls", () => {
    expect(snippetParts("a‮b")).toEqual([{ text: "ab", mark: false }]);
  });
});
