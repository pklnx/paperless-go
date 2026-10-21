import { useState, type FormEvent } from "react";
import { ApiError, profileApi, retryAfter, type User } from "./api";
import { t } from "./i18n";
import { input, primary } from "./styles";

const minLength = 12;

function changeError(err: unknown): string {
  const wait = retryAfter(err);
  if (wait !== null) return t.tooManyAttempts(wait);
  if (err instanceof ApiError && err.status === 401) return t.wrongCurrentPassword;
  return t.failed(err instanceof Error ? err.message : String(err));
}

/** Changes the logged-in user's password; calls done after success. */
export function PasswordForm({ done }: { done: () => void }) {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [repeat, setRepeat] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError("");
    if ([...next].length < minLength) return setError(t.passwordRules);
    if (next !== repeat) return setError(t.passwordsDiffer);
    setBusy(true);
    try {
      await profileApi.changePassword(current, next);
      setCurrent("");
      setNext("");
      setRepeat("");
      done();
    } catch (err) {
      setError(changeError(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form className="flex flex-col gap-3 text-sm" onSubmit={submit}>
      <label className="flex flex-col gap-1">
        {t.currentPassword}
        <input className={input} type="password" required autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} />
      </label>
      <label className="flex flex-col gap-1">
        {t.newPassword}
        <input className={input} type="password" required autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} />
      </label>
      <label className="flex flex-col gap-1">
        {t.repeatPassword}
        <input className={input} type="password" required autoComplete="new-password" value={repeat} onChange={(e) => setRepeat(e.target.value)} />
      </label>
      <p className="text-ink-muted">{t.passwordRules}</p>
      {error && (
        <p role="alert" className="text-danger">
          {error}
        </p>
      )}
      <button type="submit" className={`${primary} self-start`} disabled={busy}>
        {t.changePassword}
      </button>
    </form>
  );
}

/** The user's own login settings: for now the password. */
export function ProfilePage({ user }: { user: User }) {
  const [changed, setChanged] = useState(false);
  return (
    <div className="mx-auto flex w-full max-w-md flex-col gap-4 px-4 py-8">
      <h2 className="text-lg font-semibold">{t.profile}</h2>
      <p className="text-sm text-ink-muted">{t.loggedInAs(user.name)}</p>
      <section className="flex flex-col gap-3 rounded-lg border border-line p-4">
        <h3 className="font-semibold">{t.changePassword}</h3>
        {changed && (
          <p role="status" className="text-sm text-ok">
            {t.passwordChanged}
          </p>
        )}
        <PasswordForm done={() => setChanged(true)} />
      </section>
    </div>
  );
}
