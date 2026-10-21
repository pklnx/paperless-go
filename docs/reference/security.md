# Security

::: warning Keep it on this computer for now
Every page and API call except the health check and the UI code needs a
login, but there is no second factor (TOTP, passkeys) yet. Keep the default:
the web UI is published on `127.0.0.1` only. Two-factor login, passkeys and
API keys follow (see the [roadmap](../development/roadmap)).
:::

## What is protected how

| What | Protection |
|---|---|
| Web UI and API | Login with user name and password (below). Reachable from this computer only (`WEB_BIND=127.0.0.1`). Host check and same-origin check below. |
| Database | Published on `127.0.0.1` only; password in `.env`. |
| Document files | Plain files in the data directory, readable for the container user (UID 65532). Use an encrypted disk. |
| Backups | `./pgo backup` writes the dump with mode `0600`. |

## Network access

Docker Compose publishes the web UI on `127.0.0.1`, so only this computer can
reach it. Until two-factor login exists, keep it that way. Then the same
rules as in mail-archive will apply: HTTPS through `tailscale serve` or
a reverse proxy, every host name in `PAPERLESS_GO_ALLOWED_HOSTS`, and the
port published only as far as needed with `WEB_BIND`. Never forward the port
from the internet.

## Login

- **Everything needs a login** except `/healthz`, the UI page `/` and its
  code under `/assets/`; none of them contain data. A new endpoint needs a
  session unless the route table says otherwise.
- **Passwords** need at least 12 characters and are stored as Argon2id
  hashes (64 MiB, 3 passes). A wrong user name and a wrong password get the
  same answer and take the same time.
- **Failed logins are limited**: from the 5th failure within 15 minutes a
  user name is blocked for 1 minute, doubling up to 15 minutes; 20 failures
  from one address block it for 15 minutes. Behind a reverse proxy, set
  `PAPERLESS_GO_TRUSTED_PROXIES` so the limits count per client.
- **Sessions** end after 7 days without use and after 30 days at the latest.
  The cookie is `HttpOnly` and `SameSite=Strict`; over HTTPS (also behind a
  proxy that sends `X-Forwarded-Proto: https`) it is `__Host-` prefixed and
  `Secure`. The database stores only a hash of the token. A password reset
  by an admin, a lock or removing the user ends all their sessions;
  changing your own password ends all your other sessions.
- **Generated passwords** (a new user or a reset in the web UI, `user
  reset-password`) are shown once and must be replaced at the first login.
- **Admins** manage users and see every user's documents; users see only
  their own. The last admin who can log in cannot be demoted, locked or
  removed.

## Audit log

The [audit log](../guide/audit-log) records:

| What | Recorded |
|---|---|
| Logins and logouts | The user, client address, user agent, method (`password`). |
| Failed logins | The reason (`wrong_password`, `unknown_user`, `locked`) and the user, if known. For an unknown user the tried name is kept only if it is a valid user name, so a password typed into the name field is not stored. Attempts the login limit refuses are not recorded. |
| User changes | Adding, removing, locking, unlocking, promoting and demoting users, password resets and passwords set on the command line: who did it and to whom. |
| Own password changes | Who changed their password. |
| Command line | Every command that changes a user, and `audit export`, with the command, the operating system user and `PAPERLESS_GO_OPERATOR` (`./pgo` sets the host user; not verified). |

Passwords, generated or typed, are never recorded. A change and its entry
are written in one transaction: if the entry cannot be written, the change
is not made. The log is append-only: the database refuses to change or
delete entries, also for the database owner unless it is a superuser.
Removing a user keeps their entries under the name they had. Users see their
own entries on the Log page; admins see everyone's and can export them with
`audit export`. Entries are kept for good; there is no retention yet.

## Browser protections

Every website open in your browser could try to send requests to the server.
The server therefore adds:

### DNS rebinding

A malicious website can point its own domain at `127.0.0.1` and then read
responses from local servers. Such requests carry the attacker's domain in
the `Host` header. The server only answers requests whose `Host` is in
`PAPERLESS_GO_ALLOWED_HOSTS` (default `localhost`, `127.0.0.1`, `::1`) and
rejects everything else with `403`.

### Cross-site requests

A website can send requests to `localhost` (but not read the answers).
State-changing requests (`POST`, `PUT`, `PATCH`, `DELETE`) must have an
`Origin` header matching the server (or `Sec-Fetch-Site: same-origin`) and a
JSON content type. Plain HTML forms and simple cross-site requests can do
neither.

### Headers

The UI is served with a strict Content Security Policy (only same-origin
scripts, styles and requests, no framing), `X-Content-Type-Options: nosniff`,
`Referrer-Policy: no-referrer` and `Cross-Origin-Opener-Policy: same-origin`.

## Reporting a vulnerability

See [`SECURITY.md`](https://github.com/pklnx/paperless-go/blob/main/SECURITY.md).
