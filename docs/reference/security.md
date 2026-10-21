# Security

::: warning No login yet
This version has no user accounts. Anyone who can reach the web UI can use
it. Keep the default: the web UI is published on `127.0.0.1` only. Login,
passkeys, TOTP and API keys follow (see the
[roadmap](../development/roadmap)).
:::

## What is protected how

| What | Protection |
|---|---|
| Web UI and API | Reachable from this computer only (`WEB_BIND=127.0.0.1`). Host check and same-origin check below. |
| Database | Published on `127.0.0.1` only; password in `.env`. |
| Document files | Plain files in the data directory, readable for the container user (UID 65532). Use an encrypted disk. |
| Backups | `./pgo backup` writes the dump with mode `0600`. |

## Network access

Docker Compose publishes the web UI on `127.0.0.1`, so only this computer can
reach it. Until user accounts exist, keep it that way. With accounts, the
same rules as in mail-archive will apply: HTTPS through `tailscale serve` or
a reverse proxy, every host name in `PAPERLESS_GO_ALLOWED_HOSTS`, and the
port published only as far as needed with `WEB_BIND`. Never forward the port
from the internet.

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
