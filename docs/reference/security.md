# Security

::: warning Keep it on this computer for now
Every page and API call except the health check and the UI code needs a
login, and admins log in with a password and a TOTP code, or with a
passkey. API keys and the review of these docs follow (see the
[roadmap](../development/roadmap)); until then keep the default: the web UI
is published on `127.0.0.1` only.
:::

## What is protected how

| What | Protection |
|---|---|
| Web UI and API | Login with user name and password, plus a TOTP code for admins and everybody who turned it on (below), or with a passkey. Reachable from this computer only (`WEB_BIND=127.0.0.1`). Host check and same-origin check below. |
| TOTP secrets, recovery codes | Secrets encrypted with `PAPERLESS_GO_SECRET_KEY`; recovery codes only as keyed hashes (see [Secret key](#secret-key)). |
| Passkeys | Only public keys are stored; the private key never leaves the user's device. |
| Database | Published on `127.0.0.1` only; password in `.env`. |
| Document files | Plain files in the data directory, readable for the container user (UID 65532). Use an encrypted disk. |
| Backups | `./pgo backup` writes the dump with mode `0600`. |

## Network access

Docker Compose publishes the web UI on `127.0.0.1`, so only this computer can
reach it. Until API keys and the security review are done, keep it that
way. Then the same
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

## Two-factor login

- **Admins always need it**; `PAPERLESS_GO_REQUIRE_2FA=true` requires it for
  everybody. Other users can turn it on in their profile. TOTP follows RFC
  6238 (SHA-1, 6 digits, 30 seconds), so every authenticator app works.
- **Setting it up.** A user who needs 2FA but has none yet, also an admin
  from before this version, logs in with the password and can then only set
  it up: scan the QR code (or type the key), confirm with a code and save the
  ten recovery codes. Until then every other request answers `403`. A
  generated password is replaced first, then 2FA is set up.
- **Logging in.** After a correct password the server answers with a
  challenge, not a session; the session starts only after a valid code. The
  challenge is valid for 5 minutes and only until the user's 2FA changes.
  Each code's time step is accepted once, and never an older one than the
  last accepted (no replay). Wrong codes count for the same failure limits
  as wrong passwords; a correct password does not reset them.
- **Recovery codes.** Ten codes of 16 characters, each usable once in place
  of a TOTP code, also typed in capitals or without dashes. They are shown
  once (with a PDF and a text file made in the browser) and stored only as
  HMAC-SHA-256 hashes. New codes need a current code and replace the old
  ones.
- **Turning it off** needs the password and a current code, and ends the
  user's other sessions. Admins (and everybody when it is required) cannot
  turn it off.
- **Lost authenticator and codes.** Another admin resets it on the Users
  page, or anyone with shell access runs `./pgo user reset-2fa NAME`; this
  works for the last admin too. It logs the user out everywhere.
- **Until 2FA is set up**, after a reset or for an admin from before 2FA
  existed, the password alone protects the account, and whoever logs in
  first registers their authenticator. Such a user should log in and set up
  2FA right away. Every reset is in the audit log.
- **Refused codes and passwords** when a logged-in user changes their own
  2FA count towards the login limits and are logged as `profile.2fa_failed`.

## Passkeys

- **Off without `PAPERLESS_GO_PUBLIC_URL`.** Passkeys (WebAuthn) are bound
  to a host name, the relying party ID: the host of
  `PAPERLESS_GO_PUBLIC_URL`, which must be `https://` (or
  `http://localhost`). They work only when the browser shows exactly that
  origin; the server checks origin and relying party ID of every answer.
  Without the URL the passkey endpoints answer `503`.
- **A passkey login replaces password and second factor.** The server
  requires user verification (Face ID, Touch ID, Windows Hello, a PIN), a
  discoverable credential (no user name typed) and a sign count that goes
  up, so a copied authenticator is refused. It ends in the same state as a
  password login: a locked user is refused (only after a valid passkey, as
  with a password), a generated password must still be replaced, and an
  admin whose 2FA is not set up (after a reset) still gets only the 2FA
  setup.
- **Adding one needs the same as a login**: the current password and, with
  2FA on, a current TOTP or recovery code. Otherwise a taken-over session
  plus a phished password would skip TOTP for good. Wrong passwords and
  codes count towards the login limits and are logged as
  `profile.2fa_failed`. A user has at most 10 passkeys.
- **Ceremonies** (a registration or login in progress) last 5 minutes on
  the server clock, are used up by the first answer, also a wrong one, and
  only their token's hash is stored. Starting a login needs no session, so
  it is limited to 30 per minute and address, and to 1000 open logins in
  total; failed passkey logins count towards the address limit of the
  login limits. Concurrent logins with one passkey are serialized, so a
  replayed answer with the same sign count fails.
- **Removing.** A user removes a passkey on the profile page; that ends
  their other sessions. An admin removes all of a user's passkeys on the
  Users page (or `./pgo user remove-passkeys NAME`), which logs the user
  out everywhere. A password reset by an admin (or `user reset-password`)
  removes the passkeys too unless the admin keeps them: after a
  compromise, a new password alone would not lock the attacker out.
  Rolling the migration back (`migrate down`) removes every passkey.

## Secret key

`PAPERLESS_GO_SECRET_KEY` (32 random bytes, base64; `./pgo keygen`) encrypts
the TOTP secrets with AES-256-GCM, each bound to its user's ID, and keys the
HMAC of the recovery codes. A copy of the database alone therefore reveals
no secret and cannot test guessed recovery codes. `serve` refuses to start
without a valid key.

Keep the key with your backups, but apart from the database dump if you
can: dump and key together give the TOTP secrets. Never change it: with
another key, no TOTP or recovery code works, and every user who uses
two-factor login needs `user reset-2fa`.

## Audit log

The [audit log](../guide/audit-log) records:

| What | Recorded |
|---|---|
| Logins and logouts | The user, client address, user agent, method (`password`, `passkey` with the passkey's name, or `2fa` with the factor `totp` or `recovery_code` and, for a recovery code, how many are left). |
| Failed logins | The reason (`wrong_password`, `unknown_user`, `locked`, `second_factor`, `unknown_passkey`, `passkey_invalid`) and the user, if known. For an unknown user the tried name is kept only if it is a valid user name, so a password typed into the name field is not stored. Attempts the login limit refuses are not recorded. |
| User changes | Adding, removing, locking, unlocking, promoting and demoting users, password resets and passwords set on the command line: who did it and to whom. |
| Own login changes | Who changed their password, turned 2FA on or off, created new recovery codes, or added or removed a passkey (with its name), and which factor confirmed it. |
| Passkey removals | Who removed all of a user's passkeys, in the web UI or with `user remove-passkeys`, and how many; a password reset records how many passkeys it removed. |
| 2FA resets | Who reset whose two-factor login, in the web UI or with `user reset-2fa`. |
| Command line | Every command that changes a user, and `audit export`, with the command, the operating system user and `PAPERLESS_GO_OPERATOR` (`./pgo` sets the host user; not verified). |

Passwords, generated or typed, TOTP secrets and codes are never recorded. A change and its entry
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
