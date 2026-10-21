import { useEffect, useState, type FormEvent } from "react";
import { ApiError, profileApi, retryAfter, type TwoFactorState, type User } from "./api";
import { t } from "./i18n";
import { PasskeySection } from "./Passkeys";
import { recoveryFileName, recoveryPdf, recoveryText, saveFile, type RecoverySheet } from "./recoveryCodes";
import { button, input, primary } from "./styles";

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

/** The user's own login settings: the password, two-factor login and passkeys. */
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
      <section className="flex flex-col gap-3 rounded-lg border border-line p-4">
        <h3 className="font-semibold">{t.twoFactor}</h3>
        <TwoFactorSettings user={user.name} />
      </section>
      <section className="flex flex-col gap-3 rounded-lg border border-line p-4">
        <h3 className="font-semibold">{t.passkeys}</h3>
        <PasskeySection />
      </section>
    </div>
  );
}

function twoFactorError(err: unknown): string {
  const wait = retryAfter(err);
  if (wait !== null) return t.tooManyAttempts(wait);
  if (err instanceof ApiError && err.status === 401) return err.message.includes("password") ? t.wrongCurrentPassword : t.invalidTwoFactor;
  return t.failed(err instanceof Error ? err.message : String(err));
}

/**
 * New recovery codes, shown once, with downloads made in the browser (the
 * server keeps only their hashes). Only "I have saved the codes" leaves
 * the view.
 */
export function RecoveryCodes({ sheet, done }: { sheet: RecoverySheet; done: () => void }) {
  return (
    <div className="flex flex-col gap-3 text-sm">
      <h3 className="font-semibold">{t.twoFactorRecovery}</h3>
      <p className="text-ink-muted">{t.twoFactorRecoveryText}</p>
      <pre aria-label={t.twoFactorRecovery} className="rounded-md bg-surface-hover p-3 font-mono text-xs select-all">
        {sheet.codes.join("\n")}
      </pre>
      <div className="flex flex-wrap gap-2">
        <button
          type="button"
          className={button}
          onClick={() => saveFile(recoveryPdf(sheet), recoveryFileName(sheet.user, sheet.createdAt, "pdf"), "application/pdf")}
        >
          {t.twoFactorDownloadPdf}
        </button>
        <button
          type="button"
          className={button}
          onClick={() => saveFile(recoveryText(sheet), recoveryFileName(sheet.user, sheet.createdAt, "txt"), "text/plain;charset=utf-8")}
        >
          {t.twoFactorDownloadTxt}
        </button>
      </div>
      <button type="button" className={`${primary} self-start`} onClick={done}>
        {t.twoFactorCodesSaved}
      </button>
    </div>
  );
}

/** A code field: text, not numeric, because recovery codes contain letters. */
function CodeInput({ value, onChange, autoFocus }: { value: string; onChange: (v: string) => void; autoFocus?: boolean }) {
  return (
    <label className="flex flex-col gap-1">
      {t.twoFactorCode}
      <input
        className={input}
        required
        autoFocus={autoFocus}
        autoComplete="one-time-code"
        autoCapitalize="none"
        spellCheck={false}
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
    </label>
  );
}

/**
 * Sets up, shows and turns off the logged-in user's 2FA. onDone runs after
 * a setup, once the recovery codes were saved (the login gate then checks
 * the session again).
 */
export function TwoFactorSettings({ user, onDone }: { user: string; onDone?: () => void }) {
  const [state, setState] = useState<TwoFactorState | null>(null);
  const [setup, setSetup] = useState<TwoFactorState | null>(null);
  const [recovery, setRecovery] = useState<RecoverySheet | null>(null);
  const [code, setCode] = useState("");
  const [disableCode, setDisableCode] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  // Dated when the codes arrive, so every download shows the same time.
  const sheetFor = (codes: string[]): RecoverySheet => ({ user, origin: window.location.origin, createdAt: new Date(), codes });

  const load = () =>
    profileApi.twoFactor.get().then(setState, (err: unknown) => {
      setError(twoFactorError(err));
    });
  useEffect(() => {
    void load();
  }, []);

  const run = async (e: FormEvent | null, action: () => Promise<void>) => {
    e?.preventDefault();
    setBusy(true);
    setError("");
    try {
      await action();
    } catch (err) {
      setError(twoFactorError(err));
    } finally {
      setBusy(false);
    }
  };

  const start = () =>
    run(null, async () => {
      setSetup(await profileApi.twoFactor.setup());
    });
  const confirm = (e: FormEvent) =>
    run(e, async () => {
      const result = await profileApi.twoFactor.confirm(code);
      // onDone only after the recovery codes were shown (see RecoveryCodes).
      setRecovery(sheetFor(result.recoveryCodes));
      setSetup(null);
      setCode("");
    });
  const regenerate = (e: FormEvent) =>
    run(e, async () => {
      const result = await profileApi.twoFactor.regenerateRecoveryCodes(code);
      setRecovery(sheetFor(result.recoveryCodes));
      setCode("");
    });
  const disable = (e: FormEvent) =>
    run(e, async () => {
      await profileApi.twoFactor.disable(password, disableCode);
      setPassword("");
      setDisableCode("");
      await load();
    });

  const errorLine = error && (
    <p role="alert" className="text-sm text-danger">
      {error}
    </p>
  );
  if (recovery) {
    return (
      <RecoveryCodes
        sheet={recovery}
        done={() => {
          setRecovery(null);
          void load();
          onDone?.();
        }}
      />
    );
  }
  if (!state) return error ? errorLine : <p className="text-sm text-ink-muted">{t.loading}</p>;
  if (!state.enabled) {
    if (!setup) {
      return (
        <div className="flex flex-col gap-3 text-sm">
          <p className="text-ink-muted">{state.required ? t.twoFactorRequired : t.twoFactorOffText}</p>
          <button type="button" className={`${primary} self-start`} onClick={start} disabled={busy}>
            {t.twoFactorSetup}
          </button>
          {errorLine}
        </div>
      );
    }
    return (
      <div className="flex flex-col gap-3 text-sm">
        {setup.qrDataUrl && <img src={setup.qrDataUrl} alt={t.twoFactorQR} className="h-56 w-56 self-center rounded bg-white p-2" />}
        <p className="text-ink-muted">{t.twoFactorSetupText}</p>
        <p className="text-ink-muted">{t.twoFactorManualKey}</p>
        <code className="rounded-md bg-surface-hover p-2 font-mono text-xs break-all select-all">{setup.secret}</code>
        <form className="flex flex-col gap-3" onSubmit={confirm}>
          <CodeInput value={code} onChange={setCode} autoFocus />
          {errorLine}
          <button type="submit" className={`${primary} self-start`} disabled={busy}>
            {t.twoFactorConfirm}
          </button>
        </form>
      </div>
    );
  }
  return (
    <div className="flex flex-col gap-4 text-sm">
      <p className="text-ok">{t.twoFactorOn}</p>
      <p className="text-ink-muted">{t.recoveryCodesLeft(state.recoveryCodesLeft)}</p>
      <form className="flex flex-col gap-3" onSubmit={regenerate}>
        <p className="text-ink-muted">{t.twoFactorRegenerateText}</p>
        <CodeInput value={code} onChange={setCode} />
        <button type="submit" className={`${button} self-start`} disabled={busy}>
          {t.twoFactorRegenerate}
        </button>
      </form>
      {state.required ? (
        <p className="text-ink-muted">{state.admin ? t.twoFactorAdminRequired : t.twoFactorEveryoneRequired}</p>
      ) : (
        <form className="flex flex-col gap-3 border-t border-line pt-4" onSubmit={disable}>
          <p className="text-ink-muted">{t.twoFactorDisableText}</p>
          <label className="flex flex-col gap-1">
            {t.currentPassword}
            <input className={input} type="password" required autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
          </label>
          <CodeInput value={disableCode} onChange={setDisableCode} />
          <button type="submit" className={`${button} self-start text-danger`} disabled={busy}>
            {t.twoFactorDisable}
          </button>
        </form>
      )}
      {errorLine}
    </div>
  );
}
