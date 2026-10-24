import { useCallback, useEffect, useId, useRef, useState, type MouseEvent } from "react";
import { Header, HeaderGlobalAction, HeaderGlobalBar, HeaderMenuButton, HeaderName, IconButton, InlineNotification } from "@carbon/react";
import { Folders, Logout } from "@carbon/icons-react";
import { documentsApi, metadataApi, savedViewsApi, type DocumentCounts, type Metadata, type SavedView, type User } from "./api";
import { AuthGate } from "./Auth";
import { lg, useMediaQuery } from "./breakpoints";
import { isPlainClick } from "./clicks";
import { DocumentList } from "./DocumentList";
import { DocumentsUpload, WindowDrop, useQueue } from "./Documents";
import { fetchHealth, type Health } from "./health";
import { t } from "./i18n";
import { LogPage } from "./LogPage";
import { MetadataPage } from "./MetadataPage";
import { columnFocusTarget, NavColumn, type Loaded } from "./NavColumn";
import { ProductMark } from "./ProductMark";
import { ProfilePage } from "./Profile";
import { SaveViewButton, SavedViewList } from "./SavedViews";
import { showsView, shownViews } from "./savedViews";
import { listPatch, pagePatch, ShellNav } from "./ShellNav";
import { ThemeToggle } from "./ThemeToggle";
import { Toast, useNotice } from "./Toast";
import { useCollapsedTags } from "./tagTree";
import { UploadQueue } from "./uploads";
import { UsersPage } from "./UsersPage";
import { confirmLeave, documentQuery, useViewState, type Filter, type ViewState } from "./urlState";

/** The app: the login gate, then the logged-in shell. */
export function App() {
  return <AuthGate>{(user, logout) => <Shell user={user} logout={logout} />}</AuthGate>;
}

const errorText = (err: unknown) => (err instanceof Error ? err.message : String(err));

function Shell({ user, logout }: { user: User; logout: () => void }) {
  const [state, update] = useViewState();
  // One queue for the session: uploads keep running while another page is open.
  const [queue] = useState(() => new UploadQueue());
  const imported = useQueue(queue).filter((i) => i.state.kind === "imported").length;
  const [maxSize, setMaxSize] = useState<number | undefined>(queue.maxSize);
  // Below lg the shell's SideNav (menuOpen) and the documents view's column
  // (browseOpen) open as overlays, never both. From lg up both are always
  // on screen, the SideNav as Carbon's rail.
  const wide = useMediaQuery(lg);
  const [menuOpen, setMenuOpen] = useState(false);
  const [browseOpen, setBrowseOpen] = useState(false);
  useEffect(() => {
    if (wide) {
      setMenuOpen(false);
      setBrowseOpen(false);
    }
  }, [wide]);
  const closeOverlays = useCallback(() => {
    setMenuOpen(false);
    setBrowseOpen(false);
  }, []);
  const navId = useId();
  const asideId = useId();

  // An admin sees their own documents and sidebar unless "Everyone's
  // documents" is on (?all=1). For anybody else the server's scope is
  // their own documents anyway.
  const everyone = user.admin && state.all === "1";
  const ownOnly = user.admin && !everyone;
  const query = documentQuery(state, ownOnly);

  const [health, setHealth] = useState<Health>("checking");
  useEffect(() => {
    let live = true;
    void fetchHealth().then((h) => live && setHealth(h));
    return () => {
      live = false;
    };
  }, []);

  // The sidebar's counts and names, again after each import.
  const [counts, setCounts] = useState<DocumentCounts | null>(null);
  const [metadata, setMetadata] = useState<Loaded<Metadata>>({ data: null, error: "" });
  // Changes after each edit in a document's detail, each bulk change and
  // each change on the metadata page: counts and names again.
  const [edits, setEdits] = useState(0);
  const onChanged = useCallback(() => setEdits((n) => n + 1), []);
  // A duplicate upload's trashed document restored from the upload panel: the list and counts again.
  const [restoredUploads, setRestoredUploads] = useState(0);
  const onUploadRestored = useCallback(() => {
    setRestoredUploads((n) => n + 1);
    onChanged();
  }, [onChanged]);
  useEffect(() => {
    const ctl = new AbortController();
    documentsApi.counts(ownOnly, ctl.signal).then(setCounts, () => {
      // The sidebar shows no numbers; the list says what failed.
      if (!ctl.signal.aborted) setCounts(null);
    });
    metadataApi.load(ownOnly, ctl.signal).then(
      (data) => setMetadata({ data, error: "" }),
      (err: unknown) => {
        if (!ctl.signal.aborted) setMetadata((m) => ({ data: m.data, error: errorText(err) }));
      },
    );
    return () => ctl.abort();
  }, [ownOnly, imported, edits]);
  // The caller's saved views, again after each change of one.
  const [views, setViews] = useState<Loaded<SavedView[]>>({ data: null, error: "" });
  const [viewEdits, setViewEdits] = useState(0);
  const reloadViews = useCallback(() => setViewEdits((n) => n + 1), []);
  useEffect(() => {
    const ctl = new AbortController();
    savedViewsApi.list(ctl.signal).then(
      (data) => setViews({ data, error: "" }),
      (err: unknown) => {
        if (!ctl.signal.aborted) setViews((v) => ({ data: v.data, error: errorText(err) }));
      },
    );
    return () => ctl.abort();
  }, [viewEdits]);
  const collapsed = useCollapsedTags(user.name, metadata.data?.tags ?? null);
  // The selected tag stays visible: its parents are expanded once when the
  // selection changes, or when the tag first shows up in the data. Here, not
  // in the column, which remounts with the documents view: a parent the user
  // collapsed again stays collapsed on the way back.
  const { reveal } = collapsed;
  const revealed = useRef("");
  useEffect(() => {
    const tags = metadata.data?.tags;
    if (!state.tag || !tags || revealed.current === state.tag) return;
    if (!tags.some((x) => x.id === state.tag)) return;
    revealed.current = state.tag;
    reveal(state.tag);
  }, [state.tag, metadata.data, reveal]);
  // Opening the trash counts again: a document may have been deleted elsewhere.
  const inTrashView = state.trash === "1" && state.view === "";
  useEffect(() => {
    if (inTrashView) onChanged();
  }, [inTrashView, onChanged]);
  // The notices (Delete's Undo, results of changes) live here, so they
  // outlive the list: another page opened within their time keeps them.
  const notices = useNotice();
  const notify = notices.show;
  // An Undo of a delete: the list now shown loads every page again (the
  // list that showed the notice may be gone and mounted anew since).
  const [undone, setUndone] = useState(0);
  const onUndone = useCallback(() => setUndone((n) => n + 1), []);
  // The caller's own trash, for "Empty trash" (always the own trash): the
  // sidebar's count is everyone's while an admin's switch is on.
  const [ownTrash, setOwnTrash] = useState<number | null>(null);
  useEffect(() => {
    if (!everyone) {
      setOwnTrash(counts?.trash ?? null);
      return;
    }
    // Asked for only where the button is: in the trash.
    if (!inTrashView) {
      setOwnTrash(null);
      return;
    }
    const ctl = new AbortController();
    documentsApi.counts(true, ctl.signal).then(
      (c) => setOwnTrash(c.trash ?? null),
      () => {
        if (!ctl.signal.aborted) setOwnTrash(null);
      },
    );
    return () => ctl.abort();
  }, [everyone, counts, inTrashView]);

  // "All documents" lists everything: no filter, no search. Another entry
  // keeps the search and replaces the filter (ShellNav.listPatch).
  const select = (f: Filter | null) => {
    update(listPatch(f));
    closeOverlays();
  };
  const openView = (patch: Partial<ViewState>) => {
    update(patch);
    closeOverlays();
  };
  const setEveryone = (on: boolean) => update({ all: on ? "1" : "" });
  // Logging out leaves the app: the open document's unsaved changes are
  // asked about first, and a refusal keeps the session and the edits.
  const leave = () => {
    if (confirmLeave()) logout();
  };
  const onMaxUploadSize = useCallback(
    (n: number) => {
      queue.maxSize = n;
      setMaxSize(n);
    },
    [queue],
  );

  const view = state.view === "profile" || state.view === "log" || state.view === "metadata" || (state.view === "users" && user.admin) ? state.view : "";
  // The column overlay belongs to the documents view.
  useEffect(() => {
    if (view !== "") setBrowseOpen(false);
  }, [view]);

  // The overlays: focus moves into them and back to where it was, and the
  // rest of the page is inert meanwhile (below), so Tab stays inside. The
  // header stays usable next to the SideNav, so its menu button closes it.
  // The column overlay is modal: the header is inert too, so nothing behind
  // it opens another overlay.
  const aside = useRef<HTMLElement>(null);
  const sideNav = useRef<HTMLElement>(null);
  const opener = useRef<HTMLElement | null>(null);
  const overlay = menuOpen ? "menu" : browseOpen ? "browse" : "";
  useEffect(() => {
    if (overlay) {
      opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      const target = overlay === "menu" ? sideNav.current?.querySelector<HTMLElement>("a[href]") : columnFocusTarget(aside.current);
      target?.focus();
    } else if (opener.current) {
      opener.current.focus();
      opener.current = null;
    }
  }, [overlay]);
  // Focus moves to the page heading on a view change, so the keyboard and
  // screen readers start at the new page (after the overlay effect above,
  // so a page chosen from the SideNav overlay ends on its heading, not on
  // the menu button).
  const shownView = useRef(view);
  useEffect(() => {
    if (shownView.current === view) return;
    shownView.current = view;
    document.querySelector<HTMLElement>('main :is(h1, h2)[tabindex="-1"]')?.focus();
  });
  const pageInert = overlay !== "" || undefined;
  const closeBrowse = useCallback(() => setBrowseOpen(false), []);
  // Escape closes the open overlay. A window listener in the capture phase:
  // before any component sees the key (Carbon's TreeView keeps the keys it
  // gets from bubbling), and also with the focus on the body after a click
  // on a blank part of the overlay. A key inside a Carbon Menu or Modal
  // (portals outside the aside: a view's menu and its Rename and Delete)
  // belongs to that widget, which closes itself. The detail's own Escape
  // stays off while an overlay is open (dialogs.ts).
  useEffect(() => {
    if (!overlay) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      const el = e.target instanceof Element ? e.target : null;
      const onBody = !el || el === document.body || el === document.documentElement;
      if (overlay === "browse" && !onBody && !aside.current?.contains(el)) return;
      if (overlay === "menu" && !onBody && !sideNav.current?.contains(el) && !el.closest(".cds--header")) return;
      e.preventDefault();
      e.stopPropagation();
      if (overlay === "menu") setMenuOpen(false);
      else closeBrowse();
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [overlay, closeBrowse]);

  const column = (
    <NavColumn
      state={state}
      select={select}
      user={user}
      metadata={metadata}
      collapsed={collapsed}
      everyone={everyone}
      setEveryone={setEveryone}
      // The close button belongs to the overlay (below lg); the column has nothing to close.
      onClose={wide ? undefined : closeBrowse}
      savedViews={<SavedViewList views={views.data} error={views.error} state={state} admin={user.admin} open={openView} reload={reloadViews} notify={notify} />}
    />
  );

  return (
    <div className="app-shell">
      <WindowDrop
        onFiles={(files) => {
          queue.add(files);
          // The uploads show on the documents' page, which the trash is not.
          if (view !== "" || state.trash === "1") update({ view: "", doc: "", trash: "" });
        }}
      />
      {/* Carbon's UI Shell header at every width (#150, part 1). Usable next to the
          SideNav overlay (its menu button closes it); inert behind the column overlay.
          Header passes inert on to its <header>; its types only lack the attribute. */}
      <Header aria-label={t.appName} {...({ inert: browseOpen || undefined } as object)}>
        {/* Below lg (Carbon's breakpoint) it opens the SideNav; Carbon hides it from lg up. */}
        <HeaderMenuButton
          aria-label={t.openMenu}
          aria-controls={navId}
          aria-expanded={menuOpen}
          isActive={menuOpen}
          // Never from behind the column overlay (inert there anyway): one overlay does not open another.
          onClick={() => !browseOpen && setMenuOpen((open) => !open)}
        />
        <HeaderName
          href="/"
          prefix=""
          onClick={(e: MouseEvent) => {
            // All documents. A Ctrl/Cmd, Shift or Alt click is left to the
            // browser, so it still opens "/" in a new tab or window.
            if (!isPlainClick(e)) return;
            e.preventDefault();
            select(null);
          }}
        >
          <ProductMark className="app-header__logo" />
          {t.appName}
        </HeaderName>
        <HeaderGlobalBar>
          {/* The user's name opens the profile (from md; the SideNav has Profile at every width). */}
          <button
            type="button"
            className="app-header__user"
            aria-label={`${user.name}, ${t.profile}`}
            title={`${t.loggedInAs(user.name)} · ${t.profile}`}
            aria-current={view === "profile" ? "page" : undefined}
            onClick={() => openView(pagePatch("profile"))}
          >
            {user.name}
          </button>
          <ThemeToggle header />
          <HeaderGlobalAction aria-label={t.logOut} tooltipAlignment="end" onClick={leave}>
            <Logout size={20} />
          </HeaderGlobalAction>
        </HeaderGlobalBar>
      </Header>
      <ShellNav
        // Carbon keeps its hover state across a change of isRail: a rail expanded by
        // hover or focus would stay an inert overlay when the window narrows below lg.
        key={wide ? "rail" : "overlay"}
        ref={sideNav}
        id={navId}
        state={state}
        admin={user.admin}
        counts={counts}
        viewShown={shownViews(views.data ?? []).some(({ view: v, params }) => showsView(state, v.id, params, user.admin))}
        rail={wide}
        expanded={menuOpen && !wide}
        onClose={() => setMenuOpen(false)}
        go={(patch) => {
          update(patch);
          setBrowseOpen(false);
        }}
      />
      <div className="app-shell__body">
        {/* Only while the server is down (decision 11 of #150): the health check runs once, when the shell opens. */}
        {health === "down" && (
          <div inert={pageInert} className="app-shell__notice">
            <InlineNotification
              kind="error"
              lowContrast
              hideCloseButton
              role="status"
              statusIconDescription={t.carbon.status.error}
              title={t.serverDownTitle}
              subtitle={t.serverDownText}
            />
          </div>
        )}
        {view === "" ? (
          <div className="app-view docs-view">
            {/* The column from lg up; below lg an overlay, a modal dialog. */}
            <aside
              ref={aside}
              id={asideId}
              className={browseOpen ? "nav-column nav-column--overlay" : "nav-column"}
              inert={menuOpen || undefined}
              role={browseOpen ? "dialog" : undefined}
              aria-modal={browseOpen || undefined}
              aria-label={browseOpen ? t.browse : undefined}
            >
              {column}
            </aside>
            {browseOpen && <div aria-hidden className="nav-column__scrim" onClick={closeBrowse} />}
            <main inert={pageInert} className="docs-pane">
              <div className="docs-page">
                {state.trash !== "1" && <DocumentsUpload queue={queue} maxSize={maxSize} onRestored={onUploadRestored} />}
                <DocumentList
                  state={state}
                  update={update}
                  query={query}
                  user={user}
                  metadata={metadata.data}
                  everyone={everyone}
                  reload={imported + restoredUploads}
                  onMaxUploadSize={onMaxUploadSize}
                  onChanged={onChanged}
                  notify={notify}
                  onUndone={onUndone}
                  undone={undone}
                  ownTrash={ownTrash}
                  toolbar={
                    <>
                      {/* Below lg: opens the column as an overlay (layout.css hides it from lg). */}
                      <span className="nav-column__open">
                        <IconButton
                          kind="ghost"
                          size="md"
                          align="bottom-start"
                          label={t.browse}
                          aria-haspopup="dialog"
                          aria-expanded={browseOpen}
                          aria-controls={asideId}
                          // Never from behind the SideNav overlay (inert there anyway), nor from lg, where the column is on the page.
                          onClick={() => !menuOpen && !wide && setBrowseOpen(true)}
                        >
                          <Folders />
                        </IconButton>
                      </span>
                      <SaveViewButton
                        state={state}
                        saved={(v) => {
                          reloadViews();
                          update({ sv: v.id }, { replace: true });
                          notify({ text: t.viewSaved(v.name) });
                        }}
                      />
                    </>
                  }
                />
              </div>
            </main>
          </div>
        ) : (
          <main inert={pageInert} className="app-view app-page">
            {view === "metadata" && <MetadataPage metadata={metadata} userId={user.id} onChanged={onChanged} open={select} />}
            {view === "profile" && <ProfilePage user={user} />}
            {view === "log" && <LogPage admin={user.admin} />}
            {view === "users" && <UsersPage />}
          </main>
        )}
      </div>
      <Toast notice={notices.notice} close={notices.close} pause={notices.pause} resume={notices.resume} />
    </div>
  );
}
