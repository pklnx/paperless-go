import { useEffect, useRef, useState } from "react";
import { ApiError, bulkApi, type BulkChange, type BulkMatch, type BulkSelection, type Metadata, type NamedRow } from "./api";
import { sortByName } from "./collate";
import { t } from "./i18n";
import { Sheet } from "./Sheet";
import { button, primary } from "./styles";
import { flatTagTree } from "./tagTree";

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
  }
  if (err.status === 404) return { text: t.bulkNotFound, retry: false };
  // A deadlock lost three times (Retry-After), or no audit log: nothing changed.
  if (err.status === 503) return { text: t.serverBusy, retry: true };
  return { text: t.failed(err.message), retry: false };
}

type Message = { kind: "ok" | "info"; text: string } | { kind: "error"; text: string; retry?: () => void };

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

/**
 * The bar of a selection: add or remove tags, set or clear correspondent
 * and type, inbox and favourite. Each action is one POST
 * /api/documents/bulk. For every match of a search the bar first counts
 * the matches (dry run) and asks before it sends the change.
 */
export function BulkBar({ target, metadata, ownerId, owners, onDone }: Props) {
  const [sheet, setSheet] = useState<"" | "tags" | "corr" | "type">("");
  const [confirm, setConfirm] = useState<Confirm | null>(null);
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

  const count = "ids" in target ? target.ids.length : target.matched;
  return (
    <div role="region" aria-label={t.bulkActions} className="sticky bottom-0 z-10 -mx-4 flex flex-col gap-2 border-t border-line bg-surface-raised px-4 pt-2 pb-[calc(0.5rem+env(safe-area-inset-bottom))] shadow-lg md:mx-0 md:rounded-lg md:border">
      <div className="flex gap-2 max-sm:overflow-x-auto sm:flex-wrap">
        <span className="shrink-0 self-center text-sm font-medium">{t.selectedCount(count)}</span>
        {(
          [
            // Tags are one owner's: with several, say so before anything is sent.
            [t.bulkTags, () => (several ? setMsg({ kind: "error", text: t.bulkSeveralOwners }) : setSheet("tags"))],
            [t.bulkCorrespondent, () => setSheet("corr")],
            [t.bulkType, () => setSheet("type")],
            [t.bulkRemoveFromInbox, () => run({ inbox: false })],
            [t.bulkMoveToInbox, () => run({ inbox: true })],
            [t.bulkFavorite, () => run({ favorite: true })],
            [t.bulkUnfavorite, () => run({ favorite: false })],
          ] as const
        ).map(([label, act]) => (
          <button key={label} type="button" className={`${button} shrink-0`} disabled={busy} onClick={act}>
            {label}
          </button>
        ))}
      </div>
      {msg && (
        <p role={msg.kind === "error" ? "alert" : "status"} className={`text-sm ${msg.kind === "error" ? "text-danger" : "text-ink-muted"}`}>
          {msg.text}
          {msg.kind === "error" && msg.retry && (
            <button type="button" className="ml-2 text-accent-ink hover:underline pointer-coarse:min-h-11" onClick={msg.retry}>
              {t.tryAgain}
            </button>
          )}
        </p>
      )}
      <Sheet open={sheet === "tags"} onClose={() => setSheet("")} title={t.bulkTagsTitle}>
        <TagsForm tags={own(metadata?.tags ?? [])} onApply={run} />
      </Sheet>
      <Sheet open={sheet === "corr"} onClose={() => setSheet("")} title={t.bulkCorrespondentTitle}>
        <NamedForm rows={several ? [] : own(metadata?.correspondents ?? [])} onlyClear={several} clearLabel={t.bulkClearCorrespondent} onApply={(id) => run({ correspondent: id })} />
      </Sheet>
      <Sheet open={sheet === "type"} onClose={() => setSheet("")} title={t.bulkTypeTitle}>
        <NamedForm rows={several ? [] : own(metadata?.documentTypes ?? [])} onlyClear={several} clearLabel={t.bulkClearType} onApply={(id) => run({ documentType: id })} />
      </Sheet>
      <Sheet open={confirm !== null} onClose={() => setConfirm(null)} title={t.bulkConfirmTitle}>
        {confirm && (
          <>
            {confirm.changed && (
              <p role="status" className="text-sm font-medium">
                {t.bulkMatchChanged(confirm.n)}
              </p>
            )}
            <p className="text-sm">{t.bulkConfirmText(confirm.n)}</p>
            <div className="flex justify-end gap-2">
              <button type="button" className={button} onClick={() => setConfirm(null)}>
                {t.cancel}
              </button>
              <button
                type="button"
                className={primary}
                onClick={() => {
                  setConfirm(null);
                  // A retry counts again and asks again: the matches may
                  // have changed. Only exactly the counted set is changed
                  // (the fingerprint); if it changed, count and ask again.
                  const change = confirm.change;
                  apply({ match: confirm.match, fingerprint: confirm.fingerprint }, change, () => run(change), () => run(change, true));
                }}
              >
                {t.bulkConfirm(confirm.n)}
              </button>
            </div>
          </>
        )}
      </Sheet>
    </div>
  );
}

/** Tags to add and tags to remove, in tree order. A tag checked in both is refused before anything is sent. */
function TagsForm({ tags, onApply }: { tags: readonly NonNullable<Metadata["tags"]>[number][]; onApply: (c: BulkChange) => void }) {
  const [add, setAdd] = useState<Set<string>>(new Set());
  const [remove, setRemove] = useState<Set<string>>(new Set());
  const rows = flatTagTree(tags);
  if (rows.length === 0) return <p className="text-sm text-ink-muted">{t.bulkNoTags}</p>;
  const both = [...add].some((id) => remove.has(id));
  const toggle = (set: Set<string>, put: (s: Set<string>) => void, id: string) => {
    const next = new Set(set);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    put(next);
  };
  const column = (legend: string, set: Set<string>, put: (s: Set<string>) => void) => (
    <fieldset className="flex min-w-0 flex-1 flex-col gap-0.5">
      <legend className="mb-1 text-sm font-semibold">{legend}</legend>
      {rows.map(({ tag, depth }) => (
        <label key={tag.id} className="flex items-center gap-2 text-sm pointer-coarse:min-h-11" style={{ paddingLeft: `${Math.min(depth, 6) * 0.75}rem` }}>
          <input type="checkbox" checked={set.has(tag.id)} onChange={() => toggle(set, put, tag.id)} />
          <span className="min-w-0 wrap-anywhere">{tag.name}</span>
        </label>
      ))}
    </fieldset>
  );
  return (
    <form
      className="flex flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        if (both || (add.size === 0 && remove.size === 0)) return;
        const c: BulkChange = {};
        if (add.size > 0) c.addTags = [...add];
        if (remove.size > 0) c.removeTags = [...remove];
        onApply(c);
      }}
    >
      <div className="flex gap-4">
        {column(t.bulkAddTags, add, setAdd)}
        {column(t.bulkRemoveTags, remove, setRemove)}
      </div>
      {both && (
        <p role="alert" className="text-sm text-danger">
          {t.bulkTagAddAndRemove}
        </p>
      )}
      <button type="submit" className={`${primary} self-end`} disabled={both || (add.size === 0 && remove.size === 0)}>
        {t.apply}
      </button>
    </form>
  );
}

/** One correspondent or type, or none (clears it). */
function NamedForm({
  rows,
  onlyClear,
  clearLabel,
  onApply,
}: {
  rows: readonly NamedRow[];
  /** Documents of several owners: only clearing is offered, and the form says why. */
  onlyClear?: boolean;
  clearLabel: string;
  onApply: (id: string | null) => void;
}) {
  // "" is nothing chosen yet; "-" is the clearing choice.
  const [choice, setChoice] = useState("");
  const option = (value: string, label: string) => (
    <label key={value} className="flex items-center gap-2 text-sm pointer-coarse:min-h-11">
      <input type="radio" name="bulk-named" value={value} checked={choice === value} onChange={() => setChoice(value)} />
      <span className="min-w-0 wrap-anywhere">{label}</span>
    </label>
  );
  return (
    <form
      className="flex flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        if (choice) onApply(choice === "-" ? null : choice);
      }}
    >
      {onlyClear && <p className="text-sm text-ink-muted">{t.bulkOnlyClearSeveralOwners}</p>}
      <div className="flex flex-col gap-0.5">
        {option("-", clearLabel)}
        {sortByName(rows, (r) => r.name).map((r) => option(r.id, r.name))}
      </div>
      <button type="submit" className={`${primary} self-end`} disabled={!choice}>
        {t.apply}
      </button>
    </form>
  );
}
