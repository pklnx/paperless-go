// UI texts in English and German. The language follows the browser:
// German if the first supported preference is German, English otherwise.
// A per-user choice may follow later.

const en = {
  appName: "paperless-go",
  tagline: "Your documents, scanned, recognized and searchable, on your own server.",
  buildingTitle: "Setting up",
  buildingText:
    "This server is running and you are logged in, but documents and search are not built yet. They follow step by step; see the roadmap in the documentation.",
  serverStatus: "Server",
  statusChecking: "checking…",
  statusOk: "running, database connected",
  statusDown: "not reachable or database unavailable",
  documentation: "Documentation",
  switchTheme: (to: "light" | "dark"): string => (to === "dark" ? "Switch to dark mode" : "Switch to light mode"),
  languageName: "English",

  loading: "Loading…",
  retry: "Retry",
  failed: (err: string) => `Failed: ${err}`,
  cancel: "Cancel",
  done: "Done",
  copy: "Copy",
  copied: "Copied",

  // Login
  userName: "User name",
  fieldPassword: "Password",
  logIn: "Log in",
  loggingIn: "Logging in…",
  logOut: "Log out",
  wrongLogin: "Wrong user name or password.",
  userLocked: "This user is locked. Ask an admin to unlock it.",
  tooManyAttempts: (seconds: number) =>
    seconds < 60
      ? `Too many failed attempts. Try again in ${seconds} seconds.`
      : `Too many failed attempts. Try again in ${Math.ceil(seconds / 60)} ${Math.ceil(seconds / 60) === 1 ? "minute" : "minutes"}.`,
  setupTitle: "Create the first admin",
  setupText: "No user exists yet. Run this command in the paperless-go folder, then check again:",
  checkAgain: "Check again",

  // Navigation
  home: "Start",
  profile: "Profile",
  users: "Users",

  // Profile and passwords
  changePassword: "Change password",
  currentPassword: "Current password",
  newPassword: "New password",
  repeatPassword: "Repeat new password",
  passwordRules: "At least 12 characters. A few random words work well.",
  passwordsDiffer: "The new passwords do not match.",
  passwordChanged: "Password changed. You stay logged in here; other devices were logged out.",
  wrongCurrentPassword: "The current password is wrong.",
  chooseOwnPassword: "Choose your own password",
  chooseOwnPasswordText: "You logged in with a password an admin generated for you. Choose your own to continue.",
  loggedInAs: (name: string) => `Logged in as ${name}`,

  // Users page
  usersTitle: "Users",
  usersText: "Admins manage logins here and see every user's documents.",
  addUser: "Add user",
  userIsAdmin: "Admin",
  roleAdmin: "admin",
  roleUser: "user",
  stateActive: "active",
  stateLocked: "locked",
  stateMustChange: "must change password",
  colLastLogin: "Last login",
  colDocuments: "Documents",
  never: "never",
  you: "you",
  selfHint: "To change your own login, use your profile or ask another admin.",
  resetPassword: "Reset password",
  lock: "Lock",
  unlock: "Unlock",
  makeAdmin: "Make admin",
  removeAdmin: "Remove admin",
  removeUser: "Remove",
  confirmResetPassword: (name: string) => `Reset the password of "${name}"? They are logged out everywhere and get a new password from you.`,
  confirmLock: (name: string) => `Lock "${name}"? They are logged out everywhere and cannot log in until unlocked.`,
  confirmRemoveUser: (name: string) => `Remove "${name}"? This cannot be undone.`,
  ownsData: "Owns documents or metadata: delete them before removing the user.",
  generatedFor: (name: string) => `Password for ${name}`,
  generatedHint: "Shown only now. Hand it over safely; at the first login it must be changed.",

  // Log page
  log: "Log",
  logTitle: "Log",
  logOwnNote: "Your logins, failed logins and logouts, and the changes to your login. Admins can see it too.",
  logAdminNote: "Every user's logins, failed logins and logouts, and every change of users and passwords, also from the command line.",
  logFrom: "From",
  logTo: "To",
  logAction: "Action",
  logWho: "Who",
  logEveryone: "Everyone",
  logCommandLine: "Command line",
  logAllActions: "All actions",
  logGroup: (g: string): string =>
    ({ login: "Logins", users: "User management", profile: "Own login", export: "Log exports" })[g] ?? g,
  logEmpty: "No entries in this period.",
  logMore: "Load more",
  logCLI: "command line",
  logCLIBy: (who: string) => `command line (${who})`,
  logViaKey: (name: string) => `with API key “${name}”`,
  auditAction: (a: string): string =>
    ({
      "auth.login": "Logged in",
      "auth.login_failed": "Login failed",
      "auth.logout": "Logged out",
      "user.add": "Added user",
      "user.remove": "Removed user",
      "user.lock": "Locked user",
      "user.unlock": "Unlocked user",
      "user.promote": "Made admin",
      "user.demote": "Removed admin role",
      "user.password_reset": "Reset password",
      "user.password_set": "Set password",
      "profile.password_change": "Changed own password",
      "audit.export": "Exported log",
    })[a] ?? a,
};

export type Messages = typeof en;

const de: Messages = {
  appName: "paperless-go",
  tagline: "Deine Dokumente, gescannt, erkannt und durchsuchbar, auf deinem eigenen Server.",
  buildingTitle: "Im Aufbau",
  buildingText:
    "Dieser Server läuft und du bist angemeldet, aber Dokumente und Suche gibt es noch nicht. Sie kommen Schritt für Schritt; siehe die Roadmap in der Dokumentation.",
  serverStatus: "Server",
  statusChecking: "wird geprüft …",
  statusOk: "läuft, Datenbank verbunden",
  statusDown: "nicht erreichbar oder Datenbank nicht verfügbar",
  documentation: "Dokumentation",
  switchTheme: (to: "light" | "dark") => (to === "dark" ? "Dunkles Design" : "Helles Design"),
  languageName: "Deutsch",

  loading: "Wird geladen …",
  retry: "Erneut versuchen",
  failed: (err) => `Fehlgeschlagen: ${err}`,
  cancel: "Abbrechen",
  done: "Fertig",
  copy: "Kopieren",
  copied: "Kopiert",

  userName: "Benutzername",
  fieldPassword: "Passwort",
  logIn: "Anmelden",
  loggingIn: "Anmeldung läuft …",
  logOut: "Abmelden",
  wrongLogin: "Benutzername oder Passwort falsch.",
  userLocked: "Dieser Benutzer ist gesperrt. Bitte einen Admin, ihn zu entsperren.",
  tooManyAttempts: (seconds) =>
    seconds < 60
      ? `Zu viele Fehlversuche. Versuche es in ${seconds} Sekunden erneut.`
      : `Zu viele Fehlversuche. Versuche es in ${Math.ceil(seconds / 60)} ${Math.ceil(seconds / 60) === 1 ? "Minute" : "Minuten"} erneut.`,
  setupTitle: "Ersten Admin anlegen",
  setupText: "Es gibt noch keinen Benutzer. Führe diesen Befehl im Ordner von paperless-go aus und prüfe dann erneut:",
  checkAgain: "Erneut prüfen",

  home: "Start",
  profile: "Profil",
  users: "Benutzer",

  changePassword: "Passwort ändern",
  currentPassword: "Aktuelles Passwort",
  newPassword: "Neues Passwort",
  repeatPassword: "Neues Passwort wiederholen",
  passwordRules: "Mindestens 12 Zeichen. Ein paar zufällige Wörter funktionieren gut.",
  passwordsDiffer: "Die neuen Passwörter stimmen nicht überein.",
  passwordChanged: "Passwort geändert. Hier bleibst du angemeldet, andere Geräte wurden abgemeldet.",
  wrongCurrentPassword: "Das aktuelle Passwort ist falsch.",
  chooseOwnPassword: "Eigenes Passwort wählen",
  chooseOwnPasswordText: "Du hast dich mit einem Passwort angemeldet, das ein Admin für dich erzeugt hat. Wähle ein eigenes, um weiterzumachen.",
  loggedInAs: (name) => `Angemeldet als ${name}`,

  usersTitle: "Benutzer",
  usersText: "Admins verwalten hier die Anmeldungen und sehen die Dokumente aller Benutzer.",
  addUser: "Benutzer hinzufügen",
  userIsAdmin: "Admin",
  roleAdmin: "Admin",
  roleUser: "Benutzer",
  stateActive: "aktiv",
  stateLocked: "gesperrt",
  stateMustChange: "muss Passwort ändern",
  colLastLogin: "Letzte Anmeldung",
  colDocuments: "Dokumente",
  never: "nie",
  you: "du",
  selfHint: "Deine eigene Anmeldung änderst du im Profil oder über einen anderen Admin.",
  resetPassword: "Passwort zurücksetzen",
  lock: "Sperren",
  unlock: "Entsperren",
  makeAdmin: "Zum Admin machen",
  removeAdmin: "Admin entziehen",
  removeUser: "Entfernen",
  confirmResetPassword: (name) => `Passwort von „${name}“ zurücksetzen? Der Benutzer wird überall abgemeldet und bekommt von dir ein neues Passwort.`,
  confirmLock: (name) => `„${name}“ sperren? Der Benutzer wird überall abgemeldet und kann sich erst nach dem Entsperren wieder anmelden.`,
  confirmRemoveUser: (name) => `„${name}“ entfernen? Das lässt sich nicht rückgängig machen.`,
  ownsData: "Besitzt Dokumente oder Metadaten: lösche sie, bevor du den Benutzer entfernst.",
  generatedFor: (name) => `Passwort für ${name}`,
  generatedHint: "Wird nur jetzt angezeigt. Gib es sicher weiter; bei der ersten Anmeldung muss es geändert werden.",

  log: "Protokoll",
  logTitle: "Protokoll",
  logOwnNote: "Deine Anmeldungen, fehlgeschlagenen Anmeldungen und Abmeldungen und die Änderungen an deiner Anmeldung. Admins können es ebenfalls sehen.",
  logAdminNote: "Die Anmeldungen, fehlgeschlagenen Anmeldungen und Abmeldungen aller Benutzer und jede Änderung an Benutzern und Passwörtern, auch über die Kommandozeile.",
  logFrom: "Von",
  logTo: "Bis",
  logAction: "Aktion",
  logWho: "Wer",
  logEveryone: "Alle",
  logCommandLine: "Kommandozeile",
  logAllActions: "Alle Aktionen",
  logGroup: (g) =>
    ({ login: "Anmeldungen", users: "Benutzerverwaltung", profile: "Eigene Anmeldung", export: "Protokoll-Exporte" })[g] ?? g,
  logEmpty: "Keine Einträge in diesem Zeitraum.",
  logMore: "Mehr laden",
  logCLI: "Kommandozeile",
  logCLIBy: (who) => `Kommandozeile (${who})`,
  logViaKey: (name) => `mit API-Schlüssel „${name}“`,
  auditAction: (a) =>
    ({
      "auth.login": "Angemeldet",
      "auth.login_failed": "Anmeldung fehlgeschlagen",
      "auth.logout": "Abgemeldet",
      "user.add": "Benutzer hinzugefügt",
      "user.remove": "Benutzer entfernt",
      "user.lock": "Benutzer gesperrt",
      "user.unlock": "Benutzer entsperrt",
      "user.promote": "Zum Admin gemacht",
      "user.demote": "Admin-Rolle entzogen",
      "user.password_reset": "Passwort zurückgesetzt",
      "user.password_set": "Passwort gesetzt",
      "profile.password_change": "Eigenes Passwort geändert",
      "audit.export": "Protokoll exportiert",
    })[a] ?? a,
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
