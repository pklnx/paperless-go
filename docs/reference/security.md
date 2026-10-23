# Security

## What is protected how

| What | Protection |
|---|---|
| Web UI and API | Login with user name and password, plus a TOTP code for admins and everybody who turned it on (below), or with a passkey; scripts use API keys. Host check and same-origin check below. Published on `127.0.0.1` unless you change `WEB_BIND`; see [Network access](#network-access). |
| User passwords | Only stored as Argon2id hashes (64 MiB, 3 passes). |
| TOTP secrets, recovery codes | Secrets encrypted with `PAPERLESS_GO_SECRET_KEY`; recovery codes only as keyed hashes (see [Secret key](#secret-key)). |
| Passkeys | Only public keys are stored; the private key never leaves the user's device. |
| API keys | Only their SHA-256 and last 4 characters are stored; the key is shown once. A key reaches only the document and metadata endpoints its scopes allow (`read`, `upload`, `write`), with its owner's rights. See [API keys](#api-keys). |
| Documents | Each document has one owner; other users get `404`, admins see all. |
| The audit log | Logins, changes of users and logins, API keys and uploads are recorded; the database refuses changes and deletions. See [Audit log](#audit-log). |
| Database | Published on `127.0.0.1` only; password in `.env`. |
| Document files | Plain files in the data directory, readable for the container user (UID 65532). Use an encrypted disk. |
| Backups | `./pgo backup` writes the dump with mode `0600`. |

## Network access

Docker Compose publishes the web UI on `127.0.0.1`, so only this computer can
reach it. To use it from your home network or a VPN:

1. **Use HTTPS.** Without it, passwords, session cookies and API keys cross
   the network in plain text. Put a reverse proxy or `tailscale serve` in
   front.
2. **Allow the host name** you type in the browser in
   `PAPERLESS_GO_ALLOWED_HOSTS` (comma-separated, without port). Requests
   with other names are rejected with `host not allowed` and logged. The
   host of `PAPERLESS_GO_PUBLIC_URL` is allowed too.
3. **Publish the port** only as far as needed with `WEB_BIND`. With a proxy
   or `tailscale serve` on the same computer, keep the default `127.0.0.1`.

Do not forward the port from the internet to the web UI. To publish the
site on the internet, put a reverse proxy in front and follow [Running on
the internet](../guide/internet).

### HTTPS behind a proxy

The server itself speaks plain HTTP; the proxy or `tailscale serve`
terminates TLS. The server learns that the browser used HTTPS from the
proxy's `X-Forwarded-Proto: https` and then sets the session cookie as
`__Host-paperless_go_session` with `Secure`. The header counts only from a
peer the server trusts: a loopback address, or an address in
`PAPERLESS_GO_TRUSTED_PROXIES`. From anybody else it is ignored, so a
client cannot choose how the cookie is set.

In Docker Compose, requests do not come from loopback even when the proxy
runs on the same computer: Docker's port publishing hands them to the
container from its network's gateway. Until that address is a trusted
proxy, the login works over HTTPS, but the cookie lacks `Secure` and the
`__Host-` prefix, so set `PAPERLESS_GO_TRUSTED_PROXIES` as below.

Only the cookie of the request's scheme logs in: over HTTPS the `__Host-`
one, over plain HTTP (which includes a proxy that does not count) the
plain one. A plain cookie can be planted by anyone who reaches the browser
over plain HTTP or from another host name of the domain; over HTTPS it
must not log the browser into someone else's session. A `__Host-` cookie
on a plain request is one from a time the header counted; the server does
not set that name there, so it does not count either, and the next login
expires it. A login and a logout end the sessions of both cookies the
browser sends. So setting, removing or changing
`PAPERLESS_GO_TRUSTED_PROXIES` later, in a way that changes whether
requests count as HTTPS, means every user logs in once more.

### Trusted proxies

Behind a proxy, the server sees the proxy's address for every client: the
login limits count all clients as one and the audit log shows the proxy.
`PAPERLESS_GO_TRUSTED_PROXIES` names the proxies (CIDR networks or
addresses, comma-separated) whose `X-Forwarded-For` names the client and
whose `X-Forwarded-Proto` is believed. The server takes the last address in
`X-Forwarded-For` that is not a trusted proxy; a missing or malformed header
means the peer.

When a request with an allowed host name carries `X-Forwarded-For` from a
peer that is not a trusted proxy (loopback included), the server logs a
warning with the peer's address, once per start; the header's own value is
never logged. A request with a host name that is not allowed is refused
before that, so it cannot use up the warning.

To find the address, log in through the proxy before setting it and look at
the client address of that login on the **Log** page: that is the proxy as
the server sees it (in Docker Compose usually the gateway of the Compose
network, an address in `172.16.0.0/12`). List that address or its network,
and only if all access goes through the proxy: whoever can connect from a
listed address can claim any client address and HTTPS.

### Tailscale

`tailscale serve` provides HTTPS with a certificate for the machine's name in
your tailnet and forwards to the local port. In `.env`:

```sh
PAPERLESS_GO_ALLOWED_HOSTS=localhost,127.0.0.1,mac.example-tailnet.ts.net
PAPERLESS_GO_PUBLIC_URL=https://mac.example-tailnet.ts.net
```

Then:

```sh
docker compose up -d web
tailscale serve --bg 8080
```

Open `https://mac.example-tailnet.ts.net` from any device in your tailnet,
and set `PAPERLESS_GO_TRUSTED_PROXIES` as described
[above](#trusted-proxies) so the session cookie is `Secure`.

### Reverse proxy in the home network

For example [Caddy](https://caddyserver.com) on the same computer, with a
name that resolves in your network:

```text
docs.home.example {
    reverse_proxy 127.0.0.1:8080
}
```

With `PAPERLESS_GO_ALLOWED_HOSTS=localhost,127.0.0.1,docs.home.example`
and, for passkeys, `PAPERLESS_GO_PUBLIC_URL=https://docs.home.example`.
The proxy must pass the original `Host` header (Caddy does by default);
otherwise the server's same-origin check rejects changes. Caddy sends
`X-Forwarded-Proto` and `X-Forwarded-For`; set
`PAPERLESS_GO_TRUSTED_PROXIES` as described [above](#trusted-proxies) so the
cookie is `Secure` and the limits and the audit log see the clients. Large uploads need the proxy's
body limit at least as high as `PAPERLESS_GO_MAX_UPLOAD_SIZE`.

### Plain LAN access

`WEB_BIND=0.0.0.0` and the computer's LAN address or name in
`PAPERLESS_GO_ALLOWED_HOSTS` work without a proxy, but without HTTPS anyone
in the network who can read the traffic can take over a session or copy an
API key. Use this only in a network you fully trust. Browsers offer passkeys
only over HTTPS or on `localhost`, so they do not work this way.

## Login

- **Everything needs a login or an API key** except `/healthz`, the UI page
  `/` and its code under `/assets/`; none of them contain data. Which route
  needs what is set in one table (`internal/web/routes.go`); a route not
  listed there, or a new one without an access level, needs a session.
- **Users** are created by admins in the web UI or with `./pgo user add
  NAME` (`--admin` for admins). There is no self-registration.
- **Passwords** need at least 12 characters and are stored as Argon2id
  hashes (64 MiB, 3 passes). A wrong user name and a wrong password get the
  same answer and take the same time.
- **Failed logins are limited**: from the 5th failure within 15 minutes a
  user name is blocked for 1 minute, doubling up to 15 minutes; 20 failures
  from one address block it for 15 minutes. IPv6 clients count per `/64`
  (the block a host or network usually gets), IPv4 clients per address.
  The counters live in memory and reset when the server restarts. Behind a
  reverse proxy, set `PAPERLESS_GO_TRUSTED_PROXIES` so the limits count per
  client (see [Trusted proxies](#trusted-proxies)). Every refused login is
  logged ([below](#refused-logins-in-the-log)).
- **Slow clients.** A request has 10 seconds for its headers (at most
  64 KiB) and 30 seconds for its body; answers have 5 minutes. Uploads and
  document downloads extend both to an hour for themselves, so a large
  file over a slow line still works.
- **Sessions** end after 7 days without use and after 30 days at the latest.
  The cookie is `HttpOnly` and `SameSite=Strict`; over HTTPS (also behind a
  [trusted proxy](#https-behind-a-proxy) that sends `X-Forwarded-Proto:
  https`) it is `__Host-` prefixed and `Secure`, and only that cookie logs
  in; over plain HTTP only the plain one does. The database stores only a hash of the token. A password reset
  by an admin, a lock or removing the user ends all their sessions;
  changing your own password ends all your other sessions.
- **Generated passwords** (a new user or a reset in the web UI, `user
  reset-password`) are shown once and must be replaced at the first login.
- **Admins** manage users and see every user's documents; users see only
  their own: another user's document answers `404`, as a missing one does.
  An upload always belongs to whoever uploads it, also for an admin. The
  last admin who can log in cannot be demoted, locked or removed.

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
  2FA on, a current TOTP or recovery code (recorded as the entry's
  `factor`). Otherwise a taken-over session
  plus a phished password would skip TOTP for good. Wrong passwords and
  codes count towards the login limits and are logged as
  `profile.2fa_failed`. A user has at most 10 passkeys.
- **Ceremonies** (a registration or login in progress) last 5 minutes on
  the server clock, are used up by the first answer, also a wrong one, and
  only their token's hash is stored. Starting a login needs no session, so
  it is limited to 30 per minute and address, to 20 open logins per
  address (IPv6 per `/64`, as for the login limits), to 100 per IPv6 `/56`
  or IPv4 `/24`, to 250 per IPv6 `/48` and to 1000 open logins in total,
  so neither one client nor one site with many addresses can use up the
  total for everybody; failed passkey logins count towards the address
  limit of the login limits. Concurrent logins with one passkey are
  serialized, so a replayed answer with the same sign count fails.
  Known, accepted limit of a total cap: the 1000 can be filled from four
  IPv6 `/48`s, or from about 50 IPv4 addresses spread over at least ten
  `/24`s. Passkey login then answers `429` for everybody for up to 5
  minutes, while password login keeps working.
- **The browser's open login.** The login page starts an autofill login
  each time it loads. The server keeps its token in a cookie
  (`paperless_go_passkey_login`, `__Host-` over HTTPS; `HttpOnly`,
  `SameSite=Strict`, 5 minutes): the next login from that browser
  replaces it, a correct password drops it (also when a second factor
  follows) and a passkey login expires the cookie. So reloading the page
  holds one open login, not one per load. The cookie only names the
  browser's own login, which needs the authenticator to finish; clients
  without it count in full against every cap. Two tabs share it: a login
  started in the second tab replaces the first tab's, whose autofill then
  answers "expired" once and starts a new one. The server reads up to 4
  cookies of each name, since a sibling subdomain can set the plain name
  for this host too and a cookie with a longer path is sent before the
  browser's own. A sibling host that sets more than 3 cookies of that name
  with a longer path can still shadow the browser's cookie while the plain
  (non-`__Host-`) name is in use; the only effect is that reloads count
  again until they expire.
- **Removing.** A user removes a passkey on the profile page; that ends
  their other sessions. An admin removes all of a user's passkeys on the
  Users page (or `./pgo user remove-passkeys NAME`), which logs the user
  out everywhere. A password reset by an admin (or `user reset-password`)
  removes the passkeys too unless the admin keeps them: after a
  compromise, a new password alone would not lock the attacker out.
  Rolling the migration back (`migrate down`) removes every passkey.

## API keys

- **What a key can do.** Scripts send `Authorization: Bearer pl_…`. A key
  has one or more of the scopes `read` (list, show and download documents),
  `upload` (add documents, `POST /api/documents` only; it reads nothing) and
  `write` (add, change and delete documents, tags, correspondents and
  document types; it includes `upload`), and nothing else: logins, the
  profile (password, 2FA, passkeys, API keys), user management and the audit
  log stay with the session and the command line, and answer `403` to a key.
  Every state-changing route a key may use needs `write`, except the upload,
  which needs `upload` or `write`. A key with the wrong scope gets `403`
  with the missing `scope`. A key for a phone or a scanner gets `upload`
  only: whoever finds it can add documents, but not read or change any.
  For a file that is already in the archive, a key without `read` gets
  only `200` `{"duplicate": true}`, not which document it is (id and
  title); sessions and keys with `read` get both.
- **Creating one** happens on the profile page and needs the password and,
  with 2FA on, a current TOTP or recovery code, like adding a passkey;
  wrong ones count towards the login limits. A key gets a name (unique
  among the user's keys that are not revoked), the scopes and an expiry
  (30, 90 or 365 days, up to 10 years, or none). A user has at most 20
  keys that are neither revoked nor expired.
- **The key** is 256 random bits (`pl_` and 52 base32 characters), shown
  once. The database stores its SHA-256 and the last 4 characters, so a
  database copy contains no usable keys. The lookup goes through a unique
  index on the hash: it compares hashes, never the key, so its timing says
  nothing about a key.
- **A key does no more than a session of its owner**: the owner's role
  (an admin's key sees every user's documents), lock and gates (a password
  to change, 2FA to set up) are checked on every request, and an upload
  belongs to the owner. Locking a user suspends their keys; unlocking
  restores them. A password reset by an admin (or `user reset-password`)
  revokes them unless the admin keeps them; removing the user deletes them.
  The user's own password change keeps them, so scripts do not break on a
  routine change; the profile lists them for revocation.
- **Revoking.** Users revoke their keys on the profile page, admins any
  key on the Users page; `./pgo user revoke-api-keys NAME` revokes all of a
  user's keys. Revoked and expired keys answer `401` and stay listed for
  30 days. `./pgo user api-keys NAME` lists a user's keys.
- **Limits.** Each key may make 600 requests per minute; more than 30
  refused keys per client address and minute answer `429` for that client,
  without touching the password login limits. Keys never appear in logs
  or in the audit log. A refused key that exists is recorded each time as
  `auth.login_failed` with `method: api_key`, its reason (`revoked`,
  `expired`, `locked`), its name and owner. An unknown key (`invalid`) is
  recorded only once per client address (IPv6 per `/64`) and minute, so
  that guessing without a login cannot grow the append-only log without
  bound; every refused key is logged ([below](#refused-logins-in-the-log)).
- **No cookie with a key.** A request with a key is decided by the key
  alone: the session cookie is never read, also when the key is invalid.
  A key does not log in or out (`403`).

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
| Failed logins | The reason (`wrong_password`, `unknown_user`, `locked`, `second_factor`, `unknown_passkey`, `passkey_invalid`; for an API key `invalid`, `revoked`, `expired`, `locked` with the key's name) and the user, if known. For an unknown user the tried name is kept only if it is a valid user name, so a password typed into the name field is not stored. An unknown API key is recorded once per client address and minute. Attempts the login limit refuses are not recorded. |
| User changes | Adding, removing, locking, unlocking, promoting and demoting users, password resets and passwords set on the command line: who did it and to whom. |
| Own login changes | Who changed their password, turned 2FA on or off, created new recovery codes, or added or removed a passkey (with its name), and which factor confirmed it. |
| Passkey removals | Who removed all of a user's passkeys, in the web UI or with `user remove-passkeys`, and how many; a password reset records how many passkeys it removed. |
| 2FA resets | Who reset whose two-factor login, in the web UI or with `user reset-2fa`. |
| API keys | Who created a key (name, scopes, expiry) and who revoked which key of whom; `user revoke-api-keys` and password resets record how many keys they revoked. |
| Requests with a key | Uploads and searches made with a key are recorded with the actor `api_key` and the key's name. Other reads are not recorded. |
| Searches | The first page of each typed search, with the search text and the filters (`document.search`); sidebar filters alone are not recorded. |
| Document edits | Who changed which document, with the names of the changed fields and never their values (`document.update`); a change of several documents adds one summary entry (`document.bulk_update`). |
| Uploads | Who uploaded which document. |
| Command line | Every command that changes a user, and `audit export`, with the command, the operating system user and `PAPERLESS_GO_OPERATOR` (`./pgo` sets the host user; not verified). |

Passwords, generated or typed, TOTP secrets and codes and API keys are
never recorded. A change and its entry
are written in one transaction: if the entry cannot be written, the change
is not made. The log is append-only: the database refuses to change or
delete entries, also for the database owner unless it is a superuser.
Removing a user keeps their entries under the name they had. Users see their
own entries on the Log page, and the changes an admin or the command line
made to their account, without the admin's address, browser, API key or
the command's operator; admins see everyone's and can export them with
`audit export`. Entries are kept for good; there is no retention yet.

### Refused logins in the log

Every refused credential writes one Info line to the server log, the same
for every method, so that tools like fail2ban can match it:

```text
level=INFO msg="login refused" method=password reason=wrong_password addr=192.0.2.7 user=alice
```

- `method`: `password`, `2fa`, `passkey` or `api_key`.
- `reason`: as in the audit log (`wrong_password`, `unknown_user`,
  `locked`, `second_factor`, `unknown_passkey`, `passkey_invalid`,
  `invalid`, `revoked`, `expired`), or `rate_limited` for an attempt a
  limit refused (`429`).
- `addr`: the client address, behind a [trusted proxy](#trusted-proxies)
  the client's own.
- `user`: the user name, where it is known; a tried name that is not a
  valid user name is logged as `(invalid name)`.

A limit's refusals are logged once per block, or once per window of the
limit, and client, not once per request, so a blocked client cannot fill
the log. Passwords, codes and keys are never logged.

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

A website can send requests to the server (but not read the answers).
State-changing requests (`POST`, `PUT`, `PATCH`, `DELETE`) must have an
`Origin` header matching the server (or `Sec-Fetch-Site: same-origin`) and a
JSON content type. Plain HTML forms and simple cross-site requests can do
neither.

The upload (`POST /api/documents`) is the one route that takes a file
instead of JSON: the raw file with the type `application/pdf`,
`image/jpeg`, `image/png`, `image/heic`, `image/heif` or
`application/octet-stream`, and its name in an `X-File-Name` header. None of
these types (nor the header) is allowed in a simple cross-site request, so
another site cannot send them without the browser asking the server first,
which it refuses. Forms (`multipart/form-data`,
`application/x-www-form-urlencoded`) and `text/plain` answer `415` there
too. The server decides the file type from the content, never from the
declared type or the name.

Requests with an API key (`Authorization: Bearer pl_…`) are exempt from the
`Origin` and content type checks. The checks protect the cookie, which the
browser adds on its own to a request another site starts. A key request
carries its credential in the header itself and never reads the cookie,
also when the key is invalid, so a cross-site request with a made-up key
and the visitor's cookie gets `401`. A page on another site cannot add an
`Authorization` header without a CORS preflight, which the server never
answers with permission, and a page that already knows a key gains nothing
from a visitor's browser. Other `Authorization` headers (`Basic` from a
proxy, a `Bearer` token without `pl_`) are not key requests and get every
check. The `Host` check applies to every request.

### Headers

The UI is served with a strict Content Security Policy (only same-origin
scripts, styles and requests, no framing), `X-Content-Type-Options: nosniff`,
`Referrer-Policy: no-referrer`, `Cross-Origin-Opener-Policy: same-origin`,
`Cross-Origin-Resource-Policy: same-origin` and `Permissions-Policy:
camera=(), microphone=(), geolocation=()`.

Over HTTPS (TLS, or `X-Forwarded-Proto: https` from loopback or a
[trusted proxy](#trusted-proxies)) every answer carries
`Strict-Transport-Security: max-age=31536000`, so the browser uses HTTPS
for the host for a year. It has no `includeSubDomains` or `preload`, since
other services may share the domain. Over plain HTTP it is not sent.
HSTS covers the host name on all its ports for that year, so do not serve
the same name over plain HTTP later (for example for access in the LAN):
browsers that saw HSTS refuse it until the year is over.

Document files (`GET /api/documents/{id}/file`) are always sent as a
download (`Content-Disposition: attachment`) with `nosniff` and
`Content-Security-Policy: sandbox`, so a file is never run as a page of
the server, even when a browser shows it. They are sent with their type only if
it is one the server imports (PDF, JPEG, PNG, HEIC/HEIF), otherwise as
`application/octet-stream`, and the offered file name gets the extension
of that type, not the one the file was uploaded with.

## Login checks

The server makes no network connections on a user's behalf: no URL or host
given in a request is ever fetched, and OCR runs locally. Every logged-in
user and every key with `upload` or `write` can upload files that the server's OCR
tools (OCRmyPDF, Tesseract, Ghostscript, libheif) parse, with the container
user's rights (UID 65532; Compose adds `no-new-privileges`), so only give
logins and keys to people and scripts you trust.

## Reporting a vulnerability

See [`SECURITY.md`](https://github.com/pklnx/paperless-go/blob/main/SECURITY.md).
