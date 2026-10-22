// Line icons on a 24 grid (stroke 1.75), drawn in the current text color.

const props = {
  width: 18,
  height: 18,
  viewBox: "0 0 24 24",
  fill: "none",
  stroke: "currentColor",
  strokeWidth: 1.75,
  strokeLinecap: "round" as const,
  strokeLinejoin: "round" as const,
  "aria-hidden": true,
};

/** A square icon button, 44 px on touch screens. */
export const iconButton =
  "relative flex size-8 shrink-0 items-center justify-center rounded-md text-ink-muted hover:bg-surface-hover hover:text-ink pointer-coarse:size-11";

export const MenuIcon = () => (
  <svg {...props}>
    <path d="M4 6h16M4 12h16M4 18h16" />
  </svg>
);

export const CloseIcon = () => (
  <svg {...props}>
    <path d="M6 6l12 12M18 6L6 18" />
  </svg>
);

export const SearchIcon = () => (
  <svg {...props}>
    <circle cx="11" cy="11" r="7" />
    <path d="M20 20l-3.5-3.5" />
  </svg>
);

export const InboxIcon = () => (
  <svg {...props}>
    <path d="M22 12h-6l-2 3h-4l-2-3H2" />
    <path d="M5.5 5.1L2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.5-6.9A2 2 0 0 0 16.8 4H7.2a2 2 0 0 0-1.7 1.1z" />
  </svg>
);

export const StarIcon = ({ filled = false }: { filled?: boolean }) => (
  <svg {...props} fill={filled ? "currentColor" : "none"}>
    <path d="M12 3l2.8 5.7 6.2.9-4.5 4.4 1.1 6.2L12 17.3l-5.6 2.9 1.1-6.2L3 9.6l6.2-.9z" />
  </svg>
);

export const DocumentIcon = () => (
  <svg {...props}>
    <path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z" />
    <path d="M14 3v6h6" />
  </svg>
);

export const ArrowIcon = ({ up }: { up: boolean }) => (
  <svg {...props} className={up ? "" : "rotate-180"}>
    <path d="M12 19V5M6 11l6-6 6 6" />
  </svg>
);

export function Chevron({ open }: { open: boolean }) {
  return (
    <svg {...props} width={16} height={16} strokeWidth={2} className={open ? "" : "-rotate-90"}>
      <path d="M6 9l6 6 6-6" />
    </svg>
  );
}
