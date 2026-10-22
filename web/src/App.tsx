import { useCallback, useEffect, useState } from "react";
import { documentsApi, metadataApi, type DocumentCounts, type Metadata, type User } from "./api";
import { AuthGate } from "./Auth";
import { DocumentList } from "./DocumentList";
import { DocumentsUpload, WindowDrop, useQueue } from "./Documents";
import { fetchHealth, type Health } from "./health";
import { t } from "./i18n";
import { MenuIcon, iconButton } from "./icons";
import logo from "./logo.svg";
import { LogPage } from "./LogPage";
import { ProfilePage } from "./Profile";
import { Sheet } from "./Sheet";
import { Sidebar, type Loaded } from "./Sidebar";
import { useCollapsedTags } from "./tagTree";
import { UploadQueue } from "./uploads";
import { UsersPage } from "./UsersPage";
import { documentQuery, filterPatch, useViewState, type Filter } from "./urlState";

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
  }, [ownOnly, imported]);
  const collapsed = useCollapsedTags(user.name, metadata.data?.tags ?? null);

  // "All documents" lists everything: no filter, no search. Another entry
  // keeps the search and replaces the filter.
  const select = (f: Filter | null) => {
    update({ ...filterPatch(f), ...(f === null ? { q: "" } : {}), view: "", doc: "" });
    closeMenu();
  };
  const show = (view: string) => {
    update({ view, doc: "" });
    closeMenu();
  };
  const setEveryone = (on: boolean) => update({ all: on ? "1" : "" });
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
      logout={logout}
      counts={counts}
      metadata={metadata}
      collapsed={collapsed}
      everyone={everyone}
      setEveryone={setEveryone}
      health={health}
    />
  );
  const view = state.view === "profile" || state.view === "log" || (state.view === "users" && user.admin) ? state.view : "";

  return (
    <div className="flex h-full">
      <WindowDrop
        onFiles={(files) => {
          queue.add(files);
          if (view !== "") update({ view: "", doc: "" });
        }}
      />
      <aside className="hidden w-64 shrink-0 flex-col overflow-y-auto border-r border-line bg-surface-sunken md:flex" data-testid="sidebar">
        <div className="flex items-center gap-2 px-5 pt-4">
          <img src={logo} alt="" className="size-7" />
          <h1 className="text-base font-semibold">{t.appName}</h1>
        </div>
        {sidebar}
      </aside>
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex items-center gap-2 border-b border-line px-2 py-1.5 md:hidden">
          <button type="button" className={iconButton} aria-label={t.openMenu} title={t.openMenu} aria-expanded={menuOpen} onClick={() => setMenuOpen(true)}>
            <MenuIcon />
          </button>
          <img src={logo} alt="" className="size-6" />
          <span className="text-base font-semibold">{t.appName}</span>
        </header>
        <main className="flex-1 overflow-y-auto">
          {view === "" && (
            <div className="mx-auto flex w-full max-w-4xl flex-col gap-6 px-4 py-6">
              <DocumentsUpload queue={queue} maxSize={maxSize} />
              <DocumentList state={state} update={update} query={query} user={user} metadata={metadata.data} everyone={everyone} reload={imported} onMaxUploadSize={onMaxUploadSize} />
            </div>
          )}
          {view === "profile" && <ProfilePage user={user} />}
          {view === "log" && <LogPage admin={user.admin} />}
          {view === "users" && <UsersPage />}
        </main>
      </div>
      <Sheet open={menuOpen} onClose={closeMenu} title={t.navigation}>
        {sidebar}
      </Sheet>
    </div>
  );
}
