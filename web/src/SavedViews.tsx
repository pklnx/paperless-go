import { useId, useState, type FormEvent } from "react";
import { ApiError, savedViewsApi, type SavedView } from "./api";
import { t } from "./i18n";
import { applyView, canSave, moveView, paramsOf, showsView, shownViews, withParams, viewKey } from "./savedViews";
import { Sheet } from "./Sheet";
import { button, danger, input, primary } from "./styles";
import type { Notify } from "./Toast";
import type { ViewState } from "./urlState";

const errorText = (err: unknown) => (err instanceof ApiError && err.status === 404 ? t.viewGone : t.failed(err instanceof Error ? err.message : String(err)));

const item = "flex w-full min-w-0 items-center gap-2 rounded-md px-2 py-1 text-left text-sm pointer-coarse:min-h-11";
const active = "bg-accent-soft font-semibold text-accent-soft-ink";
const idle = "hover:bg-surface-hover";
const section = "px-2 pt-2 text-xs font-semibold tracking-wide text-ink-muted uppercase";
const menuButton =
  "relative flex size-6 shrink-0 items-center justify-center rounded text-ink-muted hover:bg-surface-hover hover:text-ink pointer-coarse:after:absolute pointer-coarse:after:-inset-2.5";

interface ListProps {
  /** The caller's views from the server, in their order; null until loaded. */
  views: SavedView[] | null;
  /** Why loading them failed, or "". */
  error: string;
  state: ViewState;
  admin: boolean;
  /** Opens a view: its URL change (savedViews.applyView). */
  open: (patch: Partial<ViewState>) => void;
  /** A view was changed or deleted: load them again. */
  reload: () => void;
  notify?: Notify;
}

/**
 * The sidebar's "Saved views": the views the web can open, in their manual
 * order. A click opens a view; its menu renames it, sets it to what the
 * list shows now, moves it up or down, or deletes it (asked first).
 */
export function SavedViewList({ views, error, state, admin, open, reload, notify }: ListProps) {
  const [menu, setMenu] = useState<SavedView | null>(null);
  const shown = shownViews(views ?? []);
  if (error && !views) {
    return (
      <p role="alert" className="px-2 text-sm text-danger">
        {t.loadViewsFailed(error)}
      </p>
    );
  }
  if (shown.length === 0) return null;
  const index = menu ? shown.findIndex((s) => s.view.id === menu.id) : -1;
  return (
    <div>
      <h2 className={section}>{t.savedViews}</h2>
      <ul aria-label={t.savedViews} className="mt-1 flex flex-col gap-0.5">
        {shown.map(({ view, params }) => {
          const on = showsView(state, view.id, params, admin);
          return (
            <li key={view.id} className="flex items-center gap-0.5">
              <button type="button" className={`${item} ${on ? active : idle}`} aria-current={on ? "page" : undefined} title={view.name} onClick={() => open(applyView(view.id, params, admin))}>
                <span className="truncate">{view.name}</span>
              </button>
              <button type="button" className={menuButton} aria-label={t.viewActions(view.name)} title={t.viewActions(view.name)} onClick={() => setMenu(view)}>
                <span aria-hidden>⋯</span>
              </button>
            </li>
          );
        })}
      </ul>
      {menu && index >= 0 && (
        <ViewMenu
          view={menu}
          shown={shown.map((s) => s.view)}
          index={index}
          state={state}
          close={() => setMenu(null)}
          done={(text) => {
            setMenu(null);
            reload();
            if (text) notify?.({ text });
          }}
          gone={reload}
        />
      )}
    </div>
  );
}

type Step = "menu" | "rename" | "delete";

/** One view's actions, in a sheet. */
function ViewMenu({
  view,
  shown,
  index,
  state,
  close,
  done,
  gone,
}: {
  view: SavedView;
  shown: SavedView[];
  index: number;
  state: ViewState;
  close: () => void;
  done: (notice: string) => void;
  gone: () => void;
}) {
  const [step, setStep] = useState<Step>("menu");
  const [name, setName] = useState(view.name);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const nameId = useId();
  const run = (work: Promise<unknown>, notice: string) => {
    setBusy(true);
    setError("");
    work.then(
      () => done(notice),
      (err: unknown) => {
        setBusy(false);
        setError(errorText(err));
        if (err instanceof ApiError && err.status === 404) gone();
      },
    );
  };
  const rename = (e: FormEvent) => {
    e.preventDefault();
    const n = name.trim();
    if (!n) return;
    run(savedViewsApi.patch(view.id, { name: n }), "");
  };
  const move = (dir: -1 | 1) => {
    // One after the other: a failure stops before the next.
    run(
      moveView(shown, index, dir).reduce<Promise<unknown>>((p, m) => p.then(() => savedViewsApi.patch(m.id, { sortIndex: m.sortIndex })), Promise.resolve()),
      "",
    );
  };
  const title = step === "delete" ? t.deleteViewTitle(view.name) : step === "rename" ? t.renameView : view.name;
  return (
    <Sheet open onClose={close} title={title}>
      {step === "menu" && (
        <div className="flex flex-col gap-2">
          <button type="button" className={button} disabled={busy} onClick={() => setStep("rename")}>
            {t.renameView}
          </button>
          <button
            type="button"
            className={button}
            disabled={busy || !canSave(state)}
            title={canSave(state) ? undefined : t.updateViewHint}
            onClick={() => run(savedViewsApi.patch(view.id, { filter: withParams(view.filter, paramsOf(state)) }), t.viewUpdated(view.name))}
          >
            {t.updateView}
          </button>
          {!canSave(state) && <p className="text-xs text-ink-muted">{t.updateViewHint}</p>}
          <div className="flex gap-2">
            <button type="button" className={`${button} flex-1`} disabled={busy || index === 0} onClick={() => move(-1)}>
              {t.moveViewUp}
            </button>
            <button type="button" className={`${button} flex-1`} disabled={busy || index === shown.length - 1} onClick={() => move(1)}>
              {t.moveViewDown}
            </button>
          </div>
          <button type="button" className={button} disabled={busy} onClick={() => setStep("delete")}>
            {t.deleteView}
          </button>
        </div>
      )}
      {step === "rename" && (
        <form className="flex flex-col gap-2" onSubmit={rename}>
          <label htmlFor={nameId} className="text-sm font-medium">
            {t.viewName}
          </label>
          <input id={nameId} className={input} value={name} maxLength={128} autoFocus onChange={(e) => setName(e.target.value)} />
          <div className="flex justify-end gap-2">
            <button type="button" className={button} onClick={close}>
              {t.cancel}
            </button>
            <button type="submit" className={primary} disabled={busy || !name.trim()}>
              {t.save}
            </button>
          </div>
        </form>
      )}
      {step === "delete" && (
        <>
          <p className="text-sm">{t.deleteViewText}</p>
          <div className="flex justify-end gap-2">
            <button type="button" className={button} onClick={close}>
              {t.cancel}
            </button>
            <button type="button" className={danger} disabled={busy} onClick={() => run(savedViewsApi.remove(view.id), t.viewDeleted(view.name))}>
              {t.deleteView}
            </button>
          </div>
        </>
      )}
      {error && (
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
      )}
    </Sheet>
  );
}

/**
 * "Save view…" in the list's toolbar, while a search, filter or sort is
 * chosen: asks for a name, then saves what the list shows. saved gets the
 * new view.
 */
export function SaveViewButton({ state, saved }: { state: ViewState; saved: (v: SavedView) => void }) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const nameId = useId();
  if (!canSave(state)) return null;
  const close = () => {
    setOpen(false);
    setError("");
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const n = name.trim();
    if (!n) return;
    setBusy(true);
    setError("");
    savedViewsApi.create(n, { [viewKey]: paramsOf(state) }).then(
      (v) => {
        setBusy(false);
        setOpen(false);
        setName("");
        saved(v);
      },
      (err: unknown) => {
        setBusy(false);
        setError(errorText(err));
      },
    );
  };
  return (
    <>
      <button type="button" className={button} onClick={() => setOpen(true)}>
        {t.saveView}
      </button>
      <Sheet open={open} onClose={close} title={t.saveViewTitle}>
        <form className="flex flex-col gap-2" onSubmit={submit}>
          <p className="text-sm text-ink-muted">{t.saveViewHint}</p>
          <label htmlFor={nameId} className="text-sm font-medium">
            {t.viewName}
          </label>
          <input id={nameId} className={input} value={name} maxLength={128} autoFocus onChange={(e) => setName(e.target.value)} />
          {error && (
            <p role="alert" className="text-sm text-danger">
              {error}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <button type="button" className={button} onClick={close}>
              {t.cancel}
            </button>
            <button type="submit" className={primary} disabled={busy || !name.trim()}>
              {t.save}
            </button>
          </div>
        </form>
      </Sheet>
    </>
  );
}
