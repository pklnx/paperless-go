import { useCallback, useEffect, useState, type MouseEvent } from "react";
import { Header, HeaderGlobalAction, HeaderGlobalBar, HeaderMenuButton, HeaderName } from "@carbon/react";
import { Logout } from "@carbon/icons-react";
import { documentsApi, metadataApi, savedViewsApi, type DocumentCounts, type Metadata, type SavedView, type User } from "./api";
import { AuthGate } from "./Auth";
import { isPlainClick } from "./clicks";
import { DocumentList } from "./DocumentList";
import { DocumentsUpload, WindowDrop, useQueue } from "./Documents";
import { fetchHealth, type Health } from "./health";
import { t } from "./i18n";
import { LogPage } from "./LogPage";
import { MetadataPage } from "./MetadataPage";
import { ProductMark } from "./ProductMark";
import { ProfilePage } from "./Profile";
import { SaveViewButton, SavedViewList } from "./SavedViews";
import { Sheet } from "./Sheet";
import { Sidebar, type Loaded } from "./Sidebar";
import { ThemeToggle } from "./ThemeToggle";
import { Toast, useNotice } from "./Toast";
import { useCollapsedTags } from "./tagTree";
import { UploadQueue } from "./uploads";
import { UsersPage } from "./UsersPage";
import { confirmLeave, documentQuery, filterPatch, useViewState, type Filter, type ViewState } from "./urlState";

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
  const [menuOpen, setMenuOpen] = useState(false);
  const closeMenu = useCallback(() => setMenuOpen(false), []);

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
  // keeps the search and replaces the filter.
  const select = (f: Filter | null) => {
    update({ ...filterPatch(f), ...(f === null ? { q: "" } : {}), view: "", doc: "", sv: "" });
    closeMenu();
  };
  const show = (view: string) => {
    update({ view, doc: "", sv: "" });
    closeMenu();
  };
  const openView = (patch: Partial<ViewState>) => {
    update(patch);
    closeMenu();
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

  const sidebar = (
    <Sidebar
      state={state}
      select={select}
      show={show}
      user={user}
      logout={leave}
      counts={counts}
      metadata={metadata}
      collapsed={collapsed}
      everyone={everyone}
      setEveryone={setEveryone}
      health={health}
      savedViews={<SavedViewList views={views.data} error={views.error} state={state} admin={user.admin} open={openView} reload={reloadViews} notify={notify} />}
    />
  );
  const view = state.view === "profile" || state.view === "log" || state.view === "metadata" || (state.view === "users" && user.admin) ? state.view : "";

  return (
    <div className="app-shell">
      <WindowDrop
        onFiles={(files) => {
          queue.add(files);
          // The uploads show on the documents' page, which the trash is not.
          if (view !== "" || state.trash === "1") update({ view: "", doc: "", trash: "" });
        }}
      />
      {/* Carbon's UI Shell header at every width (#150, part 1). */}
      <Header aria-label={t.appName}>
        {/* Opens today's navigation sheet below md; layout.css hides the
            button from md, where the sidebar is permanent (part 2 moves the
            whole shell to Carbon's lg). */}
        <HeaderMenuButton aria-label={t.openMenu} aria-expanded={menuOpen} isActive={menuOpen} onClick={() => setMenuOpen((open) => !open)} />
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
          <span className="app-header__user" aria-hidden>
            {user.name}
          </span>
          <ThemeToggle header />
          <HeaderGlobalAction aria-label={t.logOut} tooltipAlignment="end" onClick={leave}>
            <Logout size={20} />
          </HeaderGlobalAction>
        </HeaderGlobalBar>
      </Header>
      <div className="app-shell__row">
        <aside className="hidden w-64 shrink-0 flex-col overflow-y-auto border-r border-line bg-surface-sunken md:flex" data-testid="sidebar">
          {sidebar}
        </aside>
        <main className="min-w-0 flex-1 overflow-y-auto">
          {view === "" && (
            <div className="legacy mx-auto flex w-full max-w-4xl flex-col gap-6 px-4 py-6">
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
                  <SaveViewButton
                    state={state}
                    saved={(v) => {
                      reloadViews();
                      update({ sv: v.id }, { replace: true });
                      notify({ text: t.viewSaved(v.name) });
                    }}
                  />
                }
              />
            </div>
          )}
          {view === "metadata" && <MetadataPage metadata={metadata} userId={user.id} onChanged={onChanged} open={select} />}
          {view === "profile" && <ProfilePage user={user} />}
          {view === "log" && <LogPage admin={user.admin} />}
          {view === "users" && <UsersPage />}
        </main>
      </div>
      <Toast notice={notices.notice} close={notices.close} pause={notices.pause} resume={notices.resume} />
      <Sheet open={menuOpen} onClose={closeMenu} title={t.navigation}>
        {sidebar}
      </Sheet>
    </div>
  );
}
