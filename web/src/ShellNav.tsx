import { forwardRef, type ComponentType, type MouseEvent, type ReactNode } from "react";
import { SideNav, SideNavDivider, SideNavItems, SideNavLink } from "@carbon/react";
import { Document, Email, Launch, RecentlyViewed, Star, Tag, TrashCan, UserAvatar, UserMultiple } from "@carbon/icons-react";
import type { DocumentCounts } from "./api";
import { isPlainClick } from "./clicks";
import { t } from "./i18n";
import { activeFilter, filterPatch, formatState, type Filter, type ViewState } from "./urlState";

export const docsURL = "https://pklnx.github.io/paperless-go/";

interface Props {
  id: string;
  state: ViewState;
  admin: boolean;
  /** The lists' counts; none while they load or failed to load. */
  counts: DocumentCounts | null;
  /** lg and up: Carbon's rail, which expands on hover and focus. */
  rail: boolean;
  /** Below lg: the overlay opened by the header's menu button. */
  expanded: boolean;
  onClose: () => void;
  /** Goes to a place in the app (a patch of the URL state). */
  go: (patch: Partial<ViewState>) => void;
}

const sameFilter = (a: Filter | null, b: Filter | null) =>
  a === null || b === null ? a === b : a.kind === b.kind && ("id" in a ? a.id : "") === ("id" in b ? b.id : "");

/**
 * The patch that opens a list: "All documents" lists everything (no filter,
 * no search); another list keeps the search and replaces the filter. Pages
 * and the open document are left.
 */
export function listPatch(f: Filter | null): Partial<ViewState> {
  return { ...filterPatch(f), ...(f === null ? { q: "" } : {}), view: "", doc: "", sv: "" };
}

/** The patch that opens a page ("metadata", "profile", "log" or "users"). */
export function pagePatch(view: string): Partial<ViewState> {
  return { view, doc: "", sv: "" };
}

/**
 * The shell's side navigation: the document lists with their counts and the
 * pages. Saved views, tags, correspondents and types are page navigation,
 * in the documents view's column (NavColumn).
 */
export const ShellNav = forwardRef<HTMLElement, Props>(function ShellNav({ id, state, admin, counts, rail, expanded, onClose, go }, ref) {
  const docs = state.view === "";
  const current = docs ? activeFilter(state) : null;
  const link = (patch: Partial<ViewState>, isCurrent: boolean, icon: ComponentType, label: ReactNode) => (
    <SideNavLink
      renderIcon={icon}
      // A real href, so a Ctrl/Cmd, Shift or Alt click opens the place in a new tab or window.
      href={formatState({ ...state, ...patch }) || "/"}
      isActive={isCurrent}
      aria-current={isCurrent ? "page" : undefined}
      onClick={(e: MouseEvent) => {
        if (!isPlainClick(e)) return;
        e.preventDefault();
        go(patch);
        onClose();
      }}
    >
      {label}
    </SideNavLink>
  );
  const list = (f: Filter | null, label: string, icon: ComponentType, n: number | undefined) =>
    link(
      listPatch(f),
      docs && sameFilter(current, f),
      icon,
      n === undefined ? (
        label
      ) : (
        <>
          {label} <span className="shell-nav__count">{n}</span>
        </>
      ),
    );
  const page = (view: string, label: string, icon: ComponentType) => link(pagePatch(view), state.view === view, icon, label);
  return (
    <SideNav
      ref={ref}
      id={id}
      aria-label={t.navigation}
      className="shell-nav"
      isRail={rail}
      expanded={expanded}
      // Below lg the menu button and Escape (App) open and close it; Carbon's
      // focus listeners would close it on the way to the menu button, which
      // then opens it again. On lg they expand the rail on focus.
      addFocusListeners={rail}
      onOverlayClick={onClose}
    >
      <SideNavItems>
        {list(null, t.allDocuments, Document, counts?.total)}
        {list({ kind: "inbox" }, t.inbox, Email, counts?.inbox)}
        {list({ kind: "fav" }, t.favorites, Star, counts?.favorites)}
        {list({ kind: "trash" }, t.trash, TrashCan, counts?.trash)}
        <SideNavDivider />
        {page("metadata", t.manageMetadata, Tag)}
        {page("log", t.log, RecentlyViewed)}
        {admin && page("users", t.users, UserMultiple)}
        {page("profile", t.profile, UserAvatar)}
        <SideNavDivider />
        <SideNavLink renderIcon={Launch} href={docsURL} rel="noreferrer">
          {t.documentation}
        </SideNavLink>
      </SideNavItems>
    </SideNav>
  );
});
