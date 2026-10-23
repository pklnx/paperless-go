// The test fixture every browser spec uses. Its guards fail a test on:
// - an uncaught error in a page,
// - a console error,
// - a Content Security Policy violation,
// - a request to any host but the demo server (the request is aborted).
// They keep the suite off the network and catch a pdf.js worker, cmaps or
// fonts loaded from elsewhere, CDN fonts and injected styles.
import { expect, test as base } from "@playwright/test";
import { origin } from "./demo";

export { expect };

/** Lets a test declare the error answers it provokes on purpose. */
export interface Guards {
  /** An answer with this status to a request for this path is expected. */
  allowHTTPError(path: string, status: number): void;
}

export const test = base.extend<{ guards: Guards }>({
  guards: [
    async ({ context }, use) => {
      const problems: string[] = [];
      const allowed: { path: string; status: number }[] = [];
      // The browser logs every 4xx/5xx answer as a console error.
      const expectedHTTPError = (url: string, text: string) =>
        allowed.some((a) => {
          try {
            const u = new URL(url);
            return u.origin === origin && u.pathname === a.path && text.includes(`status of ${a.status} `);
          } catch {
            return false;
          }
        });
      await context.route(
        (url) => url.origin !== origin,
        (route) => {
          problems.push(`request to another host: ${route.request().method()} ${route.request().url()}`);
          return route.abort("blockedbyclient");
        },
      );
      // The browser logs CSP violations too; the listener also catches the
      // ones of report-only policies and names the directive.
      await context.addInitScript(() => {
        document.addEventListener("securitypolicyviolation", (e) => {
          console.error(`CSP violation: ${e.violatedDirective} blocked ${e.blockedURI || "inline"} in ${e.sourceFile || e.documentURI}`);
        });
      });
      context.on("console", (msg) => {
        if (msg.type() !== "error") return;
        const { url } = msg.location();
        if (expectedHTTPError(url, msg.text())) return;
        problems.push(`console error: ${msg.text()} (${url})`);
      });
      context.on("weberror", (err) => problems.push(`page error: ${err.error().stack ?? err.error().message}`));
      await use({ allowHTTPError: (path, status) => allowed.push({ path, status }) });
      expect(problems, "guards: errors, CSP violations or foreign requests").toEqual([]);
    },
    { auto: true },
  ],
});
