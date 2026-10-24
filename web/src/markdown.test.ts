import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { cleanup, render } from "@testing-library/react";
import markdownSource from "./markdown.ts?raw";
import detailSource from "./DocumentDetail.tsx?raw";
import entitiesSource from "./htmlEntities.ts?raw";
import { namedEntities } from "./htmlEntities";
import { entitiesLoaded, loadEntities, maxInlineDepth, needsEntities, parseInline, parseNotes, renderNotes, safeHref, type Block, type Inline, type Steps } from "./markdown";

// The notes' Markdown: the app's subset (MarkdownNotes.swift), links only
// to http, https, mailto and tel, and work bounded by the input's length.

const text = (v: string): Inline => ({ t: "text", v });

/** The note rendered into a container. */
function show(md: string): HTMLElement {
  return render(createElement("div", null, renderNotes(md))).container.firstElementChild as HTMLElement;
}

afterEach(cleanup);
// The named entities are a chunk of their own (#134); NotesView.test.tsx
// covers the rendering before they are loaded.
beforeAll(loadEntities);

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
    expect(parseInline("\\*kein\\* \\[x\\](https://a.example)")).toEqual([text("*kein* [x]("), { t: "link", href: "https://a.example/", c: [text("https://a.example")] }, text(")")]);
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

  it("an image shows its alt text", () => {
    expect(parseInline("![Bild](https://a.example/x.png)")).toEqual([{ t: "plain", c: [text("Bild")] }]);
    expect(show("![Bild](https://a.example/x.png)").querySelector("img, a")).toBeNull();
  });

  it("does not nest links", () => {
    expect(parseInline("[a [b](https://b.example) c](https://a.example)")).toEqual([
      text("[a "),
      { t: "link", href: "https://b.example/", c: [text("b")] },
      // The rest is text, with its bare URL linked (#131).
      text(" c]("),
      { t: "link", href: "https://a.example/", c: [text("https://a.example")] },
      text(")"),
    ]);
  });
});

/** The links of inline nodes, at any depth: [href, text]. */
function linksOf(nodes: Inline[]): [string, string][] {
  const out: [string, string][] = [];
  const walk = (ns: Inline[]) => {
    for (const n of ns) {
      if (n.t === "link") out.push([n.href, plain(n.c)]);
      if (n.t !== "text" && n.t !== "code") walk(n.c);
    }
  };
  walk(nodes);
  return out;
}
const plain = (nodes: Inline[]): string => nodes.map((n) => (n.t === "text" || n.t === "code" ? n.v : plain(n.c))).join("");
const link = (href: string, v: string): Inline => ({ t: "link", href, c: [text(v)] });

describe("bare addresses (#131)", () => {
  // The app's cases (MarkdownNotesTests.swift).
  it("links a bare URL, an e-mail address and a tel: URL, and the text stays as written", () => {
    const md = "Portal: https://example.com/login, Fragen an info@example.com, Tel. tel:+49301234";
    const nodes = parseInline(md);
    expect(nodes).toEqual([
      text("Portal: "),
      link("https://example.com/login", "https://example.com/login"),
      text(", Fragen an "),
      link("mailto:info@example.com", "info@example.com"),
      text(", Tel. "),
      link("tel:+49301234", "tel:+49301234"),
    ]);
    expect(plain(nodes)).toBe(md);
    const a = show(md).querySelectorAll("a");
    expect([...a].map((x) => [x.getAttribute("href"), x.textContent, x.getAttribute("target"), x.getAttribute("rel")])).toEqual([
      ["https://example.com/login", "https://example.com/login", "_blank", "noopener noreferrer"],
      ["mailto:info@example.com", "info@example.com", "_blank", "noopener noreferrer"],
      ["tel:+49301234", "tel:+49301234", "_blank", "noopener noreferrer"],
    ]);
  });

  it("links an e-mail address with an umlaut as a whole, and an IDN domain", () => {
    const nodes = parseInline("Bitte an müller@example.com oder info@müller.de schreiben");
    const links = linksOf(nodes);
    expect(links.map(([, t]) => t)).toEqual(["müller@example.com", "info@müller.de"]);
    expect(decodeURIComponent(links[0]![0])).toBe("mailto:müller@example.com");
    expect(links[1]![0].startsWith("mailto:info@")).toBe(true);
    expect(plain(nodes)).toBe("Bitte an müller@example.com oder info@müller.de schreiben");
  });

  it("keeps a written link's destination when its text looks like an address", () => {
    expect(parseInline("[müller@example.com](https://example.com/kontakt)")).toEqual([link("https://example.com/kontakt", "müller@example.com")]);
    expect(parseInline("[see https://a.example/x and *www.b.example*](https://c.example)")).toEqual([
      { t: "link", href: "https://c.example/", c: [text("see https://a.example/x and "), { t: "em", c: [text("www.b.example")] }] },
    ]);
  });

  it("does not link other schemes, inline code or fences", () => {
    const md = "[Datei](file:///etc/passwd)\n[Skript](javascript:alert(1))\n[App](myapp://open/1)\nfile:///Users/test/a.pdf und myapp://open/2";
    expect(linksOf(parseInline(md))).toEqual([]);
    expect(linksOf(parseInline("Aufruf: `https://example.com/api` und `info@example.com`"))).toEqual([]);
    const blocks = parseNotes("Text\n```\n**nicht fett** https://example.com a@b.example\n```");
    expect(blocks[1]).toEqual({ kind: "code", text: "**nicht fett** https://example.com a@b.example" });
    expect(show("```\nhttps://example.com\n```").querySelector("a")).toBeNull();
  });

  it("ends an address before trailing punctuation and unopened brackets", () => {
    const cases: [string, string, string][] = [
      // input, href, linked text
      ["Siehe https://a.example/x.", "https://a.example/x", "https://a.example/x"],
      ["(https://a.example/x)", "https://a.example/x", "https://a.example/x"],
      ["[https://a.example/x]", "https://a.example/x", "https://a.example/x"],
      ["{https://a.example/x}", "https://a.example/x", "https://a.example/x"],
      ["https://de.wikipedia.org/wiki/A_(B).", "https://de.wikipedia.org/wiki/A_(B)", "https://de.wikipedia.org/wiki/A_(B)"],
      ["(siehe https://de.wikipedia.org/wiki/A_(B))", "https://de.wikipedia.org/wiki/A_(B)", "https://de.wikipedia.org/wiki/A_(B)"],
      ["https://a.example/?q=1&r=2!?", "https://a.example/?q=1&r=2", "https://a.example/?q=1&r=2"],
      ["'https://a.example/x'", "https://a.example/x", "https://a.example/x"],
      ['"https://a.example/x"', "https://a.example/x", "https://a.example/x"],
      ["„https://a.example/x“", "https://a.example/x", "https://a.example/x"],
      ["https://a.example/x…", "https://a.example/x", "https://a.example/x"],
      ["https://a.example/x;", "https://a.example/x", "https://a.example/x"],
      ["<https://a.example/x>", "https://a.example/x", "https://a.example/x"],
      ["https://a.example/x<b>", "https://a.example/x", "https://a.example/x"],
      ["https://a.example/x&gt;", "https://a.example/x", "https://a.example/x"],
      ["https://a.example/~u/a_b*c", "https://a.example/~u/a_b*c", "https://a.example/~u/a_b*c"],
      ["HTTPS://A.example/X", "https://a.example/X", "HTTPS://A.example/X"],
      ["www.example.com/a.", "http://www.example.com/a", "www.example.com/a"],
      ["info@example.com.", "mailto:info@example.com", "info@example.com"],
      ["first.last+tag@sub.example.co.uk", "mailto:first.last+tag@sub.example.co.uk", "first.last+tag@sub.example.co.uk"],
      ["mailto:info@example.com?subject=Hallo", "mailto:info@example.com?subject=Hallo", "mailto:info@example.com?subject=Hallo"],
      ["https://a.example/ä/€", "https://a.example/%C3%A4/%E2%82%AC", "https://a.example/ä/€"],
    ];
    for (const [md, href, v] of cases) expect(linksOf(parseInline(md)), md).toEqual([[href, v]]);
    // The rest stays text.
    expect(parseInline("(https://a.example/x).")).toEqual([text("("), link("https://a.example/x", "https://a.example/x"), text(").")]);
  });

  it("works with emphasis around an address", () => {
    expect(parseInline("*https://a.example/x*")).toEqual([{ t: "em", c: [link("https://a.example/x", "https://a.example/x")] }]);
    expect(parseInline("**info@example.com**")).toEqual([{ t: "strong", c: [link("mailto:info@example.com", "info@example.com")] }]);
    expect(parseInline("_www.example.com_")).toEqual([{ t: "em", c: [link("http://www.example.com/", "www.example.com")] }]);
  });

  it("does not link what only looks like an address", () => {
    for (const md of [
      "example.com",
      "www.example",
      "a@b",
      "a@b.",
      "a@-",
      "a@b.c-",
      "@example.com",
      "xhttps://a.example",
      "https://",
      "https:// a.example",
      "http://.example",
      "tel:abc",
      "tel:",
      "tel:+",
      "(http://(http://",
      "mailto:",
      "C:\\Pfad",
      "10:30",
      "snake_case_name",
      "user@@example.com",
    ]) {
      expect(linksOf(parseInline(md)), md).toEqual([]);
    }
  });
});

describe("entities (#131)", () => {
  it("shows named, decimal and hex entities as their character in text", () => {
    expect(parseInline("Fa&szlig; &amp; M&uuml;ller &#228; &#xE4; &#X1F600; &copy;")).toEqual([text("Faß & Müller ä ä 😀 ©")]);
    expect(parseInline("&ngE; &fjlig; &Tab;|")).toEqual([text("≧\u0338 fj \t|")]);
    // Invalid code points become U+FFFD, as in CommonMark.
    expect(parseInline("&#0; &#xD800; &#x110000;")).toEqual([text("\ufffd \ufffd \ufffd")]);
  });

  it("leaves what is not an entity as written", () => {
    for (const md of ["&", "&amp", "& amp;", "&unknown;", "&#;", "&#x;", "&#12345678;", "&#x1234567;", "&ampx;", "&constructor;", "&hasOwnProperty;", "&toString;", "&#-1;"]) {
      expect(parseInline(md), md).toEqual([text(md)]);
    }
    expect(parseInline("\\&amp;")).toEqual([text("&amp;")]);
  });

  it("decodes in link text, emphasis and bare URLs, never in code or destinations", () => {
    expect(parseInline("[a &amp; b](https://a.example/?x=1&amp;y=2)")).toEqual([link("https://a.example/?x=1&amp;y=2", "a & b")]);
    expect(parseInline("*&lt;b&gt;*")).toEqual([{ t: "em", c: [text("<b>")] }]);
    expect(parseInline("`&amp;`")).toEqual([{ t: "code", v: "&amp;" }]);
    // A bare URL is found as written, then decoded (#134).
    expect(parseInline("https://a.example/?x=1&amp;y=2")).toEqual([link("https://a.example/?x=1&y=2", "https://a.example/?x=1&y=2")]);
    expect(parseNotes("```\n&amp;\n```")).toEqual([{ kind: "code", text: "&amp;" }]);
  });

  it("an entity is text: it never makes markup, a delimiter or a link", () => {
    expect(parseInline("&#42;a&#42;")).toEqual([text("*a*")]);
    expect(parseInline("&lt;script&gt;alert(1)&lt;/script&gt;")).toEqual([text("<script>alert(1)</script>")]);
    const el = show("&lt;img src=x onerror=alert(1)&gt; &#91;x&#93;&#40;javascript:alert(1)&#41;");
    expect(el.querySelector("img, a")).toBeNull();
    expect(el.textContent).toBe("<img src=x onerror=alert(1)> [x](javascript:alert(1))");
  });

  it("the table has the HTML names, as text", () => {
    expect(Object.keys(namedEntities)).toHaveLength(2125);
    for (const [name, v] of Object.entries(namedEntities)) {
      expect(name).toMatch(/^[A-Za-z][A-Za-z0-9]{1,31}$/);
      expect([...v].length).toBeGreaterThanOrEqual(1);
      expect([...v].length).toBeLessThanOrEqual(2);
    }
    // The values agree with the browser's HTML parser (jsdom), checked
    // through a text node so no markup is built.
    const sample = ["amp", "lt", "gt", "quot", "apos", "nbsp", "auml", "Ouml", "szlig", "euro", "hellip", "ndash", "NotNestedLessLess", "Afr", "bne"];
    for (const name of sample) {
      const doc = new DOMParser().parseFromString(`<p>&${name};</p>`, "text/html");
      expect(namedEntities[name], name).toBe(doc.querySelector("p")!.textContent);
    }
  });
});

describe("bare addresses: hostile input (#131)", () => {
  it("never links another scheme written bare, or an entity-encoded one", () => {
    for (const md of [
      "javascript:alert(1)",
      "javascript://example.com/%0aalert(1)",
      "JAVASCRIPT:alert('https://a.example')",
      "data:text/html,<script>alert(1)</script>",
      "vbscript:msgbox(1)",
      "file:///etc/passwd",
      "&#106;avascript:alert(1)",
      "&#104;ttps://evil.example",
      "&#x68;ttp://evil.example",
      "http&#58;//evil.example",
      "https&colon;//evil.example",
      "&lt;javascript:alert(1)&gt;",
      "mailto&#58;a@b.example",
      "ｈｔｔｐｓ://evil.example",
      "\u200bhttps\u200b://evil.example",
      "java\u00adscript:alert(1)",
    ]) {
      const nodes = parseInline(md);
      for (const [href] of linksOf(nodes)) expect(href, md).toMatch(/^(https?|mailto|tel):/);
      expect(linksOf(nodes).filter(([href]) => href.includes("evil") || href.startsWith("javascript")), md).toEqual([]);
      const el = show(md);
      for (const a of el.querySelectorAll("a")) expect(a.getAttribute("href"), md).toMatch(/^(https?|mailto|tel):/);
    }
  });

  it("a javascript: text before a URL links only the URL", () => {
    expect(linksOf(parseInline("javascript:alert(1)//https://a.example/x"))).toEqual([["https://a.example/x", "https://a.example/x"]]);
  });

  it("stops at invisible and control characters, and the href is what the browser follows", () => {
    expect(linksOf(parseInline("https://a.example/x\u202e/gpj.exe"))).toEqual([["https://a.example/x", "https://a.example/x"]]);
    expect(linksOf(parseInline("https://a.example/x\u200bevil"))).toEqual([["https://a.example/x", "https://a.example/x"]]);
    expect(linksOf(parseInline("https://a.example/x\u00a0y"))).toEqual([["https://a.example/x", "https://a.example/x"]]);
    expect(linksOf(parseInline("https://\u202eevil.example"))).toEqual([]);
    // A look-alike host: the href is the host's punycode, as the browser reads it.
    expect(linksOf(parseInline("https://аpple.example/"))).toEqual([["https://xn--pple-43d.example/", "https://аpple.example/"]]);
    // User info: the href names the real host.
    expect(linksOf(parseInline("https://bank.example@evil.example/"))).toEqual([["https://bank.example@evil.example/", "https://bank.example@evil.example/"]]);
  });

  it("a very long address is one link", () => {
    const long = "https://a.example/" + "a".repeat(100_000);
    const links = linksOf(parseInline(long));
    expect(links).toHaveLength(1);
    expect(links[0]![1]).toBe(long);
    const mail = "a".repeat(50_000) + "@" + "b".repeat(50_000) + ".example";
    expect(linksOf(parseInline(mail))).toEqual([["mailto:" + mail, mail]]);
  });
});

describe("bare URLs and entities, written links after URLs (#134)", () => {
  it("decodes entities in a bare http(s) or www. URL after finding it, and checks the result again", () => {
    const cases: [string, string, string][] = [
      // input, href, linked text
      ["https://a.example/?a=1&amp;b=2&amp;c=3", "https://a.example/?a=1&b=2&c=3", "https://a.example/?a=1&b=2&c=3"],
      ["http://a.example/?q=M&uuml;ller", "http://a.example/?q=M%C3%BCller", "http://a.example/?q=Müller"],
      ["www.a.example/?a=1&#38;b=2", "http://www.a.example/?a=1&b=2", "www.a.example/?a=1&b=2"],
      ["https://a.example/x&#x2F;y", "https://a.example/x/y", "https://a.example/x/y"],
      // A trailing entity still comes off first.
      ["https://a.example/?a=1&amp;b=2&gt;", "https://a.example/?a=1&b=2", "https://a.example/?a=1&b=2"],
      // What is no entity stays as written.
      ["https://a.example/?a=1&b=2&unknown;c&amp", "https://a.example/?a=1&b=2&unknown;c&amp", "https://a.example/?a=1&b=2&unknown;c&amp"],
      // The decoded host is what the browser follows: as if typed.
      ["https://bank.example&#64;evil.example/", "https://bank.example@evil.example/", "https://bank.example@evil.example/"],
    ];
    for (const [md, href, v] of cases) expect(linksOf(parseInline(md)), md).toEqual([[href, v]]);
    // mailto: and tel: stay as written, as do e-mail addresses.
    expect(linksOf(parseInline("mailto:a@b.example?subject=a&amp;b"))).toEqual([["mailto:a@b.example?subject=a&amp;b", "mailto:a@b.example?subject=a&amp;b"]]);
  });

  it("an entity never makes a URL or a scheme, and a decoded character that ends an address is never linked", () => {
    for (const md of ["&#104;ttps://a.example", "https&#58;//a.example", "https:&#47;&#47;a.example", "www&#46;a.example", "w&#119;w.a.example", "www.a&#46;example"]) {
      expect(linksOf(parseInline(md)), md).toEqual([]);
    }
    // White space, controls, format characters and <>" from an entity:
    // the URL stays as written, so none of them is in the link.
    for (const ent of ["&#32;", "&Tab;", "&NewLine;", "&#x202E;", "&#x200B;", "&shy;", "&lt;", "&quot;", "&#x85;", "&nbsp;"]) {
      const md = `https://a.example/x${ent}y`;
      const links = linksOf(parseInline(md));
      expect(links, md).toEqual([[`https://a.example/x${ent}y`, md]]);
      expect(links[0]![0], md).toBe(safeHref(md));
    }
    // A decoded URL that does not parse is no link.
    expect(linksOf(parseInline("https://a.example:&#57;9999/"))).toEqual([]);
    // Decoded into another scheme's look: still the http(s) host as typed.
    for (const md of ["https://a.example/&#106;avascript:alert(1)", "https://&#106;avascript:alert(1)"]) {
      for (const [href] of linksOf(parseInline(md))) expect(href, md).toMatch(/^https:\/\//);
    }
  });

  it("before the table is loaded, a named entity in a bare URL stays as written", async () => {
    vi.resetModules();
    const fresh = await import("./markdown");
    expect(fresh.entitiesLoaded()).toBe(false);
    expect(linksOf(fresh.parseInline("https://a.example/?a=1&amp;b=2&#38;c"))).toEqual([["https://a.example/?a=1&amp;b=2&c", "https://a.example/?a=1&amp;b=2&c"]]);
    await fresh.loadEntities();
    expect(linksOf(fresh.parseInline("https://a.example/?a=1&amp;b=2&#38;c"))).toEqual([["https://a.example/?a=1&b=2&c", "https://a.example/?a=1&b=2&c"]]);
  });

  it("keeps a written link right after a bare URL", () => {
    expect(parseInline("https://a.example/[x](https://b.example)")).toEqual([link("https://a.example/", "https://a.example/"), link("https://b.example/", "x")]);
    expect(parseInline('https://a.example/path[x y](https://b.example "t") z')).toEqual([
      link("https://a.example/path", "https://a.example/path"),
      link("https://b.example/", "x y"),
      text(" z"),
    ]);
    expect(parseInline("www.a.example/![Bild](x.png)")).toEqual([link("http://www.a.example/", "www.a.example/"), { t: "plain", c: [text("Bild")] }]);
    // A written link with a refused destination: its text shows.
    expect(parseInline("https://a.example/[x](javascript:alert(1))")).toEqual([link("https://a.example/", "https://a.example/"), { t: "plain", c: [text("x")] }]);
    // Brackets that are no written link stay in the URL.
    for (const md of ["https://a.example/[x]", "https://a.example/[x](", "https://a.example/[x] (y)", "https://a.example/a[1]b"]) {
      const links = linksOf(parseInline(md));
      expect(links.length, md).toBeGreaterThanOrEqual(1);
      expect(links[0]![1].length, md).toBeGreaterThan("https://a.example/".length);
    }
  });

  it("ends a URL at a ' in its host", () => {
    expect(parseInline("https://s.example's page")).toEqual([link("https://s.example/", "https://s.example"), text("'s page")]);
    expect(parseInline("www.s.example’s")).toEqual([link("http://www.s.example/", "www.s.example"), text("’s")]);
    expect(parseInline("https://s.example:8080's")).toEqual([link("https://s.example:8080/", "https://s.example:8080"), text("'s")]);
    // After the host, a ' belongs to the path as before.
    expect(linksOf(parseInline("https://a.example/it's/x"))).toEqual([["https://a.example/it's/x", "https://a.example/it's/x"]]);
    expect(linksOf(parseInline("https://a.example?q=it's"))).toEqual([["https://a.example/?q=it%27s", "https://a.example?q=it's"]]);
  });

  it("a quote in the user info does not end the URL; one in the host after it does", () => {
    expect(parseInline("https://o'brien@a.example/")).toEqual([link("https://o'brien@a.example/", "https://o'brien@a.example/")]);
    expect(parseInline("https://o’brien:pw@a.example/x")).toEqual([link("https://o%E2%80%99brien:pw@a.example/x", "https://o’brien:pw@a.example/x")]);
    expect(parseInline("https://o'brien@s.example's")).toEqual([link("https://o'brien@s.example/", "https://o'brien@s.example"), text("'s")]);
    expect(parseInline("https://a'b@c'd@e.example/")).toEqual([link("https://a'b%40c'd@e.example/", "https://a'b@c'd@e.example/")]);
    // Ends at the first quote after the last "@", whatever follows in the host.
    expect(parseInline("https://s.example's'x/y")).toEqual([link("https://s.example/", "https://s.example"), text("'s'x/y")]);
  });

  it("a quote or @ written as an entity in the host counts as typed", () => {
    expect(parseInline("https://s.example&rsquo;s page")).toEqual([link("https://s.example/", "https://s.example"), text("’s page")]);
    expect(parseInline("https://s.example&#39;s")).toEqual([link("https://s.example/", "https://s.example"), text("'s")]);
    expect(parseInline("www.s.example&apos;s")).toEqual([link("http://www.s.example/", "www.s.example"), text("'s")]);
    expect(parseInline("https://o&#39;brien&#64;a.example/")).toEqual([link("https://o'brien@a.example/", "https://o'brien@a.example/")]);
    expect(parseInline("https://o&#39;brien@a.example/")).toEqual([link("https://o'brien@a.example/", "https://o'brien@a.example/")]);
    // After the host it is part of the URL, decoded.
    expect(linksOf(parseInline("https://a.example/it&#39;s"))).toEqual([["https://a.example/it's", "https://a.example/it's"]]);
  });

  it("keeps a complete entity at the end of a URL and decodes it, unless it stands for what would come off", () => {
    const cases: [string, string, string][] = [
      ["https://a.example/id&#49;", "https://a.example/id1", "https://a.example/id1"],
      ["https://a.example/id&#x31;.", "https://a.example/id1", "https://a.example/id1"],
      ["https://a.example/M&uuml;ller", "https://a.example/M%C3%BCller", "https://a.example/Müller"],
      ["https://a.example/?a=1&amp;", "https://a.example/?a=1&", "https://a.example/?a=1&"],
      ["https://a.example/&#x1F600;", "https://a.example/%F0%9F%98%80", "https://a.example/😀"],
      // What comes off: punctuation, closing brackets, address-ending characters, no entity.
      ["https://a.example/x&#41;", "https://a.example/x", "https://a.example/x"],
      ["https://a.example/x&rpar;&#93;&#125;", "https://a.example/x", "https://a.example/x"],
      ["https://a.example/x&#46;&period;&excl;&#39;", "https://a.example/x", "https://a.example/x"],
      ["https://a.example/x&gt;&quot;&#32;&nbsp;&ThickSpace;&#x202E;", "https://a.example/x", "https://a.example/x"],
      ["https://a.example/x&foo;", "https://a.example/x", "https://a.example/x"],
      ["https://a.example/x&#;", "https://a.example/x&#", "https://a.example/x&#"],
      // A kept entity before one that comes off.
      ["https://a.example/id&#49;&#41;", "https://a.example/id1", "https://a.example/id1"],
    ];
    for (const [md, href, v] of cases) expect(linksOf(parseInline(md)), md).toEqual([[href, v]]);
    // What came off shows as text, decoded.
    expect(parseInline("(https://a.example/x&#41;")).toEqual([text("("), link("https://a.example/x", "https://a.example/x"), text(")")]);
    expect(parseInline("https://a.example/x&#41;")).toEqual([link("https://a.example/x", "https://a.example/x"), text(")")]);
    // mailto: keeps its entities as written.
    expect(linksOf(parseInline("mailto:a@b.example?x=&#49;"))).toEqual([["mailto:a@b.example?x=&#49;", "mailto:a@b.example?x=&#49;"]]);
  });

  it("does not link an entity-encoded e-mail address", () => {
    for (const md of ["a&#64;b.example", "a&commat;b.example", "a&#x40;b.example"]) {
      expect(linksOf(parseInline(md)), md).toEqual([]);
    }
    expect(parseInline("a&#64;b.example")).toEqual([text("a@b.example")]);
  });

  it("counts letters outside the basic plane as word characters", () => {
    expect(linksOf(parseInline("x𝐚@example.com"))).toEqual([["mailto:x%F0%9D%90%9A@example.com", "x𝐚@example.com"]]);
    expect(linksOf(parseInline("𠀀@example.com"))).toEqual([["mailto:%F0%A0%80%80@example.com", "𠀀@example.com"]]);
    // An address does not start in the middle of a word.
    expect(linksOf(parseInline("𝐚https://a.example"))).toEqual([]);
    expect(linksOf(parseInline("𝐚www.a.example"))).toEqual([]);
    // Not a letter (an emoji) or a lone surrogate: the address starts after it.
    expect(linksOf(parseInline("😀a@example.com"))).toEqual([["mailto:a@example.com", "a@example.com"]]);
    expect(linksOf(parseInline("\ud835a@example.com"))).toEqual([["mailto:a@example.com", "a@example.com"]]);
    expect(linksOf(parseInline("a\udc00@example.com"))).toEqual([]);
  });

  it("the entity table is not in the main bundle: markdown.ts imports it only dynamically", () => {
    expect(markdownSource).not.toMatch(/^\s*import\s[^(]*["']\.\/htmlEntities/m);
    expect(markdownSource).toContain('import("./htmlEntities")');
  });

  it("needsEntities: whether a text has a named entity", () => {
    for (const md of ["&amp;", "a &uuml; b", "&x;", "&" + "a".repeat(32) + ";", "& &a;"]) expect(needsEntities(md), md).toBe(true);
    for (const md of ["", "a & b", "&;", "&#228;", "&#x1F;", "&amp", "& amp;", "&" + "a".repeat(33) + ";", "&a-b;"]) expect(needsEntities(md), md).toBe(false);
    expect(entitiesLoaded()).toBe(true);
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
      ["htmlEntities.ts", entitiesSource],
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
    // Bare addresses and entities (#131).
    ["bare URLs", repeat("https://a.example/x, ")],
    ["one bare URL", "https://a.example/" + "a".repeat(size - 18)],
    ["URL starts without a host", repeat("http://")],
    ["URL starts after punctuation", repeat("(http://")],
    ["www starts", repeat("www.")],
    ["www without a dot", repeat("www.a ")],
    ["trailing punctuation", "https://a.example/" + ".".repeat(size - 18)],
    ["trailing entities", "https://a.example/" + "&amp;".repeat(Math.ceil((size - 18) / 5))],
    ["trailing semicolons", "https://a.example/" + "a;".repeat((size - 18) / 2)],
    ["unopened brackets", repeat("https://a.example/)")],
    ["nested brackets", "https://a.example/" + "(".repeat((size - 18) / 2) + ")".repeat((size - 18) / 2)],
    ["e-mail addresses", repeat("a@b.example ")],
    ["local parts without a domain", repeat("a.b_c@")],
    ["underscores before one @", "a_".repeat(size / 2 - 15) + "@" + "b".repeat(30)],
    ["underscores before an @ without a domain", "a_".repeat(size / 2 - 15) + "@" + "b-".repeat(15)],
    ["at signs", repeat("@")],
    ["long domain without a dot", "a@" + "b".repeat(size - 2)],
    ["dots", repeat("a.")],
    ["bare addresses inside a link's text", "[" + repeat("www.a.example ").slice(0, size - 20) + "](https://a.example)"],
    ["bare addresses inside link texts", repeat("[a@b.example](https://a.example) ")],
    ["entities", repeat("&amp;&#228;&#x1F600;")],
    ["entity starts", repeat("&")],
    ["long entity names", repeat("&" + "a".repeat(40))],
    // Entities in bare URLs, written links after URLs, hosts with ', astral letters (#134).
    ["entities in one bare URL", "https://a.example/?" + "a=1&amp;".repeat(Math.ceil((size - 19) / 8))],
    ["entities in bare URLs", repeat("https://a.example/?a=1&amp;b=&#38;&#x26; ")],
    ["entity starts in one bare URL", "https://a.example/" + "&".repeat(size - 18)],
    ["long entity names in one bare URL", "https://a.example/" + ("&" + "a".repeat(31)).repeat(Math.ceil((size - 18) / 32))],
    ["entities that end an address in bare URLs", repeat("https://a.example/&#32;&lt; ")],
    ["written links after bare URLs", repeat("https://a.example/[x](https://b.example)")],
    ["brackets in one bare URL", "https://a.example/" + "[".repeat(size - 18)],
    ["bracket pairs without a destination in one bare URL", "https://a.example/" + "[x]".repeat(Math.ceil((size - 18) / 3))],
    ["bracket pairs and unclosed destinations in one bare URL", "https://a.example/" + "[x](".repeat(Math.ceil((size - 18) / 4))],
    ["nested brackets around one bare URL's written link", "https://a.example/" + "[".repeat((size - 40) / 2) + "x](https://b.example)" + "]".repeat((size - 40) / 2)],
    ["one long written link after a bare URL", "https://a.example/[x](https://b.example/" + "a".repeat(size - 40) + ")"],
    ["hosts with '", repeat("https://s.example's ")],
    ["quotes in one host", "https://" + "'".repeat(size - 8)],
    ["astral letters before one @", "𝐚".repeat(size / 2 - 5) + "@b.example"],
    ["astral letters and @", repeat("𝐚@")],
    ["astral letters", repeat("𝐚")],
    ["lone surrogates", repeat("\ud835@")],
    ["entity-encoded e-mail addresses", repeat("a&#64;b.example ")],
    ["quotes in user info", "https://" + "a'@".repeat(Math.ceil((size - 8) / 3))],
    ["quotes then one @ in a host", "https://" + "'".repeat(size - 20) + "@a.example/"],
    ["quote entities in one host", "https://" + "&#39;".repeat(Math.ceil((size - 8) / 5))],
    ["@ entities in one host", "https://" + "a&#64;".repeat(Math.ceil((size - 8) / 6))],
    ["long entity names in one host", "https://" + ("&" + "a".repeat(31)).repeat(Math.ceil((size - 8) / 32))],
    ["hosts with quote entities", repeat("https://s.example&rsquo;s ")],
    ["kept entities at the ends of URLs", repeat("https://a.example/id&#49; ")],
    ["trailing entities that come off", "https://a.example/x" + "&#41;".repeat(Math.ceil((size - 19) / 5))],
    ["trailing numeric entities that are kept", "https://a.example/" + "&#49;".repeat(Math.ceil((size - 18) / 5))],
    ["trailing hex entities that come off", "https://a.example/" + "&#x2E;".repeat(Math.ceil((size - 18) / 6))],
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

  it("needsEntities: at most 40 steps per character", () => {
    for (const md of [repeat("&"), repeat("&" + "a".repeat(31)), repeat("&" + "a".repeat(40)), repeat("a")]) {
      const steps: Steps = { n: 0 };
      expect(needsEntities(md, steps)).toBe(false);
      expect(steps.n).toBeLessThanOrEqual(40 * md.length);
    }
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
