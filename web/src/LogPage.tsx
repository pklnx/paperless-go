import { useEffect, useState } from "react";
import { auditApi, usersApi, type AuditEntry, type AuditFilter, type ManagedUser } from "./api";
import { auditGroups, commandOperator, defaultFilter, entryDetail, shortUserAgent, targetLabel } from "./auditLog";
import { lang, t } from "./i18n";
import { button } from "./styles";

const control = "rounded-md border border-line-control bg-field px-2.5 py-1.5 text-sm pointer-coarse:min-h-11";
const message = (err: unknown) => t.failed(err instanceof Error ? err.message : String(err));

function formatTime(iso: string): string {
  return new Date(iso).toLocaleString(lang, { dateStyle: "medium", timeStyle: "medium" });
}

/** The audit log: a user's own entries, or everyone's for admins. */
export function LogPage({ admin }: { admin: boolean }) {
  const [filter, setFilter] = useState<AuditFilter>(() => defaultFilter());
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [cursor, setCursor] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [users, setUsers] = useState<ManagedUser[]>([]);

  useEffect(() => {
    if (admin) usersApi.list().then(setUsers, () => setUsers([]));
  }, [admin]);

  useEffect(() => {
    const ctl = new AbortController();
    setLoading(true);
    setError("");
    auditApi.list(filter, "", ctl.signal).then(
      (page) => {
        setEntries(page.entries);
        setCursor(page.nextCursor ?? "");
        setLoading(false);
      },
      (err: unknown) => {
        if (ctl.signal.aborted) return;
        setEntries([]);
        setCursor("");
        setError(message(err));
        setLoading(false);
      },
    );
    return () => ctl.abort();
  }, [filter]);

  const more = async () => {
    setLoading(true);
    try {
      const page = await auditApi.list(filter, cursor);
      setEntries((cur) => [...cur, ...page.entries]);
      setCursor(page.nextCursor ?? "");
    } catch (err) {
      setError(message(err));
    } finally {
      setLoading(false);
    }
  };

  const set = (patch: Partial<AuditFilter>) => setFilter((f) => ({ ...f, ...patch }));

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-4 px-4 py-8">
      <h2 className="text-lg font-semibold">{t.logTitle}</h2>
      <p className="text-sm text-ink-muted">{admin ? t.logAdminNote : t.logOwnNote}</p>

      <div className="flex flex-wrap items-end gap-3 text-sm">
        <label className="flex flex-col gap-1">
          {t.logFrom}
          <input className={control} type="date" value={filter.since} onChange={(e) => set({ since: e.target.value })} />
        </label>
        <label className="flex flex-col gap-1">
          {t.logTo}
          <input className={control} type="date" value={filter.until} onChange={(e) => set({ until: e.target.value })} />
        </label>
        <label className="flex flex-col gap-1">
          {t.logAction}
          <select className={control} value={filter.group} onChange={(e) => set({ group: e.target.value })}>
            <option value="">{t.logAllActions}</option>
            {auditGroups.map((g) => (
              <option key={g} value={g}>
                {t.logGroup(g)}
              </option>
            ))}
          </select>
        </label>
        {admin && (
          <label className="flex flex-col gap-1">
            {t.logWho}
            <select className={control} value={filter.who} onChange={(e) => set({ who: e.target.value })}>
              <option value="">{t.logEveryone}</option>
              <option value="cli">{t.logCommandLine}</option>
              <option value="api_key">{t.logApiKeys}</option>
              {users.map((u) => (
                <option key={u.name} value={u.name}>
                  {u.name}
                </option>
              ))}
            </select>
          </label>
        )}
      </div>

      {error && (
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
      )}
      {!loading && !error && entries.length === 0 && <p className="text-sm text-ink-muted">{t.logEmpty}</p>}

      {entries.length > 0 && (
        <ul className="flex flex-col divide-y divide-line rounded-lg border border-line text-sm">
          {entries.map((e) => (
            <li key={e.id} className="px-3 py-2">
              <EntryLine entry={e} admin={admin} />
            </li>
          ))}
        </ul>
      )}
      {cursor && (
        <button type="button" className={`${button} self-start`} disabled={loading} onClick={() => void more()}>
          {t.logMore}
        </button>
      )}
      {loading && entries.length === 0 && <p className="text-sm text-ink-muted">{t.loading}</p>}
    </div>
  );
}

function EntryLine({ entry: e, admin }: { entry: AuditEntry; admin: boolean }) {
  const target = targetLabel(e);
  const detail = entryDetail(e);
  const failed = e.action === "auth.login_failed";
  const operator = e.actor === "cli" ? commandOperator(e) : "";
  return (
    <div className="flex flex-col gap-0.5 md:grid md:grid-cols-[13rem_1fr_14rem] md:gap-3">
      <span className="text-ink-muted tabular-nums">{formatTime(e.at)}</span>
      <span className="min-w-0">
        <span className={`font-medium ${failed ? "text-danger" : ""}`}>{t.auditAction(e.action)}</span>
        {admin && e.user && <span className="text-ink-muted"> · {e.user}</span>}
        {e.actor === "cli" && <span className="text-ink-muted"> · {operator ? t.logCLIBy(operator) : t.logCLI}</span>}
        {e.apiKey && <span className="text-ink-muted"> · {t.logViaKey(e.apiKey)}</span>}
        {target && <span> · {target}</span>}
        {detail && <span className="block break-words text-ink-muted">{detail}</span>}
      </span>
      <span className="truncate text-xs text-ink-muted" title={e.userAgent ?? ""}>
        {[e.clientIp, e.userAgent ? shortUserAgent(e.userAgent) : ""].filter(Boolean).join(" · ")}
      </span>
    </div>
  );
}
