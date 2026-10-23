// The demo server the end-to-end tests run against (tools/demo), its
// made-up logins and seed documents. The values match tools/demo/main.go
// and tools/demo/seed.go.
import { createHmac, randomInt } from "node:crypto";

/** The demo's port: E2E_PORT, so that two local runs do not collide. */
export const port = Number(process.env.E2E_PORT || 18280);
// localhost, not 127.0.0.1: the server checks the Host header (localhost
// is allowed by default), and passkeys need a host name.
export const origin = `http://localhost:${port}`;

export interface Login {
  name: string;
  password: string;
  totpSecret?: string;
}

/** The demo admin: owns the seed documents. Admins need 2FA; globalSetup logs it in once. */
export const admin = { name: "demo", password: "demo-password", totpSecret: "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP" } satisfies Login;
/** A user with 2FA for the login test only, so its time steps are its own. */
export const twoFactorUser = { name: "kim", password: "kim-password", totpSecret: "KRSXG5BAKRSXG5BAKRSXG5BAKRSXG5BA" } satisfies Login;
/** An admin without 2FA: must set it up before anything else. */
export const adminWithout2FA = { name: "neo", password: "neo-password" } satisfies Login;
/** A user with a generated password: must choose their own first. */
export const generatedPasswordUser = { name: "pat", password: "pat-password" } satisfies Login;
/** A locked user. */
export const lockedUser = { name: "lee", password: "lee-password" } satisfies Login;
/** A plain user with two documents of their own; the API key test. Its password never changes. */
export const plainUser = { name: "max", password: "max-password" } satisfies Login;
/** A plain user for the password change test. */
export const passwordChangeUser = { name: "ida", password: "ida-password" } satisfies Login;
/** A user with 2FA whose 2FA the admin spec resets. Nothing else logs in as eli. */
export const resetTwoFactorUser = { name: "eli", password: "eli-password" } satisfies Login;

/** Seed documents of demo the tests use: the title and the word only this document has. */
export const seed = {
  electricity: { title: "Electricity bill October 2026", word: "amberlight" },
  water: { title: "Water bill September 2026", word: "bluefinch" },
  incomeTax: { title: "Income tax assessment 2025", word: "copperleaf" },
  lease: { title: "Apartment lease", word: "glimmerbay" },
  termination: { title: "Kündigung des Mietvertrags", word: "ironquill" },
  internet: { title: "Internet bill August 2026", word: "juniperfox" },
  dentist: { title: "Dentist appointment reminder", word: "kestrelwind" },
  warranty: { title: "Warranty card for the washing machine", word: "lanternbrook" },
  bank: { title: "Letter from the bank", word: "mossharbor" },
  /** max's documents: never in demo's own list. */
  gym: { title: "Gym membership", word: "nettlecrest" },
  phone: { title: "Phone contract", word: "opalridge" },
};

/**
 * The documents the specs of bulk edit, trash and metadata change, each
 * for one spec. They are the oldest ones, so a spec finds them by a
 * search for their word. Same as workDocs in tools/demo/seed.go.
 */
export const work = {
  /** 52 documents with one word: more than the 50 of a page. */
  batch: { word: "bulkbatch", count: 52 },
  /** Two documents with one word. */
  pair: { titles: ["Bulk pair one", "Bulk pair two"], word: "bulkpair" },
  trashUndo: { title: "Trash flow undo", word: "trashundo" },
  trashPurge: { title: "Trash flow purge", word: "trashpurge" },
  /** The lines of its PDF (the seed adds the reference word to the last of the first page): the same file can be uploaded again. */
  trashAgain: { title: "Trash flow upload again", word: "trashagain", lines: ["Moved to the trash, then uploaded again", "The same file."] },
  metadata: { title: "Metadata sample", word: "metasample" },
};

/** The names the bulk edit spec sets: seeded, so that they exist before the spec. */
export const bulkNames = { tag: "Review", correspondent: "Meridian Bank", type: "Statement" };

/** demo's documents tagged Bills (a sub-tag of Finance). */
export const bills = [seed.electricity.title, seed.water.title, seed.internet.title];

/** The current RFC 6238 time step (30 seconds). */
export function timeStep(now = Date.now()): number {
  return Math.floor(now / 30000);
}

/** The six-digit TOTP code of a base32 secret for a time step. */
export function totp(secret: string, step: number): string {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = "";
  for (const c of secret.replace(/[\s=]/g, "").toUpperCase()) bits += alphabet.indexOf(c).toString(2).padStart(5, "0");
  const key = Buffer.from((bits.match(/.{8}/g) ?? []).map((b) => parseInt(b, 2)));
  const msg = Buffer.alloc(8);
  msg.writeBigUInt64BE(BigInt(step));
  const mac = createHmac("sha1", key).update(msg).digest();
  const off = mac[mac.length - 1]! & 0xf;
  return String((mac.readUInt32BE(off) & 0x7fffffff) % 1000000).padStart(6, "0");
}

/** A name no earlier run has used, for things a test creates. */
export function unique(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}${randomInt(1296).toString(36)}`;
}

/** A word of letters only that no earlier run has used: a search finds it as one word. */
export function uniqueWord(prefix: string): string {
  const digits = `${Date.now().toString(36)}${randomInt(46656).toString(36)}`;
  // Digits become letters, so the word stays one token for the search.
  return prefix + [...digits].map((c) => (/[0-9]/.test(c) ? "abcdefghij"[Number(c)] : c)).join("");
}
