import { useEffect, useId, useMemo, useRef, useState, type MouseEvent, type ReactNode, type TouchEvent } from "react";
import { Button, Checkbox, ContentSwitcher, Form, IconButton, InlineNotification, Link, Switch, TextArea, TextInput } from "@carbon/react";
import { ChevronLeft, ChevronRight, Close } from "@carbon/icons-react";
import { ApiError, documentsApi, metadataApi, type DocumentDetail as Doc, type DocumentSummary, type Metadata, type MetadataKind, type User } from "./api";
import { DangerModal } from "./DangerModal";
import { documentName, ProcessingStatus } from "./DocumentParts";
import { dialogOpen, ownedByWidget } from "./dialogs";
import { formFrom, isDirty, patchFrom, rebaseForm, type DocumentForm, type InvalidField } from "./documentForm";
import { lang, t } from "./i18n";
import { NamedPicker, TagPicker, type Choice, type TagChoice } from "./DocumentPickers";
import { Preview } from "./DocumentPreview";
import { inTrash, PurgeConfirm, foreignOwners, trashDay, trashErrorText } from "./Trash";
import { buildTagTree, type TagNode } from "./tagTree";
import { sortByName } from "./collate";
import { formatSize } from "./uploads";
import { NotesView } from "./NotesView";
import { formatState, parseState, setNavigationGuard } from "./urlState";

interface Props {
  id: string;
  user: User;
  /** The list's documents in its order: previous and next come from here. */
  docs: readonly DocumentSummary[];
  /** The list is the inbox: "Done" then opens the next document of it. */
  inboxList: boolean;
  /** Opens another document (through the URL, so the unsaved-changes guard asks first). */
  open: (id: string) => void;
  close: () => void;
  /** A change was saved: the document as it is now. */
  onSaved: (d: Doc) => void;
  /** The document was moved to the trash (Delete): the list drops it, offers Undo and closes the detail. */
  onTrashed: (d: Doc) => void;
  /** The document came back from the trash: the document as it is now. */
  onRestored: (d: Doc) => void;
  /** The document was deleted permanently: the list drops it and closes the detail. */
  onPurged: (d: Doc) => void;
  /** The document turned out to be changed elsewhere (trashed): the counts load again. */
  onStale?: () => void;
  /** A delete or purge failed after the detail moved on: the list says so instead. */
  onTrashError?: (text: string) => void;
}

/** The fields an error can be shown at. */
type ErrorField = InvalidField | "title" | "notes";

/** The URL of another document's detail, for a link. */
const docHref = (id: string) => window.location.pathname + formatState({ ...parseState(window.location.search), doc: id });

const day = (iso: string) => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleString(lang, { dateStyle: "medium", timeStyle: "short" });
};

/** Whether a touch starts in a field or a list box (a swipe there scrolls or selects, it does not change the document). */
function inField(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  return target.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName) || target.closest("[role=listbox], .cds--list-box") !== null;
}

/** The tags in tree order, each with its path from the root ("Finance / Bills"). */
function tagChoices(nodes: TagNode[], above = ""): TagChoice[] {
  return nodes.flatMap((n) => {
    const path = above ? `${above} / ${n.item.name}` : n.item.name;
    return [{ id: n.item.id, name: n.item.name, path, color: n.item.color || undefined }, ...tagChoices(n.children, path)];
  });
}

/**
 * The detail of one document over the list (?doc=<id>): a page on larger
 * screens, a sheet on phones. Left the preview, right the form with every
 * field, below it the file's facts and downloads. Save sends the changed
 * fields only; Done also takes the document out of the inbox and, in the
 * inbox, opens the next one. The arrow keys or a swipe go to the previous
 * or next document of the list, Escape closes. Unsaved changes are asked
 * about before leaving (in the app and when the page is left).
 */
export function DocumentDetail({ id, user, docs, inboxList, open, close, onSaved, onTrashed, onRestored, onPurged, onStale, onTrashError }: Props) {
  const headingId = useId();
  const [doc, setDoc] = useState<Doc | null>(null);
  const [form, setForm] = useState<DocumentForm | null>(null);
  const [loadError, setLoadError] = useState("");
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [errors, setErrors] = useState<Partial<Record<ErrorField, ReactNode>>>({});
  const [general, setGeneral] = useState("");
  // Delete, Restore or Delete permanently in flight; the confirmation of the last.
  const [trashBusy, setTrashBusy] = useState(false);
  const [purgeAsk, setPurgeAsk] = useState<{ error: string } | null>(null);
  // The notes: rendered from their Markdown, or the textarea (Edit, and
  // always for empty notes when the document opens or is saved).
  const [notesEditing, setNotesEditing] = useState(false);
  const notesField = useRef<HTMLTextAreaElement>(null);
  const focusNotes = useRef(false);
  // The trash banner, and whether the title takes the focus once a Restore
  // from it has made the form editable.
  const banner = useRef<HTMLDivElement>(null);
  const titleField = useRef<HTMLInputElement>(null);
  const focusTitle = useRef(false);
  // The pickers' rows: the document owner's (an admin may open another user's document).
  const [meta, setMeta] = useState<{ owner: number; data: Metadata } | null>(null);
  const [metaError, setMetaError] = useState("");

  // What the guard and the key handlers read; set at once after a save, so
  // the navigation that follows a save is not asked about.
  const docRef = useRef<Doc | null>(null);
  const formRef = useRef<DocumentForm | null>(null);
  docRef.current = doc;
  formRef.current = form;
  const dirtyNow = () => docRef.current !== null && formRef.current !== null && isDirty(docRef.current, formRef.current);
  const dirty = doc !== null && form !== null && isDirty(doc, form);

  // Which opening of a document is shown: it changes when another document
  // opens (the detail stays mounted for ?doc=B) and on unmount. An answer
  // to a save (or to its reload after a conflict) applies only while the
  // opening it was started from is still shown; a late one never lands on
  // another document's form, nor on the same document opened again.
  const visit = useRef(0);

  useEffect(() => {
    const ctl = new AbortController();
    const mine = ++visit.current;
    setDoc(null);
    setForm(null);
    setLoadError("");
    setErrors({});
    setGeneral("");
    setSaved(false);
    setSaving(false);
    setNotesEditing(false);
    setTrashBusy(false);
    setPurgeAsk(null);
    documentsApi.get(id, ctl.signal).then(
      (d) => {
        setDoc(d);
        setForm(formFrom(d));
        setNotesEditing(d.notes === "");
      },
      (err: unknown) => {
        if (ctl.signal.aborted) return;
        setLoadError(err instanceof ApiError && err.status === 404 ? t.documentNotFound : t.failed(err instanceof Error ? err.message : String(err)));
      },
    );
    return () => {
      ctl.abort();
      // Unmount or another document: answers started now are late.
      if (visit.current === mine) visit.current++;
    };
  }, [id]);

  const owner = doc?.ownerId;
  useEffect(() => {
    if (owner === undefined || meta?.owner === owner) return;
    const ctl = new AbortController();
    setMetaError("");
    // A user's scope is their own rows; an admin names the owner.
    (user.admin ? metadataApi.loadFor(owner, ctl.signal) : metadataApi.load(false, ctl.signal)).then(
      (data) => setMeta({ owner, data }),
      (err: unknown) => {
        if (!ctl.signal.aborted) setMetaError(t.loadMetadataFailed(err instanceof Error ? err.message : String(err)));
      },
    );
    return () => ctl.abort();
  }, [owner, user.admin, meta?.owner]);

  // The "Discard changes?" question: the modal is shown while asking is
  // set, and question answers it (once; the modal unmounts with it). One
  // question at a time: the guard answers "stay" without asking while one
  // is open (from Delete, or from the guard itself).
  const [asking, setAsking] = useState(false);
  const question = useRef<((discard: boolean) => void) | null>(null);
  const askDiscard = () =>
    new Promise<boolean>((resolve) => {
      question.current = (discard) => {
        question.current = null;
        setAsking(false);
        resolve(discard);
      };
      setAsking(true);
    });
  const answer = (discard: boolean) => question.current?.(discard);

  // Leaving with unsaved changes: in the app (the URL guard), and the page itself.
  useEffect(() => {
    const remove = setNavigationGuard((next) => {
      // null: leaving the app (logging out).
      if (next && next.doc === id && next.view === "") return true;
      if (!dirtyNow()) return true;
      if (question.current) return false;
      return askDiscard();
    });
    return () => {
      // Another document, or unmounted: an open question settles as "stay".
      remove();
      answer(false);
    };
  }, [id]);
  useEffect(() => {
    if (!dirty) return;
    const onUnload = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = "";
    };
    window.addEventListener("beforeunload", onUnload);
    return () => window.removeEventListener("beforeunload", onUnload);
  }, [dirty]);

  const index = docs.findIndex((d) => d.id === id);
  const prev = index > 0 ? docs[index - 1] : undefined;
  const next = index >= 0 ? docs[index + 1] : undefined;
  // While a save is in flight there is no previous or next (buttons, arrow
  // keys, swipe): Done may still navigate when it answers. Escape, close,
  // Back and the sidebar still go through the guard; a late answer is
  // dropped then (visit).
  // Nor while a delete, restore or purge runs: its answer belongs to this document.
  const busy = saving || trashBusy;
  const nav = useRef({ prev, next, close, open, saving: busy });
  nav.current = { prev, next, close, open, saving: busy };

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // A picker that took Escape (its list closes, its text clears) has
      // prevented the default or stopped the event: the document stays.
      if (e.defaultPrevented || e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
      // A dialog (the guard's modal, the purge question) or a navigation
      // overlay over the detail has the keys.
      if (dialogOpen()) return;
      const n = nav.current;
      if (e.key === "Escape") {
        e.preventDefault();
        n.close();
      } else if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
        // Not in a field, a list box, a tab list or an open popover: there the keys move inside.
        if (ownedByWidget(e)) return;
        const to = e.key === "ArrowLeft" ? n.prev : n.next;
        if (!to || n.saving) return;
        e.preventDefault();
        n.open(to.id);
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  // A swipe left or right goes to the next or previous document, unless
  // the page is zoomed wider than the screen (then it scrolls).
  const touch = useRef<{ x: number; y: number; ok: boolean } | null>(null);
  const onTouchStart = (e: TouchEvent) => {
    const p = e.touches[0];
    if (!p || e.touches.length > 1) {
      touch.current = null;
      return;
    }
    const pages = (e.target as HTMLElement).closest?.("[data-testid=pdf-pages]");
    const scrolls = pages !== null && pages !== undefined && pages.scrollWidth > pages.clientWidth + 1;
    touch.current = { x: p.clientX, y: p.clientY, ok: !scrolls && !inField(e.target) };
  };
  const onTouchEnd = (e: TouchEvent) => {
    const s = touch.current;
    touch.current = null;
    const p = e.changedTouches[0];
    if (!s?.ok || !p) return;
    const dx = p.clientX - s.x;
    const dy = p.clientY - s.y;
    if (Math.abs(dx) < 70 || Math.abs(dy) > 50) return;
    const n = nav.current;
    if (n.saving) return;
    if (dx < 0 && n.next) n.open(n.next.id);
    if (dx > 0 && n.prev) n.open(n.prev.id);
  };

  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => heading.current?.focus(), [id]);
  // In the trash the form is read-only: the server refuses changes there (409 in_trash).
  const trashedNow = doc !== null && inTrash(doc);
  // Restored from the banner: the title, no longer read-only, has the focus.
  useEffect(() => {
    if (trashedNow || !focusTitle.current) return;
    focusTitle.current = false;
    titleField.current?.focus();
  }, [trashedNow]);
  // Edit puts the caret into the notes.
  useEffect(() => {
    if (notesEditing && focusNotes.current) notesField.current?.focus();
    focusNotes.current = false;
  }, [notesEditing]);

  const set = <K extends keyof DocumentForm>(k: K, v: DocumentForm[K]) => {
    setForm((f) => (f ? { ...f, [k]: v } : f));
    setSaved(false);
    if (k === "title" || k === "date" || k === "asn" || k === "notes") setErrors((e) => ({ ...e, [k]: undefined }));
  };

  const errorOf = (err: unknown): { field?: ErrorField; text: ReactNode } => {
    if (!(err instanceof ApiError)) return { text: t.failed(err instanceof Error ? err.message : String(err)) };
    const code = err.body.code;
    if (code === "asn_taken") {
      const other = typeof err.body.documentId === "string" ? err.body.documentId : "";
      const taken = err.body.trashed === true ? t.asnTakenTrashed : t.asnTaken;
      return {
        field: "asn",
        text: other ? (
          <>
            {taken}{" "}
            <Link
              href={docHref(other)}
              onClick={(e: MouseEvent<HTMLAnchorElement>) => {
                e.preventDefault();
                open(other);
              }}
            >
              {t.openThatDocument}
            </Link>
          </>
        ) : (
          taken
        ),
      };
    }
    if (code === "invalid_asn") return { field: "asn", text: t.invalidASN };
    if (code === "invalid_title") return { field: "title", text: t.invalidTitle };
    if (code === "invalid_notes") return { field: "notes", text: t.invalidNotes };
    if (code === "owner_mismatch") return { text: t.ownerMismatch };
    if (code === "document_changed") return { text: t.documentChanged };
    if (code === "in_trash") return { text: t.inTrashError };
    if (err.status === 404) return { text: t.documentNotFound };
    if (err.status === 503) return { text: t.tryAgainLater };
    return { text: t.failed(err.message) };
  };

  const save = (done: boolean) => {
    if (!doc || !form || saving) return;
    const r = patchFrom(doc, form);
    if ("invalid" in r) {
      setErrors({ [r.invalid]: r.invalid === "asn" ? t.invalidASN : t.invalidDate });
      return;
    }
    const patch = r.patch;
    // Done: out of the inbox, whatever the switch says.
    if (done) {
      if (doc.isInbox) patch.isInbox = false;
      else delete patch.isInbox;
    }
    // Where Done goes: in the inbox the next document, which is known
    // only before the list drops this one.
    const after = () => (done && inboxList && next ? open(next.id) : close());
    if (Object.keys(patch).length === 0) {
      if (done) after();
      return;
    }
    setSaving(true);
    setErrors({});
    setGeneral("");
    const mine = visit.current;
    const current = () => visit.current === mine;
    // The precondition: modifiedAt exactly as the server answered it.
    documentsApi.patch(id, { ...patch, expectedModifiedAt: doc.modifiedAt }).then(
      (d) => {
        // The save happened: the list shows it even when it came late.
        onSaved(d);
        if (!current()) return;
        setSaving(false);
        docRef.current = d;
        formRef.current = formFrom(d);
        setDoc(d);
        setForm(formRef.current);
        // A saved edit shows rendered.
        setNotesEditing(d.notes === "");
        if (done) after();
        else setSaved(true);
      },
      (err: unknown) => {
        if (!current()) return;
        if (err instanceof ApiError && err.body.code === "document_changed") {
          rebase(doc, form);
          return;
        }
        // Moved to the trash elsewhere: load it again, so the banner
        // shows, and keep the user's edits on top of it (as for
        // document_changed): after Restore they can be saved.
        if (err instanceof ApiError && err.body.code === "in_trash") {
          setSaving(false);
          setGeneral(t.inTrashEditsKept);
          onStale?.();
          documentsApi.get(id).then(
            (fresh) => {
              if (!current()) return;
              const kept = rebaseForm(doc, form, fresh);
              docRef.current = fresh;
              formRef.current = kept;
              setDoc(fresh);
              setForm(kept);
            },
            () => {},
          );
          return;
        }
        setSaving(false);
        const e = errorOf(err);
        if (e.field === "notes") setNotesEditing(true);
        if (e.field) setErrors({ [e.field]: e.text });
        else setGeneral(typeof e.text === "string" ? e.text : "");
      },
    );
  };

  // The document changed meanwhile (409 document_changed): load it again
  // and keep the user's edits on top of it (rebaseForm). The reloaded
  // document becomes the base, so the next save sends only the user's
  // fields with its modifiedAt as the precondition. Nothing is saved by
  // itself, and Done does not navigate. The form stays disabled until the
  // reload answers; a late answer is dropped (visit).
  const rebase = (old: Doc, edited: DocumentForm) => {
    const mine = visit.current;
    documentsApi.get(id).then(
      (fresh) => {
        if (visit.current !== mine) return;
        setSaving(false);
        const rebased = rebaseForm(old, edited, fresh);
        docRef.current = fresh;
        formRef.current = rebased;
        setDoc(fresh);
        setForm(rebased);
        setGeneral(t.documentChanged);
      },
      (err: unknown) => {
        if (visit.current !== mine) return;
        setSaving(false);
        // The form keeps its base: the next save is refused again and
        // tries the reload once more.
        setGeneral(t.failed(err instanceof Error ? err.message : String(err)));
      },
    );
  };

  // Delete: to the trash at once, without a question (#12, decision 4);
  // the list offers Undo. Unsaved changes are asked about first, as for
  // leaving (the same modal): they would be refused in the trash anyway.
  const moveToTrash = () => {
    if (!doc || saving || trashBusy || question.current) return;
    if (dirtyNow()) {
      const mine = visit.current;
      void askDiscard().then((discard) => {
        if (discard && visit.current === mine) trash();
      });
      return;
    }
    trash();
  };
  const trash = () => {
    const d = docRef.current;
    if (!d) return;
    const mine = visit.current;
    setTrashBusy(true);
    setGeneral("");
    documentsApi.trash(d.id).then(
      () => {
        if (visit.current === mine) {
          setTrashBusy(false);
          // Nothing unsaved any more: the close that follows is not asked about.
          formRef.current = formFrom(d);
          setForm(formRef.current);
        }
        // The delete happened: the list drops the row and offers Undo even
        // when the detail was left meanwhile (it closes only if still open).
        onTrashed(d);
      },
      (err: unknown) => {
        if (visit.current !== mine) {
          onTrashError?.(trashErrorText(err, "trash"));
          return;
        }
        setTrashBusy(false);
        setGeneral(trashErrorText(err, "trash"));
        // Trashed elsewhere meanwhile: show it as it is now (the banner).
        if (err instanceof ApiError && err.body.code === "in_trash") {
          onStale?.();
          documentsApi.get(d.id).then(
            (fresh) => {
              if (visit.current !== mine) return;
              const kept = formRef.current ? rebaseForm(d, formRef.current, fresh) : formFrom(fresh);
              docRef.current = fresh;
              formRef.current = kept;
              setDoc(fresh);
              setForm(kept);
            },
            () => {},
          );
        }
      },
    );
  };
  const restore = () => {
    if (!doc || trashBusy) return;
    const mine = visit.current;
    setTrashBusy(true);
    setGeneral("");
    const before = doc;
    documentsApi.restore(doc.id).then(
      (d) => {
        onRestored(d);
        if (visit.current !== mine) return;
        // The banner with its Restore goes away: the focus stays in the
        // detail, on the title, which can be edited now.
        const active = document.activeElement;
        if (!active || active === document.body || (banner.current?.contains(active) ?? false)) focusTitle.current = true;
        setTrashBusy(false);
        // Edits kept from a save refused in the trash stay, ready to save.
        const kept = formRef.current ? rebaseForm(before, formRef.current, d) : formFrom(d);
        docRef.current = d;
        formRef.current = kept;
        setDoc(d);
        setForm(kept);
        setGeneral("");
        if (!isDirty(d, kept)) setNotesEditing(d.notes === "");
      },
      (err: unknown) => {
        if (visit.current !== mine) return;
        setTrashBusy(false);
        setGeneral(trashErrorText(err, "restore"));
      },
    );
  };
  const purge = () => {
    if (!doc || trashBusy) return;
    const mine = visit.current;
    const d = doc;
    setTrashBusy(true);
    documentsApi.purge(d.id).then(
      () => {
        if (visit.current === mine) {
          setTrashBusy(false);
          setPurgeAsk(null);
        }
        // As for a delete: the list follows even when the detail was left.
        onPurged(d);
      },
      (err: unknown) => {
        if (visit.current !== mine) {
          onTrashError?.(trashErrorText(err, "purge"));
          return;
        }
        setTrashBusy(false);
        setPurgeAsk({ error: trashErrorText(err, "purge") });
      },
    );
  };

  // Inline creation: an admin creates the row for the document's owner,
  // so it can be linked (a row of another owner is refused).
  const creator = (kind: MetadataKind) => async (name: string) => {
    if (!doc) throw new Error("no document");
    const row = await metadataApi.create(kind, name, user.admin ? doc.ownerId : undefined);
    setMeta((m) => {
      if (!m || m.owner !== doc.ownerId) return m;
      const data = { ...m.data };
      if (kind === "tags") data.tags = [...data.tags, row as Metadata["tags"][number]];
      else if (kind === "correspondents") data.correspondents = [...data.correspondents, row];
      else data.documentTypes = [...data.documentTypes, row];
      return { ...m, data };
    });
    return row.id;
  };

  const metaData = meta && doc && meta.owner === doc.ownerId ? meta.data : null;
  const options = useMemo(() => {
    if (!metaData) return null;
    const named = (rows: Metadata["correspondents"]): Choice[] => sortByName(rows, (r) => r.name).map((r) => ({ id: r.id, name: r.name }));
    return { tags: tagChoices(buildTagTree(metaData.tags)), correspondents: named(metaData.correspondents), types: named(metaData.documentTypes) };
  }, [metaData]);

  const name = doc ? documentName({ title: form?.title ?? doc.title, originalFileName: doc.originalFileName }) : "";
  const foreignOwner = doc && user.admin && doc.ownerName && doc.ownerId !== user.id ? doc.ownerName : "";
  const trashed = trashedNow;
  const fieldId = (f: string) => `${headingId}-${f}`;
  // Carbon shows invalidText under the field (id "<field>-error-msg"); the
  // TextInput does not describe itself by it, the TextArea names it as
  // aria-errormessage.
  const invalid = (f: ErrorField) => (errors[f] ? { invalid: true, invalidText: errors[f], "aria-describedby": `${fieldId(f)}-error-msg` } : {});
  const notesLabelId = fieldId("notes-label");

  return (
    <section aria-labelledby={headingId} data-testid="document-detail" onTouchStart={onTouchStart} onTouchEnd={onTouchEnd} className="doc-detail">
      <header className="doc-detail__head">
        <IconButton kind="ghost" size="md" align="bottom-start" label={t.closeDocument} onClick={close}>
          <Close />
        </IconButton>
        <h2 id={headingId} ref={heading} tabIndex={-1} className="doc-detail__title">
          {doc ? name : loadError ? t.document : t.loading}
        </h2>
        <IconButton kind="ghost" size="md" align="bottom" label={t.previousDocument} disabled={!prev || busy} onClick={() => prev && !busy && open(prev.id)}>
          <ChevronLeft />
        </IconButton>
        <IconButton kind="ghost" size="md" align="bottom-end" label={t.nextDocument} disabled={!next || busy} onClick={() => next && !busy && open(next.id)}>
          <ChevronRight />
        </IconButton>
      </header>
      {loadError && (
        <div className="doc-detail__notice">
          <InlineNotification className="notification--full" kind="error" lowContrast hideCloseButton role="alert" statusIconDescription={t.carbon.status.error} subtitle={loadError} />
        </div>
      )}
      {doc && form && (
        <div className="doc-detail__body">
          <div className="doc-detail__preview">
            <Preview doc={doc} processing={doc.processing?.state === "queued" || doc.processing?.state === "running"} />
          </div>
          <div className="doc-detail__panel">
            {foreignOwner && <p className="doc-detail__owner app-text-secondary">{t.ownedBy(foreignOwner)}</p>}
            {trashed && (
              <div ref={banner} role="region" aria-label={t.trash} className="doc-trash">
                {/* Carbon's notification takes no buttons: the actions sit under it, in the same region. */}
                <InlineNotification className="notification--full" kind="warning" lowContrast hideCloseButton role="status" statusIconDescription={t.carbon.status.warning} title={t.trashBanner(trashDay(doc.deletedAt))} subtitle={doc.purgeAt ? t.trashBannerRemoved(trashDay(doc.purgeAt)) : undefined} />
                <div className="doc-trash__actions">
                  <Button kind="primary" size="sm" disabled={trashBusy} onClick={restore}>
                    {t.restore}
                  </Button>
                  <Button kind="danger--ghost" size="sm" disabled={trashBusy} onClick={() => setPurgeAsk({ error: "" })}>
                    {t.purge}
                  </Button>
                </div>
              </div>
            )}
            <Form
              className="doc-form"
              aria-label={t.editDocument}
              onSubmit={(e) => {
                e.preventDefault();
                save(false);
              }}
            >
              {/* In the trash, and while a save runs, every field is read-only: the disabled fieldset. */}
              <fieldset disabled={saving || trashed} className="doc-form__fields">
                <TextInput id={fieldId("title")} ref={titleField} labelText={t.fieldTitle} value={form.title} maxLength={1024} placeholder={doc.originalFileName} onChange={(e) => set("title", e.target.value)} {...invalid("title")} />
                <div className="doc-form__row">
                  <TextInput id={fieldId("date")} type="date" required labelText={t.fieldDate} value={form.date} onChange={(e) => set("date", e.target.value)} {...invalid("date")} />
                  <TextInput id={fieldId("asn")} labelText={t.fieldASN} inputMode="numeric" value={form.asn} maxLength={10} onChange={(e) => set("asn", e.target.value)} {...invalid("asn")} />
                </div>
                {metaError && <InlineNotification className="notification--full" kind="error" lowContrast hideCloseButton role="alert" statusIconDescription={t.carbon.status.error} subtitle={metaError} />}
                <NamedPicker label={t.fieldCorrespondent} choices={options?.correspondents ?? []} selected={form.correspondentId} disabled={!options} onChange={(id) => set("correspondentId", id)} onCreate={creator("correspondents")} onEscape={close} />
                <NamedPicker label={t.fieldType} choices={options?.types ?? []} selected={form.documentTypeId} disabled={!options} onChange={(id) => set("documentTypeId", id)} onCreate={creator("document-types")} onEscape={close} />
                <TagPicker label={t.tags} choices={options?.tags ?? []} selected={form.tagIds} disabled={!options} readOnly={trashed} onChange={(ids) => set("tagIds", ids)} onCreate={creator("tags")} onEscape={close} />
                <div className="doc-notes">
                  <div className="doc-notes__head">
                    {/* The field's own label is Carbon's (hidden); this one names the rendered notes. */}
                    <span id={notesLabelId} className="cds--label" aria-hidden={notesEditing || undefined}>
                      {t.fieldNotes}
                    </span>
                    {(!notesEditing || form.notes !== "") && (
                      <ContentSwitcher
                        size="sm"
                        aria-label={t.notesMode}
                        selectedIndex={notesEditing ? 0 : 1}
                        onChange={({ index }) => {
                          const editing = index === 0;
                          if (editing === notesEditing) return;
                          focusNotes.current = editing;
                          setNotesEditing(editing);
                        }}
                      >
                        <Switch name="edit" text={t.notesEdit} aria-label={t.notesEditLabel} />
                        <Switch name="preview" text={t.notesPreview} aria-label={t.notesPreviewLabel} />
                      </ContentSwitcher>
                    )}
                  </div>
                  {notesEditing ? (
                    <TextArea id={fieldId("notes")} ref={notesField} labelText={t.fieldNotes} hideLabel rows={5} value={form.notes} maxLength={100000} onChange={(e) => set("notes", e.target.value)} helperText={t.notesHint} {...invalid("notes")} />
                  ) : (
                    <div role="group" aria-labelledby={notesLabelId} data-testid="notes-rendered" className={trashed ? "notes-rendered notes-rendered--disabled" : "notes-rendered"}>
                      {form.notes !== "" ? <NotesView text={form.notes} /> : <p className="app-text-secondary">{t.notesNone}</p>}
                    </div>
                  )}
                </div>
                <div className="doc-form__flags">
                  <Checkbox id={fieldId("inbox")} labelText={t.inInbox} checked={form.isInbox} onChange={(e) => set("isInbox", e.target.checked)} />
                  <Checkbox id={fieldId("favorite")} labelText={t.favorite} checked={form.isFavorite} onChange={(e) => set("isFavorite", e.target.checked)} />
                </div>
              </fieldset>
              {general && <InlineNotification className="notification--full" kind="error" lowContrast hideCloseButton role="alert" statusIconDescription={t.carbon.status.error} subtitle={general} />}
              {!trashed && (
                <div className="doc-form__actions">
                  <Button type="submit" kind="primary" size="md" disabled={saving || !dirty}>
                    {saving ? t.saving : t.save}
                  </Button>
                  <Button type="button" kind="secondary" size="md" disabled={saving} title={inboxList ? t.doneHintInbox : t.doneHint} onClick={() => save(true)}>
                    {t.done}
                  </Button>
                  {saved && !dirty && (
                    <span role="status" className="doc-form__state app-text-secondary">
                      {t.saved}
                    </span>
                  )}
                  {dirty && !saving && <span className="doc-form__state app-text-secondary">{t.unsaved}</span>}
                  <Button type="button" kind="danger--ghost" size="md" className="doc-form__delete" disabled={saving || trashBusy} aria-label={t.deleteDocumentLabel(name)} title={t.deleteDocumentLabel(name)} onClick={moveToTrash}>
                    {t.deleteDocument}
                  </Button>
                </div>
              )}
            </Form>
            <FileFacts doc={doc} onChange={(d) => setDoc((x) => (x ? { ...x, processing: d.processing } : x))} />
          </div>
        </div>
      )}
      {asking && (
        <DangerModal
          size="sm"
          modalHeading={t.discardChangesTitle}
          primaryButtonText={t.discardChanges}
          secondaryButtonText={t.keepEditing}
          // Escape and the close button mean "Keep editing".
          onRequestClose={() => answer(false)}
          onSecondarySubmit={() => answer(false)}
          onRequestSubmit={() => answer(true)}
          // A question opened by Back or Forward has no opener: the focus goes to the heading.
          returnFocus={() => heading.current}
        >
          <p>{t.unsavedChanges}</p>
        </DangerModal>
      )}
      <PurgeConfirm
        open={purgeAsk !== null && doc !== null}
        title={t.purgeTitle(name)}
        n={1}
        owners={doc ? foreignOwners([doc], user) : []}
        busy={trashBusy}
        error={purgeAsk?.error}
        confirm={purge}
        cancel={() => setPurgeAsk(null)}
      />
    </section>
  );
}

/** The file's facts, the downloads and processing (with Retry). Text from the server shows as text. */
function FileFacts({ doc, onChange }: { doc: Doc; onChange: (d: DocumentSummary) => void }) {
  const rows: [string, string][] = [
    [t.fileName, doc.originalFileName],
    [t.fileType, doc.originalMime],
    [t.fileSize, formatSize(doc.originalSize, lang)],
    ...(doc.pageCount > 0 ? [[t.filePages, String(doc.pageCount)] as [string, string]] : []),
    [t.fileAdded, day(doc.createdAt)],
    ...(doc.processingNote ? [[t.processingNote, doc.processingNote] as [string, string]] : []),
  ];
  return (
    <section aria-label={t.fileFacts} className="doc-facts">
      <h3 className="doc-facts__title">{t.fileFacts}</h3>
      <dl className="doc-facts__list">
        {rows.map(([k, v]) => (
          <div key={k} className="doc-facts__row">
            <dt className="app-text-secondary">{k}</dt>
            <dd>{v}</dd>
          </div>
        ))}
      </dl>
      <div className="doc-facts__actions">
        <Button as="a" kind="tertiary" size="sm" href={documentsApi.fileURL(doc.id, "original")} download>
          {t.downloadOriginal}
        </Button>
        {doc.hasArchive && (
          <Button as="a" kind="tertiary" size="sm" href={documentsApi.fileURL(doc.id, "archive")} download>
            {t.downloadArchive}
          </Button>
        )}
      </div>
      <ProcessingStatus doc={doc} onChange={onChange} />
    </section>
  );
}
