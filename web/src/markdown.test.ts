import { afterEach, describe, expect, it } from "vitest";
import { createElement } from "react";
import { cleanup, render } from "@testing-library/react";
import markdownSource from "./markdown.ts?raw";
import detailSource from "./DocumentDetail.tsx?raw";
import { maxInlineDepth, parseInline, parseNotes, renderNotes, safeHref, type Block, type Inline, type Steps } from "./markdown";

// The notes' Markdown: the app's subset (MarkdownNotes.swift), links only
// to http, https, mailto and tel, and work bounded by the input's length.

const text = (v: string): Inline => ({ t: "text", v });

/** The note rendered into a container. */
function show(md: string): HTMLElement {
  return render(createElement("div", null, renderNotes(md))).container.firstElementChild as HTMLElement;
}

afterEach(cleanup);

describe("blocks", () => {
  it("renders headings # to ###, deeper ones as level 3, and drops a closing run", () => {
    expect(parseNotes("# Eins\n## Zwei ##\n### Drei\n###### Sechs")).toEqual([
      { kind: "heading", level: 1, text: [text("Eins")] },
      { kind: "heading", level: 2, text: [text("Zwei")] },
      { kind: "heading", level: 3, text: [text("Drei")] },
      { kind: "heading", level: 3, text: [text("Sechs")] },
    ]);
    // No space after the marks, or seven marks: text.
    expect(parseNotes("#tag\n####### sieben")).toEqual([{ kind: "paragraph", text: [text("#tag\n####### sieben")] }]);
    expect(parseNotes("# C#")).toEqual([{ kind: "heading", level: 1, text: [text("C#")] }]);
    const el = show("# Titel");
    expect(el.querySelector("h3")?.textContent).toBe("Titel");
  });

  it("keeps a paragraph's lines as separate lines, and a blank line starts a new one", () => {
    expect(parseNotes("  eins  \nzwei\n\ndrei")).toEqual([
      { kind: "paragraph", text: [text("eins\nzwei")] },
      { kind: "paragraph", text: [text("drei")] },
    ]);
    const p = show("eins\nzwei").querySelector("p")!;
    expect(p.textContent).toBe("eins\nzwei");
    expect(p.className).toContain("whitespace-pre-wrap");
  });

  it("renders bullet, numbered and task items with their indentation level", () => {
    expect(parseNotes("- a\n  * b\n\t+ c\n            - deep\n1. eins\n  2) zwei\n- [ ] offen\n- [x] erledigt\n  - [X] auch")).toEqual([
      { kind: "bullet", level: 0, text: [text("a")] },
      { kind: "bullet", level: 1, text: [text("b")] },
      { kind: "bullet", level: 1, text: [text("c")] },
      { kind: "bullet", level: 6, text: [text("deep")] },
      { kind: "numbered", marker: "1.", level: 0, text: [text("eins")] },
      { kind: "numbered", marker: "2)", level: 1, text: [text("zwei")] },
      { kind: "task", checked: false, level: 0, text: [text("offen")] },
      { kind: "task", checked: true, level: 0, text: [text("erledigt")] },
      { kind: "task", checked: true, level: 1, text: [text("auch")] },
    ]);
    // Not items: no space after the marker, ten digits, a box without text.
    expect(parseNotes("-a\n1234567890. x")).toEqual([{ kind: "paragraph", text: [text("-a\n1234567890. x")] }]);
    expect(parseNotes("- [ ]")).toEqual([{ kind: "bullet", level: 0, text: [text("[ ]")] }]);

    const el = show("- a\n  - b\n3. drei\n- [x] fertig");
    const items = el.querySelectorAll("ul > li");
    expect(items).toHaveLength(4);
    expect(items[1]!.className).toContain("ps-5");
    expect(items[2]!.textContent).toBe("3.drei");
    const box = items[3]!.querySelector("input[type=checkbox]") as HTMLInputElement;
    expect(box.checked).toBe(true);
    // Read-only, as in the app.
    expect(box.disabled).toBe(true);
  });

  it("renders quotes, rules and fences of ``` and ~~~ verbatim", () => {
    expect(parseNotes("> eins\n>zwei\n---\n* * *\n___\n```js\n  *nicht* [x](https://a.example)\n```\n~~~\n# kein Titel\n~~~")).toEqual([
      { kind: "quote", text: [text("eins\nzwei")] },
      { kind: "rule" },
      { kind: "rule" },
      { kind: "rule" },
      { kind: "code", text: "  *nicht* [x](https://a.example)" },
      { kind: "code", text: "# kein Titel" },
    ]);
    // An unclosed fence runs to the end; a ``` line does not close ~~~.
    expect(parseNotes("~~~\n```\nrest")).toEqual([{ kind: "code", text: "```\nrest" }]);
    const el = show("> zitiert\n\n---\n\n```\n<b>x</b> [l](https://a.example)\n```");
    expect(el.querySelector("blockquote")?.textContent).toBe("zitiert");
    expect(el.querySelector("hr")).not.toBeNull();
    expect(el.querySelector("pre code")?.textContent).toBe("<b>x</b> [l](https://a.example)");
    expect(el.querySelector("a, b")).toBeNull();
  });

  it("ends a quote at a paragraph line and a paragraph at a quote", () => {
    expect(parseNotes("text\n> q\nmehr")).toEqual([
      { kind: "paragraph", text: [text("text")] },
      { kind: "quote", text: [text("q")] },
      { kind: "paragraph", text: [text("mehr")] },
    ]);
  });

  it("splits lines at \\r\\n and \\r", () => {
    expect(parseNotes("# A\r\nb\rc")).toEqual([
      { kind: "heading", level: 1, text: [text("A")] },
      { kind: "paragraph", text: [text("b\nc")] },
    ]);
  });
});

describe("inline", () => {
  it("renders emphasis, strong, strikethrough and code", () => {
    expect(parseInline("*a* _b_ **c** __d__ ~~e~~ `f *g*`")).toEqual([
      { t: "em", c: [text("a")] },
      text(" "),
      { t: "em", c: [text("b")] },
      text(" "),
      { t: "strong", c: [text("c")] },
      text(" "),
      { t: "strong", c: [text("d")] },
      text(" "),
      { t: "del", c: [text("e")] },
      text(" "),
      { t: "code", v: "f *g*" },
    ]);
    expect(parseInline("***both***")).toEqual([{ t: "em", c: [{ t: "strong", c: [text("both")] }] }]);
    expect(parseInline("``a ` b``")).toEqual([{ t: "code", v: "a ` b" }]);
    const el = show("*a* **b** ~~c~~ `d`");
    expect(el.querySelector("em")?.textContent).toBe("a");
    expect(el.querySelector("strong")?.textContent).toBe("b");
    expect(el.querySelector("del")?.textContent).toBe("c");
    expect(el.querySelector("code")?.textContent).toBe("d");
  });

  it("leaves unbalanced and intraword markers as text", () => {
    expect(parseInline("*a **b _c ~~d `e [f")).toEqual([text("*a **b _c ~~d `e [f")]);
    expect(parseInline("snake_case_name 2 * 3 * 4 a]b")).toEqual([text("snake_case_name 2 * 3 * 4 a]b")]);
    expect(parseInline("\\*kein\\* \\[x\\](https://a.example)")).toEqual([text("*kein* [x](https://a.example)")]);
  });

  it("links [text](url) with the allowed schemes, in a new tab without opener or referrer", () => {
    expect(parseInline("[Seite](https://a.example/x?q=1) [m](mailto:a@b.example) [t](tel:+49301234) [h](http://a.example)")).toEqual([
      { t: "link", href: "https://a.example/x?q=1", c: [text("Seite")] },
      text(" "),
      { t: "link", href: "mailto:a@b.example", c: [text("m")] },
      text(" "),
      { t: "link", href: "tel:+49301234", c: [text("t")] },
      text(" "),
      { t: "link", href: "http://a.example/", c: [text("h")] },
    ]);
    // Parentheses in the URL, a title, white space around, angle brackets, emphasis inside.
    expect(parseInline("[w](https://de.wikipedia.org/wiki/A_(B)) [t]( https://a.example \"Titel\" ) [<>](<https://a.example/b>) [**x**](https://a.example)")).toEqual([
      { t: "link", href: "https://de.wikipedia.org/wiki/A_(B)", c: [text("w")] },
      text(" "),
      { t: "link", href: "https://a.example/", c: [text("t")] },
      text(" "),
      { t: "link", href: "https://a.example/b", c: [text("<>")] },
      text(" "),
      { t: "link", href: "https://a.example/", c: [{ t: "strong", c: [text("x")] }] },
    ]);
    expect(parseInline("<https://a.example/x>")).toEqual([{ t: "link", href: "https://a.example/x", c: [text("https://a.example/x")] }]);
    const a = show("[Seite](https://a.example)").querySelector("a")!;
    expect(a.getAttribute("href")).toBe("https://a.example/");
    expect(a.getAttribute("target")).toBe("_blank");
    expect(a.getAttribute("rel")).toBe("noopener noreferrer");
    expect(a.textContent).toBe("Seite");
  });

  it("does not link bare URLs, and an image shows its alt text", () => {
    expect(parseInline("https://a.example")).toEqual([text("https://a.example")]);
    expect(parseInline("![Bild](https://a.example/x.png)")).toEqual([{ t: "plain", c: [text("Bild")] }]);
    expect(show("![Bild](https://a.example/x.png)").querySelector("img, a")).toBeNull();
  });

  it("does not nest links", () => {
    expect(parseInline("[a [b](https://b.example) c](https://a.example)")).toEqual([
      text("[a "),
      { t: "link", href: "https://b.example/", c: [text("b")] },
      text(" c](https://a.example)"),
    ]);
  });
});

describe("link safety", () => {
  const hostile = [
    "javascript:alert(1)",
    "JavaScript:alert(1)",
    "java\tscript:alert(1)",
    "java\nscript:alert(1)",
    "jav\ta\nscript:alert(1)",
    "\x01javascript:alert(1)",
    "\x00\x1f javascript:alert(1)",
    " javascript:alert(1)",
    "javascript\t:alert(1)",
    "data:text/html,<script>alert(1)</script>",
    "data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==",
    "vbscript:msgbox(1)",
    "file:///etc/passwd",
    "blob:https://a.example/1",
    "/relative/path",
    "relative",
    "//evil.example/x",
    "?q=1",
    "#frag",
    "",
    "http://",
  ];

  it.each(hostile)("safeHref refuses %j", (href) => {
    expect(safeHref(href)).toBeNull();
  });

  it("safeHref allows the four schemes, as the URL parser reads them", () => {
    expect(safeHref("HTTPS://A.example")).toBe("https://a.example/");
    expect(safeHref("ht\ttp://a.example")).toBe("http://a.example/");
    expect(safeHref("mailto:a@b.example")).toBe("mailto:a@b.example");
    expect(safeHref("tel:+49 30 1234")).toBe("tel:+49 30 1234");
  });

  it("renders links with other schemes, and relative ones, as their text", () => {
    const md = [
      "[a](javascript:alert(1))",
      "[b](JAVASCRIPT:alert(1))",
      "[c](java\tscript:alert(1))",
      "[d](java\\\nscript:alert(1))",
      "[e](\x01javascript:alert(1))",
      "[f](data:text/html,<script>alert(1)</script>)",
      "[g](vbscript:msgbox(1))",
      "[h](/relative)",
      "[i](//evil.example)",
      "<javascript:alert(1)>",
      "<data:text/html,x>",
      "[j](<javascript:alert(1)>)",
      "[k](file:///etc/passwd)",
      "![l](javascript:alert(1))",
    ].join("\n");
    const el = show(md);
    expect(el.querySelector("a, img, script")).toBeNull();
    expect(el.textContent).toContain("a");
    expect(el.textContent).toContain("javascript:alert(1)");
  });

  it("a tab or line break inside the scheme never makes a javascript: link, even where the parser accepts the destination", () => {
    // The browser drops the tab when it parses the href: what the check
    // sees is what the browser would follow.
    for (const md of ["[x](java\tscript:alert(1))", "[x](java&#9;script:alert(1))", "[x](<java\tscript:alert(1)>)", "<java\tscript:alert(1)>"]) {
      const nodes = parseInline(md);
      const links = JSON.stringify(nodes).match(/"t":"link"/g) ?? [];
      expect(links).toHaveLength(0);
      expect(show(md).querySelector("a")).toBeNull();
    }
  });

  it("never builds HTML from a string", () => {
    for (const [file, src] of <[string, string][]>[
      ["markdown.ts", markdownSource],
      ["DocumentDetail.tsx", detailSource],
    ]) {
      expect(src.length, file).toBeGreaterThan(1000);
      expect(src, file).not.toMatch(/dangerouslySetInnerHTML|innerHTML|outerHTML|insertAdjacentHTML|createContextualFragment/);
    }
  });

  it("shows HTML as text", () => {
    const el = show("<script>alert(1)</script>\n<img src=x onerror=alert(1)>\n# <b>t</b>\n- <i>i</i>");
    expect(el.querySelector("script, img, b, i")).toBeNull();
    expect(el.textContent).toContain("<script>alert(1)</script>");
    expect(el.textContent).toContain("<img src=x onerror=alert(1)>");
  });
});

describe("work bound", () => {
  // Linear by construction: a fixed number of steps per character, for
  // inputs built to make a backtracking or rescanning parser quadratic.
  const size = 100_000;
  const repeat = (unit: string) => unit.repeat(Math.ceil(size / unit.length)).slice(0, size);
  const inputs: [string, string][] = [
    ["plain text", repeat("Rechnung bezahlt ")],
    ["openers without closers", repeat("*a _b ")],
    ["one long emphasis chain", repeat("*")],
    ["mixed emphasis", repeat("*a **b ***c _d __e ")],
    ["rule-of-3 openers", repeat("a**b*")],
    ["alternating markers", repeat("*_")],
    ["strikethrough openers", repeat("~~a ")],
    ["brackets without links", repeat("[")],
    ["link starts without a close", repeat("[a](")],
    ["link starts in one word", repeat("[a](b")],
    ["link starts before white space", repeat("[a](b ")],
    ["titles without a close", repeat('[a](b "')],
    ["nested parentheses", repeat("[a](((")],
    ["one open link around many links", ("[a](" + "[a](b)".repeat(size / 6)).slice(0, size - 2) + " z"],
    ["one open link around closed parentheses", ("[a](b" + "(c)".repeat(size / 3)).slice(0, size - 2) + " z"],
    ["images", repeat("![")],
    ["closing brackets", repeat("[a]")],
    ["links", repeat("[a](https://a.example) ")],
    ["backtick runs of growing length", Array.from({ length: 450 }, (_, k) => "`".repeat(k + 1) + " ").join("").slice(0, size)],
    ["single backticks", repeat("`a")],
    ["angle brackets", repeat("<a:b")],
    ["escapes", repeat("\\")],
    ["deep indentation", repeat("                 - x\n")],
    ["headings", repeat("# a #\n")],
    ["quotes", repeat("> *a\n")],
    ["fences", repeat("```\n")],
    ["one line of spaces", " ".repeat(size - 1) + "x"],
  ];

  it.each(inputs)("%s: at most 10 steps per character", (_, md) => {
    expect(md.length).toBeGreaterThanOrEqual(size - 1);
    const steps: Steps = { n: 0 };
    parseNotes(md, steps);
    expect(steps.n).toBeLessThanOrEqual(10 * md.length);
    // The same input as one paragraph's inline text.
    const inline: Steps = { n: 0 };
    parseInline(md.split("\n").join(" "), inline);
    expect(inline.n).toBeLessThanOrEqual(10 * md.length);
  });

  it("the counter counts: a longer input takes more steps", () => {
    const short: Steps = { n: 0 };
    const long: Steps = { n: 0 };
    parseNotes("*a ".repeat(100), short);
    parseNotes("*a ".repeat(1000), long);
    expect(long.n).toBeGreaterThan(5 * short.n);
  });
});

describe("nesting depth", () => {
  /** The deepest nesting of inline nodes. */
  const depth = (nodes: Inline[]): number => {
    let max = 0;
    for (const n of nodes) if (n.t !== "text" && n.t !== "code") max = Math.max(max, 1 + depth(n.c));
    return max;
  };
  const blockDepth = (blocks: Block[]) => {
    let max = 0;
    for (const b of blocks) if ("text" in b && typeof b.text !== "string") max = Math.max(max, depth(b.text));
    return max;
  };
  const size = 100_000;
  /** k openers, then k closers: k levels deep, at least size characters. */
  const nested = (open: string, close: string, middle = "") => {
    const k = Math.ceil((size - middle.length) / (open.length + close.length));
    return open.repeat(k) + middle + close.repeat(k);
  };
  const inputs: [string, string][] = [
    ["emphasis", nested("*a ", "b*")],
    ["strong", nested("**a ", "b**")],
    ["strikethrough", nested("~~a ", "b~~")],
    ["images", nested("![", "](x)", "a")],
    ["emphasis inside link text", `[${nested("*a ", "b*")}](https://a.example)`],
    ["emphasis inside a quote", `> ${nested("*a ", "b*")}`],
    ["images inside a link", `[${nested("![", "](x)", "a")}](https://a.example)`],
  ];

  it.each(inputs)("%s, 100 000 characters deep: nesting capped, linear work, and it renders", (_, md) => {
    expect(md.length).toBeGreaterThanOrEqual(size);
    const steps: Steps = { n: 0 };
    const blocks = parseNotes(md, steps);
    expect(blockDepth(blocks)).toBeLessThanOrEqual(maxInlineDepth);
    expect(steps.n).toBeLessThanOrEqual(10 * md.length);
    const el = show(md);
    expect(el.textContent).toContain("a");
  });

  it("keeps the formatting up to the cap and shows deeper content as text", () => {
    const shallow = parseInline("*a ".repeat(3) + "b*".repeat(3));
    expect(depth(shallow)).toBe(3);
    const full = parseInline("*a ".repeat(maxInlineDepth) + "b*".repeat(maxInlineDepth));
    expect(depth(full)).toBe(maxInlineDepth);
    const deep = parseInline("*a ".repeat(40) + "b*".repeat(40));
    expect(depth(deep)).toBeLessThanOrEqual(maxInlineDepth);
    expect(deep[0]!.t).toBe("em");
    // Nothing of the text is lost.
    const text = (nodes: Inline[]): string => nodes.map((n) => (n.t === "text" || n.t === "code" ? n.v : text(n.c))).join("");
    expect(text(deep)).toBe("a ".repeat(40) + "b".repeat(40));
  });
});
