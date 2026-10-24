// The notes' Markdown (#12, part 5): the subset the app renders
// (pklnx/document-archive, Services/MarkdownNotes.swift), so a note looks
// the same in the app and here. The blocks follow that file line by line:
// headings # to ### (deeper ones shown as level 3), paragraphs whose lines
// stay separate lines, bullet, numbered and read-only task items with an
// indentation level (two spaces or a tab per level, at most 6), quotes,
// code fences of ``` or ~~~ (verbatim, no formatting or links inside) and
// rules. Inside a block: emphasis, strong, strikethrough, inline code and
// links, as the app's inline parser does; an image shows its alt text.
// Entities (`&amp;`, `&#228;`) show as their character in text; the
// named ones once their table is loaded (`loadEntities`, a chunk of its
// own). Bare addresses become links as the app's link detector makes
// them (#131): `http://`, `https://`, `mailto:`, `tel:` and `www.`
// addresses and e-mail addresses, never inside inline code or a link.
// An address ends at white space or `<>"\``, at a ")", "]" or "}" it did
// not open, before a written link (`[x](url)`), and without trailing
// punctuation (`.,:;!?*_~'` and the like).
//
// Safety: the result is React elements built with createElement, never an
// HTML string, so any text stays text. A link is made only from an
// absolute URL that `new URL` parses and whose protocol is http:, https:,
// mailto: or tel: (the app's schemes); anything else shows its link text.
// A bare address goes through the same check, and it is found in the text
// as written: an entity never makes a URL or its scheme. Only after that
// are the entities in a bare http(s) or www. URL decoded (#134), and the
// decoded URL goes through the check again.
// The browser's URL parser drops tabs and line breaks anywhere and control
// characters at the ends, so the check runs on what it parses, not on the
// text as written.
//
// Speed: one pass over the lines, and per block a few linear pre-passes
// plus one left-to-right scan; no regular expression that can backtrack
// (the only ones test a single character). Every loop step counts one
// `steps` tick, so a test can bound the work for any input.
import { createElement, Fragment, type ReactNode } from "react";

/**
 * The HTML named character references, once loaded (#134): the table is
 * about 12 kB gzip, so it is a chunk of its own, loaded only when a note
 * has a `&name;`. Until then `&name;` shows as written; numeric entities
 * need no table.
 */
let namedEntities: Readonly<Record<string, string>> | null = null;
let entitiesLoading: Promise<void> | null = null;

/** Whether the named entity table is loaded. */
export const entitiesLoaded = () => namedEntities !== null;

/**
 * Loads the named entity table (once; a failed load can be tried again).
 * Notes rendered before it resolves show `&name;` as written.
 */
export function loadEntities(): Promise<void> {
  if (namedEntities) return Promise.resolve();
  entitiesLoading ??= import("./htmlEntities").then(
    (m) => {
      namedEntities = m.namedEntities;
    },
    (err: unknown) => {
      entitiesLoading = null;
      throw err;
    },
  );
  return entitiesLoading;
}

/**
 * Whether the text has something that looks like a named entity
 * (`&` and 1 to 32 letters or digits, then `;`), so the table is needed.
 * Linear: each "&" reads at most 33 characters.
 */
export function needsEntities(text: string, steps: Steps = { n: 0 }): boolean {
  for (let i = text.indexOf("&"); i >= 0; i = text.indexOf("&", i + 1)) {
    steps.n++;
    let j = i + 1;
    while (j - i - 1 < 32 && isAsciiAlnum(text[j])) {
      steps.n++;
      j++;
    }
    if (j > i + 1 && text[j] === ";") return true;
  }
  return false;
}

/** The protocols a link may have (the app's `linkSchemes`). */
const linkProtocols = new Set(["http:", "https:", "mailto:", "tel:"]);

/**
 * The href for a link to `raw`, or null when it is not one: not an
 * absolute URL, or another protocol (javascript:, data:, file:, …).
 */
export function safeHref(raw: string): string | null {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return null;
  }
  return linkProtocols.has(url.protocol) ? url.href : null;
}

export type Inline =
  | { t: "text"; v: string }
  | { t: "code"; v: string }
  | { t: "em" | "strong" | "del"; c: Inline[] }
  /** A link: href is already checked by safeHref. */
  | { t: "link"; href: string; c: Inline[] }
  /** The text of a link that is not allowed, or an image's alt text. */
  | { t: "plain"; c: Inline[] };

export type Block =
  | { kind: "heading"; level: 1 | 2 | 3; text: Inline[] }
  | { kind: "paragraph" | "quote"; text: Inline[] }
  | { kind: "bullet"; level: number; text: Inline[] }
  | { kind: "numbered"; marker: string; level: number; text: Inline[] }
  | { kind: "task"; checked: boolean; level: number; text: Inline[] }
  | { kind: "code"; text: string }
  | { kind: "rule" };

/** The work counter: one tick per loop step. */
export interface Steps {
  n: number;
}

// ---- Characters

/** Swift's `.whitespaces`: horizontal white space (trimming a line). */
const isHSpace = (c: string) => c === " " || c === "\t" || (c > "\x7f" && /^\p{Zs}$/u.test(c));
/** Regex `\s` and Character.isWhitespace: any white space, line breaks included. */
const isWhite = (c: string | undefined) => c !== undefined && (c === " " || c === "\t" || c === "\n" || /^\s$/u.test(c));
const asciiPunct = "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~";
const isPunct = (c: string | undefined) => c !== undefined && (asciiPunct.includes(c) || (c > "\x7f" && /^[\p{P}\p{S}]$/u.test(c)));
const isDigit = (c: string | undefined) => c !== undefined && c >= "0" && c <= "9";
const isAsciiAlnum = (c: string | undefined) => c !== undefined && ((c >= "a" && c <= "z") || (c >= "A" && c <= "Z") || (c >= "0" && c <= "9"));
const isHex = (c: string | undefined) => c !== undefined && ((c >= "a" && c <= "f") || (c >= "A" && c <= "F") || (c >= "0" && c <= "9"));
const wordRe = /^[\p{L}\p{N}\p{M}]$/u;
/**
 * Whether s[i] is part of a letter, digit or combining mark: what an
 * address is made of. Both halves of a surrogate pair count when the pair
 * is one (`𝐚`, CJK Extension B, #134); a lone surrogate does not.
 */
function isWordAt(s: string, i: number): boolean {
  const c = s[i];
  if (c === undefined) return false;
  if (isAsciiAlnum(c)) return true;
  if (c <= "\x7f") return false;
  const u = c.charCodeAt(0);
  if (u >= 0xd800 && u <= 0xdbff) {
    const lo = s.charCodeAt(i + 1);
    return lo >= 0xdc00 && lo <= 0xdfff && wordRe.test(s.slice(i, i + 2));
  }
  if (u >= 0xdc00 && u <= 0xdfff) {
    const hi = s.charCodeAt(i - 1);
    return hi >= 0xd800 && hi <= 0xdbff && wordRe.test(s.slice(i - 1, i + 1));
  }
  return wordRe.test(c);
}
/** The local part of an e-mail address. */
const isLocalAt = (s: string, i: number) => isWordAt(s, i) || s[i] === "." || s[i] === "-" || s[i] === "_" || s[i] === "+";
/** A domain's labels (between the dots). */
const isDomainAt = (s: string, i: number) => isWordAt(s, i) || s[i] === "-" || s[i] === "_";

// ---- Blocks

/** The blocks of a note. Never fails: what is not Markdown shows as written. */
export function parseNotes(markdown: string, steps: Steps = { n: 0 }): Block[] {
  const out: Block[] = [];
  let paragraph: string[] = [];
  let quote: string[] = [];
  let code: string[] = [];
  let fence: string | null = null;

  const flushParagraph = () => {
    if (paragraph.length === 0) return;
    out.push({ kind: "paragraph", text: parseInline(paragraph.join("\n"), steps) });
    paragraph = [];
  };
  const flushQuote = () => {
    if (quote.length === 0) return;
    out.push({ kind: "quote", text: parseInline(quote.join("\n"), steps) });
    quote = [];
  };
  const closeFence = () => {
    out.push({ kind: "code", text: code.join("\n") });
    code = [];
    fence = null;
  };

  for (const raw of lines(markdown, steps)) {
    const line = trim(raw, steps);
    if (fence !== null) {
      if (line.startsWith(fence)) closeFence();
      else code.push(raw);
      continue;
    }
    const marker = fenceMarker(line);
    if (marker) {
      flushParagraph();
      flushQuote();
      fence = marker;
      continue;
    }
    if (line === "") {
      flushParagraph();
      flushQuote();
      continue;
    }
    if (line[0] === ">") {
      flushParagraph();
      // `^>\s?(.*)$`: one white space after the marker belongs to it.
      quote.push(line.slice(isWhite(line[1]) ? 2 : 1));
      continue;
    }
    const block = singleLineBlock(line, raw, steps);
    if (block) {
      flushParagraph();
      flushQuote();
      out.push(block);
      continue;
    }
    flushQuote();
    paragraph.push(line);
  }
  // An unclosed fence runs to the end, as in CommonMark.
  if (fence !== null) closeFence();
  flushParagraph();
  flushQuote();
  return out;
}

/** The lines of a text; "\r\n" and "\r" count as one line break. */
function lines(text: string, steps: Steps): string[] {
  const out: string[] = [];
  let start = 0;
  for (let i = 0; i < text.length; i++) {
    steps.n++;
    const c = text[i];
    if (c === "\n" || c === "\r") {
      out.push(text.slice(start, i));
      if (c === "\r" && text[i + 1] === "\n") i++;
      start = i + 1;
    }
  }
  out.push(text.slice(start));
  return out;
}

function trim(line: string, steps: Steps): string {
  let a = 0;
  let b = line.length;
  while (a < b && isHSpace(line[a]!)) {
    steps.n++;
    a++;
  }
  while (b > a && isHSpace(line[b - 1]!)) {
    steps.n++;
    b--;
  }
  return line.slice(a, b);
}

/** "```" or "~~~" when the trimmed line opens (or closes) a fence. */
function fenceMarker(line: string): string | null {
  if (line.startsWith("```")) return "```";
  if (line.startsWith("~~~")) return "~~~";
  return null;
}

/** "---", "***", "___": three or more of one mark, white space allowed. */
function isRule(line: string, steps: Steps): boolean {
  let first = "";
  let count = 0;
  for (const c of line) {
    steps.n++;
    if (isWhite(c)) continue;
    if (first === "") {
      if (c !== "-" && c !== "*" && c !== "_") return false;
      first = c;
    } else if (c !== first) return false;
    count++;
  }
  return count >= 3;
}

/** Two spaces (a tab counts as two) per level, at most 6. */
function indentLevel(raw: string, steps: Steps): number {
  let spaces = 0;
  for (let i = 0; i < raw.length && (raw[i] === " " || raw[i] === "\t"); i++) {
    steps.n++;
    spaces += raw[i] === "\t" ? 2 : 1;
  }
  return Math.min(Math.floor(spaces / 2), 6);
}

/** The index after the white space starting at i. */
function skipWhite(s: string, i: number, steps: Steps): number {
  while (i < s.length && isWhite(s[i])) {
    steps.n++;
    i++;
  }
  return i;
}

/** Rules, headings and list items (one line each); null for paragraph text. */
function singleLineBlock(line: string, raw: string, steps: Steps): Block | null {
  if (isRule(line, steps)) return { kind: "rule" };
  const h = heading(line, steps);
  if (h) return { kind: "heading", level: h.level, text: parseInline(h.text, steps) };
  const c = line[0]!;
  if ((c === "-" || c === "*" || c === "+") && isWhite(line[1])) {
    const level = indentLevel(raw, steps);
    const j = skipWhite(line, 1, steps);
    // `^[-*+]\s+\[([ xX])\]\s+(.*)$`; the line is trimmed, so white space
    // after the "]" is followed by text.
    const box = line[j + 1];
    if (line[j] === "[" && (box === " " || box === "x" || box === "X") && line[j + 2] === "]" && isWhite(line[j + 3])) {
      return { kind: "task", checked: box !== " ", level, text: parseInline(line.slice(skipWhite(line, j + 3, steps)), steps) };
    }
    return { kind: "bullet", level, text: parseInline(line.slice(j), steps) };
  }
  // `^(\d{1,9}[.)])\s+(.*)$`
  let d = 0;
  while (isDigit(line[d])) {
    steps.n++;
    d++;
  }
  if (d >= 1 && d <= 9 && (line[d] === "." || line[d] === ")") && isWhite(line[d + 1])) {
    return { kind: "numbered", marker: line.slice(0, d + 1), level: indentLevel(raw, steps), text: parseInline(line.slice(skipWhite(line, d + 1, steps)), steps) };
  }
  return null;
}

/**
 * `^(#{1,6})\s+(.*?)(\s+#+)?$` on a trimmed line: the level and the text
 * without a closing run of "#" that follows white space.
 */
function heading(line: string, steps: Steps): { level: 1 | 2 | 3; text: string } | null {
  let i = 0;
  while (line[i] === "#") {
    steps.n++;
    i++;
  }
  if (i === 0 || i > 6 || !isWhite(line[i])) return null;
  const j = skipWhite(line, i, steps);
  let end = line.length;
  let k = end;
  while (k > j && line[k - 1] === "#") {
    steps.n++;
    k--;
  }
  if (k < end) {
    if (k === j) {
      // Only "#" after the marker: a closing run needs white space of its
      // own, beyond the one the marker needs.
      if (j - i >= 2) end = j;
    } else if (isWhite(line[k - 1])) {
      end = k;
      while (end > j && isWhite(line[end - 1])) {
        steps.n++;
        end--;
      }
    }
  }
  return { level: Math.min(i, 3) as 1 | 2 | 3, text: line.slice(j, end) };
}

// ---- Inline text

/** A piece of the inline text: text (also a delimiter run) or a finished node. */
interface Tok {
  text: string;
  node: Inline | null;
  /** The node's nesting depth (1 for a node without nodes inside). */
  depth?: number;
  prev: Tok | null;
  next: Tok | null;
}

/**
 * The deepest nesting of inline nodes (emphasis, links, images). A node
 * that would be deeper keeps its own formatting but shows its content as
 * plain text, so rendering never recurses deeper than this, whatever the
 * input (`*a *a … b* b*` thousands deep would overflow the stack).
 */
export const maxInlineDepth = 16;

/** The text of inline nodes, without their formatting. */
function plainText(nodes: Inline[], steps: Steps): string {
  let out = "";
  for (const node of nodes) {
    steps.n++;
    out += node.t === "text" || node.t === "code" ? node.v : plainText(node.c, steps);
  }
  return out;
}

/** A run of "*", "_" or "~~" that may open or close emphasis. */
interface Delim {
  tok: Tok;
  ch: string;
  count: number;
  orig: number;
  open: boolean;
  close: boolean;
  prev: Delim | null;
  next: Delim | null;
}

/** A "[" or "![" waiting for its "]". */
interface Bracket {
  tok: Tok;
  image: boolean;
  seq: number;
  /** The last delimiter before the bracket: emphasis inside the link is processed above it. */
  delimBefore: Delim | null;
  /** The number of bare links made before the bracket. */
  bareAt: number;
  prev: Bracket | null;
}

/**
 * The inline Markdown of one block: CommonMark's inline rules for code
 * spans, links, images, emphasis, entities and (GFM) "~~" strikethrough,
 * without HTML and reference links (they stay text as typed), and bare
 * addresses linked as the app's link detector does.
 */
export function parseInline(s: string, steps: Steps = { n: 0 }): Inline[] {
  const n = s.length;
  const pre = prepass(s, steps);

  const head: Tok = { text: "", node: null, prev: null, next: null };
  let tail = head;
  const append = (tok: Tok) => {
    tok.prev = tail;
    tail.next = tok;
    tail = tok;
  };
  const unlink = (tok: Tok) => {
    if (tok.prev) tok.prev.next = tok.next;
    if (tok.next) tok.next.prev = tok.prev;
    if (tail === tok) tail = tok.prev!;
  };
  let buf = "";
  const flush = () => {
    if (buf === "") return;
    append({ text: buf, node: null, prev: null, next: null });
    buf = "";
  };
  const addNode = (node: Inline, depth = 1) => {
    flush();
    append({ text: "", node, depth, prev: null, next: null });
  };

  let delimHead: Delim | null = null;
  let delimTail: Delim | null = null;
  const removeDelim = (d: Delim) => {
    if (d.prev) d.prev.next = d.next;
    else delimHead = d.next;
    if (d.next) d.next.prev = d.prev;
    else delimTail = d.prev;
  };

  let brackets: Bracket | null = null;
  let seq = 0;
  /** The links made from bare addresses: a [text](url) link shows them as text. */
  const bareNodes = new Set<Inline>();
  /** No bare address starts before this index: the text up to it was read as one already. */
  let urlFloor = 0;
  /** The end of the e-mail domain after each "@" (-1: none), read once per "@". */
  const domains = new Map<number, number>();

  /**
   * The bare address starting at i, as a link: the app's detector finds
   * them in the text outside code and links. Every scan here starts at a
   * new index past the last one (urlFloor) or reads a pre-pass entry, so
   * the work stays linear.
   */
  const bareLink = (i: number): { node: Inline; end: number } | null => {
    const prefix = i >= urlFloor ? barePrefix(s, i) : null;
    // What follows the prefix: a host, a phone number or an e-mail address.
    const k = i + (prefix?.length ?? 0);
    const starts =
      prefix === "tel:" ? isDigit(s[k]) || (s[k] === "+" && isDigit(s[k + 1])) : prefix === "mailto:" ? isLocalAt(s, k) : isWordAt(s, k);
    if (prefix && starts) {
      const r = bareAddress(s, i, prefix.length, pre, steps);
      urlFloor = r.stop;
      const raw = s.slice(i, r.end);
      // Found and checked as written; then (http, https and www. only,
      // #134) entities in it are decoded and the result is checked again,
      // so a URL copied from an HTML e-mail (`&amp;`) works. The prefix
      // was matched before, so an entity never makes a URL or a scheme.
      const url = prefix === "mailto:" || prefix === "tel:" ? raw : decodeUrlEntities(raw, steps);
      const href = prefix !== "www." ? safeHref(url) : domainEnd(s, k, r.end, steps) >= 0 ? safeHref("http://" + url) : null;
      if (href) return { node: { t: "link", href, c: [{ t: "text", v: url }] }, end: r.end };
    }
    // An e-mail address: the local part, "@" and a domain with a dot.
    const at = pre.localEnd[i]!;
    if (s[at] !== "@") return null;
    let end = domains.get(at);
    if (end === undefined) {
      end = domainEnd(s, at + 1, s.length, steps);
      domains.set(at, end);
    }
    if (end < 0) return null;
    const address = s.slice(i, end);
    const href = safeHref("mailto:" + address);
    return href ? { node: { t: "link", href, c: [{ t: "text", v: address }] }, end } : null;
  };

  /** Nodes with their bare links turned back into text (inside a written link). */
  const unlinkBare = (nodes: Inline[]): Inline[] => {
    const out: Inline[] = [];
    const push = (node: Inline) => {
      const last = out[out.length - 1];
      if (node.t === "text" && last?.t === "text") out[out.length - 1] = { t: "text", v: last.v + node.v };
      else out.push(node);
    };
    for (const node of nodes) {
      steps.n++;
      if (bareNodes.has(node) && node.t === "link") push({ t: "text", v: plainText(node.c, steps) });
      else if (node.t === "text" || node.t === "code") push(node);
      else {
        node.c = unlinkBare(node.c);
        push(node);
      }
    }
    return out;
  };
  /** "[" openers with a smaller seq are inactive: links cannot hold links. */
  let linkFloor = 0;

  /** The tokens after `from` and before `stop` (null: to the end), unlinked, as inline nodes. */
  /** The deepest node among the tokens the last collect took. */
  let collectedDepth = 0;
  const collect = (from: Tok, stop: Tok | null): Inline[] => {
    const out: Inline[] = [];
    collectedDepth = 0;
    let tok = from.next;
    while (tok && tok !== stop) {
      steps.n++;
      if (tok.node) {
        out.push(tok.node);
        collectedDepth = Math.max(collectedDepth, tok.depth ?? 1);
      }
      else if (tok.text !== "") {
        const last = out[out.length - 1];
        if (last?.t === "text") last.v += tok.text;
        else out.push({ t: "text", v: tok.text });
      }
      tok = tok.next;
    }
    from.next = tok;
    if (tok) tok.prev = from;
    else tail = from;
    return out;
  };

  /**
   * The children of a new node taken by the last collect, and the node's
   * depth: beyond maxInlineDepth the children become their plain text.
   */
  const nest = (children: Inline[]): { c: Inline[]; depth: number } => {
    if (collectedDepth + 1 <= maxInlineDepth) return { c: children, depth: collectedDepth + 1 };
    return { c: [{ t: "text", v: plainText(children, steps) }], depth: 1 };
  };

  /** CommonMark's "process emphasis" over the delimiters above `bottom`. */
  const processEmphasis = (bottom: Delim | null) => {
    const openersBottom = new Map<string, Delim | null>();
    let closer = bottom ? bottom.next : delimHead;
    while (closer) {
      steps.n++;
      if (!closer.close) {
        closer = closer.next;
        continue;
      }
      const key = `${closer.ch}${closer.open ? 1 : 0}${closer.orig % 3}`;
      const floor = openersBottom.has(key) ? openersBottom.get(key)! : bottom;
      let opener = closer.prev;
      let found: Delim | null = null;
      while (opener && opener !== bottom && opener !== floor) {
        steps.n++;
        if (opener.open && opener.ch === closer.ch && !oddMatch(opener, closer)) {
          found = opener;
          break;
        }
        opener = opener.prev;
      }
      if (!found) {
        openersBottom.set(key, closer.prev);
        const next: Delim | null = closer.next;
        if (!closer.open) removeDelim(closer);
        closer = next;
        continue;
      }
      const use = closer.ch === "~" ? 2 : found.count >= 2 && closer.count >= 2 ? 2 : 1;
      const inner = nest(collect(found.tok, closer.tok));
      const node: Inline = { t: closer.ch === "~" ? "del" : use === 2 ? "strong" : "em", c: inner.c };
      const nodeTok: Tok = { text: "", node, depth: inner.depth, prev: found.tok, next: found.tok.next };
      if (found.tok.next) found.tok.next.prev = nodeTok;
      found.tok.next = nodeTok;
      if (tail === found.tok) tail = nodeTok;
      // The delimiters between are text now.
      for (let d = found.next; d && d !== closer; d = d.next) {
        steps.n++;
        removeDelim(d);
      }
      found.count -= use;
      found.tok.text = found.tok.text.slice(use);
      closer.count -= use;
      closer.tok.text = closer.tok.text.slice(use);
      if (found.count === 0) {
        unlink(found.tok);
        removeDelim(found);
      }
      if (closer.count === 0) {
        const next: Delim | null = closer.next;
        unlink(closer.tok);
        removeDelim(closer);
        closer = next;
      }
    }
    // Everything above bottom is done.
    if (bottom) {
      bottom.next = null;
      delimTail = bottom;
    } else {
      delimHead = null;
      delimTail = null;
    }
  };

  let i = 0;
  while (i < n) {
    steps.n++;
    const c = s[i]!;
    if (c === "\\") {
      const next = s[i + 1];
      if (next !== undefined && (asciiPunct.includes(next) || next === "\n")) {
        buf += next;
        i += 2;
      } else {
        buf += c;
        i++;
      }
      continue;
    }
    if (c === "`") {
      let end = i;
      while (s[end] === "`") {
        steps.n++;
        end++;
      }
      const len = end - i;
      const close = pre.nextTickRun(len, end);
      if (close < 0) {
        buf += s.slice(i, end);
        i = end;
        continue;
      }
      let v = s.slice(end, close).split("\n").join(" ");
      if (v.length >= 2 && v[0] === " " && v[v.length - 1] === " " && v.trim() !== "") v = v.slice(1, -1);
      steps.n += close - end;
      addNode({ t: "code", v });
      i = close + len;
      continue;
    }
    if (c === "<") {
      const auto = autolink(s, i, steps);
      if (auto) {
        const href = safeHref(auto.url);
        if (href) addNode({ t: "link", href, c: [{ t: "text", v: auto.url }] });
        else buf += auto.url;
        i = auto.end;
        continue;
      }
      buf += c;
      i++;
      continue;
    }
    if (c === "[" || (c === "!" && s[i + 1] === "[")) {
      flush();
      const image = c === "!";
      const tok: Tok = { text: image ? "![" : "[", node: null, prev: null, next: null };
      append(tok);
      brackets = { tok, image, seq: ++seq, delimBefore: delimTail, bareAt: bareNodes.size, prev: brackets };
      i += image ? 2 : 1;
      continue;
    }
    if (c === "]") {
      const opener: Bracket | null = brackets;
      if (!opener) {
        buf += c;
        i++;
        continue;
      }
      brackets = opener.prev;
      const dest = opener.image || opener.seq > linkFloor ? destination(s, i + 1, pre, steps) : null;
      if (!dest) {
        // Not a link: the bracket is text.
        buf += c;
        i++;
        continue;
      }
      flush();
      processEmphasis(opener.delimBefore);
      const inner = nest(collect(opener.tok, null));
      unlink(opener.tok);
      const href = opener.image ? null : safeHref(dest.url);
      // Links do not hold links: an address in the text stays text (the
      // app keeps a written link's destination). Links do not nest, so
      // every node is walked here at most once.
      if (href && bareNodes.size > opener.bareAt) inner.c = unlinkBare(inner.c);
      addNode(href ? { t: "link", href, c: inner.c } : { t: "plain", c: inner.c }, inner.depth);
      if (!opener.image) linkFloor = seq;
      i = dest.end + 1;
      continue;
    }
    if (c === "*" || c === "_" || c === "~") {
      let end = i;
      while (s[end] === c) {
        steps.n++;
        end++;
      }
      const run = s.slice(i, end);
      if (c === "~" && run.length !== 2) {
        buf += run;
        i = end;
        continue;
      }
      const before = i > 0 ? s[i - 1] : "\n";
      const after = end < n ? s[end] : "\n";
      const left = !isWhite(after) && (!isPunct(after) || isWhite(before) || isPunct(before));
      const right = !isWhite(before) && (!isPunct(before) || isWhite(after) || isPunct(after));
      const open = c === "_" ? left && (!right || isPunct(before)) : left;
      const close = c === "_" ? right && (!left || isPunct(after)) : right;
      flush();
      const tok: Tok = { text: run, node: null, prev: null, next: null };
      append(tok);
      if (open || close) {
        const d: Delim = { tok, ch: c, count: run.length, orig: run.length, open, close, prev: delimTail, next: null };
        if (delimTail) delimTail.next = d;
        else delimHead = d;
        delimTail = d;
      }
      i = end;
      continue;
    }
    if (c === "&") {
      const ent = entity(s, i, steps);
      if (ent) {
        buf += ent.v;
        i = ent.end;
        continue;
      }
    }
    // An address starts at a word character that does not continue a word.
    const before = s[i - 1];
    if (isWordAt(s, i) && !isWordAt(s, i - 1) && before !== "." && before !== "-" && before !== "+" && before !== "@") {
      const link = bareLink(i);
      if (link) {
        bareNodes.add(link.node);
        addNode(link.node);
        i = link.end;
        continue;
      }
    }
    buf += c;
    i++;
  }
  flush();
  processEmphasis(null);
  return collect(head, null);
}

/** The "rule of 3": a run that can both open and close matches only in multiples of 3 together. */
function oddMatch(opener: Delim, closer: Delim): boolean {
  if (closer.ch === "~") return false;
  return (opener.close || closer.open) && (opener.orig + closer.orig) % 3 === 0 && !(opener.orig % 3 === 0 && closer.orig % 3 === 0);
}

/**
 * A character reference at the "&" at i: `&name;` (the HTML names),
 * `&#123;` or `&#x1F;`, as CommonMark reads it; null when it is none.
 */
function entity(s: string, i: number, steps: Steps): { v: string; end: number } | null {
  let j = i + 1;
  if (s[j] === "#") {
    j++;
    const hex = s[j] === "x" || s[j] === "X";
    if (hex) j++;
    const start = j;
    while (j - start < (hex ? 6 : 7) && (hex ? isHex(s[j]) : isDigit(s[j]))) {
      steps.n++;
      j++;
    }
    if (j === start || s[j] !== ";") return null;
    const cp = parseInt(s.slice(start, j), hex ? 16 : 10);
    const v = cp === 0 || cp > 0x10ffff || (cp >= 0xd800 && cp <= 0xdfff) ? "\ufffd" : String.fromCodePoint(cp);
    return { v, end: j + 1 };
  }
  const start = j;
  while (j - start < 32 && isAsciiAlnum(s[j])) {
    steps.n++;
    j++;
  }
  if (j === start || s[j] !== ";") return null;
  const name = s.slice(start, j);
  return namedEntities && Object.hasOwn(namedEntities, name) ? { v: namedEntities[name]!, end: j + 1 } : null;
}

/**
 * A bare URL with its entities decoded (#134); the URL as written when
 * there are none, or when one decodes to a character that ends a bare
 * address (white space, a control or format character, `<>"`…): such a
 * character never gets into a link, neither its href nor its text.
 */
function decodeUrlEntities(url: string, steps: Steps): string {
  let out = "";
  let k = 0;
  for (let j = url.indexOf("&"); j >= 0; j = url.indexOf("&", j)) {
    steps.n++;
    const ent = entity(url, j, steps);
    if (!ent) {
      j++;
      continue;
    }
    for (const c of ent.v) if (endsAddress(c)) return url;
    out += url.slice(k, j) + ent.v;
    k = j = ent.end;
  }
  return k === 0 ? url : out + url.slice(k);
}

/** What a bare address starts with (any case), the detector's schemes that may link. */
const barePrefixes = ["https://", "http://", "mailto:", "tel:", "www."];

function barePrefix(s: string, i: number): string | null {
  for (const p of barePrefixes) if (s.slice(i, i + p.length).toLowerCase() === p) return p;
  return null;
}

/** Punctuation that ends a sentence, not an address. */
const trailingPunct = "?!.,:;*_~'";
const isTrailing = (c: string) => trailingPunct.includes(c) || (c > "\x7f" && /^[\p{Po}\p{Pi}\p{Pf}]$/u.test(c));

/** Characters that end a bare address: white space, controls, format characters, `<>"`, a backtick and a backslash. */
const endsAddress = (c: string) =>
  isWhite(c) || c < " " || c === "\x7f" || c === "<" || c === ">" || c === '"' || c === "`" || c === "\\" || (c > "\x7f" && /^[\p{Cc}\p{Cf}]$/u.test(c));

/**
 * The bare address that starts at i with a prefix of `from` characters:
 * where it ends (`end`) and where its scan stopped (`stop`). It ends at
 * white space, a control or format character, `<`, `>`, `"`, a backtick
 * or a backslash, or at a ")", "]" or "}" that closes nothing inside it;
 * at a "[" that starts a written link (`[x](url)`: the written link is
 * parsed first, as in the app); and at a `'` or `’` (typed or as an
 * entity) in a URL's host, after any user info (`https://s.example's`,
 * but `https://o'brien@a.example/` as a whole). Then trailing punctuation
 * comes off; a complete entity at the end stays, unless it stands for
 * such punctuation, a closing bracket or a character that ends an
 * address (`&gt;`), or is no entity (`&foo;`).
 */
function bareAddress(s: string, i: number, from: number, pre: Prepass, steps: Steps): { end: number; stop: number } {
  const n = s.length;
  const start = i + from;
  let j = start;
  let paren = 0;
  let square = 0;
  let curly = 0;
  // http(s):// and www. (the prefixes ending in "/" or ".") start with a
  // host; mailto: and tel: do not.
  let host = s[start - 1] === "/" || s[start - 1] === ".";
  // The first quote in the host after the last "@" so far: the URL ends
  // there once the host ends, unless an "@" follows (then it was user info).
  let quote = -1;
  while (j < n) {
    steps.n++;
    const c = s[j]!;
    if (endsAddress(c)) break;
    if (host) {
      if (c === "/" || c === "?" || c === "#") {
        if (quote >= 0) break;
        host = false;
      } else if (c === "'" || c === "\u2019") {
        if (quote < 0) quote = j;
      } else if (c === "@") quote = -1;
      else if (c === "&") {
        // `&#39;`, `&rsquo;`, `&#64;`… count as typed (#134). The entity is
        // read once: the scan goes on after it.
        const ent = entity(s, j, steps);
        if (ent && (ent.v === "'" || ent.v === "\u2019" || ent.v === "@")) {
          if (ent.v === "@") quote = -1;
          else if (quote < 0) quote = j;
          j = ent.end;
          continue;
        }
      }
    }
    if (c === "[") {
      // A written link right here: its "]" (the pre-pass pairs brackets)
      // is followed by a destination. Each "[" is asked once, as the bare
      // scans do not overlap, and destination reads only the pre-pass
      // until it has found one, which ends this scan.
      const close = pre.closeBracket[j]!;
      if (close >= 0 && destination(s, close + 1, pre, steps)) break;
    }
    if (c === "(") paren++;
    else if (c === "[") square++;
    else if (c === "{") curly++;
    else if (c === ")") {
      if (paren === 0) break;
      paren--;
    } else if (c === "]") {
      if (square === 0) break;
      square--;
    } else if (c === "}") {
      if (curly === 0) break;
      curly--;
    }
    j++;
  }
  let end = host && quote >= 0 ? quote : j;
  while (end > start) {
    steps.n++;
    const c = s[end - 1]!;
    if (c === ";") {
      // An entity at the end (`&#49;`, `&uuml;`) is part of the URL and is
      // decoded with it, unless it stands for what would come off or end
      // the address (`&gt;`, `&#41;`); `&foo;` comes off as GFM drops it.
      let k = end - 1;
      while (k > start && isAsciiAlnum(s[k - 1])) {
        steps.n++;
        k--;
      }
      const amp = k > start && s[k - 1] === "#" ? k - 2 : k - 1;
      if (amp >= start && s[amp] === "&") {
        const ent = entity(s, amp, steps);
        if (ent && ent.end === end) {
          const v = ent.v;
          if (!(v.length === 1 && (isTrailing(v) || v === ")" || v === "]" || v === "}")) && ![...v].some(endsAddress)) break;
          end = amp;
          continue;
        }
      }
      end = k < end - 1 && k > start && s[k - 1] === "&" ? k - 1 : end - 1;
      continue;
    }
    if (!isTrailing(c)) break;
    end--;
  }
  return { end, stop: j };
}

/**
 * The end of the domain at j, before `limit`: labels of letters, digits, "-" and "_"
 * separated by single dots, at least one dot, not ending in "-" or "_";
 * -1 when there is none. A dot at the end is not part of it.
 */
function domainEnd(s: string, j: number, limit: number, steps: Steps): number {
  let k = j;
  let dots = 0;
  while (k < limit) {
    steps.n++;
    const c = s[k];
    if (isDomainAt(s, k)) k++;
    else if (c === "." && k > j && k + 1 < limit && isDomainAt(s, k + 1)) {
      dots++;
      k++;
    } else break;
  }
  if (dots === 0) return -1;
  const last = s[k - 1];
  return last === "-" || last === "_" ? -1 : k;
}

/** `<scheme:…>`: the URL and the index after ">", or null. */
function autolink(s: string, i: number, steps: Steps): { url: string; end: number } | null {
  let j = i + 1;
  // A scheme: a letter, then 1–31 letters, digits, "+", "." or "-", then ":".
  const letter = (c: string | undefined) => c !== undefined && ((c >= "a" && c <= "z") || (c >= "A" && c <= "Z"));
  if (!letter(s[j])) return null;
  j++;
  while (j < s.length && (letter(s[j]) || isDigit(s[j]) || s[j] === "+" || s[j] === "." || s[j] === "-")) {
    steps.n++;
    j++;
  }
  if (j - i - 1 < 2 || j - i - 1 > 32 || s[j] !== ":") return null;
  // The rest: no white space, controls, "<" or ">"; it ends at the first
  // of them, so every "<" scans to the next "<" at the most.
  while (j < s.length) {
    steps.n++;
    const c = s[j]!;
    if (c === ">") return { url: s.slice(i + 1, j), end: j + 1 };
    if (c === "<" || c <= " " || c === "\x7f") return null;
    j++;
  }
  return null;
}

/** What the scan looks up instead of searching: built in a few linear passes over one block. */
interface Prepass {
  /** The start of the next run of exactly len backticks at or after `from`, or -1. */
  nextTickRun(len: number, from: number): number;
  /** For "(" at i: the ")" that closes it within its word (no white space between), or -1. */
  closeAt: Int32Array;
  /** The first white space at or after i (n when none). */
  nextWhite: Int32Array;
  /** The first non-white character at or after i (n when none). */
  nextNonWhite: Int32Array;
  /** Whether a "(" not closed within its word comes at or after i in the same word. */
  openAfter: Uint8Array;
  /** The first ")" at or after i in the same word that closes no "(" of the word, or -1. */
  strayAt: Int32Array;
  /** The next unescaped '"' / "'" at or after i, or -1. */
  nextQuote: { '"': Int32Array; "'": Int32Array };
  /** The end of the run of e-mail local-part characters starting at i. */
  localEnd: Int32Array;
  /** For an unescaped "[" at i: the unescaped "]" that pairs with it (brackets nest), or -1. */
  closeBracket: Int32Array;
}

function prepass(s: string, steps: Steps): Prepass {
  const n = s.length;
  const escaped = new Uint8Array(n + 1);
  const closeAt = new Int32Array(n + 1).fill(-1);
  const runs = new Map<number, number[]>();
  const stray = new Uint8Array(n + 1);
  const stack: number[] = [];
  const closeBracket = new Int32Array(n + 1).fill(-1);
  const squares: number[] = [];
  for (let i = 0; i < n; i++) {
    steps.n++;
    const c = s[i]!;
    if (c === "\\" && !escaped[i]) escaped[i + 1] = 1;
    if (c === "`" && s[i - 1] !== "`") {
      let e = i;
      while (s[e] === "`") {
        steps.n++;
        e++;
      }
      const at = runs.get(e - i);
      if (at) at.push(i);
      else runs.set(e - i, [i]);
    }
    if (!escaped[i] && c === "[") squares.push(i);
    else if (!escaped[i] && c === "]" && squares.length > 0) closeBracket[squares.pop()!] = i;
    if (isWhite(c)) stack.length = 0;
    else if (!escaped[i] && c === "(") stack.push(i);
    else if (!escaped[i] && c === ")") {
      if (stack.length > 0) closeAt[stack.pop()!] = i;
      else stray[i] = 1;
    }
  }
  const nextWhite = new Int32Array(n + 1);
  const nextNonWhite = new Int32Array(n + 1);
  const openAfter = new Uint8Array(n + 1);
  const strayAt = new Int32Array(n + 1);
  const dq = new Int32Array(n + 1);
  const sq = new Int32Array(n + 1);
  const localEnd = new Int32Array(n + 1);
  localEnd[n] = n;
  nextWhite[n] = n;
  nextNonWhite[n] = n;
  strayAt[n] = -1;
  dq[n] = -1;
  sq[n] = -1;
  for (let i = n - 1; i >= 0; i--) {
    steps.n++;
    const c = s[i]!;
    const white = isWhite(c);
    nextWhite[i] = white ? i : nextWhite[i + 1]!;
    nextNonWhite[i] = white ? nextNonWhite[i + 1]! : i;
    openAfter[i] = white ? 0 : (c === "(" && !escaped[i] && closeAt[i] === -1) || openAfter[i + 1] ? 1 : 0;
    strayAt[i] = white ? -1 : stray[i] ? i : strayAt[i + 1]!;
    dq[i] = c === '"' && !escaped[i] ? i : dq[i + 1]!;
    sq[i] = c === "'" && !escaped[i] ? i : sq[i + 1]!;
    localEnd[i] = isLocalAt(s, i) ? localEnd[i + 1]! : i;
  }
  // The scan asks with growing positions, so each list is read once.
  const cursor = new Map<number, number>();
  const nextTickRun = (len: number, from: number) => {
    const at = runs.get(len);
    if (!at) return -1;
    let k = cursor.get(len) ?? 0;
    while (k < at.length && at[k]! < from) {
      steps.n++;
      k++;
    }
    cursor.set(len, k);
    return k < at.length ? at[k]! : -1;
  };
  return { nextTickRun, closeAt, nextWhite, nextNonWhite, openAfter, strayAt, nextQuote: { '"': dq, "'": sq }, localEnd, closeBracket };
}

/**
 * The inline link's destination after "]" at `o - 1` (s[o] must be "("):
 * `(url)`, `( url )`, `(<url>)` or with a "title" in quotes, and the index
 * of its ")". CommonMark's rules: no white space in the URL and balanced
 * parentheses. Every step is a lookup in the pre-pass, so a "](" costs the
 * same whatever follows it.
 */
function destination(s: string, o: number, pre: Prepass, steps: Steps): { url: string; end: number } | null {
  steps.n++;
  if (s[o] !== "(") return null;
  const n = s.length;
  const a = pre.nextNonWhite[o + 1]!;
  if (a >= n) return null;
  if (s[a] === ")") return { url: "", end: a };
  // The URL ends at the ")" that closes the "(" (after white space: the
  // first ")" that closes nothing in its word), else at the white space
  // after it, where a title or the ")" follows.
  const close = a === o + 1 ? pre.closeAt[o]! : pre.strayAt[a]!;
  let urlEnd: number;
  let end = -1;
  if (close >= 0) {
    urlEnd = close;
    end = close;
  } else {
    // A "(" not closed in the URL: unbalanced, not a destination.
    if (pre.openAfter[a]) return null;
    urlEnd = pre.nextWhite[a]!;
    if (urlEnd >= n) return null;
  }
  if (end < 0) {
    const b = pre.nextNonWhite[urlEnd]!;
    if (s[b] === ")") end = b;
    else if (s[b] === '"' || s[b] === "'") {
      const q = pre.nextQuote[s[b] as '"' | "'"][b + 1]!;
      if (q < 0) return null;
      const e = pre.nextNonWhite[q + 1]!;
      if (s[e] !== ")") return null;
      end = e;
    } else return null;
  }
  // Read only once the link is certain: the scan goes on after its ")".
  let url = s.slice(a, urlEnd);
  steps.n += urlEnd - a;
  if (url.startsWith("<") && url.endsWith(">")) url = url.slice(1, -1);
  return { url: unescape(url, steps), end };
}

/** Backslash escapes in a URL. */
function unescape(url: string, steps: Steps): string {
  if (!url.includes("\\")) return url;
  let out = "";
  for (let i = 0; i < url.length; i++) {
    steps.n++;
    const c = url[i]!;
    if (c === "\\" && url[i + 1] !== undefined && asciiPunct.includes(url[i + 1]!)) {
      out += url[i + 1];
      i++;
    } else out += c;
  }
  return out;
}

// ---- React

const levelPad = ["", "ps-5", "ps-10", "ps-15", "ps-20", "ps-25", "ps-30"];
const headingClass = ["text-lg font-semibold", "text-base font-semibold", "text-sm font-semibold"];

function inlineNodes(nodes: Inline[]): ReactNode[] {
  return nodes.map((node, i) => {
    switch (node.t) {
      case "text":
        return node.v;
      case "code":
        return createElement("code", { key: i, className: "rounded bg-surface-hover px-1 font-mono text-[0.9em]" }, node.v);
      case "em":
        return createElement("em", { key: i }, ...inlineNodes(node.c));
      case "strong":
        return createElement("strong", { key: i }, ...inlineNodes(node.c));
      case "del":
        return createElement("del", { key: i }, ...inlineNodes(node.c));
      case "link":
        return createElement("a", { key: i, href: node.href, target: "_blank", rel: "noopener noreferrer", className: "text-accent-ink underline" }, ...inlineNodes(node.c));
      case "plain":
        return createElement(Fragment, { key: i }, ...inlineNodes(node.c));
    }
  });
}

type Item = Extract<Block, { kind: "bullet" | "numbered" | "task" }>;
const isItem = (b: Block): b is Item => b.kind === "bullet" || b.kind === "numbered" || b.kind === "task";

function itemElement(b: Item, key: number): ReactNode {
  const marker =
    b.kind === "bullet"
      ? createElement("span", { "aria-hidden": true, className: "shrink-0 text-ink-muted" }, "•")
      : b.kind === "numbered"
        ? createElement("span", { className: "shrink-0 text-ink-muted tabular-nums" }, b.marker)
        : // Read-only, as in the app: the box shows the state, a click changes nothing.
          createElement("input", { type: "checkbox", checked: b.checked, readOnly: true, disabled: true, className: "mt-1 shrink-0" });
  return createElement(
    "li",
    { key, className: `flex gap-2 ${levelPad[b.level] ?? ""}` },
    marker,
    createElement("span", { className: "min-w-0 whitespace-pre-wrap wrap-anywhere" }, ...inlineNodes(b.text)),
  );
}

function blockElement(b: Exclude<Block, Item>, key: number): ReactNode {
  switch (b.kind) {
    case "heading":
      return createElement(`h${b.level + 2}`, { key, className: headingClass[b.level - 1] }, ...inlineNodes(b.text));
    case "paragraph":
      return createElement("p", { key, className: "whitespace-pre-wrap wrap-anywhere" }, ...inlineNodes(b.text));
    case "quote":
      return createElement("blockquote", { key, className: "border-l-2 border-line-control ps-3 text-ink-muted whitespace-pre-wrap wrap-anywhere" }, ...inlineNodes(b.text));
    case "code":
      return createElement("pre", { key, className: "overflow-x-auto rounded-md bg-surface-hover p-2 font-mono text-xs" }, createElement("code", null, b.text));
    case "rule":
      return createElement("hr", { key, className: "border-line" });
  }
}

/** The note as React elements: blocks in order, consecutive list items in one list. */
export function renderNotes(markdown: string): ReactNode {
  const blocks = parseNotes(markdown);
  const out: ReactNode[] = [];
  for (let i = 0; i < blocks.length; ) {
    const b = blocks[i]!;
    if (isItem(b)) {
      const items: ReactNode[] = [];
      while (i < blocks.length && isItem(blocks[i]!)) {
        items.push(itemElement(blocks[i] as Item, i));
        i++;
      }
      out.push(createElement("ul", { key: `l${i}`, className: "flex flex-col gap-0.5" }, ...items));
      continue;
    }
    out.push(blockElement(b, i));
    i++;
  }
  return createElement(Fragment, null, ...out);
}
