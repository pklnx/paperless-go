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

  // Passkeys
  or: "or",
  passkeys: "Passkeys",
  passkeySignIn: "Log in with a passkey",
  passkeyCancelled: "No passkey was used.",
  passkeyFailed: "The passkey was not accepted.",
  passkeyUnknown: "This passkey is not registered (any more). Log in with your password.",
  passkeyExpired: "The passkey login took too long. Try again.",
  passkeysText: "Log in with Face ID, Touch ID, Windows Hello or a security key, without password and code.",
  passkeysUnavailable: "Passkeys are not set up on this server (PAPERLESS_GO_PUBLIC_URL).",
  passkeysOtherOrigin: (origin: string) => `Passkeys work only at ${origin}.`,
  passkeysUnsupported: "This browser does not support passkeys.",
  passkeyName: "Name of the passkey",
  passkeyNameHint: "For example iPhone or YubiKey",
  passkeyAdd: "Add passkey",
  passkeyAdded: (name: string) => `Passkey “${name}” added.`,
  passkeyConfirm2FA: "Confirm with your password and an authentication code.",
  passkeyConfirm: "Confirm with your password.",
  passkeyRemove: "Remove",
  passkeyRemoveConfirm: (name: string) => `Remove the passkey “${name}”? Your other devices are logged out.`,
  passkeyUsed: (when: string) => `last used ${when}`,
  passkeyNeverUsed: "never used",
  passkeyNone: "No passkeys yet.",
  passkeyLimit: (n: number) => `You have the maximum of ${n} passkeys. Remove one first.`,
  passkeyOnDevice: "This device already has a passkey for you.",
  passkeyNameTaken: "You already have a passkey with this name.",
  passkeyCount: (n: number) => (n === 1 ? "1 passkey" : `${n} passkeys`),
  removePasskeys: "Remove passkeys",
  confirmRemovePasskeys: (name: string, n: number) => `Remove the ${n} passkey(s) of “${name}”? They are logged out everywhere.`,
  resetAlsoPasskeys: (n: number) => `Also remove their ${n} passkey(s) (recommended after a compromise)`,
  confirmReset: "Reset password",

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
  logOwnNote: "Your logins, failed logins and logouts, and the changes you made to your password, two-factor login and passkeys. Admins see these entries too.",
  logAdminNote: "Every user's logins, failed logins and logouts, and every change of users, passwords, two-factor logins and passkeys, also from the command line.",
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
      passkey: "passkey",
      unknown_passkey: "unknown passkey",
      passkey_invalid: "passkey not accepted",
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
      "user.passkeys_removed": "Removed passkeys",
      "profile.passkey_add": "Added passkey",
      "profile.passkey_remove": "Removed passkey",
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

  or: "oder",
  passkeys: "Passkeys",
  passkeySignIn: "Mit Passkey anmelden",
  passkeyCancelled: "Es wurde kein Passkey verwendet.",
  passkeyFailed: "Der Passkey wurde nicht akzeptiert.",
  passkeyUnknown: "Dieser Passkey ist nicht (mehr) registriert. Melde dich mit deinem Passwort an.",
  passkeyExpired: "Die Anmeldung mit Passkey hat zu lange gedauert. Versuche es noch einmal.",
  passkeysText: "Melde dich mit Face ID, Touch ID, Windows Hello oder einem Sicherheitsschlüssel an, ohne Passwort und Code.",
  passkeysUnavailable: "Passkeys sind auf diesem Server nicht eingerichtet (PAPERLESS_GO_PUBLIC_URL).",
  passkeysOtherOrigin: (origin) => `Passkeys funktionieren nur unter ${origin}.`,
  passkeysUnsupported: "Dieser Browser unterstützt keine Passkeys.",
  passkeyName: "Name des Passkeys",
  passkeyNameHint: "Zum Beispiel iPhone oder YubiKey",
  passkeyAdd: "Passkey hinzufügen",
  passkeyAdded: (name) => `Passkey „${name}“ hinzugefügt.`,
  passkeyConfirm2FA: "Bestätige mit deinem Passwort und einem Bestätigungscode.",
  passkeyConfirm: "Bestätige mit deinem Passwort.",
  passkeyRemove: "Entfernen",
  passkeyRemoveConfirm: (name) => `Passkey „${name}“ entfernen? Deine anderen Geräte werden abgemeldet.`,
  passkeyUsed: (when) => `zuletzt genutzt ${when}`,
  passkeyNeverUsed: "noch nie genutzt",
  passkeyNone: "Noch keine Passkeys.",
  passkeyLimit: (n) => `Du hast bereits die maximal ${n} Passkeys. Entferne zuerst einen.`,
  passkeyOnDevice: "Auf diesem Gerät gibt es schon einen Passkey für dich.",
  passkeyNameTaken: "Du hast schon einen Passkey mit diesem Namen.",
  passkeyCount: (n) => (n === 1 ? "1 Passkey" : `${n} Passkeys`),
  removePasskeys: "Passkeys entfernen",
  confirmRemovePasskeys: (name, n) => `${n} Passkey(s) von „${name}“ entfernen? Der Benutzer wird überall abgemeldet.`,
  resetAlsoPasskeys: (n) => `Auch die ${n} Passkey(s) entfernen (empfohlen nach einer Kompromittierung)`,
  confirmReset: "Passwort zurücksetzen",

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
    "Deine Anmeldungen, fehlgeschlagenen Anmeldungen und Abmeldungen und die Änderungen, die du an deinem Passwort, deiner Zwei-Faktor-Anmeldung und deinen Passkeys vorgenommen hast. Admins sehen diese Einträge ebenfalls.",
  logAdminNote:
    "Die Anmeldungen, fehlgeschlagenen Anmeldungen und Abmeldungen aller Benutzer und jede Änderung an Benutzern, Passwörtern, Zwei-Faktor-Anmeldungen und Passkeys, auch über die Kommandozeile.",
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
      passkey: "Passkey",
      unknown_passkey: "unbekannter Passkey",
      passkey_invalid: "Passkey nicht akzeptiert",
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
      "user.passkeys_removed": "Passkeys entfernt",
      "profile.passkey_add": "Passkey hinzugefügt",
      "profile.passkey_remove": "Passkey entfernt",
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
