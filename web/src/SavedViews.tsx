import { useId, useRef, useState, type FormEvent } from "react";
import { ContainedList, ContainedListItem, FeatureFlags, InlineNotification, MenuItem, MenuItemDivider, OverflowMenu, TextInput } from "@carbon/react";
import { ApiError, savedViewsApi, type SavedView } from "./api";
import { AppModal, DangerModal } from "./DangerModal";
import { t } from "./i18n";
import { applyView, canSave, moveView, paramsOf, showsView, shownViews, viewKey } from "./savedViews";
import { Sheet } from "./Sheet";
import { button, input, primary } from "./styles";
import type { Notify } from "./Toast";
import type { ViewState } from "./urlState";

const errorText = (err: unknown) => (err instanceof ApiError && err.status === 404 ? t.viewGone : t.failed(err instanceof Error ? err.message : String(err)));

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
 * The navigation column's "Saved views": the views the web can open, in
 * their manual order. A click opens a view; its menu renames it, sets it to
 * what the list shows now, moves it up or down, or deletes it (asked first).
 */
export function SavedViewList({ views, error, state, admin, open, reload, notify }: ListProps) {
  const shown = shownViews(views ?? []);
  if (error && !views) {
    return (
      <p role="alert" className="nav-browse__note app-text-error">
        {t.loadViewsFailed(error)}
      </p>
    );
  }
  if (shown.length === 0) return null;
  const done = (text: string) => {
    reload();
    if (text) notify?.({ text });
  };
  return (
    // The OverflowMenu of Carbon v12 (on Menu): the older one adds two tab
    // stops named "Focus sentinel" to every open menu.
    <FeatureFlags enableV12Overflowmenu>
      <ContainedList label={t.savedViews} kind="disclosed" size="sm" className="nav-views">
        {shown.map(({ view, params }, index) => {
          const on = showsView(state, view.id, params, admin);
          return (
            <ContainedListItem
              key={view.id}
              className={on ? "nav-views__item--current" : undefined}
              aria-current={on ? "page" : undefined}
              onClick={() => open(applyView(view.id, params, admin))}
              action={
                <ViewMenu
                  view={view}
                  shown={shown.map((s) => s.view)}
                  index={index}
                  state={state}
                  done={done}
                  gone={reload}
                  notify={notify}
                />
              }
            >
              {view.name}
            </ContainedListItem>
          );
        })}
      </ContainedList>
    </FeatureFlags>
  );
}

type Ask = "rename" | "delete";

/**
 * One view's actions: an OverflowMenu with rename (a modal with the name),
 * update to the current list, move up and down, and delete (a danger
 * modal). Every request is the same as before the menu was Carbon's: a
 * rename sends { name }, an update { paperlessGo } only (the server keeps
 * the app's keys), a move { sortIndex } per affected view, one after the
 * other, and a delete nothing.
 */
function ViewMenu({
  view,
  shown,
  index,
  state,
  done,
  gone,
  notify,
}: {
  view: SavedView;
  shown: SavedView[];
  index: number;
  state: ViewState;
  done: (notice: string) => void;
  gone: () => void;
  notify?: Notify;
}) {
  const [ask, setAsk] = useState<Ask | null>(null);
  const [name, setName] = useState(view.name);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const nameId = useId();
  const hintId = `${nameId}-hint`;
  // The menu's trigger: where the focus goes when a modal closes, as the
  // menu item that opened it is gone by then.
  const menu = useRef<HTMLDivElement>(null);
  const returnFocus = () => menu.current?.querySelector<HTMLButtonElement>("button");
  const close = () => {
    setAsk(null);
    setError("");
  };
  /** Runs one request; its failure shows in the open modal, else as a notice. A 404 reloads the views. */
  const run = (work: Promise<unknown>, notice: string, inModal: boolean) => {
    setBusy(true);
    setError("");
    work.then(
      () => {
        setBusy(false);
        setAsk(null);
        done(notice);
      },
      (err: unknown) => {
        setBusy(false);
        const text = errorText(err);
        if (inModal) setError(text);
        else notify?.({ text, error: true });
        if (err instanceof ApiError && err.status === 404) gone();
      },
    );
  };
  const rename = (e?: FormEvent) => {
    e?.preventDefault();
    const n = name.trim();
    if (!n || busy) return;
    run(savedViewsApi.patch(view.id, { name: n }), "", true);
  };
  const move = (dir: -1 | 1) => {
    // One after the other: a failure stops before the next.
    run(
      moveView(shown, index, dir).reduce<Promise<unknown>>((p, m) => p.then(() => savedViewsApi.patch(m.id, { sortIndex: m.sortIndex })), Promise.resolve()),
      "",
      false,
    );
  };
  const updatable = canSave(state);
  return (
    <>
      <OverflowMenu ref={menu} label={t.viewActions(view.name)} size="sm" menuAlignment="bottom-end" tooltipAlignment="left">
        <MenuItem
          label={t.renameView}
          onClick={() => {
            setName(view.name);
            setAsk("rename");
          }}
        />
        <MenuItem
          label={t.updateView}
          // Only with a search, a filter or a sort to save; the hint (below the
          // menu, read with the item) says so. Carbon's MenuItem has no text of
          // its own for it and sets the item's title itself.
          disabled={!updatable}
          aria-describedby={updatable ? undefined : hintId}
          // Only the web's part: the server keeps the app's keys as they are now, not as loaded.
          onClick={() => run(savedViewsApi.patch(view.id, { paperlessGo: { ...paramsOf(state) } }), t.viewUpdated(view.name), false)}
        />
        <MenuItem label={t.moveViewUp} disabled={index === 0} onClick={() => move(-1)} />
        <MenuItem label={t.moveViewDown} disabled={index === shown.length - 1} onClick={() => move(1)} />
        <MenuItemDivider />
        <MenuItem label={t.deleteView} kind="danger" onClick={() => setAsk("delete")} />
      </OverflowMenu>
      {!updatable && (
        <span id={hintId} className="cds--visually-hidden">
          {t.updateViewHint}
        </span>
      )}
      {ask === "rename" && (
        <AppModal
          size="sm"
          modalHeading={t.renameView}
          primaryButtonText={t.save}
          secondaryButtonText={t.cancel}
          primaryButtonDisabled={busy || !name.trim()}
          preventCloseOnClickOutside
          returnFocus={returnFocus}
          onRequestClose={() => {
            if (!busy) close();
          }}
          onRequestSubmit={() => rename()}
        >
          <form className="modal-stack" onSubmit={rename} noValidate>
            <TextInput id={nameId} data-modal-primary-focus="" labelText={t.viewName} value={name} maxLength={128} onChange={(e) => setName(e.target.value)} />
            {error && <InlineNotification className="notification--full" kind="error" lowContrast hideCloseButton role="alert" statusIconDescription={t.carbon.status.error} subtitle={error} />}
          </form>
        </AppModal>
      )}
      {ask === "delete" && (
        <DangerModal
          size="sm"
          modalHeading={t.deleteViewTitle(view.name)}
          primaryButtonText={t.deleteView}
          secondaryButtonText={t.cancel}
          primaryButtonDisabled={busy}
          returnFocus={returnFocus}
          onRequestClose={() => {
            if (!busy) close();
          }}
          onRequestSubmit={() => {
            if (!busy) run(savedViewsApi.remove(view.id), t.viewDeleted(view.name), true);
          }}
        >
          <div className="modal-stack">
            <p>{t.deleteViewText}</p>
            {error && <InlineNotification className="notification--full" kind="error" lowContrast hideCloseButton role="alert" statusIconDescription={t.carbon.status.error} subtitle={error} />}
          </div>
        </DangerModal>
      )}
    </>
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
