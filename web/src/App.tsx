import { useEffect, useState } from "react";
import type { User } from "./api";
import { AuthGate } from "./Auth";
import { fetchHealth, type Health } from "./health";
import { t } from "./i18n";
import logo from "./logo.svg";
import { LogPage } from "./LogPage";
import { ProfilePage } from "./Profile";
import { ThemeToggle } from "./ThemeToggle";
import { UsersPage } from "./UsersPage";

const docsURL = "https://pklnx.github.io/paperless-go/";

type View = "home" | "profile" | "log" | "users";

/** The app: the login gate, then the logged-in shell. */
export function App() {
  return <AuthGate>{(user, logout) => <Shell user={user} logout={logout} />}</AuthGate>;
}

function Shell({ user, logout }: { user: User; logout: () => void }) {
  const [view, setView] = useState<View>("home");
  const nav: { id: View; label: string }[] = [
    { id: "home", label: t.home },
    { id: "profile", label: t.profile },
    { id: "log", label: t.log },
    ...(user.admin ? [{ id: "users" as const, label: t.users }] : []),
  ];
  return (
    <div className="flex min-h-full flex-col">
      <header className="flex flex-wrap items-center gap-3 border-b border-line px-4 py-2">
        <img src={logo} alt="" className="size-7" />
        <h1 className="text-base font-semibold">{t.appName}</h1>
        <nav className="flex flex-1 gap-1">
          {nav.map((n) => (
            <button
              key={n.id}
              type="button"
              aria-current={view === n.id ? "page" : undefined}
              className={`rounded-md px-2.5 py-1 text-sm pointer-coarse:min-h-11 ${
                view === n.id ? "bg-accent-soft text-accent-soft-ink" : "text-ink-muted hover:bg-surface-hover hover:text-ink"
              }`}
              onClick={() => setView(n.id)}
            >
              {n.label}
            </button>
          ))}
        </nav>
        <span className="text-sm text-ink-muted">{user.name}</span>
        <button type="button" className="text-sm text-accent-ink hover:underline" onClick={logout}>
          {t.logOut}
        </button>
        <ThemeToggle />
      </header>
      <main className="flex-1">
        {view === "home" && <Home />}
        {view === "profile" && <ProfilePage user={user} />}
        {view === "log" && <LogPage admin={user.admin} />}
        {view === "users" && user.admin && <UsersPage />}
      </main>
    </div>
  );
}

/** The start page until documents exist: what this server is and whether it is healthy. */
function Home() {
  const [health, setHealth] = useState<Health>("checking");
  useEffect(() => {
    let live = true;
    void fetchHealth().then((h) => live && setHealth(h));
    return () => {
      live = false;
    };
  }, []);

  const status = { checking: t.statusChecking, ok: t.statusOk, down: t.statusDown }[health];
  const statusColor = { checking: "text-ink-muted", ok: "text-ok", down: "text-danger" }[health];

  return (
    <div className="mx-auto w-full max-w-xl px-4 py-10">
      <p className="text-lg">{t.tagline}</p>
      <section className="mt-8 rounded-lg border border-line bg-surface-sunken p-5">
        <h2 className="font-semibold">{t.buildingTitle}</h2>
        <p className="mt-2 text-sm text-ink-muted">{t.buildingText}</p>
        <p className="mt-4 text-sm">
          {t.serverStatus}: <span className={statusColor} role="status">{status}</span>
        </p>
        <p className="mt-4 text-sm">
          <a className="text-accent-ink hover:underline" href={docsURL} rel="noreferrer">
            {t.documentation}
          </a>
        </p>
      </section>
    </div>
  );
}
