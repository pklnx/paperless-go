import { describe, expect, it } from "vitest";
import { auditGroups } from "./auditLog";
import { detectLang, languages, messagesFor } from "./i18n";

describe("detectLang", () => {
  it("picks the first supported browser language", () => {
    expect(detectLang(["de-DE", "en-US"])).toBe("de");
    expect(detectLang(["fr-FR", "en-GB", "de"])).toBe("en");
    expect(detectLang(["DE-at"])).toBe("de");
  });
  it("does not mistake a longer code for a language", () => {
    expect(detectLang(["dex", "den-XY"])).toBe("en");
  });
  it("falls back to English", () => {
    expect(detectLang(["fr", "es"])).toBe("en");
    expect(detectLang([])).toBe("en");
  });
});

describe("messages", () => {
  it("translates every text", () => {
    const en = messagesFor("en");
    for (const l of languages) {
      const m = messagesFor(l);
      expect(Object.keys(m).sort()).toEqual(Object.keys(en).sort());
      for (const key of Object.keys(en) as (keyof typeof en)[]) {
        const v = m[key];
        if (typeof v === "string") expect(v, `${l}.${key}`).not.toBe("");
        // Every text function takes one argument: a name, a number or a theme.
        if (typeof v === "function") expect((v as (arg: never) => string)("dark" as never), `${l}.${key}`).not.toBe("");
      }
    }
  });
  it("tells a user that changes by admins and the server are in their log", () => {
    expect(messagesFor("en").logOwnNote).toContain("an admin, the command line or the server made to your account");
    expect(messagesFor("de").logOwnNote).toContain("ein Admin, die Kommandozeile oder der Server an deinem Konto");
    expect(messagesFor("en").logOwnNote).toContain("consume folder");
    expect(messagesFor("de").logOwnNote).toContain("Eingangsordner");
    expect(messagesFor("en").logByAdmin("root")).toBe("by root");
    expect(messagesFor("de").logByAdmin("root")).toBe("von root");
  });
  it("names every log filter group and the document edits in both languages", () => {
    for (const l of languages) {
      const m = messagesFor(l);
      for (const g of auditGroups) expect(m.logGroup(g), `${l}.${g}`).not.toBe(g);
      for (const a of ["document.update", "document.bulk_update"]) expect(m.auditAction(a), `${l}.${a}`).not.toBe(a);
    }
  });
  it("names each language in itself", () => {
    expect(messagesFor("de").languageName).toBe("Deutsch");
    expect(messagesFor("en").languageName).toBe("English");
  });
});
