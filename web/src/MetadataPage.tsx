import { useId, useState } from "react";
import { ApiError, metadataEditApi, type Metadata, type MetadataKind, type NamedRow, type Tag, type TagPatch } from "./api";
import { sortByName } from "./collate";
import { t } from "./i18n";
import { Sheet } from "./Sheet";
import type { Loaded } from "./Sidebar";
import { button, input, primary } from "./styles";
import { buildTagTree, chipInk, flatTagTree, type TagNode } from "./tagTree";
import type { Filter } from "./urlState";

/** The colours offered for tags; any other #RRGGBB can be typed. */
export const tagPalette = ["#D32F2F", "#C2185B", "#7B1FA2", "#512DA8", "#1E88E5", "#0097A7", "#00897B", "#43A047", "#9E9D24", "#F9A825", "#EF6C00", "#6D4C41", "#546E7A"] as const;

const colorPattern = /^#[0-9a-fA-F]{6}$/;

/** What the server's refusal of a metadata change means, in words. */
export function metadataErrorText(err: unknown): string {
  if (!(err instanceof ApiError)) return t.failed(err instanceof Error ? err.message : String(err));
  const code = typeof err.body.code === "string" ? err.body.code : "";
  switch (code) {
    case "name_taken":
      return t.nameTaken;
    case "tag_cycle":
      return t.tagCycle;
    case "tag_name_conflict": {
      const child = typeof err.body.childName === "string" ? err.body.childName : "";
      return child ? t.tagNameConflict(child) : t.tagNameConflictUnnamed;
    }
    case "invalid_name":
      return t.invalidName;
    case "invalid_color":
      return t.invalidColor;
    case "owner_mismatch":
      return t.parentOtherOwner;
  }
  if (err.status === 404) return t.entryGone;
  if (err.status === 503) return t.serverBusy;
  return t.failed(err.message);
}

type Tab = "tags" | "correspondents" | "document-types";
type Row = { kind: "tags"; row: Tag } | { kind: "correspondents" | "document-types"; row: NamedRow };

interface Props {
  /** The names in the list's owner scope (an admin's own unless "Everyone's documents" is on). */
  metadata: Loaded<Metadata>;
  /** After a change: the names and counts load again. */
  onChanged: () => void;
  /** Opens the document list filtered by a row. */
  open: (f: Filter) => void;
}

/**
 * The page for tags, correspondents and document types: rename, delete
 * with a confirmation that names the documents affected (they stay), and
 * for tags the colour and the parent (drag in the tree, or the parent
 * picker in the edit sheet).
 */
export function MetadataPage({ metadata, onChanged, open }: Props) {
  const [tab, setTab] = useState<Tab>("tags");
  const [editing, setEditing] = useState<Row | null>(null);
  const [deleting, setDeleting] = useState<Row | null>(null);
  const [error, setError] = useState("");
  const baseId = useId();
  const { data } = metadata;

  const tabs: [Tab, string][] = [
    ["tags", t.tags],
    ["correspondents", t.correspondents],
    ["document-types", t.documentTypes],
  ];
  const actions = (r: Row) => ({ edit: () => setEditing(r), remove: () => setDeleting(r) });

  // A tag dropped on another (or on the top-level zone): one PATCH of its parent.
  const move = (tag: Tag, parentId: string | null) => {
    if (tag.parentId === parentId) return;
    setError("");
    metadataEditApi.patch("tags", tag.id, { parentId }).then(
      () => onChanged(),
      (err: unknown) => {
        setError(metadataErrorText(err));
        if (err instanceof ApiError && err.status === 404) onChanged();
      },
    );
  };

  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-4 px-4 py-6">
      <h2 className="text-lg font-semibold">{t.manageMetadata}</h2>
      <div role="tablist" aria-label={t.manageMetadata} className="flex gap-1 border-b border-line">
        {tabs.map(([k, label]) => (
          <button
            key={k}
            type="button"
            role="tab"
            id={`${baseId}-${k}`}
            aria-selected={tab === k}
            aria-controls={`${baseId}-${k}-panel`}
            className={`-mb-px border-b-2 px-3 py-1.5 text-sm pointer-coarse:min-h-11 ${tab === k ? "border-accent font-semibold" : "border-transparent text-ink-muted hover:text-ink"}`}
            onClick={() => {
              setTab(k);
              setError("");
            }}
          >
            {label}
          </button>
        ))}
      </div>
      {metadata.error && !data && (
        <p role="alert" className="text-sm text-danger">
          {t.loadMetadataFailed(metadata.error)}
        </p>
      )}
      {!data && !metadata.error && <p className="text-sm text-ink-muted">{t.loading}</p>}
      {error && (
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
      )}
      {data && (
        <div role="tabpanel" id={`${baseId}-${tab}-panel`} aria-labelledby={`${baseId}-${tab}`}>
          {tab === "tags" && <TagTree tags={data.tags} actions={(row) => actions({ kind: "tags", row })} open={open} move={move} />}
          {tab === "correspondents" && (
            <NamedList rows={data.correspondents} empty={t.noCorrespondentsYet} label={t.correspondents} actions={(row) => actions({ kind: "correspondents", row })} open={(id) => open({ kind: "corr", id })} />
          )}
          {tab === "document-types" && (
            <NamedList rows={data.documentTypes} empty={t.noTypesYet} label={t.documentTypes} actions={(row) => actions({ kind: "document-types", row })} open={(id) => open({ kind: "type", id })} />
          )}
        </div>
      )}
      <Sheet open={editing !== null} onClose={() => setEditing(null)} title={editing ? t.editEntry(editing.row.name) : ""}>
        {editing && (
          <EditForm
            entry={editing}
            tags={data?.tags ?? []}
            done={() => {
              setEditing(null);
              onChanged();
            }}
            gone={onChanged}
          />
        )}
      </Sheet>
      <Sheet open={deleting !== null} onClose={() => setDeleting(null)} title={deleting ? t.deleteTitle(deleting.row.name) : ""}>
        {deleting && (
          <DeleteForm
            entry={deleting}
            hasChildren={deleting.kind === "tags" && (data?.tags ?? []).some((x) => x.parentId === deleting.row.id)}
            cancel={() => setDeleting(null)}
            done={() => {
              setDeleting(null);
              onChanged();
            }}
            gone={onChanged}
          />
        )}
      </Sheet>
    </div>
  );
}

const rowClass = "flex flex-wrap items-center gap-x-3 gap-y-1 px-2 py-2";
const linkButton = "text-accent-ink hover:underline pointer-coarse:min-h-11";

function RowActions({ name, actions }: { name: string; actions: { edit: () => void; remove: () => void } }) {
  return (
    <span className="ml-auto flex shrink-0 gap-2">
      <button type="button" className={button} aria-label={t.editEntry(name)} title={t.editEntry(name)} onClick={actions.edit}>
        {t.edit}
      </button>
      <button type="button" className={`${button} text-danger`} aria-label={t.deleteEntry(name)} title={t.deleteEntry(name)} onClick={actions.remove}>
        {t.deleteButton}
      </button>
    </span>
  );
}

function NamedList({ rows, empty, label, actions, open }: { rows: NamedRow[]; empty: string; label: string; actions: (r: NamedRow) => { edit: () => void; remove: () => void }; open: (id: string) => void }) {
  if (rows.length === 0) return <p className="py-4 text-sm text-ink-muted">{empty}</p>;
  return (
    <ul aria-label={label} className="divide-y divide-line">
      {sortByName(rows, (r) => r.name).map((r) => (
        <li key={r.id} className={rowClass}>
          <button type="button" className={`${linkButton} min-w-0 text-left font-medium wrap-anywhere`} title={t.showDocumentsOf(r.name)} onClick={() => open(r.id)}>
            {r.name}
          </button>
          <span className="text-sm text-ink-muted">{t.documentCount(r.documentCount ?? 0)}</span>
          <RowActions name={r.name} actions={actions(r)} />
        </li>
      ))}
    </ul>
  );
}

/** Whether target is tag itself or below it: no place for tag. */
function insideOf(node: TagNode, id: string): boolean {
  return node.item.id === id || node.children.some((c) => insideOf(c, id));
}

function findNode(nodes: TagNode[], id: string): TagNode | null {
  for (const n of nodes) {
    if (n.item.id === id) return n;
    const f = findNode(n.children, id);
    if (f) return f;
  }
  return null;
}

interface TreeProps {
  tags: Tag[];
  actions: (t: Tag) => { edit: () => void; remove: () => void };
  open: (f: Filter) => void;
  move: (tag: Tag, parentId: string | null) => void;
}

/** The tag tree; a tag dragged onto another of the same owner moves under it, onto the top zone to the top level. */
function TagTree({ tags, actions, open, move }: TreeProps) {
  const [dragId, setDragId] = useState<string | null>(null);
  const [over, setOver] = useState<string | null>(null);
  if (tags.length === 0) return <p className="py-4 text-sm text-ink-muted">{t.noTagsYet}</p>;
  const tree = buildTagTree(tags);
  const dragged = dragId ? tags.find((x) => x.id === dragId) ?? null : null;
  const draggedNode = dragId ? findNode(tree, dragId) : null;
  const canDrop = (target: Tag) => dragged !== null && target.ownerId === dragged.ownerId && !(draggedNode && insideOf(draggedNode, target.id)) && dragged.parentId !== target.id;
  const end = () => {
    setDragId(null);
    setOver(null);
  };
  return (
    <div className="flex flex-col gap-2">
      <p className="text-sm text-ink-muted max-md:hidden">{t.dragHint}</p>
      {dragged && dragged.parentId !== null && (
        <div
          data-testid="drop-top"
          className={`rounded-md border-2 border-dashed px-3 py-2 text-sm ${over === "" ? "border-accent bg-accent-soft" : "border-line-control text-ink-muted"}`}
          onDragOver={(e) => {
            e.preventDefault();
            setOver("");
          }}
          onDragLeave={() => setOver(null)}
          onDrop={(e) => {
            e.preventDefault();
            move(dragged, null);
            end();
          }}
        >
          {t.dropTopLevel}
        </div>
      )}
      <ul aria-label={t.tags} className="divide-y divide-line">
        {flatTagTree(tags).map(({ tag, depth }) => (
          <li
            key={tag.id}
            draggable
            data-testid={`tag-row-${tag.id}`}
            className={`${rowClass} ${over === tag.id ? "bg-accent-soft" : ""}`}
            style={{ paddingLeft: `${0.5 + Math.min(depth, 6) * 1.25}rem` }}
            onDragStart={(e) => {
              e.dataTransfer?.setData("text/plain", tag.id);
              if (e.dataTransfer) e.dataTransfer.effectAllowed = "move";
              setDragId(tag.id);
            }}
            onDragEnd={end}
            onDragOver={(e) => {
              if (!canDrop(tag)) return;
              e.preventDefault();
              setOver(tag.id);
            }}
            onDragLeave={() => setOver((o) => (o === tag.id ? null : o))}
            onDrop={(e) => {
              if (!dragged || !canDrop(tag)) return;
              e.preventDefault();
              move(dragged, tag.id);
              end();
            }}
          >
            <span aria-hidden className={`size-3 shrink-0 rounded-full ${tag.color ? "" : "border border-line-control"}`} style={tag.color ? { backgroundColor: tag.color } : undefined} />
            <button type="button" className={`${linkButton} min-w-0 text-left font-medium wrap-anywhere`} title={t.showDocumentsOf(tag.name)} onClick={() => open({ kind: "tag", id: tag.id })}>
              {tag.name}
            </button>
            <span className="text-sm text-ink-muted">{t.documentCount(tag.documentCount ?? 0)}</span>
            <RowActions name={tag.name} actions={actions(tag)} />
          </li>
        ))}
      </ul>
    </div>
  );
}

function EditForm({ entry, tags, done, gone }: { entry: Row; tags: Tag[]; done: () => void; gone: () => void }) {
  const tag = entry.kind === "tags" ? entry.row : null;
  const [name, setName] = useState(entry.row.name);
  const [color, setColor] = useState(tag?.color ?? "");
  const [parent, setParent] = useState(tag?.parentId ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const id = useId();

  // A parent of the same owner, never the tag itself or one of its sub-tags.
  const self = tag ? findNode(buildTagTree(tags), tag.id) : null;
  const parents = tag ? flatTagTree(tags.filter((x) => x.ownerId === tag.ownerId)).filter(({ tag: x }) => !(self && insideOf(self, x.id))) : [];
  const knownParent = !tag || parent === "" || parents.some((p) => p.tag.id === parent);

  const save = () => {
    const patch: TagPatch = {};
    if (name.trim() !== entry.row.name) patch.name = name;
    if (tag && color !== tag.color) {
      if (!colorPattern.test(color)) {
        setError(t.invalidColor);
        return;
      }
      patch.color = color;
    }
    if (tag && parent !== (tag.parentId ?? "")) patch.parentId = parent === "" ? null : parent;
    if (Object.keys(patch).length === 0) {
      done();
      return;
    }
    setBusy(true);
    setError("");
    metadataEditApi.patch(entry.kind, entry.row.id, patch).then(
      () => {
        setBusy(false);
        done();
      },
      (err: unknown) => {
        setBusy(false);
        setError(metadataErrorText(err));
        if (err instanceof ApiError && err.status === 404) gone();
      },
    );
  };

  return (
    <form
      className="flex flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        save();
      }}
    >
      <label className="flex flex-col gap-1 text-sm">
        {t.entryName}
        <input className={input} value={name} maxLength={128} required onChange={(e) => setName(e.target.value)} />
      </label>
      {tag && (
        <fieldset className="flex flex-col gap-2 text-sm">
          <legend className="mb-1">{t.tagColor}</legend>
          <div className="flex flex-wrap gap-2">
            {tagPalette.map((c) => (
              <button
                key={c}
                type="button"
                aria-label={t.colorSwatch(c)}
                aria-pressed={color.toUpperCase() === c}
                title={c}
                className={`grid size-8 place-items-center rounded-full border-2 pointer-coarse:size-11 ${color.toUpperCase() === c ? "border-ink" : "border-transparent"}`}
                style={{ backgroundColor: c, color: chipInk(c) }}
                onClick={() => setColor(c)}
              >
                {color.toUpperCase() === c ? "✓" : ""}
              </button>
            ))}
          </div>
          <label className="flex flex-col gap-1">
            {t.tagColorHex}
            <input className={`${input} font-mono`} value={color} maxLength={7} placeholder="#1E88E5" spellCheck={false} onChange={(e) => setColor(e.target.value.trim())} />
          </label>
        </fieldset>
      )}
      {tag && (
        <label className="flex flex-col gap-1 text-sm" htmlFor={`${id}-parent`}>
          {t.parentTag}
          <select id={`${id}-parent`} className={input} value={parent} onChange={(e) => setParent(e.target.value)}>
            <option value="">{t.topLevel}</option>
            {!knownParent && <option value={parent}>{t.unknownEntry}</option>}
            {parents.map(({ tag: x, depth }) => (
              <option key={x.id} value={x.id}>
                {"  ".repeat(Math.min(depth, 6)) + x.name}
              </option>
            ))}
          </select>
        </label>
      )}
      {error && (
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
      )}
      <button type="submit" className={`${primary} self-end`} disabled={busy}>
        {t.save}
      </button>
    </form>
  );
}

function DeleteForm({ entry, hasChildren, cancel, done, gone }: { entry: Row; hasChildren: boolean; cancel: () => void; done: () => void; gone: () => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const n = entry.row.documentCount ?? 0;
  const text = entry.kind === "tags" ? t.deleteTagText(n) : entry.kind === "correspondents" ? t.deleteCorrespondentText(n) : t.deleteTypeText(n);
  const kind: MetadataKind = entry.kind;
  return (
    <div className="flex flex-col gap-3">
      <p className="text-sm">{text}</p>
      {hasChildren && <p className="text-sm">{t.deleteTagChildren}</p>}
      {error && (
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
      )}
      <div className="flex justify-end gap-2">
        <button type="button" className={button} onClick={cancel}>
          {t.cancel}
        </button>
        <button
          type="button"
          className="rounded-md bg-danger-solid px-3 py-1.5 text-sm font-medium text-white hover:bg-danger-solid-hover disabled:opacity-50 pointer-coarse:min-h-11"
          disabled={busy}
          onClick={() => {
            setBusy(true);
            setError("");
            metadataEditApi.remove(kind, entry.row.id).then(
              () => {
                setBusy(false);
                done();
              },
              (err: unknown) => {
                setBusy(false);
                setError(metadataErrorText(err));
                if (err instanceof ApiError && err.status === 404) gone();
              },
            );
          }}
        >
          {t.deleteButton}
        </button>
      </div>
    </div>
  );
}
