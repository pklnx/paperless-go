// The PDF writer of the tests (pdf.ts) gives the same bytes as the demo's
// (tools/demo/pdf.go), checked on the golden file both test against. So
// the files a test uploads are ones the demo's stand-in tools can read. No
// browser needed.
import { readFileSync } from "node:fs";
import { expect, test } from "@playwright/test";
import { makePDF } from "./pdf";

// The pages of tools/demo/testdata/sample.pdf, as in tools/demo/tools_test.go.
const samplePages = [
  ["Sample document (golden file)", "Grüße aus Köln: Kündigung", "A backslash \\ and parentheses ()"],
  ["Page two of the sample", "Reference word: samplequartz"],
];

test("pdf.ts writes the golden file of tools/demo byte for byte", () => {
  const golden = readFileSync(new URL("../../tools/demo/testdata/sample.pdf", import.meta.url));
  expect(makePDF(samplePages).equals(golden)).toBe(true);
});

test("pdf.ts refuses characters outside WinAnsi's Latin-1 part", () => {
  expect(() => makePDF([["Euro €"]])).toThrow(/WinAnsiEncoding/);
  expect(() => makePDF([])).toThrow(/no pages/);
});
