import { useEffect, useId, useRef, useState, type MouseEvent } from "react";
import { Button, Checkbox, CheckboxGroup, RadioButton, RadioButtonGroup } from "@carbon/react";
import { ErrorFilled } from "@carbon/icons-react";
import { ApiError, bulkApi, type BulkAction, type BulkChange, type BulkMatch, type BulkSelection, type Metadata, type NamedRow } from "./api";
import { sortByName } from "./collate";
import { AppModal, DangerModal } from "./DangerModal";
import { t } from "./i18n";
import { flatTagTree } from "./tagTree";
import type { Notify } from "./Toast";
import { PurgeConfirm } from "./Trash";

/** What the bar changes: the checked documents, or every match of the list's search (counted by a dry run). */
export type BulkTarget = { ids: string[] } | { match: BulkMatch; matched: number };

interface Props {
  target: BulkTarget;
  metadata: Metadata | null;
  /** The one owner of the checked documents, when known: the pickers then offer only that owner's names. */
  ownerId: number | null;
  /**
   * How many owners the documents have (the dry run's count for all
   * matches). With more than one, tags cannot be set and the pickers offer
   * only clearing, so no owner's names are mixed with another's.
   */
  owners: number;
  /** After a change went through: the list and counts load again. */
  onDone: () => void;
  /** The list is the trash: the bar offers Restore and Delete permanently only. */
  trash?: boolean;
  /**
   * The owners of the checked documents when an admin checked other users'
   * documents: the confirmations name them. Empty otherwise.
   */
  ownerNames?: readonly string[];
  /**
   * Whether every match may be deleted permanently: only the caller's own
   * trash (match owner me), so not while an admin sees everyone's.
   */
  purgeByMatch?: boolean;
  /**
   * The shell's notice: the result of a trash action goes there, since the
   * documents leave the list, the selection with them, and the bar.
   */
  notify?: Notify;
}

/** What the server's refusal of a bulk change means, in words; whether trying again may help. */
export function bulkErrorText(err: unknown): { text: string; retry: boolean } {
  if (!(err instanceof ApiError)) return { text: t.failed(err instanceof Error ? err.message : String(err)), retry: false };
  const code = typeof err.body.code === "string" ? err.body.code : "";
  switch (code) {
    case "too_many_documents": {
      const limit = typeof err.body.limit === "number" ? err.body.limit : 0;
      const matched = typeof err.body.matched === "number" ? err.body.matched : 0;
      return { text: [matched ? t.bulkMatchCount(matched) : "", t.bulkTooMany(limit)].filter(Boolean).join(" "), retry: false };
    }
    case "several_owners":
      return { text: t.bulkSeveralOwners, retry: false };
    case "owner_mismatch":
      return { text: t.bulkOwnerMismatch, retry: false };
    case "tag_add_and_remove":
      return { text: t.bulkTagAddAndRemove, retry: false };
    case "nothing_to_change":
      return { text: t.bulkNothingToChange, retry: false };
    case "empty_selection":
      return { text: t.bulkEmptySelection, retry: false };
    case "match_changed":
      return { text: t.bulkMatchChangedError, retry: true };
    case "in_trash":
      return { text: t.bulkInTrash, retry: false };
    case "not_in_trash":
      return { text: t.bulkNotInTrash, retry: false };
  }
  if (err.status === 404) return { text: t.bulkNotFound, retry: false };
  // A deadlock lost three times or a busy owner (Retry-After), or no audit log: nothing changed.
  if (err.status === 503) return { text: err.retryAfter && err.retryAfter > 1 ? t.tryAgainIn(err.retryAfter) : t.serverBusy, retry: true };
  return { text: t.failed(err.message), retry: false };
}

type Message = { kind: "ok" | "info"; text: string } | { kind: "error"; text: string; retry?: () => void };

/** Where the focus goes when a modal closes: the bar's button that asked (a ref, since the button is disabled while a dry run counts, and a disabled button has already lost the focus when the modal mounts). */
type ReturnFocus = () => HTMLElement | null | undefined;

/** Carbon's Modal focuses its primary button by default; a confirmation of a change to every match focuses Cancel instead, so a second Enter confirms nothing. */
const focusCancel = ".cds--modal-footer .cds--btn--secondary";

/** Whether c links documents to one owner's rows, which needs documents of a single owner. */
function needsOneOwner(c: BulkChange): boolean {
  return (c.addTags?.length ?? 0) > 0 || (c.removeTags?.length ?? 0) > 0 || typeof c.correspondent === "string" || typeof c.documentType === "string";
}

/** What the confirmation asks about: the change, and the dry run whose count it shows. */
interface Confirm {
  change: BulkChange;
  n: number;
  match: BulkMatch;
  fingerprint: string;
  /** The server refused the last confirmation: the matches changed since then. */
  changed: boolean;
}

/** The question before a trash action: how many, which selection (the dry run's fingerprint for a match). */
interface Ask {
  action: BulkAction;
  n: number;
  sel: BulkSelection;
  /** By search: the confirmation says "every match", and a 409 match_changed counts again. */
  byMatch: boolean;
  changed: boolean;
}

/** What the bar says after a trash action went through. */
const actionDone: Record<BulkAction, (n: number) => string> = {
  trash: (n) => t.bulkTrashed(n),
  restore: (n) => t.bulkRestored(n),
  purge: (n) => t.purgedCount(n),
};

/**
 * The bar of a selection: add or remove tags, set or clear correspondent
 * and type, inbox and favourite, and delete (to the trash); in the trash
 * restore and delete permanently. Each action is one POST
 * /api/documents/bulk. For every match of a search the bar first counts
 * the matches (dry run) and asks before it sends the change. Deleting
 * asks with the count, also for checked documents; deleting permanently
 * always asks (#12); restoring checked documents does not.
 *
 * The look of Carbon's batch-action bar (decision 1 of #150): a bar on the
 * brand colour, sticky at the bottom of the list, with the count and the
 * actions as primary buttons (they blend into the bar, as in Carbon's
 * TableBatchActions). Carbon's own component sits absolutely inside a
 * table toolbar of one row, so the bar is layout.css markup instead. Its
 * forms and confirmations are Carbon Modals; Cancel, Escape and the close
 * button of each send nothing.
 */
export function BulkBar({ target, metadata, ownerId, owners, onDone, trash = false, ownerNames = [], purgeByMatch = true, notify }: Props) {
  const [sheet, setSheet] = useState<"" | "tags" | "corr" | "type">("");
  const [confirm, setConfirm] = useState<Confirm | null>(null);
  const [ask, setAsk] = useState<Ask | null>(null);
  const [busy, setBusy] = useState(false);
  const targetKey = "ids" in target ? `ids:${target.ids.join(",")}` : `match:${JSON.stringify(target.match)}`;
  // The current target, for answers that arrive after it changed.
  const targetRef = useRef(targetKey);
  targetRef.current = targetKey;
  // A message belongs to the selection it was shown for: an error and its
  // "Try again", or "Counting…", were about that one; the result of a
  // change stays. Kept by key, so a message shown right after the
  // selection changed is not dropped by a late effect.
  const [shown, setShown] = useState<{ key: string; msg: Message } | null>(null);
  const msg = shown && (shown.msg.kind === "ok" || shown.key === targetKey) ? shown.msg : null;
  const setMsg = (m: Message | null | ((m: Message | null) => Message | null)) =>
    setShown((s) => {
      const next = typeof m === "function" ? m(s?.msg ?? null) : m;
      return next && { key: targetRef.current, msg: next };
    });
  // Another selection: forget the old one's error, so going back to it does not show it again.
  useEffect(() => setShown((s) => (s && s.msg.kind !== "ok" && s.key !== targetKey ? null : s)), [targetKey]);
  const several = owners > 1;
  // The bar's button that opened the current modal: the modal returns the
  // focus there. While a dry run counts, the buttons are disabled, and a
  // browser drops the focus from a disabled button, so the modal cannot
  // find its opener by itself (AppModal falls back to this).
  const opener = useRef<HTMLButtonElement | null>(null);
  const returnFocus: ReturnFocus = () => opener.current;

  const own = <T extends { ownerId: number }>(rows: readonly T[]) => (ownerId === null ? rows : rows.filter((r) => r.ownerId === ownerId));

  const fail = (err: unknown, again: () => void) => {
    const e = bulkErrorText(err);
    setMsg({ kind: "error", text: e.text, retry: e.retry ? again : undefined });
  };

  /**
   * Sends the change; again is what "Try again" does (by default the same
   * request). changed is what a 409 match_changed does: count again and
   * ask again, never resend by itself.
   */
  const apply = (sel: BulkSelection, change: BulkChange, again?: () => void, changed?: () => void) => {
    setBusy(true);
    setMsg(null);
    bulkApi.apply(sel, change).then(
      (n) => {
        setBusy(false);
        setMsg({ kind: "ok", text: t.bulkUpdated(n) });
        onDone();
      },
      (err: unknown) => {
        setBusy(false);
        if (changed && err instanceof ApiError && err.body.code === "match_changed") changed();
        else fail(err, again ?? (() => apply(sel, change)));
      },
    );
  };

  /** Runs change; after a 409 match_changed (changed) the confirmation says the matches changed. */
  const run = (change: BulkChange, changed = false) => {
    setSheet("");
    if (several && needsOneOwner(change)) {
      setMsg({ kind: "error", text: t.bulkSeveralOwners });
      return;
    }
    if ("ids" in target) {
      apply({ ids: target.ids }, change);
      return;
    }
    // Every match: count again now, then ask with that number.
    const match = target.match;
    const key = targetKey;
    setBusy(true);
    setMsg({ kind: "info", text: t.bulkCounting });
    bulkApi.count(match).then(
      (c) => {
        setBusy(false);
        // Counted for a selection the bar no longer shows: ask nothing.
        if (targetRef.current !== key) {
          setMsg((m) => (m?.kind === "info" ? null : m));
          return;
        }
        if (c.matched === 0) setMsg({ kind: "error", text: t.bulkNoMatches });
        else if (c.matched > c.limit || c.fingerprint === undefined) setMsg({ kind: "error", text: `${t.bulkMatchCount(c.matched)} ${t.bulkTooMany(c.limit)}` });
        // The owners may have changed since the bar was shown.
        else if (c.owners > 1 && needsOneOwner(change)) setMsg({ kind: "error", text: t.bulkSeveralOwners });
        else {
          setMsg(null);
          setConfirm({ change, n: c.matched, match, fingerprint: c.fingerprint, changed });
        }
      },
      (err: unknown) => {
        setBusy(false);
        if (targetRef.current !== key) {
          setMsg((m) => (m?.kind === "info" ? null : m));
          return;
        }
        fail(err, () => run(change, changed));
      },
    );
  };

  /** Sends a trash action; a 409 match_changed of a change by search counts and asks again. */
  const act = (sel: BulkSelection, action: BulkAction, byMatch: boolean) => {
    setBusy(true);
    setMsg(null);
    bulkApi.act(sel, action).then(
      (n) => {
        setBusy(false);
        if (notify) {
          setMsg(null);
          notify({ text: actionDone[action](n), focus: true });
        } else setMsg({ kind: "ok", text: actionDone[action](n) });
        onDone();
      },
      (err: unknown) => {
        setBusy(false);
        if (byMatch && err instanceof ApiError && err.body.code === "match_changed") prepare(action, true);
        else fail(err, byMatch ? () => prepare(action) : () => act(sel, action, false));
      },
    );
  };

  /** A trash action: by IDs at once (restore) or after asking; by search after a dry run of the action, then asking with its count. */
  const prepare = (action: BulkAction, changed = false) => {
    if ("ids" in target) {
      if (action === "restore") act({ ids: target.ids }, action, false);
      else setAsk({ action, n: target.ids.length, sel: { ids: target.ids }, byMatch: false, changed: false });
      return;
    }
    if (action === "purge" && !purgeByMatch) {
      setMsg({ kind: "error", text: t.purgeMatchOwnOnly });
      return;
    }
    // Every match deleted permanently: the caller's own trash only.
    const match: BulkMatch = action === "purge" ? { ...target.match, owner: "me" } : target.match;
    const key = targetKey;
    setBusy(true);
    setMsg({ kind: "info", text: t.bulkCounting });
    bulkApi.count(match, action).then(
      (c) => {
        setBusy(false);
        if (targetRef.current !== key) {
          setMsg((m) => (m?.kind === "info" ? null : m));
          return;
        }
        if (c.matched === 0) setMsg({ kind: "error", text: t.bulkNoMatches });
        else if (c.matched > c.limit || c.fingerprint === undefined) setMsg({ kind: "error", text: `${t.bulkMatchCount(c.matched)} ${t.bulkTooMany(c.limit)}` });
        else {
          setMsg(null);
          setAsk({ action, n: c.matched, sel: { match, fingerprint: c.fingerprint }, byMatch: true, changed });
        }
      },
      (err: unknown) => {
        setBusy(false);
        if (targetRef.current !== key) {
          setMsg((m) => (m?.kind === "info" ? null : m));
          return;
        }
        fail(err, () => prepare(action, changed));
      },
    );
  };

  const confirmAsk = () => {
    if (!ask) return;
    setAsk(null);
    act(ask.sel, ask.action, ask.byMatch);
  };
  const askNote = ask ? (ask.changed ? t.bulkMatchChanged(ask.n) : ask.byMatch ? t.bulkEveryMatch : undefined) : undefined;
  const askOwners = ask && !ask.byMatch ? ownerNames : [];

  const actions: readonly (readonly [string, () => void])[] = trash
    ? [
        [t.restore, () => prepare("restore")],
        [t.purge, () => prepare("purge")],
      ]
    : [
        // Tags are one owner's: with several, say so before anything is sent.
        [t.bulkTags, () => (several ? setMsg({ kind: "error", text: t.bulkSeveralOwners }) : setSheet("tags"))],
        [t.bulkCorrespondent, () => setSheet("corr")],
        [t.bulkType, () => setSheet("type")],
        [t.bulkRemoveFromInbox, () => run({ inbox: false })],
        [t.bulkMoveToInbox, () => run({ inbox: true })],
        [t.bulkFavorite, () => run({ favorite: true })],
        [t.bulkUnfavorite, () => run({ favorite: false })],
        [t.bulkDelete, () => prepare("trash")],
      ];

  const count = "ids" in target ? target.ids.length : target.matched;
  const closeSheet = () => setSheet("");
  // The modal's own forms and confirmations: Cancel, Escape and the close button close without sending anything (Carbon's transactional Modal ignores a click outside).
  return (
    <div role="region" aria-label={t.bulkActions} className="bulk-bar">
      <div className="bulk-bar__row">
        <span className="bulk-bar__count">{t.selectedCount(count)}</span>
        <div className="bulk-bar__actions">
          {actions.map(([label, run]) => (
            <Button
              key={label}
              kind="primary"
              size="md"
              disabled={busy}
              onClick={(e: MouseEvent<HTMLButtonElement>) => {
                opener.current = e.currentTarget;
                run();
              }}
            >
              {label}
            </Button>
          ))}
        </div>
      </div>
      {msg && (
        <p role={msg.kind === "error" ? "alert" : "status"} className={`bulk-bar__msg${msg.kind === "error" ? " bulk-bar__msg--error" : ""}`}>
          {msg.kind === "error" && <ErrorFilled size={16} aria-hidden className="bulk-bar__icon" />}
          <span>{msg.text}</span>
          {msg.kind === "error" && msg.retry && (
            <Button kind="primary" size="sm" onClick={msg.retry}>
              {t.tryAgain}
            </Button>
          )}
        </p>
      )}
      {sheet === "tags" && <TagsModal tags={own(metadata?.tags ?? [])} onApply={run} onClose={closeSheet} returnFocus={returnFocus} />}
      {sheet === "corr" && (
        <NamedModal
          title={t.bulkCorrespondentTitle}
          rows={several ? [] : own(metadata?.correspondents ?? [])}
          onlyClear={several}
          clearLabel={t.bulkClearCorrespondent}
          onApply={(id) => run({ correspondent: id })}
          onClose={closeSheet}
          returnFocus={returnFocus}
        />
      )}
      {sheet === "type" && (
        <NamedModal
          title={t.bulkTypeTitle}
          rows={several ? [] : own(metadata?.documentTypes ?? [])}
          onlyClear={several}
          clearLabel={t.bulkClearType}
          onApply={(id) => run({ documentType: id })}
          onClose={closeSheet}
          returnFocus={returnFocus}
        />
      )}
      {ask && ask.action !== "purge" && (
        <AskModal ask={ask} note={askNote} owners={askOwners} confirm={confirmAsk} cancel={() => setAsk(null)} returnFocus={returnFocus} />
      )}
      <PurgeConfirm
        open={ask !== null && ask.action === "purge"}
        title={t.purgeBulkTitle}
        n={ask?.n ?? 0}
        owners={askOwners}
        note={askNote}
        confirm={confirmAsk}
        cancel={() => setAsk(null)}
        returnFocus={returnFocus}
      />
      {confirm && (
        <AppModal
          size="sm"
          modalHeading={t.bulkConfirmTitle}
          primaryButtonText={t.bulkConfirm(confirm.n)}
          secondaryButtonText={t.cancel}
          selectorPrimaryFocus={focusCancel}
          returnFocus={returnFocus}
          onRequestClose={() => setConfirm(null)}
          onRequestSubmit={() => {
            setConfirm(null);
            // A retry counts again and asks again: the matches may
            // have changed. Only exactly the counted set is changed
            // (the fingerprint); if it changed, count and ask again.
            const change = confirm.change;
            apply({ match: confirm.match, fingerprint: confirm.fingerprint }, change, () => run(change), () => run(change, true));
          }}
        >
          <div className="modal-stack">
            {confirm.changed && <p role="status">{t.bulkMatchChanged(confirm.n)}</p>}
            <p>{t.bulkConfirmText(confirm.n)}</p>
          </div>
        </AppModal>
      )}
    </div>
  );
}

/** The question before checked or matched documents are moved to the trash (a danger modal) or every match is restored. Both open with the focus on Cancel. */
function AskModal({ ask, note, owners, confirm, cancel, returnFocus }: { ask: Ask; note?: string; owners: readonly string[]; confirm: () => void; cancel: () => void; returnFocus: ReturnFocus }) {
  const Modal = ask.action === "trash" ? DangerModal : AppModal;
  return (
    <Modal
      size="sm"
      modalHeading={ask.action === "restore" ? t.bulkRestoreTitle : t.bulkTrashTitle}
      primaryButtonText={ask.action === "restore" ? t.bulkRestoreConfirm(ask.n) : t.bulkTrashConfirm(ask.n)}
      secondaryButtonText={t.cancel}
      selectorPrimaryFocus={focusCancel}
      returnFocus={returnFocus}
      onRequestClose={cancel}
      onRequestSubmit={confirm}
    >
      <div className="modal-stack">
        {note && <p role="status">{note}</p>}
        <p>{ask.action === "restore" ? t.bulkRestoreText(ask.n) : t.bulkTrashText(ask.n)}</p>
        {owners.length > 0 && <p className="app-break-words">{t.purgeOwners(owners.join(", "))}</p>}
      </div>
    </Modal>
  );
}

/**
 * Tags to add and tags to remove, two columns of checkboxes in tree order.
 * A tag checked in both is refused before anything is sent. The focus
 * starts on the first checkbox (Apply, Carbon's default, is disabled until
 * something is chosen and would take no focus); with no tags, on Close.
 */
function TagsModal({ tags, onApply, onClose, returnFocus }: { tags: readonly NonNullable<Metadata["tags"]>[number][]; onApply: (c: BulkChange) => void; onClose: () => void; returnFocus: ReturnFocus }) {
  const [add, setAdd] = useState<Set<string>>(new Set());
  const [remove, setRemove] = useState<Set<string>>(new Set());
  const base = useId();
  const rows = flatTagTree(tags);
  const both = [...add].some((id) => remove.has(id));
  const nothing = add.size === 0 && remove.size === 0;
  const toggle = (set: Set<string>, put: (s: Set<string>) => void, id: string) => {
    const next = new Set(set);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    put(next);
  };
  const submit = () => {
    if (both || nothing) return;
    const c: BulkChange = {};
    if (add.size > 0) c.addTags = [...add];
    if (remove.size > 0) c.removeTags = [...remove];
    onApply(c);
  };
  const column = (which: "add" | "remove", legend: string, set: Set<string>, put: (s: Set<string>) => void) => (
    <CheckboxGroup legendText={legend} className="bulk-tags__column">
      {rows.map(({ tag, depth }, i) => (
        <div key={tag.id} className="bulk-tags__row" style={{ paddingInlineStart: `${Math.min(depth, 6) * 0.75}rem` }}>
          <Checkbox
            id={`${base}-${which}-${tag.id}`}
            labelText={tag.name}
            checked={set.has(tag.id)}
            onChange={() => toggle(set, put, tag.id)}
            {...(which === "add" && i === 0 ? { "data-modal-primary-focus": "" } : {})}
          />
        </div>
      ))}
    </CheckboxGroup>
  );
  return (
    <AppModal
      modalHeading={t.bulkTagsTitle}
      primaryButtonText={t.apply}
      secondaryButtonText={t.cancel}
      primaryButtonDisabled={both || nothing}
      hasScrollingContent
      selectorPrimaryFocus={rows.length === 0 ? ".cds--modal-close" : undefined}
      returnFocus={returnFocus}
      onRequestClose={onClose}
      onRequestSubmit={submit}
    >
      {rows.length === 0 ? (
        <p className="app-text-secondary">{t.bulkNoTags}</p>
      ) : (
        <form
          className="modal-stack"
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
        >
          <div className="bulk-tags">
            {column("add", t.bulkAddTags, add, setAdd)}
            {column("remove", t.bulkRemoveTags, remove, setRemove)}
          </div>
          {both && (
            <p role="alert" className="app-text-error">
              {t.bulkTagAddAndRemove}
            </p>
          )}
        </form>
      )}
    </AppModal>
  );
}

/** One correspondent or type, or none (clears it): a radio group. The focus starts on the first radio button (Apply is disabled until one is chosen). */
function NamedModal({
  title,
  rows,
  onlyClear,
  clearLabel,
  onApply,
  onClose,
  returnFocus,
}: {
  title: string;
  rows: readonly NamedRow[];
  /** Documents of several owners: only clearing is offered, and the form says why. */
  onlyClear?: boolean;
  clearLabel: string;
  onApply: (id: string | null) => void;
  onClose: () => void;
  returnFocus: ReturnFocus;
}) {
  // "" is nothing chosen yet; "-" is the clearing choice.
  const [choice, setChoice] = useState("");
  const base = useId();
  const submit = () => {
    if (choice) onApply(choice === "-" ? null : choice);
  };
  return (
    <AppModal
      modalHeading={title}
      primaryButtonText={t.apply}
      secondaryButtonText={t.cancel}
      primaryButtonDisabled={!choice}
      hasScrollingContent
      returnFocus={returnFocus}
      onRequestClose={onClose}
      onRequestSubmit={submit}
    >
      <form
        className="modal-stack"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        {onlyClear && <p className="app-text-secondary">{t.bulkOnlyClearSeveralOwners}</p>}
        <RadioButtonGroup
          className="bulk-named"
          name={`${base}-named`}
          orientation="vertical"
          legendText={<span className="cds--visually-hidden">{title}</span>}
          valueSelected={choice}
          onChange={(value) => setChoice(String(value))}
        >
          <RadioButton id={`${base}-clear`} value="-" labelText={clearLabel} data-modal-primary-focus="" />
          {sortByName(rows, (r) => r.name).map((r) => (
            <RadioButton key={r.id} id={`${base}-${r.id}`} value={r.id} labelText={r.name} />
          ))}
        </RadioButtonGroup>
      </form>
    </AppModal>
  );
}
