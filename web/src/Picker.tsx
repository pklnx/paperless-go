import { useId, useMemo, useRef, useState } from "react";
import { ApiError } from "./api";
import { CloseIcon } from "./icons";
import { t } from "./i18n";
import { input } from "./styles";
import { chipInk } from "./tagTree";

/** A choice of a picker. depth indents it (the tag tree). */
export interface PickerOption {
  id: string;
  name: string;
  depth: number;
  color?: string;
}

interface Props {
  label: string;
  /** In the order shown. */
  options: PickerOption[];
  selected: string[];
  /** Several choices (tags), or one at most (correspondent, type). */
  multiple: boolean;
  onChange: (ids: string[]) => void;
  /** Creates a row named name and answers its ID; without it, nothing can be created here. */
  onCreate?: (name: string) => Promise<string>;
  disabled?: boolean;
}

const fold = (s: string) => s.toLocaleLowerCase();

/**
 * A picker with type-ahead: the chosen entries as chips, a field that
 * filters the choices, and "Create …" for a name that does not exist yet.
 * The arrow keys move in the list, Enter chooses, Escape closes the list.
 * Names are text only.
 */
export function Picker({ label, options, selected, multiple, onChange, onCreate, disabled }: Props) {
  const id = useId();
  const listId = `${id}-list`;
  const [text, setText] = useState("");
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);

  const byId = useMemo(() => new Map(options.map((o) => [o.id, o])), [options]);
  const chosen = selected.map((s) => byId.get(s) ?? { id: s, name: t.unknownEntry, depth: 0 });
  const q = fold(text.trim());
  const matches = options.filter((o) => (!multiple || !selected.includes(o.id)) && (q === "" || fold(o.name).includes(q)));
  const canCreate = onCreate !== undefined && q !== "" && !options.some((o) => fold(o.name) === q);
  // The rows of the list: the matches, then "Create" when it applies.
  const rows: ({ kind: "option"; option: PickerOption } | { kind: "create" })[] = [
    ...matches.map((option) => ({ kind: "option" as const, option })),
    ...(canCreate ? [{ kind: "create" as const }] : []),
  ];
  const shown = open && rows.length > 0;
  const at = Math.min(active, rows.length - 1);

  const choose = (optionId: string) => {
    onChange(multiple ? [...selected.filter((s) => s !== optionId), optionId] : [optionId]);
    setText("");
    setActive(0);
    if (!multiple) setOpen(false);
  };
  const create = () => {
    if (!onCreate || creating) return;
    const name = text.trim();
    setCreating(true);
    setError("");
    onCreate(name).then(
      (newId) => {
        setCreating(false);
        choose(newId);
      },
      (err: unknown) => {
        setCreating(false);
        setError(
          err instanceof ApiError && err.body.code === "name_taken"
            ? t.nameTaken
            : err instanceof ApiError && err.body.code === "invalid_name"
              ? t.invalidName
              : t.failed(err instanceof Error ? err.message : String(err)),
        );
      },
    );
  };
  const pick = (i: number) => {
    const r = rows[i];
    if (!r) return;
    if (r.kind === "create") create();
    else choose(r.option.id);
  };

  return (
    <div className="flex flex-col gap-1">
      <label htmlFor={id} className="text-sm font-medium">
        {label}
      </label>
      {chosen.length > 0 && (
        <ul aria-label={t.chosenIn(label)} className="flex flex-wrap gap-1">
          {chosen.map((o) => (
            <li
              key={o.id}
              className={`flex items-center gap-0.5 rounded-full pl-2 text-xs font-medium ${o.color ? "" : "bg-surface-hover text-ink"}`}
              style={o.color ? { backgroundColor: o.color, color: chipInk(o.color) } : undefined}
            >
              <span className="wrap-anywhere">{o.name}</span>
              <button
                type="button"
                disabled={disabled}
                aria-label={t.removeChoice(o.name)}
                title={t.removeChoice(o.name)}
                className="grid size-6 place-items-center rounded-full pointer-coarse:size-11"
                onClick={() => onChange(selected.filter((s) => s !== o.id))}
              >
                <CloseIcon />
              </button>
            </li>
          ))}
        </ul>
      )}
      <div className="relative">
        <input
          ref={inputRef}
          id={id}
          className={input}
          role="combobox"
          aria-expanded={shown}
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={shown ? `${id}-${at}` : undefined}
          autoComplete="off"
          disabled={disabled || creating}
          placeholder={multiple ? t.addChoice : chosen.length > 0 ? t.changeChoice : t.chooseOne}
          value={text}
          maxLength={128}
          onChange={(e) => {
            setText(e.target.value);
            setActive(0);
            setOpen(true);
            setError("");
          }}
          onFocus={() => setOpen(true)}
          onClick={() => setOpen(true)}
          onBlur={() => setOpen(false)}
          onKeyDown={(e) => {
            if (e.key === "ArrowDown") {
              e.preventDefault();
              setOpen(true);
              setActive(Math.min(at + 1, rows.length - 1));
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              setActive(Math.max(at - 1, 0));
            } else if (e.key === "Enter") {
              // Never submits the form: Enter chooses here.
              e.preventDefault();
              if (shown) pick(at);
            } else if (e.key === "Escape" && (shown || text)) {
              // Closes the list, not the document.
              e.preventDefault();
              e.stopPropagation();
              setOpen(false);
              setText("");
            }
          }}
        />
        {shown && (
          <ul id={listId} role="listbox" aria-label={label} className="absolute inset-x-0 top-full z-10 mt-1 max-h-60 overflow-y-auto rounded-md border border-line bg-surface-raised py-1 shadow-lg">
            {rows.map((r, i) => (
              <li
                key={r.kind === "create" ? "\u0000create" : r.option.id}
                id={`${id}-${i}`}
                role="option"
                aria-selected={i === at}
                className={`cursor-pointer px-2.5 py-1.5 text-sm wrap-anywhere pointer-coarse:min-h-11 ${i === at ? "bg-surface-hover" : ""}`}
                style={r.kind === "option" && q === "" ? { paddingLeft: `${0.625 + r.option.depth}rem` } : undefined}
                // Before the field's blur closes the list.
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => pick(i)}
              >
                {r.kind === "create" ? t.createNamed(text.trim()) : r.option.name}
              </li>
            ))}
          </ul>
        )}
      </div>
      {error && (
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
      )}
    </div>
  );
}
