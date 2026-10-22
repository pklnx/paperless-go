# Running on the internet

paperless-go can sit behind a reverse proxy on a public address, with the
protections described in [Security](../reference/security). This guide is the
checklist and the proxy configurations for that. The server itself speaks
plain HTTP; the proxy provides HTTPS. Never publish the server's own port to
the internet.

::: warning Your documents are the target
Everything in the archive is private. Only run it on the internet if you
keep the machine updated and read the [known limits](#known-limits). A VPN
or `tailscale serve` (see [Network access](../reference/security#network-access))
needs none of this and is the safer choice when everybody who needs access
can use it.
:::

## Checklist

Each line links to its section. Names like `example.com` stand for yours.

1. [HTTPS from a reverse proxy](#reverse-proxy), with the proxy's body limit
   and timeouts set for uploads.
2. `PAPERLESS_GO_ALLOWED_HOSTS` or `PAPERLESS_GO_PUBLIC_URL` with the public
   name ([Host name and URL](#host-name-and-url)).
3. `PAPERLESS_GO_TRUSTED_PROXIES` ([Trusted proxies](#trusted-proxies)).
4. `PAPERLESS_GO_REQUIRE_2FA=true` ([Logins on the internet](#logins-on-the-internet)).
5. Passkeys on, with `PAPERLESS_GO_PUBLIC_URL`, and users adding one.
6. API keys for phones and scripts with only the scope they need, `upload`
   for an upload-only client, and an expiry ([API keys](#api-keys-for-phones-and-scripts)).
7. The database port and the consume folder not exposed
   ([What must never be exposed](#what-must-never-be-exposed)).
8. Backups off the server, the secret key apart from the dump
   ([Backups](#backups)).
9. Updates by hand ([Updates](#updates)).
10. The proxy's access log on ([Logs](#logs-and-the-audit-log)).
11. A look at the Log page after the first day.

## What is reachable without a login

| Path | What it gives | Limits |
|---|---|---|
| `/` and `/assets/` | The web UI's page and code; no data. | none |
| `/healthz` | `ok`, or `503` when the database does not answer. It pings the database (5 seconds at most). | none; see [Health check](#health-check) |
| `POST /api/session`, `POST /api/session/2fa` | Password login and its second step. | [Login limits](#logins-on-the-internet) |
| `POST /api/session/passkey/begin` and `/finish` | Passkey login (needs `PAPERLESS_GO_PUBLIC_URL`, else `503`). | 30 starts per minute and address, and caps on open logins |
| `GET`, `DELETE /api/session` | The state of your session; logging out. | none |
| Anything else | `401`; `403` for a host that is not allowed. | API keys: 30 refused keys per minute and address |

Every other route needs a session or an API key with the right scope. The
details are in [Security](../reference/security#login).

## Host name and URL

Set the public name in `.env`:

```sh
PAPERLESS_GO_ALLOWED_HOSTS=localhost,127.0.0.1,docs.example.com
PAPERLESS_GO_PUBLIC_URL=https://docs.example.com
```

Requests with another `Host` get `403`, which also stops a request that
reaches the server by its IP address. The proxy must pass the original
`Host` header; otherwise the same-origin check rejects every change.
`PAPERLESS_GO_PUBLIC_URL` turns passkeys on and allows its host too.

## Reverse proxy

Whatever proxy you use needs the same things:

- It terminates HTTPS and forwards to the server over plain HTTP.
- It passes the original `Host` header.
- It sets `X-Forwarded-For` to the client's address (replacing, not
  extending, what the client sent) and `X-Forwarded-Proto: https`.
- Its request body limit is at least `PAPERLESS_GO_MAX_UPLOAD_SIZE`
  (default 100 MiB): an upload is the raw file as the request body.
- Its timeouts allow an upload or a download over a slow line, up to an
  hour (the server's own limit for these two).
- Its access log is on.

The server sends `Strict-Transport-Security: max-age=31536000` itself over
HTTPS. Do not set it a second time in the proxy, or give it the same value.

### Caddy on the host

Caddy obtains and renews the certificate itself (ports 80 and 443 must
reach it and `docs.example.com` must point at the machine). With the server
on `127.0.0.1:8080`, the default of `WEB_BIND` and `WEB_PORT`:

```text
docs.example.com {
    request_body {
        max_size 100MiB
    }

    log {
        output file /var/log/caddy/paperless-go.log
    }

    reverse_proxy 127.0.0.1:8080
}
```

`max_size` must be at least `PAPERLESS_GO_MAX_UPLOAD_SIZE`. Caddy passes the
`Host` header unchanged, sets `X-Forwarded-Proto` and, since it does not
trust a forwarded address by default (Caddy 2.6 or newer), sets
`X-Forwarded-For` to the client itself. Caddy sets no read or write
timeouts by default, so transfers of up to an hour work.

With the server in Docker Compose, it does not see Caddy as loopback but
as the Compose network's gateway. Until `PAPERLESS_GO_TRUSTED_PROXIES`
lists that address, the login limits and the audit log see Caddy instead of
the clients; see [Trusted proxies](#trusted-proxies).

### Caddy in Docker Compose

To run Caddy next to the server, add a `docker-compose.override.yml` beside
`docker-compose.yml` (Compose reads it automatically, and an upgrade keeps
it). Keep `WEB_BIND` at its default, so the web server is not published on
the network; Caddy reaches it as `web:8080` over the Compose network:

```yaml
services:
  caddy:
    image: caddy:2 # pin an exact version
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
      - "443:443/udp"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy_data:/data
      - caddy_config:/config
    depends_on:
      - web

volumes:
  caddy_data:
  caddy_config:
```

In `./Caddyfile`, use the same site block as above with
`reverse_proxy web:8080` and without the `log` file (or send the log to
`output stdout` and read it with `docker compose logs caddy`). The server
then sees Caddy at an address of the Compose network: set
`PAPERLESS_GO_TRUSTED_PROXIES` as described in [Trusted
proxies](#trusted-proxies). This snippet is not run by the project's tests:
check it on your machine, including that the certificate is issued and that
the client address in the Log page is yours.

### nginx

```nginx
server {
    listen 443 ssl;
    http2 on;
    server_name docs.example.com;

    ssl_certificate     /etc/letsencrypt/live/docs.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/docs.example.com/privkey.pem;

    access_log /var/log/nginx/paperless-go.log;

    # At least PAPERLESS_GO_MAX_UPLOAD_SIZE.
    client_max_body_size 100m;
    # Stream uploads to the server instead of writing them to disk first.
    proxy_request_buffering off;
    # An upload or a download may take up to an hour.
    proxy_read_timeout 3600s;
    proxy_send_timeout 3600s;
    client_body_timeout 3600s;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host              $http_host;
        proxy_set_header X-Forwarded-For   $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}

server {
    listen 80;
    server_name docs.example.com;
    return 301 https://$host$request_uri;
}
```

`$remote_addr` replaces any `X-Forwarded-For` the client sent. Issue and
renew the certificate with a tool of your choice (certbot, acme.sh);
the paths above are certbot's.

### Traefik and others

Traefik, HAProxy and cloud load balancers work with the same four
requirements: the `Host` header passed on, `X-Forwarded-For` and
`X-Forwarded-Proto: https` set, a body limit of at least
`PAPERLESS_GO_MAX_UPLOAD_SIZE`, and timeouts of up to an hour. No
configuration for them is given here.

### Health check

`/healthz` needs no login and says only whether the server and its
database are up; it is cheap. Keep it for an uptime monitor, or answer it
from the proxy only inside your network. In Caddy, before `reverse_proxy`:

```text
    @healthz_outside {
        path /healthz
        not remote_ip private_ranges
    }
    respond @healthz_outside 404
```

## Trusted proxies

Without `PAPERLESS_GO_TRUSTED_PROXIES` the server believes
`X-Forwarded-Proto` only from loopback and ignores `X-Forwarded-For`
everywhere: all clients look like the proxy, so the login limits count them
as one client (one attacker can block everybody, and a blocked address is
the proxy's) and the audit log shows the proxy's address. Set it
before you open the site to others:

| Proxy | Value |
|---|---|
| A proxy on the same host, the server in Docker Compose | the Compose network's gateway or network, usually inside `172.16.0.0/12` |
| A proxy on the same host, the server run without Docker | `127.0.0.1` |
| Caddy in the same Compose project | the Compose network, usually inside `172.16.0.0/12` |
| A proxy on another machine, over Tailscale or WireGuard | the tunnel address the server sees |

To find the address, log in through the proxy once and read the client
address of that login on the Log page; that is the proxy as the server
sees it.

List only the proxy, and only if all access goes through it: whoever can
connect from a listed address can claim any client address. The server
takes the last address in `X-Forwarded-For` that is not a trusted proxy.

### A remote proxy over a tunnel

A common setup is a small server with a public address running Caddy, which
forwards over Tailscale or WireGuard to the computer that holds the
documents. Then:

- Set `WEB_BIND` to the home server's tunnel address (not `0.0.0.0`), so
  only the tunnel reaches the web server.
- List the proxy's tunnel address in `PAPERLESS_GO_TRUSTED_PROXIES`, and
  point Caddy at `TUNNEL-ADDRESS:8080`.
- The tunnel carries plain HTTP, but encrypted by the tunnel. Make sure
  nothing else on the tunnel network can reach the port, for example with
  Tailscale ACLs.
- The documents then pass the proxy's machine in the clear, behind its
  TLS: the operator of that machine can read them. Use one you control.

## Logins on the internet

The limits are in [Security](../reference/security#login): a user name is
blocked after 5 failures within 15 minutes (1 minute, doubling up to 15),
20 failures from one address block it for 15 minutes. IPv6 clients count per
`/64`. The counters are in the server's memory and start from zero when it
restarts.

- **The name block can be used against a user.** Whoever knows a user name
  can keep that user's password login blocked with one wrong password a
  minute. This is the price of slowing down guessing against one account.
  A **passkey login still works for a blocked name**: it counts per address
  only. So give everybody who logs in from the internet a passkey; the Log
  page of an admin shows the failed logins (`auth.login_failed`) that
  cause a block.
- **Two-factor login for everybody.** Admins always need it. Set
  `PAPERLESS_GO_REQUIRE_2FA=true` (default `false`) for all users. It costs
  passkey users nothing: a passkey login replaces password and second
  factor.
- **Passkeys** need `PAPERLESS_GO_PUBLIC_URL` and work only at that exact
  address. See [Passkeys](../reference/security#passkeys).
- **Passwords** need 12 characters; there is no self-registration and no
  CAPTCHA.

## API keys for phones and scripts

Create one key per device on the profile page, with only the scope it needs
(`upload` adds documents and reads nothing; `read` lists and downloads;
`write` also changes and deletes), and an expiry. A
key reaches no login, profile or user management, and a lost phone's key is
revoked on its own without touching the account. A key still needs the
proxy: it is sent as `Authorization: Bearer pl_…` over HTTPS. See [API
keys](../reference/security#api-keys). For an iPhone that scans and
uploads, see [Scanning with the iPhone](./iphone).

## What must never be exposed

- **The PostgreSQL port.** Compose publishes it on `127.0.0.1` only. Remove
  the `ports:` entry of `postgres` if nothing on the host needs it, and
  never publish it on a public address.
- **The consume folder.** Anything written into it is imported for that
  user. Share it, if at all, only inside your own network.
- **The data directory and the backups.** They hold the documents as plain
  files.
- **The web server's port itself**, `WEB_BIND=0.0.0.0` on a public
  machine: it would offer plain HTTP and let clients choose their address
  headers. Keep it on `127.0.0.1` (or the tunnel address) and let only the
  proxy reach it; check that your firewall or Docker's published ports do
  not undo this.

The server has no metrics endpoint.

## Backups

Keep backups off the server, so that a compromised or lost machine does not
take them too, and keep `PAPERLESS_GO_SECRET_KEY` apart from the database
dump. See [Backups](./operations#backups).

## Updates

Read the release notes, back up, then `./pgo migrate` and `docker compose
up -d web`; see [Upgrades](./operations#upgrades). Do not use an
auto-updater such as Watchtower or a floating image tag: migrations run
only with `./pgo migrate`, and the web server refuses to start while any
are pending, so an automatic pull would take the site down. The bundle
names an exact image version in `PAPERLESS_GO_IMAGE`; the PostgreSQL image
`postgres:18` follows the major version only. Subscribe to the repository's
releases to hear about security fixes.

## Logs and the audit log

The server logs no requests, only refused logins and errors. Turn on the
proxy's access log. Note that a search's text is in the URL of the request
(`GET /api/documents?q=…`), so the access log holds what users searched
for; keep it like the documents.

The audit log shows what happens to accounts. During an attack, look for:

- `auth.login_failed` with `wrong_password` or `unknown_user` in numbers,
  or with `method: api_key` and the reason `invalid` from many addresses
  (an unknown key is recorded once per client address and minute);
- `auth.login_failed` with `revoked` or `expired` for a key that should
  not be in use any more;
- `api_key.create`, `profile.passkey_add` or `profile.password_change` from
  an account that should not do these;
- `document.search` with an API key actor that is not one of your scripts.

On the Log page, an admin filters by kind of action; on the host:

```sh
./pgo audit export --since 2026-10-01 --action login --format csv
./pgo audit export --since 2026-10-01 --user alice --format jsonl
```

`--since` is required. See [Audit log](./audit-log).

### Blocking addresses with fail2ban

Every refused credential writes one line to the server log:

```text
time=2026-10-22T17:03:35.000Z level=INFO msg="login refused" method=password reason=wrong_password addr=192.0.2.7 user=alice
```

Its fields are in [Security](../reference/security#refused-logins-in-the-log).
A filter for [fail2ban](https://github.com/fail2ban/fail2ban) can ban the
address. This is an **example, verify on your host**: nothing in this
project runs it, and it depends on how your host stores container logs.

`/etc/fail2ban/filter.d/paperless-go.conf`:

```ini
[Definition]
failregex = msg="login refused" method=\S+ reason=(?:wrong_password|unknown_user|second_factor|unknown_passkey|passkey_invalid|invalid|rate_limited) addr=<ADDR>
```

`/etc/fail2ban/jail.d/paperless-go.local`:

```ini
[paperless-go]
enabled  = true
filter   = paperless-go
backend  = systemd
journalmatch = CONTAINER_NAME=paperless-go-web-1
maxretry = 10
findtime = 10m
bantime  = 1h
port     = http,https
```

Points to check:

- The filter matches the per-attempt reasons (`wrong_password`,
  `unknown_user`, `second_factor`, `unknown_passkey`, `passkey_invalid`,
  `invalid`) and `rate_limited`. It leaves out `locked`, `revoked` and
  `expired`, which come from a known account or key and are usually a
  mistake, not guessing.
- `<ADDR>` needs fail2ban 0.10 or newer.
- `docker compose logs` is not a file. Either run the containers with
  Docker's `journald` log driver and use fail2ban's `systemd` backend as
  above (check the container name with `docker ps`), or point `logpath`
  at the `json-file` log under `/var/lib/docker/containers/` (then the
  lines are JSON and the regex needs adapting).
- The `addr` is the client the server sees. Without
  `PAPERLESS_GO_TRUSTED_PROXIES` it is the proxy, and fail2ban would ban
  the proxy. Check with `fail2ban-regex` against real lines first.
- A ban on the host's firewall does not reach traffic that Docker
  publishes through its own chains. Use an action that targets the
  `DOCKER-USER` chain, or ban in the proxy's machine.
- Banning shared addresses (an office, mobile carriers) locks out their
  other users; use a long `findtime` and a high `maxretry` accordingly.

## After a compromise

If an account, key or the machine may be in someone else's hands:

1. Lock the user and end their sessions: `./pgo user lock NAME`.
2. Revoke their API keys: `./pgo user revoke-api-keys NAME`.
3. Reset the password: `./pgo user reset-password NAME`. It also revokes the
   keys and removes the passkeys, unless you keep them. Add
   `./pgo user reset-2fa NAME` if the authenticator may be known, and
   `./pgo user remove-passkeys NAME` for a lost device.
4. See what the account did:
   `./pgo audit export --since 2026-10-01 --user NAME --format jsonl`.
   `--user` lists what the user did; the changes made to the user are in
   `--action users`.
5. If the server itself was reached: assume the documents were read. The
   database dump and the secret key together reveal the TOTP secrets, and
   a leaked secret key is replaced only by `user reset-2fa` for everybody.
   Restore from a backup made before the incident onto a fresh machine
   (dump, data directory and secret key; see
   [Restore](./operations#restore)), change `POSTGRES_PASSWORD` and every
   user's password.

## Known limits

- The server speaks plain HTTP; HTTPS is the proxy's job, and so is any
  protection that needs a network view (a web application firewall, a ban
  list).
- No CAPTCHA, and no general request rate limit: only logins, API keys and
  passkey starts are limited.
- The login counters are in memory: a restart resets them, and with
  several server processes each counts on its own.
- A name block can be used to keep a password login blocked (see [Logins
  on the internet](#logins-on-the-internet)); passkeys avoid it.
- Every user and every key with `write` can upload files that the OCR
  tools parse. Give logins only to people you trust.
- An attacker with many IPv6 `/64` networks gets one audit entry per network
  and minute for unknown API keys.
