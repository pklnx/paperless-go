import { useEffect, useId, useMemo, useRef, useState, type FocusEvent, type KeyboardEvent } from "react";
import { ComboBox, DismissibleTag, FilterableMultiSelect } from "@carbon/react";
import { ApiError } from "./api";
import { t, translateWithId } from "./i18n";
import { chipInk } from "./tagTree";

// The detail's pickers (#150 part 4, decision 2): Carbon's ComboBox for the
// correspondent and the type, its FilterableMultiSelect for the tags. Both
// offer a synthetic "Create “x”" item while the typed name matches nothing,
// which creates the row and chooses it. Names are text only.

/** One choice: a row's id and name. A tag also has its path ("Finance / Bills") and colour. */
export interface Choice {
  id: string;
  name: string;
}

export interface TagChoice extends Choice {
  /** The names from the root down, joined with " / "; the name itself for a root tag. */
  path: string;
  color?: string;
}

/** The id of the synthetic "Create “x”" item. */
export const CREATE = "\u0000create";

const fold = (s: string) => s.toLocaleLowerCase();

/** Whether a typed name (trimmed) names none of the choices (by name, or by a tag's path), so "Create" applies. */
export function canCreate(name: string, choices: readonly (Choice & { path?: string })[]): boolean {
  const q = fold(name);
  return q !== "" && !choices.some((c) => fold(c.name) === q || (c.path !== undefined && fold(c.path) === q));
}

/** Whether the picker's list box (inside root) is open: Carbon marks the open ListBox. */
const listOpen = (root: HTMLElement | null) => root?.querySelector(".cds--list-box--expanded") !== null;

/**
 * Escape in a picker's field. With the list open, Carbon closes the list;
 * the default is prevented so that the document's own Escape (which
 * yields on a prevented key) leaves the document open. With the list
 * closed, Carbon would clear the chosen entries (the
 * FilterableMultiSelect) or empty the text but keep the choice (the
 * ComboBox, through downshift): neither reaches the field, the key is
 * the document's Escape, as before (onEscape), and the field stays as it is.
 */
function escapeClosed(e: KeyboardEvent<HTMLElement>, root: HTMLElement | null, onEscape: (() => void) | undefined): boolean {
  if (e.key !== "Escape" || !(e.target instanceof HTMLInputElement)) return false;
  if (listOpen(root)) {
    e.preventDefault();
    return false;
  }
  e.stopPropagation();
  onEscape?.();
  return true;
}

/** What went wrong when a row could not be created. */
function createErrorText(err: unknown): string {
  if (err instanceof ApiError && err.body.code === "name_taken") return t.nameTaken;
  if (err instanceof ApiError && err.body.code === "invalid_name") return t.invalidName;
  return t.failed(err instanceof Error ? err.message : String(err));
}

/**
 * Creates the row named name and answers its id; the error is kept for
 * the field. The form is told while a creation is pending (onPending), so
 * it does not save without the row; an answer that comes after the picker
 * is gone (another document is shown) is dropped, never applied elsewhere.
 */
function useCreator(onCreate: ((name: string) => Promise<string>) | undefined, onPending: ((pending: boolean) => void) | undefined) {
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState("");
  const live = useRef(true);
  useEffect(() => {
    live.current = true;
    return () => {
      live.current = false;
    };
  }, []);
  const tell = useRef(onPending);
  tell.current = onPending;
  useEffect(() => {
    if (!creating) return;
    tell.current?.(true);
    return () => tell.current?.(false);
  }, [creating]);
  const create = (name: string, then: (id: string) => void) => {
    if (!onCreate || creating) return;
    setCreating(true);
    setError("");
    onCreate(name).then(
      (id) => {
        if (!live.current) return;
        setCreating(false);
        then(id);
      },
      (err: unknown) => {
        if (!live.current) return;
        setCreating(false);
        setError(createErrorText(err));
      },
    );
  };
  return { creating, error, clearError: () => setError(""), create };
}

/**
 * The field takes the focus back once a creation has settled: it was
 * disabled while the row was created, and a browser drops the focus from
 * a disabled field (review B of #192). Only when the focus is still there
 * or nowhere (the body): a user who moved on to another field keeps it.
 */
function useRefocus(creating: boolean, root: () => HTMLElement | null, field: () => HTMLElement | null | undefined) {
  const was = useRef(creating);
  useEffect(() => {
    if (was.current && !creating) {
      const active = document.activeElement;
      if (active === null || active === document.body || (root()?.contains(active) ?? false)) field()?.focus();
    }
    was.current = creating;
  });
}

interface NamedProps {
  label: string;
  /** In the order shown. */
  choices: readonly Choice[];
  /** The chosen row's id; an id without a row shows as "unknown". */
  selected: string | null;
  onChange: (id: string | null) => void;
  /** Creates a row named name and answers its id; without it, nothing can be created here. */
  onCreate?: (name: string) => Promise<string>;
  disabled?: boolean;
  /** Escape in the field while its list is closed: the document's Escape. */
  onEscape?: () => void;
  /** A creation started (true) or settled (false): the form waits for it. */
  onPending?: (pending: boolean) => void;
}

/**
 * One choice at most (the correspondent, the type): Carbon's ComboBox with
 * the rows, a "Create “x”" item for a typed name that is no row yet, and
 * its clear button. Enter chooses the highlighted row (the first match
 * while typing), Escape closes the list and keeps the document open.
 */
export function NamedPicker({ label, choices, selected, onChange, onCreate, disabled, onEscape, onPending }: NamedProps) {
  const id = useId();
  const root = useRef<HTMLDivElement>(null);
  const [typed, setTyped] = useState("");
  const creator = useCreator(onCreate, onPending);
  const items = useMemo<Choice[]>(() => {
    const name = typed.trim();
    return onCreate && canCreate(name, choices) ? [...choices, { id: CREATE, name: t.createNamed(name) }] : [...choices];
  }, [choices, typed, onCreate]);
  // The choice's own object while it stays (Carbon resets its field to the
  // choice's name whenever this prop changes); null while the rows load.
  // resync: a new object makes Carbon write the choice's name into the field again.
  const [resync, setResync] = useState(0);
  // Stable by id and name (not by the choices array, which other pickers'
  // creations rebuild): a new object only when the choice or its name
  // changes, or on resync, since Carbon then overwrites the field's text.
  const found = selected === null ? undefined : choices.find((c) => c.id === selected);
  const foundName = found?.name;
  // No rows yet (they are loading): nothing to show; an id among rows that lack it is "unknown".
  const shownName = found ? foundName : choices.length === 0 ? undefined : t.unknownEntry;
  const selectedItem = useMemo<Choice | null>(
    () => (selected === null || shownName === undefined ? null : { id: selected, name: shownName }),
    [selected, shownName, resync],
  );
  useRefocus(
    creator.creating,
    () => root.current,
    () => root.current?.querySelector("input"),
  );
  // Leaving the field: what it shows is what the form has. An emptied
  // field clears the choice (the form becomes dirty); other text that is
  // not the choice's name gives way to the name again (a choice is made
  // from the list, with Enter or a click). Carbon does either only while
  // the list is open (review B of #192).
  const onBlurCapture = (e: FocusEvent<HTMLDivElement>) => {
    if (!(e.target instanceof HTMLInputElement) || selectedItem === null) return;
    if (e.target.value === "") onChange(null);
    else if (e.target.value !== selectedItem.name) setResync((n) => n + 1);
  };
  return (
    <div ref={root} className="doc-form__picker" onKeyDownCapture={(e) => escapeClosed(e, root.current, onEscape)} onBlurCapture={onBlurCapture}>
      <ComboBox<Choice>
        id={`${id}-combo`}
        titleText={label}
        placeholder={t.chooseOne}
        items={items}
        itemToString={(item) => item?.name ?? ""}
        selectedItem={selectedItem}
        disabled={disabled || creator.creating}
        invalid={creator.error !== ""}
        invalidText={creator.error}
        translateWithId={translateWithId}
        // The rows that contain the typed text; "Create" always.
        shouldFilterItem={({ item, inputValue }) => item.id === CREATE || !inputValue || fold(item.name).includes(fold(inputValue))}
        onInputChange={(text) => {
          setTyped(text);
          creator.clearError();
        }}
        onChange={({ selectedItem: item }) => {
          if (!item) {
            if (selected !== null) onChange(null);
            return;
          }
          if (item.id === CREATE) {
            creator.create(typed.trim(), onChange);
            return;
          }
          if (item.id !== selected) onChange(item.id);
        }}
        inputProps={{ maxLength: 128 }}
      />
    </div>
  );
}

interface TagsProps {
  label: string;
  /** In tree order, each with its path. */
  choices: readonly TagChoice[];
  /** The chosen ids in their order; an id without a row shows as "unknown". */
  selected: readonly string[];
  onChange: (ids: string[]) => void;
  onCreate?: (name: string) => Promise<string>;
  disabled?: boolean;
  /** The document is read-only (in the trash): the chips take Carbon's disabled look (the fieldset disables the rest). */
  readOnly?: boolean;
  /** Escape in the field while its list is closed: the document's Escape. */
  onEscape?: () => void;
  /** A creation started (true) or settled (false): the form waits for it. */
  onPending?: (pending: boolean) => void;
}

/**
 * Several choices (the tags): Carbon's FilterableMultiSelect with the tags
 * in tree order and their path ("Finance / Bills"), so a nested tag is
 * found by its own name and by its parent's; a "Create “x”" item when the
 * typed name is no tag yet; the chosen tags as dismissible chips under the
 * field ("Chosen: Tags"), in the order they were chosen. Backspace or
 * Delete in the empty field removes the last chosen tag; Carbon's own keys
 * that would remove every chosen tag at once (Escape, Delete, Backspace
 * while the list is closed) never reach the form.
 */
export function TagPicker({ label, choices, selected, onChange, onCreate, disabled, readOnly, onEscape, onPending }: TagsProps) {
  const id = useId();
  const root = useRef<HTMLDivElement>(null);
  const creator = useCreator(onCreate, onPending);
  const byId = useMemo(() => new Map(choices.map((c) => [c.id, c])), [choices]);
  const chosen = selected.map((s) => byId.get(s) ?? { id: s, name: t.unknownEntry, path: t.unknownEntry });
  const selectedItems = useMemo(() => selected.map((s) => byId.get(s)).filter((c): c is TagChoice => c !== undefined), [selected, byId]);
  // Carbon compares items by value: the same objects as in choices, so the
  // chosen ones are the choices themselves, and "Create" is appended per filter.
  const filterItems = (items: readonly TagChoice[], { inputValue }: { inputValue: string | null }) => {
    const name = (inputValue ?? "").trim();
    const q = fold(name);
    const matches = q === "" ? [...items] : items.filter((c) => fold(c.path).includes(q));
    if (onCreate && canCreate(name, choices)) matches.push({ id: CREATE, name, path: t.createNamed(name) });
    return matches;
  };
  const remove = (tagId: string) => onChange(selected.filter((s) => s !== tagId));
  const onKeyDownCapture = (e: KeyboardEvent<HTMLDivElement>) => {
    if (escapeClosed(e, root.current, onEscape)) return;
    if (!(e.target instanceof HTMLInputElement) || (e.key !== "Backspace" && e.key !== "Delete")) return;
    if (e.target.value === "") {
      // The empty field: the last chosen tag goes (Carbon would drop them
      // all); a held key (auto-repeat, after erasing the text) removes none.
      e.preventDefault();
      e.stopPropagation();
      if (!e.repeat && selected.length > 0 && !disabled && !readOnly) onChange(selected.slice(0, -1));
    } else if (e.key === "Delete" && !listOpen(root.current)) {
      // Delete edits the text; Carbon would clear the text and every chosen tag.
      e.stopPropagation();
    }
  };
  const chipsDisabled = disabled || readOnly;
  useRefocus(
    creator.creating,
    () => root.current,
    () => root.current?.querySelector("input"),
  );
  return (
    <div ref={root} className="doc-form__tags" onKeyDownCapture={onKeyDownCapture}>
      <FilterableMultiSelect<TagChoice>
        id={`${id}-tags`}
        titleText={label}
        placeholder={t.addChoice}
        items={choices as TagChoice[]}
        itemToString={(item) => item?.path ?? ""}
        selectedItems={selectedItems}
        // Tree order, whatever is chosen (Carbon would sort the chosen ones to the top).
        sortItems={(items) => items as TagChoice[]}
        selectionFeedback="fixed"
        filterItems={filterItems}
        disabled={disabled || creator.creating}
        invalid={creator.error !== ""}
        invalidText={creator.error}
        translateWithId={translateWithId}
        clearSelectionDescription={t.chosenCount}
        clearSelectionText={t.clearChosenHint}
        inputProps={{ "aria-label": label, maxLength: 128 }}
        onChange={({ selectedItems: items }) => {
          creator.clearError();
          const create = items.find((c) => c.id === CREATE);
          if (create) {
            creator.create(create.name, (newId) => onChange([...selected.filter((s) => s !== newId), newId]));
            return;
          }
          // The ids of the rows, in the order they were chosen; an id
          // without a row (unknown here) is kept as it is.
          const ids = items.map((c) => c.id);
          const kept = selected.filter((s) => !byId.has(s) || ids.includes(s));
          const next = [...kept, ...ids.filter((x) => !kept.includes(x))];
          if (next.length !== selected.length || next.some((x, i) => x !== selected[i])) onChange(next);
        }}
      />
      {chosen.length > 0 && (
        <ul aria-label={t.chosenIn(label)} className="doc-form__chosen">
          {chosen.map((c) => (
            <li key={c.id}>
              {/* A tag's own colour, with black or white text for contrast; without one, and read-only, Carbon's gray. */}
              <DismissibleTag
                size="md"
                type={c.color && !chipsDisabled ? undefined : "gray"}
                text={c.name}
                title={t.removeChoice(c.name)}
                dismissTooltipLabel={t.removeChoice(c.name)}
                disabled={chipsDisabled}
                style={c.color && !chipsDisabled ? { backgroundColor: c.color, color: chipInk(c.color) } : undefined}
                onClose={() => remove(c.id)}
              />
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
