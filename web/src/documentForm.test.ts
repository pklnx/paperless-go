import { describe, expect, it } from "vitest";
import type { DocumentDetail } from "./api";
import { formFrom, isDirty, localDay, parseASN, patchFrom, withDay } from "./documentForm";

const stored: DocumentDetail = {
  id: "d1",
  title: "Rechnung",
  ownerId: 1,
  createdAt: "2026-10-21T10:00:00Z",
  modifiedAt: "2026-10-21T10:00:00Z",
  documentDate: new Date(2026, 2, 15, 9, 30, 5).toISOString(),
  originalFileName: "r.pdf",
  originalMime: "application/pdf",
  originalSize: 1,
  hasArchive: false,
  archiveSize: null,
  pageCount: 1,
  processingNote: "",
  notes: "n",
  isInbox: true,
  isFavorite: false,
  archiveSerialNumber: 4,
  correspondentId: "c1",
  documentTypeId: null,
  tagIds: ["t1", "t2"],
};

describe("dates", () => {
  it("shows the day in the browser's time zone and keeps the time of day on a change", () => {
    expect(localDay(stored.documentDate)).toBe("2026-03-15");
    const at = new Date(withDay(stored.documentDate, "2026-12-01")!);
    expect([at.getFullYear(), at.getMonth(), at.getDate(), at.getHours(), at.getMinutes(), at.getSeconds()]).toEqual([2026, 11, 1, 9, 30, 5]);
  });
  it("gives a date without a valid time 12:00 local", () => {
    const at = new Date(withDay("not a date", "2026-12-01")!);
    expect([at.getDate(), at.getHours(), at.getMinutes()]).toEqual([1, 12, 0]);
  });
  it("refuses days that do not exist", () => {
    expect(withDay(stored.documentDate, "2026-02-31")).toBeNull();
    expect(withDay(stored.documentDate, "")).toBeNull();
    expect(localDay("nope")).toBe("");
  });
});

describe("parseASN", () => {
  it("takes empty as none and positive 32-bit numbers", () => {
    expect(parseASN(" ")).toBeNull();
    expect(parseASN("42")).toBe(42);
    expect(parseASN("2147483647")).toBe(2147483647);
    for (const bad of ["0", "-1", "1.5", "2147483648", "12a", "1e3"]) expect(parseASN(bad), bad).toBe("invalid");
  });
});

describe("patchFrom", () => {
  it("is empty for the stored values, tags in any order", () => {
    const f = formFrom(stored);
    expect(patchFrom(stored, f)).toEqual({ patch: {} });
    expect(patchFrom(stored, { ...f, tagIds: ["t2", "t1"] })).toEqual({ patch: {} });
    expect(isDirty(stored, f)).toBe(false);
  });
  it("holds only the changed fields, with null to clear", () => {
    const f = formFrom(stored);
    expect(patchFrom(stored, { ...f, title: "", asn: "", correspondentId: null, tagIds: ["t1"], isInbox: false })).toEqual({
      patch: { title: "", archiveSerialNumber: null, correspondentId: null, tagIds: ["t1"], isInbox: false },
    });
    expect(patchFrom(stored, { ...f, notes: "m", isFavorite: true, documentTypeId: "x" })).toEqual({ patch: { notes: "m", isFavorite: true, documentTypeId: "x" } });
  });
  it("names a value that cannot be sent", () => {
    const f = formFrom(stored);
    expect(patchFrom(stored, { ...f, asn: "x" })).toEqual({ invalid: "asn" });
    expect(patchFrom(stored, { ...f, date: "" })).toEqual({ invalid: "date" });
    expect(isDirty(stored, { ...f, asn: "x" })).toBe(true);
  });
});
