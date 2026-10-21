import type { AuditEntry, AuditFilter } from "./api";
import { t, type Messages } from "./i18n";

/** The action groups of the filter, as the API names them. */
export const auditGroups = ["login", "users", "profile", "export"] as const;

const isoDay = (d: Date) => d.toISOString().slice(0, 10);

/** The default period: the last 30 days. */
export function defaultFilter(now = new Date()): AuditFilter {
  return { since: isoDay(new Date(now.getTime() - 30 * 86_400_000)), until: "", group: "", who: "" };
}

/** The target of an entry in words: its name, otherwise its ID. */
export function targetLabel(e: AuditEntry): string {
  if (!e.target) return "";
  return e.target.name || e.target.id;
}

/**
 * The detail worth a glance: a login's method, second factor and reason, a
 * failed login's name, the role of a new user, the factor that confirmed a
 * 2FA change.
 */
export function entryDetail(e: AuditEntry, m: Messages = t): string {
  const d = e.details;
  const str = (v: unknown) => (typeof v === "string" ? v : "");
  const code = (v: unknown) => (str(v) ? m.auditCode(str(v)) : "");
  if (e.action.startsWith("auth."))
    return [str(d.name) && m.logTriedName(str(d.name)), code(d.method), code(d.factor), code(d.reason)].filter(Boolean).join(", ");
  if (e.action === "profile.2fa_disable" || e.action === "profile.recovery_codes") return code(d.factor);
  if (e.action === "profile.2fa_failed") return [str(d.attempted) && m.auditAction(str(d.attempted)), code(d.reason)].filter(Boolean).join(", ");
  if (e.action === "user.add" && d.admin === true) return m.auditCode("admin");
  return "";
}

/** Who ran a command: the operator ./pgo passes, else the OS user. */
export function commandOperator(e: AuditEntry): string {
  const op = e.details.operator ?? e.details.os_user;
  return typeof op === "string" ? op : "";
}

/** A short form of a user agent: the browser and its platform, like "Firefox 141 · Windows". */
export function shortUserAgent(ua: string): string {
  let browser = "";
  let m: RegExpExecArray | null;
  if ((m = /Edg\/(\d+)/.exec(ua))) browser = `Edge ${m[1]}`;
  else if ((m = /Firefox\/(\d+)/.exec(ua))) browser = `Firefox ${m[1]}`;
  else if ((m = /Chrome\/(\d+)/.exec(ua))) browser = `Chrome ${m[1]}`;
  else if ((m = /Version\/(\d+).*Safari/.exec(ua))) browser = `Safari ${m[1]}`;
  // In this order: Android user agents also say Linux, iPhones "like Mac OS X".
  const platform = ["iPhone", "iPad", "Android", "Windows", "Mac OS X", "Linux"].find((p) => ua.includes(p))?.replace("Mac OS X", "macOS") ?? "";
  const parts = [browser, platform].filter(Boolean);
  return parts.length > 0 ? parts.join(" · ") : ua.slice(0, 40);
}
