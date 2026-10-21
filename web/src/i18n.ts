// UI texts in English and German. The language follows the browser:
// German if the first supported preference is German, English otherwise.
// A per-user choice may follow later.

const en = {
  appName: "paperless-go",
  tagline: "Your documents, scanned, recognized and searchable, on your own server.",
  buildingTitle: "Setting up",
  buildingText:
    "Uploads work; text recognition, search and the full document list follow step by step. See the roadmap in the documentation.",
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

  // Upload and recent documents (start page)
  uploadTitle: "Upload documents",
  uploadHint: (limit: string) => `Drop files anywhere in this window, or choose them. PDF, JPEG, PNG and HEIC${limit ? `, up to ${limit} each` : ""}.`,
  chooseFiles: "Choose files",
  dropHere: "Drop files to upload them",
  uploadsLabel: "Uploads",
  uploadWaiting: "waiting",
  uploadProcessing: "saving…",
  uploadImported: "imported",
  uploadDuplicate: "already there",
  uploadDuplicateOf: (title: string) => `already there → ${title}`,
  uploadCancelled: "cancelled",
  uploadTooLarge: (limit: string) => (limit ? `larger than ${limit}` : "too large"),
  uploadUnsupported: "not a PDF, JPEG, PNG or HEIC file",
  uploadUnreadable: "cannot be read",
  uploadTooManyPixels: "image has more than 50 megapixels",
  uploadNetworkError: "upload failed; check the connection",
  cancelUpload: (name: string) => `Cancel ${name}`,
  clearFinished: "Clear finished",
  recentTitle: "Recent documents",
  recentEmpty: "No documents yet.",
  download: "Download",
  untitled: "Untitled",

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

  // Two-factor login
  twoFactor: "Two-factor authentication",
  twoFactorRequired: "Two-factor authentication is required before you can use paperless-go.",
  twoFactorSetup: "Set up authenticator app",
  twoFactorSetupText: "Scan the QR code with your authenticator app, then enter the six-digit code.",
  twoFactorOffText: "Protect your login with a code from an authenticator app on your phone, in addition to your password.",
  twoFactorCode: "Authentication code",
  twoFactorConfirm: "Enable two-factor authentication",
  twoFactorRecovery: "Recovery codes",
  twoFactorRecoveryText:
    "Download these codes or copy them, and keep them somewhere safe. Each code can be used once in place of a code from the app. They are shown only now.",
  twoFactorDownloadPdf: "Download PDF",
  twoFactorDownloadTxt: "Download .txt",
  recoverySheetUser: "User",
  recoverySheetServer: "Server",
  recoverySheetCreated: "Created",
  recoverySheetNote: [
    "Each code works once, in place of a code from your authenticator app.",
    "Keep this sheet somewhere safe, apart from your phone.",
    "New recovery codes replace this sheet: then these codes stop working.",
  ],
  twoFactorDisable: "Disable two-factor authentication",
  twoFactorDisableText: "Disabling 2FA requires your password and a current TOTP or recovery code.",
  twoFactorRegenerate: "Generate new recovery codes",
  twoFactorRegenerateText: "New codes need a current code from your app or a recovery code. The old codes stop working.",
  twoFactorAdminRequired: "Administrators must keep two-factor authentication enabled.",
  twoFactorEveryoneRequired: "Two-factor authentication is required on this server.",
  recoveryCodesLeft: (n: number) => (n === 1 ? "1 recovery code left." : `${n} recovery codes left.`),
  twoFactorReset: "Reset 2FA",
  twoFactorResetConfirm: (name: string) => `Reset two-factor authentication for "${name}"? They are logged out and must set it up again.`,
  twoFactorLogin: "Enter your authentication code",
  twoFactorLoginText: "Enter the six-digit code from your authenticator app, or a recovery code.",
  invalidTwoFactor: "The authentication code is invalid or has already been used.",
  twoFactorExpired: "The login took too long. Log in again.",
  twoFactorOn: "Two-factor authentication is on.",
  twoFactorQR: "QR code for your authenticator app",
  twoFactorManualKey: "If you cannot scan it, enter this key in the app:",
  twoFactorCodesSaved: "I have saved the codes",
  twoFactorShortOn: "2FA on",
  twoFactorShortOff: "no 2FA",

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
  logOwnNote: "Your logins, failed logins and logouts, and the changes you made to your password and two-factor login. Admins see these entries too.",
  logAdminNote: "Every user's logins, failed logins and logouts, and every change of users, passwords and two-factor logins, also from the command line.",
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
  logTriedName: (name: string) => `“${name}”`,
  /** A code in an entry's details: a login method, the reason of a failed login, a role. */
  auditCode: (c: string): string =>
    ({
      password: "password",
      "2fa": "password and code",
      totp: "authenticator app",
      recovery_code: "recovery code",
      wrong_password: "wrong password",
      second_factor: "wrong code",
      unknown_user: "unknown user",
      locked: "user locked",
      admin: "admin",
    })[c] ?? c,
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
      "profile.2fa_enable": "Enabled 2FA",
      "profile.2fa_disable": "Disabled 2FA",
      "profile.recovery_codes": "New recovery codes",
      "profile.2fa_failed": "2FA change refused",
      "user.2fa_reset": "Reset 2FA",
      "audit.export": "Exported log",
      "document.create": "Uploaded document",
    })[a] ?? a,
};

export type Messages = typeof en;

const de: Messages = {
  appName: "paperless-go",
  tagline: "Deine Dokumente, gescannt, erkannt und durchsuchbar, auf deinem eigenen Server.",
  buildingTitle: "Im Aufbau",
  buildingText:
    "Hochladen funktioniert; Texterkennung, Suche und die vollständige Dokumentliste kommen Schritt für Schritt. Siehe die Roadmap in der Dokumentation.",
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

  uploadTitle: "Dokumente hochladen",
  uploadHint: (limit) => `Dateien irgendwo in dieses Fenster ziehen oder auswählen. PDF, JPEG, PNG und HEIC${limit ? `, je bis ${limit}` : ""}.`,
  chooseFiles: "Dateien auswählen",
  dropHere: "Dateien zum Hochladen loslassen",
  uploadsLabel: "Uploads",
  uploadWaiting: "wartet",
  uploadProcessing: "wird gespeichert …",
  uploadImported: "importiert",
  uploadDuplicate: "schon vorhanden",
  uploadDuplicateOf: (title) => `schon vorhanden → ${title}`,
  uploadCancelled: "abgebrochen",
  uploadTooLarge: (limit) => (limit ? `größer als ${limit}` : "zu groß"),
  uploadUnsupported: "keine PDF-, JPEG-, PNG- oder HEIC-Datei",
  uploadUnreadable: "kann nicht gelesen werden",
  uploadTooManyPixels: "Bild hat mehr als 50 Megapixel",
  uploadNetworkError: "Hochladen fehlgeschlagen; prüfe die Verbindung",
  cancelUpload: (name) => `${name} abbrechen`,
  clearFinished: "Erledigte entfernen",
  recentTitle: "Neueste Dokumente",
  recentEmpty: "Noch keine Dokumente.",
  download: "Herunterladen",
  untitled: "Ohne Titel",

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

  twoFactor: "Zwei-Faktor-Authentifizierung",
  twoFactorRequired: "Bevor du paperless-go nutzen kannst, musst du die Zwei-Faktor-Authentifizierung einrichten.",
  twoFactorSetup: "Authenticator-App einrichten",
  twoFactorSetupText: "Scanne den QR-Code mit deiner Authenticator-App und gib anschließend den sechsstelligen Code ein.",
  twoFactorOffText: "Schütze deine Anmeldung zusätzlich zum Passwort mit einem Code aus einer Authenticator-App auf deinem Handy.",
  twoFactorCode: "Bestätigungscode",
  twoFactorConfirm: "Zwei-Faktor-Authentifizierung aktivieren",
  twoFactorRecovery: "Wiederherstellungscodes",
  twoFactorRecoveryText:
    "Lade diese Codes herunter oder kopiere sie und bewahre sie sicher auf. Jeder Code ersetzt einmal einen Code aus der App. Sie werden nur jetzt angezeigt.",
  twoFactorDownloadPdf: "Als PDF herunterladen",
  twoFactorDownloadTxt: ".txt herunterladen",
  recoverySheetUser: "Benutzer",
  recoverySheetServer: "Server",
  recoverySheetCreated: "Erstellt",
  recoverySheetNote: [
    "Jeder Code funktioniert einmal, anstelle eines Codes aus der Authenticator-App.",
    "Bewahre dieses Blatt sicher auf, getrennt von deinem Handy.",
    "Neue Codes ersetzen dieses Blatt, diese Codes gelten dann nicht mehr.",
  ],
  twoFactorDisable: "Zwei-Faktor-Authentifizierung deaktivieren",
  twoFactorDisableText: "Zum Deaktivieren brauchst du dein Passwort und einen aktuellen TOTP- oder Wiederherstellungscode.",
  twoFactorRegenerate: "Neue Wiederherstellungscodes erzeugen",
  twoFactorRegenerateText: "Neue Codes brauchen einen aktuellen Code aus der App oder einen Wiederherstellungscode. Die alten Codes gelten dann nicht mehr.",
  twoFactorAdminRequired: "Admins müssen die Zwei-Faktor-Authentifizierung aktiviert lassen.",
  twoFactorEveryoneRequired: "Auf diesem Server ist die Zwei-Faktor-Authentifizierung Pflicht.",
  recoveryCodesLeft: (n) => (n === 1 ? "Noch 1 Wiederherstellungscode." : `Noch ${n} Wiederherstellungscodes.`),
  twoFactorReset: "2FA zurücksetzen",
  twoFactorResetConfirm: (name) => `Zwei-Faktor-Authentifizierung für „${name}“ zurücksetzen? Der Benutzer wird abgemeldet und muss sie neu einrichten.`,
  twoFactorLogin: "Bestätigungscode eingeben",
  twoFactorLoginText: "Gib den sechsstelligen Code deiner Authenticator-App oder einen Wiederherstellungscode ein.",
  invalidTwoFactor: "Der Bestätigungscode ist ungültig oder wurde bereits verwendet.",
  twoFactorExpired: "Die Anmeldung hat zu lange gedauert. Melde dich erneut an.",
  twoFactorOn: "Die Zwei-Faktor-Authentifizierung ist aktiv.",
  twoFactorQR: "QR-Code für deine Authenticator-App",
  twoFactorManualKey: "Falls das Scannen nicht klappt, gib diesen Schlüssel in der App ein:",
  twoFactorCodesSaved: "Ich habe die Codes gespeichert",
  twoFactorShortOn: "2FA aktiv",
  twoFactorShortOff: "ohne 2FA",

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
  logOwnNote:
    "Deine Anmeldungen, fehlgeschlagenen Anmeldungen und Abmeldungen und die Änderungen, die du an deinem Passwort und deiner Zwei-Faktor-Anmeldung vorgenommen hast. Admins sehen diese Einträge ebenfalls.",
  logAdminNote:
    "Die Anmeldungen, fehlgeschlagenen Anmeldungen und Abmeldungen aller Benutzer und jede Änderung an Benutzern, Passwörtern und Zwei-Faktor-Anmeldungen, auch über die Kommandozeile.",
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
  logTriedName: (name) => `„${name}“`,
  auditCode: (c) =>
    ({
      password: "Passwort",
      "2fa": "Passwort und Code",
      totp: "Authenticator-App",
      recovery_code: "Wiederherstellungscode",
      wrong_password: "falsches Passwort",
      second_factor: "falscher Code",
      unknown_user: "unbekannter Benutzer",
      locked: "Benutzer gesperrt",
      admin: "Admin",
    })[c] ?? c,
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
      "profile.2fa_enable": "2FA aktiviert",
      "profile.2fa_disable": "2FA deaktiviert",
      "profile.recovery_codes": "Neue Wiederherstellungscodes",
      "profile.2fa_failed": "2FA-Änderung abgelehnt",
      "user.2fa_reset": "2FA zurückgesetzt",
      "audit.export": "Protokoll exportiert",
      "document.create": "Dokument hochgeladen",
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
