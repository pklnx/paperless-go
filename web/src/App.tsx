import { useEffect, useState } from "react";
import { fetchHealth, type Health } from "./health";
import { t } from "./i18n";
import logo from "./logo.svg";
import { ThemeToggle } from "./ThemeToggle";

const docsURL = "https://pklnx.github.io/paperless-go/";

/** The start page until documents and login exist: what this server is and whether it is healthy. */
export function App() {
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
    <div className="flex min-h-full flex-col">
      <header className="flex items-center gap-3 border-b border-line px-4 py-2">
        <img src={logo} alt="" className="size-7" />
        <h1 className="flex-1 text-base font-semibold">{t.appName}</h1>
        <ThemeToggle />
      </header>
      <main className="mx-auto w-full max-w-xl flex-1 px-4 py-10">
        <p className="text-lg">{t.tagline}</p>
        <section className="mt-8 rounded-lg border border-line bg-surface-sunken p-5">
          <h2 className="font-semibold">{t.setupTitle}</h2>
          <p className="mt-2 text-sm text-ink-muted">{t.setupText}</p>
          <p className="mt-4 text-sm">
            {t.serverStatus}: <span className={statusColor} role="status">{status}</span>
          </p>
          <p className="mt-4 text-sm">
            <a className="text-accent-ink hover:underline" href={docsURL} rel="noreferrer">
              {t.documentation}
            </a>
          </p>
        </section>
      </main>
    </div>
  );
}
