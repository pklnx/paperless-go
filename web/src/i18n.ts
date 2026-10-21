// UI texts in English and German. The language follows the browser:
// German if the first supported preference is German, English otherwise.
// A per-user choice follows with user accounts.

const en = {
  appName: "paperless-go",
  tagline: "Your documents, scanned, recognized and searchable, on your own server.",
  setupTitle: "Setting up",
  setupText:
    "This server is running, but documents, search and login are not built yet. They follow step by step; see the roadmap in the documentation.",
  serverStatus: "Server",
  statusChecking: "checking…",
  statusOk: "running, database connected",
  statusDown: "not reachable or database unavailable",
  documentation: "Documentation",
  switchTheme: (to: "light" | "dark"): string => (to === "dark" ? "Switch to dark mode" : "Switch to light mode"),
  languageName: "English",
};

export type Messages = typeof en;

const de: Messages = {
  appName: "paperless-go",
  tagline: "Deine Dokumente, gescannt, erkannt und durchsuchbar, auf deinem eigenen Server.",
  setupTitle: "Im Aufbau",
  setupText:
    "Dieser Server läuft, aber Dokumente, Suche und Anmeldung gibt es noch nicht. Sie kommen Schritt für Schritt; siehe die Roadmap in der Dokumentation.",
  serverStatus: "Server",
  statusChecking: "wird geprüft …",
  statusOk: "läuft, Datenbank verbunden",
  statusDown: "nicht erreichbar oder Datenbank nicht verfügbar",
  documentation: "Dokumentation",
  switchTheme: (to: "light" | "dark") => (to === "dark" ? "Dunkles Design" : "Helles Design"),
  languageName: "Deutsch",
};

const dictionaries = { en, de } satisfies Record<string, Messages>;

/** A UI language: one per dictionary. */
export type Lang = keyof typeof dictionaries;

/** The UI languages. */
export const languages = Object.keys(dictionaries) as Lang[];

/** The language of a browser preference like "de-AT", if there is a dictionary. */
function langOf(pref: string): Lang | undefined {
  const p = pref.toLowerCase();
  return languages.find((l) => p === l || p.startsWith(`${l}-`));
}

function browserPrefs(): readonly string[] {
  return navigator.languages ?? [navigator.language];
}

/** The first supported language in the browser's preferences, else English. */
export function detectLang(prefs: readonly string[] = browserPrefs()): Lang {
  for (const p of prefs) {
    const l = langOf(p);
    if (l) return l;
  }
  return "en";
}

export function messagesFor(l: Lang): Messages {
  return dictionaries[l];
}

export const lang: Lang = detectLang();

/** The texts in the UI language. */
export const t: Messages = messagesFor(lang);
