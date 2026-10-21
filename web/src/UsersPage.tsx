import { useCallback, useEffect, useState, type FormEvent } from "react";
import { ownsData, usersApi, type GeneratedPassword, type ManagedUser } from "./api";
import { lang, t } from "./i18n";
import { button, input, primary } from "./styles";

const message = (err: unknown) => t.failed(err instanceof Error ? err.message : String(err));

function formatTime(iso: string): string {
  return new Date(iso).toLocaleString(lang, { dateStyle: "medium", timeStyle: "short" });
}

/** User management for admins. */
export function UsersPage() {
  const [users, setUsers] = useState<ManagedUser[] | null>(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [generated, setGenerated] = useState<GeneratedPassword | null>(null);

  const reload = useCallback(() => {
    usersApi.list().then(
      (u) => {
        setUsers(u);
        setError("");
      },
      (err: unknown) => setError(message(err)),
    );
  }, []);
  useEffect(reload, [reload]);

  const run = async (action: () => Promise<unknown>) => {
    setError("");
    try {
      await action();
    } catch (err) {
      setError(message(err));
    }
    reload();
  };

  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-4 px-4 py-8">
      <div className="flex items-center gap-3">
        <h2 className="flex-1 text-lg font-semibold">{t.usersTitle}</h2>
        {!adding && (
          <button type="button" className={primary} onClick={() => setAdding(true)}>
            {t.addUser}
          </button>
        )}
      </div>
      <p className="text-sm text-ink-muted">{t.usersText}</p>

      {generated && <GeneratedPanel value={generated} done={() => setGenerated(null)} />}
      {adding && (
        <AddUserForm
          cancel={() => setAdding(false)}
          added={(g) => {
            setAdding(false);
            setGenerated(g);
            reload();
          }}
        />
      )}
      {error && (
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
      )}
      {!users && !error && <p className="text-sm text-ink-muted">{t.loading}</p>}

      <ul className="flex flex-col gap-3">
        {users?.map((u) => (
          <li key={u.name} className="flex flex-col gap-2 rounded-lg border border-line p-3">
            <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
              <span className="font-semibold">{u.name}</span>
              {u.self && <span className="text-sm text-ink-muted">({t.you})</span>}
              <span className="text-sm text-ink-muted">{u.admin ? t.roleAdmin : t.roleUser}</span>
              <span className={`text-sm ${u.locked ? "text-danger" : "text-ink-muted"}`}>
                {u.locked ? t.stateLocked : u.mustChangePassword ? t.stateMustChange : t.stateActive}
              </span>
              <span className="ml-auto text-sm text-ink-muted">
                {t.colDocuments}: {u.owned.documents} · {t.colLastLogin}: {u.lastLoginAt ? formatTime(u.lastLoginAt) : t.never}
              </span>
            </div>
            {u.self ? (
              <p className="text-sm text-ink-muted">{t.selfHint}</p>
            ) : (
              <div className="flex flex-wrap gap-2">
                <button
                  type="button"
                  className={button}
                  onClick={() => window.confirm(t.confirmResetPassword(u.name)) && run(async () => setGenerated(await usersApi.resetPassword(u.name)))}
                >
                  {t.resetPassword}
                </button>
                <button
                  type="button"
                  className={button}
                  onClick={() => (u.locked || window.confirm(t.confirmLock(u.name))) && run(() => usersApi.setLocked(u.name, !u.locked))}
                >
                  {u.locked ? t.unlock : t.lock}
                </button>
                <button type="button" className={button} onClick={() => run(() => usersApi.setAdmin(u.name, !u.admin))}>
                  {u.admin ? t.removeAdmin : t.makeAdmin}
                </button>
                <button
                  type="button"
                  className={`${button} text-danger`}
                  disabled={ownsData(u)}
                  title={ownsData(u) ? t.ownsData : undefined}
                  onClick={() => window.confirm(t.confirmRemoveUser(u.name)) && run(() => usersApi.remove(u.name))}
                >
                  {t.removeUser}
                </button>
              </div>
            )}
            {!u.self && ownsData(u) && <p className="text-sm text-ink-muted">{t.ownsData}</p>}
          </li>
        ))}
      </ul>
    </div>
  );
}

function AddUserForm({ cancel, added }: { cancel: () => void; added: (g: GeneratedPassword) => void }) {
  const [name, setName] = useState("");
  const [admin, setAdmin] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      added(await usersApi.create(name, admin));
    } catch (err) {
      setError(message(err));
      setBusy(false);
    }
  };

  return (
    <form className="flex flex-col gap-3 rounded-lg border border-line p-3 text-sm" onSubmit={submit}>
      <label className="flex flex-col gap-1">
        {t.userName}
        <input className={input} required autoFocus autoCapitalize="none" spellCheck={false} value={name} onChange={(e) => setName(e.target.value)} />
      </label>
      <label className="flex items-center gap-2">
        <input type="checkbox" checked={admin} onChange={(e) => setAdmin(e.target.checked)} />
        {t.userIsAdmin}
      </label>
      {error && (
        <p role="alert" className="text-danger">
          {error}
        </p>
      )}
      <div className="flex gap-2">
        <button type="submit" className={primary} disabled={busy}>
          {t.addUser}
        </button>
        <button type="button" className={button} onClick={cancel}>
          {t.cancel}
        </button>
      </div>
    </form>
  );
}

/** Shows a generated password once, with a copy button. */
function GeneratedPanel({ value, done }: { value: GeneratedPassword; done: () => void }) {
  const [copied, setCopied] = useState(false);
  return (
    <div role="status" className="flex flex-col gap-2 rounded-lg border border-warning-line bg-warning-soft p-3 text-sm">
      <p className="font-semibold">{t.generatedFor(value.name)}</p>
      <code className="w-fit rounded bg-field px-2 py-1 font-mono text-base select-all">{value.password}</code>
      <p>{t.generatedHint}</p>
      <div className="flex gap-2">
        <button
          type="button"
          className={button}
          onClick={() => navigator.clipboard?.writeText(value.password).then(() => setCopied(true), () => setCopied(false))}
        >
          {copied ? t.copied : t.copy}
        </button>
        <button type="button" className={primary} onClick={done}>
          {t.done}
        </button>
      </div>
    </div>
  );
}
