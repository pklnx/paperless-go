import { useEffect, useId, useMemo, useRef, useState, type ReactNode, type TouchEvent } from "react";
import { ApiError, documentsApi, metadataApi, type DocumentDetail as Doc, type DocumentSummary, type Metadata, type MetadataKind, type User } from "./api";
import { documentName, ProcessingStatus } from "./DocumentParts";
import { formFrom, isDirty, patchFrom, rebaseForm, type DocumentForm, type InvalidField } from "./documentForm";
import { ArrowIcon, CloseIcon, InboxIcon, StarIcon, iconButton } from "./icons";
import { lang, t } from "./i18n";
import type { PickerOption } from "./Picker";
import { Picker } from "./Picker";
import { Preview } from "./DocumentPreview";
import { button, input, primary } from "./styles";
import { buildTagTree, type TagNode } from "./tagTree";
import { sortByName } from "./collate";
import { formatSize } from "./uploads";
import { renderNotes } from "./markdown";
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
}

/** The fields an error can be shown at. */
type ErrorField = InvalidField | "title" | "notes";

/** The URL of another document's detail, for a link. */
const docHref = (id: string) => window.location.pathname + formatState({ ...parseState(window.location.search), doc: id });

const day = (iso: string) => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleString(lang, { dateStyle: "medium", timeStyle: "short" });
};

/** Whether a key press belongs to a field (arrow keys move the caret there, not between documents). */
function inField(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  return target.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName) || target.closest("[role=listbox]") !== null;
}

function tagOptions(nodes: TagNode[], depth = 0): PickerOption[] {
  return nodes.flatMap((n) => [{ id: n.item.id, name: n.item.name, depth, color: n.item.color || undefined }, ...tagOptions(n.children, depth + 1)]);
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
export function DocumentDetail({ id, user, docs, inboxList, open, close, onSaved }: Props) {
  const headingId = useId();
  const [doc, setDoc] = useState<Doc | null>(null);
  const [form, setForm] = useState<DocumentForm | null>(null);
  const [loadError, setLoadError] = useState("");
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [errors, setErrors] = useState<Partial<Record<ErrorField, ReactNode>>>({});
  const [general, setGeneral] = useState("");
  // The notes: rendered from their Markdown, or the textarea (Edit, and
  // always for empty notes when the document opens or is saved).
  const [notesEditing, setNotesEditing] = useState(false);
  const notesField = useRef<HTMLTextAreaElement>(null);
  const focusNotes = useRef(false);
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

  // Leaving with unsaved changes: in the app (the URL guard), and the page itself.
  useEffect(
    () =>
      setNavigationGuard((next) => {
        // null: leaving the app (logging out).
        if (next && next.doc === id && next.view === "") return true;
        return !dirtyNow() || window.confirm(t.unsavedChanges);
      }),
    [id],
  );
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
  const nav = useRef({ prev, next, close, open, saving });
  nav.current = { prev, next, close, open, saving };

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
      // A dialog over the detail (the phone menu) has the keys.
      if (document.querySelector("dialog[open]")) return;
      const n = nav.current;
      if (e.key === "Escape") {
        e.preventDefault();
        n.close();
      } else if (!inField(e.target) && e.key === "ArrowLeft" && n.prev && !n.saving) {
        e.preventDefault();
        n.open(n.prev.id);
      } else if (!inField(e.target) && e.key === "ArrowRight" && n.next && !n.saving) {
        e.preventDefault();
        n.open(n.next.id);
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
      return {
        field: "asn",
        text: other ? (
          <>
            {t.asnTaken}{" "}
            <a
              className="text-accent-ink underline"
              href={docHref(other)}
              onClick={(e) => {
                e.preventDefault();
                open(other);
              }}
            >
              {t.openThatDocument}
            </a>
          </>
        ) : (
          t.asnTaken
        ),
      };
    }
    if (code === "invalid_asn") return { field: "asn", text: t.invalidASN };
    if (code === "invalid_title") return { field: "title", text: t.invalidTitle };
    if (code === "invalid_notes") return { field: "notes", text: t.invalidNotes };
    if (code === "owner_mismatch") return { text: t.ownerMismatch };
    if (code === "document_changed") return { text: t.documentChanged };
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

  const notesText = form?.notes ?? "";
  const renderedNotes = useMemo(() => renderNotes(notesText), [notesText]);

  const metaData = meta && doc && meta.owner === doc.ownerId ? meta.data : null;
  const options = useMemo(() => {
    if (!metaData) return null;
    const named = (rows: Metadata["correspondents"]) => sortByName(rows, (r) => r.name).map((r) => ({ id: r.id, name: r.name, depth: 0 }));
    return { tags: tagOptions(buildTagTree(metaData.tags)), correspondents: named(metaData.correspondents), types: named(metaData.documentTypes) };
  }, [metaData]);

  const name = doc ? documentName({ title: form?.title ?? doc.title, originalFileName: doc.originalFileName }) : "";
  const foreignOwner = doc && user.admin && doc.ownerName && doc.ownerId !== user.id ? doc.ownerName : "";
  const fieldError = (f: ErrorField) =>
    errors[f] ? (
      <p id={`${headingId}-${f}-error`} role="alert" className="text-sm text-danger">
        {errors[f]}
      </p>
    ) : null;
  const errorProps = (f: ErrorField) => (errors[f] ? { "aria-invalid": true, "aria-describedby": `${headingId}-${f}-error` } : {});
  const label = "text-sm font-medium";

  return (
    <>
      <div aria-hidden="true" className="fixed inset-0 z-20 bg-scrim md:hidden" />
      <section
        aria-labelledby={headingId}
        data-testid="document-detail"
        onTouchStart={onTouchStart}
        onTouchEnd={onTouchEnd}
        className="fixed inset-x-0 top-3 bottom-0 z-20 flex flex-col overflow-hidden rounded-t-2xl bg-bg shadow-2xl md:top-0 md:left-64 md:rounded-none md:shadow-none"
      >
        <header className="flex items-center gap-1 border-b border-line px-2 py-1.5">
          <button type="button" className={iconButton} aria-label={t.closeDocument} title={t.closeDocument} onClick={close}>
            <CloseIcon />
          </button>
          <h2 id={headingId} ref={heading} tabIndex={-1} className="min-w-0 flex-1 truncate px-1 font-semibold outline-none">
            {doc ? name : loadError ? t.document : t.loading}
          </h2>
          <button type="button" className={`${iconButton} disabled:opacity-40`} disabled={!prev || saving} aria-label={t.previousDocument} title={t.previousDocument} onClick={() => prev && !saving && open(prev.id)}>
            <span className="-rotate-90">
              <ArrowIcon up />
            </span>
          </button>
          <button type="button" className={`${iconButton} disabled:opacity-40`} disabled={!next || saving} aria-label={t.nextDocument} title={t.nextDocument} onClick={() => next && !saving && open(next.id)}>
            <span className="rotate-90">
              <ArrowIcon up />
            </span>
          </button>
        </header>
        {loadError && (
          <p role="alert" className="p-4 text-sm text-danger">
            {loadError}
          </p>
        )}
        {doc && form && (
          <div className="flex min-h-0 flex-1 flex-col overflow-y-auto md:grid md:grid-cols-[minmax(0,1fr)_24rem] md:overflow-hidden">
            <div className="h-[60dvh] shrink-0 md:h-auto md:min-h-0">
              <Preview doc={doc} processing={doc.processing?.state === "queued" || doc.processing?.state === "running"} />
            </div>
            <div className="flex flex-col gap-4 border-line p-4 md:min-h-0 md:overflow-y-auto md:border-l">
              {foreignOwner && <p className="text-sm text-ink-muted">{t.ownedBy(foreignOwner)}</p>}
              <form
                className="flex flex-col gap-3"
                aria-label={t.editDocument}
                onSubmit={(e) => {
                  e.preventDefault();
                  save(false);
                }}
              >
                <fieldset disabled={saving} className="flex flex-col gap-3">
                  <div className="flex flex-col gap-1">
                    <label htmlFor={`${headingId}-title`} className={label}>
                      {t.fieldTitle}
                    </label>
                    <input id={`${headingId}-title`} className={input} value={form.title} maxLength={1024} placeholder={doc.originalFileName} onChange={(e) => set("title", e.target.value)} {...errorProps("title")} />
                    {fieldError("title")}
                  </div>
                  <div className="grid grid-cols-2 gap-3">
                    <div className="flex flex-col gap-1">
                      <label htmlFor={`${headingId}-date`} className={label}>
                        {t.fieldDate}
                      </label>
                      <input id={`${headingId}-date`} type="date" required className={input} value={form.date} onChange={(e) => set("date", e.target.value)} {...errorProps("date")} />
                      {fieldError("date")}
                    </div>
                    <div className="flex flex-col gap-1">
                      <label htmlFor={`${headingId}-asn`} className={label}>
                        {t.fieldASN}
                      </label>
                      <input id={`${headingId}-asn`} className={input} inputMode="numeric" value={form.asn} maxLength={10} onChange={(e) => set("asn", e.target.value)} {...errorProps("asn")} />
                      {fieldError("asn")}
                    </div>
                  </div>
                  {metaError && (
                    <p role="alert" className="text-sm text-danger">
                      {metaError}
                    </p>
                  )}
                  <Picker
                    label={t.fieldCorrespondent}
                    options={options?.correspondents ?? []}
                    selected={form.correspondentId ? [form.correspondentId] : []}
                    multiple={false}
                    disabled={!options}
                    onChange={(ids) => set("correspondentId", ids[0] ?? null)}
                    onCreate={creator("correspondents")}
                  />
                  <Picker
                    label={t.fieldType}
                    options={options?.types ?? []}
                    selected={form.documentTypeId ? [form.documentTypeId] : []}
                    multiple={false}
                    disabled={!options}
                    onChange={(ids) => set("documentTypeId", ids[0] ?? null)}
                    onCreate={creator("document-types")}
                  />
                  <Picker label={t.tags} options={options?.tags ?? []} selected={form.tagIds} multiple disabled={!options} onChange={(ids) => set("tagIds", ids)} onCreate={creator("tags")} />
                  <div className="flex flex-col gap-1">
                    <div className="flex items-center justify-between gap-2">
                      {notesEditing ? (
                        <label htmlFor={`${headingId}-notes`} className={label}>
                          {t.fieldNotes}
                        </label>
                      ) : (
                        <span id={`${headingId}-notes-label`} className={label}>
                          {t.fieldNotes}
                        </span>
                      )}
                      {(!notesEditing || form.notes !== "") && (
                        <button
                          type="button"
                          className="rounded-md px-1.5 text-sm text-accent-ink hover:bg-surface-hover pointer-coarse:min-h-11"
                          aria-label={notesEditing ? t.notesPreviewLabel : t.notesEditLabel}
                          onClick={() => {
                            focusNotes.current = !notesEditing;
                            setNotesEditing(!notesEditing);
                          }}
                        >
                          {notesEditing ? t.notesPreview : t.notesEdit}
                        </button>
                      )}
                    </div>
                    {notesEditing ? (
                      <>
                        <textarea
                          id={`${headingId}-notes`}
                          ref={notesField}
                          className={`${input} min-h-24`}
                          value={form.notes}
                          maxLength={100000}
                          onChange={(e) => set("notes", e.target.value)}
                          aria-describedby={errors.notes ? `${headingId}-notes-error` : `${headingId}-notes-hint`}
                          aria-invalid={errors.notes ? true : undefined}
                        />
                        <p id={`${headingId}-notes-hint`} className="text-xs text-ink-muted">
                          {t.notesHint}
                        </p>
                      </>
                    ) : (
                      <div role="group" aria-labelledby={`${headingId}-notes-label`} data-testid="notes-rendered" className="flex flex-col gap-2 text-sm wrap-anywhere">
                        {form.notes !== "" ? renderedNotes : <p className="text-ink-muted">{t.notesNone}</p>}
                      </div>
                    )}
                    {fieldError("notes")}
                  </div>
                  <div className="flex flex-wrap gap-x-5 gap-y-2">
                    <label className="flex items-center gap-2 text-sm pointer-coarse:min-h-11">
                      <input type="checkbox" checked={form.isInbox} onChange={(e) => set("isInbox", e.target.checked)} />
                      <InboxIcon />
                      {t.inInbox}
                    </label>
                    <label className="flex items-center gap-2 text-sm pointer-coarse:min-h-11">
                      <input type="checkbox" checked={form.isFavorite} onChange={(e) => set("isFavorite", e.target.checked)} />
                      <StarIcon filled={form.isFavorite} />
                      {t.favorite}
                    </label>
                  </div>
                </fieldset>
                {general && (
                  <p role="alert" className="text-sm text-danger">
                    {general}
                  </p>
                )}
                <div className="flex flex-wrap items-center gap-2">
                  <button type="submit" className={primary} disabled={saving || !dirty}>
                    {saving ? t.saving : t.save}
                  </button>
                  <button type="button" className={button} disabled={saving} title={inboxList ? t.doneHintInbox : t.doneHint} onClick={() => save(true)}>
                    {t.done}
                  </button>
                  {saved && !dirty && (
                    <span role="status" className="text-sm text-ink-muted">
                      {t.saved}
                    </span>
                  )}
                  {dirty && !saving && <span className="text-sm text-ink-muted">{t.unsaved}</span>}
                </div>
              </form>
              <FileFacts doc={doc} onChange={(d) => setDoc((x) => (x ? { ...x, processing: d.processing } : x))} />
            </div>
          </div>
        )}
      </section>
    </>
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
    <section aria-label={t.fileFacts} className="flex flex-col gap-2 border-t border-line pt-3 text-sm">
      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1">
        {rows.map(([k, v]) => (
          <div key={k} className="contents">
            <dt className="text-ink-muted">{k}</dt>
            <dd className="wrap-anywhere">{v}</dd>
          </div>
        ))}
      </dl>
      <div className="flex flex-wrap gap-2">
        <a className={button} href={documentsApi.fileURL(doc.id, "original")} download>
          {t.downloadOriginal}
        </a>
        {doc.hasArchive && (
          <a className={button} href={documentsApi.fileURL(doc.id, "archive")} download>
            {t.downloadArchive}
          </a>
        )}
      </div>
      <ProcessingStatus doc={doc} onChange={onChange} />
    </section>
  );
}
