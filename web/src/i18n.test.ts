import { describe, expect, it } from "vitest";
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
  it("names each language in itself", () => {
    expect(messagesFor("de").languageName).toBe("Deutsch");
    expect(messagesFor("en").languageName).toBe("English");
  });
});
