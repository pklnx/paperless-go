import { useState } from "react";
import { Button, InlineNotification } from "@carbon/react";
import { ApiError, bulkApi, type BulkMatch, type DocumentSummary, type User } from "./api";
import { bulkErrorText } from "./BulkBar";
import { DangerModal } from "./DangerModal";
import { lang, t } from "./i18n";
import type { Notify } from "./Toast";

// Parts of the trash (#12) shared by the list, the bulk bar and the detail.

/** A day as the trash shows it ("Deleted on 3 Oct 2026"), or "" for none. */
export function trashDay(iso: string | null | undefined): string {
  if (!iso) return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleDateString(lang, { dateStyle: "medium" });
}

/** Whether a document is in the trash. */
export const inTrash = (d: Pick<DocumentSummary, "deletedAt">) => typeof d.deletedAt === "string" && d.deletedAt !== "";

/** What went wrong with a single delete (trash), restore or purge, in words about that one document. */
export function trashErrorText(err: unknown, action: "trash" | "restore" | "purge"): string {
  if (!(err instanceof ApiError)) return t.failed(err instanceof Error ? err.message : String(err));
  const code = err.body.code;
  if (code === "in_trash") return t.alreadyInTrash;
  if (code === "not_in_trash") return t.restoredMeanwhile;
  // A delete of a document that is gone; a restore or purge of one restored
  // meanwhile, or due for purge (the server hides it then).
  if (err.status === 404) return action === "trash" ? t.documentNotFound : t.notInTrashAnyMore;
  if (err.status === 503) return err.retryAfter ? t.tryAgainIn(err.retryAfter) : t.tryAgainLater;
  return t.failed(err.message);
}

/**
 * The owners of documents an admin acts on, when any of them is another
 * user's: their names, sorted, for the confirmation of a permanent
 * delete. Empty when all are the user's own (nothing to name then).
 */
export function foreignOwners(docs: readonly DocumentSummary[], user: User): string[] {
  if (!docs.some((d) => d.ownerId !== undefined && d.ownerId !== user.id)) return [];
  const names = new Set(docs.map((d) => (d.ownerId === user.id || d.ownerId === undefined ? user.name : (d.ownerName ?? `#${d.ownerId}`))));
  return [...names].sort((a, b) => a.localeCompare(b, lang));
}

/**
 * The confirmation of a permanent delete: always asked (maintainer
 * decision 4 on #12), naming other users' documents' owners. A Carbon
 * danger Modal, mounted only while open; Cancel, Escape, the close button
 * and a click outside send nothing (cancel), also while the request runs,
 * so a late failure goes to the caller's notice.
 */
export function PurgeConfirm({
  open,
  title,
  n,
  owners,
  busy,
  error,
  note,
  confirm,
  cancel,
  returnFocus,
}: {
  open: boolean;
  title: string;
  n: number;
  owners: readonly string[];
  busy?: boolean;
  error?: string;
  /** A line above the question (the matches changed since they were counted, or "every match"). */
  note?: string;
  confirm: () => void;
  cancel: () => void;
  /** Where the focus goes when the modal closes and its opener is gone (a purged row). */
  returnFocus?: () => HTMLElement | null | undefined;
}) {
  if (!open) return null;
  return (
    <DangerModal
      size="sm"
      modalHeading={title}
      primaryButtonText={t.purgeConfirm(n)}
      secondaryButtonText={t.cancel}
      primaryButtonDisabled={busy}
      returnFocus={returnFocus}
      onRequestClose={cancel}
      onRequestSubmit={() => {
        if (!busy) confirm();
      }}
    >
      <div className="modal-stack">
        {note && <p role="status">{note}</p>}
        <p>{t.purgeText(n)}</p>
        {owners.length > 0 && <p className="app-break-words">{t.purgeOwners(owners.join(", "))}</p>}
        {error && <InlineNotification className="notification--full" kind="error" lowContrast hideCloseButton role="alert" statusIconDescription={t.carbon.status.error} subtitle={error} />}
      </div>
    </DangerModal>
  );
}

type Message = { kind: "ok" | "info"; text: string } | { kind: "error"; text: string; retry?: () => void };

/** The match of "Empty trash": the caller's own trash, whole, whatever the list shows (also for an admin who sees everyone's). */
export const ownTrash: BulkMatch = { trashed: true, owner: "me" };

/**
 * "Empty trash": counts the caller's own trash (a dry run of a purge),
 * asks with that number, then deletes exactly that set permanently (the
 * fingerprint). If the trash changed meanwhile (409 match_changed), it
 * counts and asks again. For an admin with everyone's documents shown it
 * says "Empty my trash": other users' trashed documents are removed one by
 * one or by checking them, never by this button. A danger--tertiary
 * button (decision 3 of #150: it is no primary).
 */
export function EmptyTrash({ everyone, onDone, notify }: { everyone: boolean; onDone: () => void; notify?: Notify }) {
  const [confirm, setConfirm] = useState<{ n: number; fingerprint: string; changed: boolean } | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<Message | null>(null);
  const count = (changed: boolean) => {
    setBusy(true);
    setMsg({ kind: "info", text: t.bulkCounting });
    bulkApi.count(ownTrash, "purge").then(
      (c) => {
        setBusy(false);
        if (c.matched === 0) setMsg({ kind: "info", text: t.emptyTrashNothing });
        else if (c.matched > c.limit || c.fingerprint === undefined) setMsg({ kind: "error", text: t.emptyTrashTooMany(c.limit) });
        else {
          setMsg(null);
          setConfirm({ n: c.matched, fingerprint: c.fingerprint, changed });
        }
      },
      (err: unknown) => {
        setBusy(false);
        const e = bulkErrorText(err);
        setMsg({ kind: "error", text: e.text, retry: e.retry ? () => count(changed) : undefined });
      },
    );
  };

  const purge = (fingerprint: string) => {
    setConfirm(null);
    setBusy(true);
    setMsg(null);
    bulkApi.act({ match: ownTrash, fingerprint }, "purge").then(
      (n) => {
        setBusy(false);
        setMsg(null);
        // In the shell's notice: an emptied trash takes this button away with it.
        notify?.({ text: t.purgedCount(n), focus: true });
        onDone();
      },
      (err: unknown) => {
        setBusy(false);
        // The trash changed since it was counted: count and ask again, never resend.
        if (err instanceof ApiError && err.body.code === "match_changed") {
          count(true);
          return;
        }
        const e = bulkErrorText(err);
        setMsg({ kind: "error", text: e.text, retry: e.retry ? () => count(false) : undefined });
      },
    );
  };

  return (
    <div className="trash-actions">
      <Button kind="danger--tertiary" size="md" disabled={busy} onClick={() => count(false)}>
        {everyone ? t.emptyMyTrash : t.emptyTrash}
      </Button>
      {msg && (
        <p role={msg.kind === "error" ? "alert" : "status"} className={`trash-actions__msg ${msg.kind === "error" ? "app-text-error" : "app-text-secondary"}`}>
          {msg.text}
          {msg.kind === "error" && msg.retry && (
            <Button kind="ghost" size="sm" onClick={msg.retry}>
              {t.tryAgain}
            </Button>
          )}
        </p>
      )}
      <PurgeConfirm
        open={confirm !== null}
        title={t.emptyTrashTitle}
        n={confirm?.n ?? 0}
        owners={[]}
        note={confirm?.changed ? t.bulkMatchChanged(confirm.n) : everyone ? t.emptyTrashOwnOnly : undefined}
        confirm={() => confirm && purge(confirm.fingerprint)}
        cancel={() => setConfirm(null)}
      />
    </div>
  );
}
