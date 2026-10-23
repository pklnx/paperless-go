// Shared class names of form controls.

export const primary =
  "rounded-md bg-accent px-3 py-1.5 text-sm font-medium text-on-accent hover:bg-accent-hover disabled:opacity-50 pointer-coarse:min-h-11";
export const button =
  "rounded-md border border-line-control px-2.5 py-1 text-sm hover:bg-surface-hover disabled:opacity-50 pointer-coarse:min-h-11";
/** Also inside a disabled fieldset (a document in the trash is read-only), a field looks disabled. */
export const input =
  "w-full rounded-md border border-line-control bg-field px-2.5 py-1.5 text-sm disabled:cursor-not-allowed disabled:bg-surface-hover disabled:text-ink-muted pointer-coarse:min-h-11";
/** A destructive primary button (delete permanently), white text. */
export const danger =
  "rounded-md bg-danger-solid px-3 py-1.5 text-sm font-medium text-white hover:bg-danger-solid-hover disabled:opacity-50 pointer-coarse:min-h-11";
