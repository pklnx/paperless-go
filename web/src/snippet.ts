import { displayName } from "./uploads";

// Snippets of a search: the server puts the private-use characters U+E000
// and U+E001 around each match (store.SnippetStart, store.SnippetStop).
// The UI splits at them into text parts and marks; it never reads a
// snippet as HTML.

export const SNIPPET_START = "";
export const SNIPPET_STOP = "";

/** One run of a snippet: plain text, marked or not. */
export interface SnippetPart {
  text: string;
  mark: boolean;
}

/**
 * Splits a snippet into runs at the markers. A start marker inside a mark
 * and a stop marker outside one are dropped, so the markers never show;
 * a mark that does not end runs to the end. Bidi control characters are
 * removed, as in names. Empty runs are left out.
 */
export function snippetParts(snippet: string): SnippetPart[] {
  const parts: SnippetPart[] = [];
  let mark = false;
  let text = "";
  const flush = () => {
    if (text) parts.push({ text, mark });
    text = "";
  };
  for (const ch of displayName(snippet)) {
    if (ch === SNIPPET_START || ch === SNIPPET_STOP) {
      const next = ch === SNIPPET_START;
      if (next !== mark) {
        flush();
        mark = next;
      }
      continue;
    }
    text += ch;
  }
  flush();
  return parts;
}
