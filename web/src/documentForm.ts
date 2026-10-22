// The edit form of a document: its values, and the PATCH of what changed.
import type { DocumentDetail, DocumentPatch } from "./api";

/** The form's values. date is a day ("YYYY-MM-DD") in the browser's time zone; asn is the text typed. */
export interface DocumentForm {
  title: string;
  date: string;
  asn: string;
  correspondentId: string | null;
  documentTypeId: string | null;
  tagIds: string[];
  notes: string;
  isInbox: boolean;
  isFavorite: boolean;
}

/** The largest archive serial number the server stores (a 32-bit integer). */
export const maxASN = 2147483647;

const pad = (n: number, w = 2) => String(n).padStart(w, "0");

/** The day of an ISO time in the browser's time zone, or "" for an invalid one. */
export function localDay(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return `${pad(d.getFullYear(), 4)}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

/**
 * The time of day "YYYY-MM-DD" with the local time of day of iso, so a
 * changed date keeps the stored time; 12:00 local when iso is no valid
 * time. null for a day that is not a valid date.
 */
export function withDay(iso: string, day: string): string | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(day);
  if (!m) return null;
  const [y, mo, d] = [Number(m[1]), Number(m[2]), Number(m[3])];
  const old = new Date(iso);
  const t = Number.isNaN(old.getTime()) ? null : old;
  const out = new Date(y, mo - 1, d, t ? t.getHours() : 12, t ? t.getMinutes() : 0, t ? t.getSeconds() : 0, t ? t.getMilliseconds() : 0);
  // new Date rolls 2026-02-31 over to March: such a day is not valid.
  if (out.getFullYear() !== y || out.getMonth() !== mo - 1 || out.getDate() !== d) return null;
  // Years below 100 are taken as 19xx by the constructor above, so set it again.
  out.setFullYear(y);
  return out.toISOString();
}

/** The form of a document as stored. */
export function formFrom(d: DocumentDetail): DocumentForm {
  return {
    title: d.title,
    date: localDay(d.documentDate),
    asn: d.archiveSerialNumber === null ? "" : String(d.archiveSerialNumber),
    correspondentId: d.correspondentId,
    documentTypeId: d.documentTypeId,
    tagIds: [...d.tagIds],
    notes: d.notes,
    isInbox: d.isInbox,
    isFavorite: d.isFavorite,
  };
}

/** The ASN typed: null for none, a number, or "invalid". */
export function parseASN(text: string): number | null | "invalid" {
  const s = text.trim();
  if (s === "") return null;
  if (!/^\d{1,10}$/.test(s)) return "invalid";
  const n = Number(s);
  return n >= 1 && n <= maxASN ? n : "invalid";
}

const sameSet = (a: readonly string[], b: readonly string[]) => {
  const sa = new Set(a);
  const sb = new Set(b);
  return sa.size === sb.size && [...sa].every((x) => sb.has(x));
};

/** A field of the form whose value cannot be sent. */
export type InvalidField = "date" | "asn";

/**
 * The PATCH body with only the fields of form that differ from the stored
 * document d, or the first field whose value cannot be sent.
 */
export function patchFrom(d: DocumentDetail, form: DocumentForm): { patch: DocumentPatch } | { invalid: InvalidField } {
  const patch: DocumentPatch = {};
  if (form.title !== d.title) patch.title = form.title;
  if (form.date !== localDay(d.documentDate)) {
    const at = withDay(d.documentDate, form.date);
    if (at === null) return { invalid: "date" };
    patch.documentDate = at;
  }
  const asn = parseASN(form.asn);
  if (asn === "invalid") return { invalid: "asn" };
  if (asn !== d.archiveSerialNumber) patch.archiveSerialNumber = asn;
  if (form.correspondentId !== d.correspondentId) patch.correspondentId = form.correspondentId;
  if (form.documentTypeId !== d.documentTypeId) patch.documentTypeId = form.documentTypeId;
  if (!sameSet(form.tagIds, d.tagIds)) patch.tagIds = [...new Set(form.tagIds)];
  if (form.notes !== d.notes) patch.notes = form.notes;
  if (form.isInbox !== d.isInbox) patch.isInbox = form.isInbox;
  if (form.isFavorite !== d.isFavorite) patch.isFavorite = form.isFavorite;
  return { patch };
}

/** Whether the form differs from the stored document; an invalid value counts as a change. */
export function isDirty(d: DocumentDetail, form: DocumentForm): boolean {
  const r = patchFrom(d, form);
  return "invalid" in r || Object.keys(r.patch).length > 0;
}
