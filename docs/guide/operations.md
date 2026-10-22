# Backups and upgrades

## Backups

A complete backup has two parts: the database and the data directory
(`DATA_DIR` in `.env`, default `./data`). Back up the database first, then
the files:

```sh
./pgo backup ./backups   # pg_dump in the database container, written to ./backups
```

Then copy the data directory with the tool you use for backups (Time Machine,
restic, rsync). The dump is written with mode `0600` into a directory only
you can read; keep it like the documents themselves.

Keep `PAPERLESS_GO_SECRET_KEY` from `.env` too, ideally apart from the dump
(a password manager): a restore with another key makes every two-factor
login fail, and each user then needs `./pgo user reset-2fa NAME`.

### Restore

Stop the web server, restore the data directory, then the dump:

```sh
docker compose stop web
docker compose exec -T postgres pg_restore --clean --if-exists -U paperless -d paperless < backups/paperless-YYYYMMDDTHHMMSSZ.dump
./pgo migrate
docker compose up -d web
```

## Upgrades

Back up first (see [Backups](#backups)) and read the release notes of every
version you skip: they list database migrations and manual steps. Then, in
the directory that holds your `paperless-go/` directory:

```sh
gh release download --repo pklnx/paperless-go --pattern paperless-go.tar.gz --clobber && tar -xzf paperless-go.tar.gz
cd paperless-go
./pgo migrate              # apply new database migrations with the new image
docker compose up -d web   # restart the web server with the new image
./pgo --version            # the version you are running
```

The new bundle replaces `docker-compose.yml`, `./pgo`, `.env.example` and
`README.txt` and keeps `.env` and `data/`. Compare `.env.example` with your
`.env` for new settings. The web server refuses to start while migrations are
pending.

With a clone of the repository, `git pull` replaces the download, and
`./pgo migrate` rebuilds the image.

### Upgrade notes

**Two-factor login (TOTP, #18).** The web server now refuses to start
without `PAPERLESS_GO_SECRET_KEY`. Before you restart it:

```sh
./pgo keygen   # prints a key; add it to .env as PAPERLESS_GO_SECRET_KEY=...
./pgo migrate
docker compose up -d web
```

Admins set up two-factor login at their next login (nobody is locked out:
the password still logs them in, to the setup). Rolling the migration back
(`migrate down`) turns 2FA off for everybody.

**Consume folder (#4).** Compose now mounts a consume folder
(`CONSUME_DIR`, default `./consume`) and the web server imports from it.
Run `./pgo migrate` before `docker compose up -d web`: it creates the folder
and makes it writable for the container user. Set
`PAPERLESS_GO_CONSUME_DIR=` (empty) in `.env` to turn it off. See [Consume
folder](./import#consume-folder).

**Passkeys (#19).** Set `PAPERLESS_GO_PUBLIC_URL` in `.env` to the
address you open (with HTTPS) to turn passkeys on, then `./pgo migrate`
and `docker compose up -d web`. Without it nothing changes. Rolling the
migration back (`migrate down`) removes every passkey. A password reset by
an admin (Users page or `./pgo user reset-password`) now also removes the
user's passkeys, unless the admin unticks it or passes `--keep-passkeys`.
The login limits now count IPv6 clients per `/64`, not per address.

**API keys (#20).** Run `./pgo migrate` before `docker compose up -d web`.
`X-Forwarded-Proto: https` now counts only from loopback or a trusted
proxy. Behind a proxy or `tailscale serve` in Docker Compose, requests come
from the Compose network's gateway, so set `PAPERLESS_GO_TRUSTED_PROXIES`
in `.env` (for example the Compose network, or `172.16.0.0/12`) to keep the
`Secure` `__Host-` session cookie; see [Trusted
proxies](../reference/security#trusted-proxies). Without it the login still
works, but with a plain cookie (an old `__Host-` cookie is dropped at the
next login). A password reset by an admin (Users page or `./pgo user
reset-password`) now also revokes the user's API keys, unless the admin
unticks it or passes `--keep-api-keys`.

**OCR and images (#5).** Scans (PDFs with pages without text) and photos
(JPEG, PNG, HEIC) now get a text layer with OCRmyPDF and Tesseract. The
version before failed them with a message to run `reprocess --failed`; you
need not do that: `./pgo migrate` queues them again, together with the PDFs
it left without OCR because of an owner password, and the web server
processes them after `docker compose up -d web`. A PDF that opens without
a password is OCRed unless its permissions forbid changes; the archive PDF
is then not encrypted (the original stays as it is).

**Full-text search index (#6).** The migration indexes every document's
text once: plan on a few minutes per 100 000 documents (about 4–5 for
documents of about 5 000 characters; it depends on the machine), during which the documents table is locked (the web server does not
run while migrations are pending), and on about 1.5–2 times the stored text
in extra disk space. It creates the PostgreSQL extension `unaccent`, a trusted extension
that needs no superuser: in the Compose setup the database user creates it
itself. With your own PostgreSQL server and a user without the `CREATE`
privilege on the database, have an administrator run
`CREATE EXTENSION unaccent SCHEMA public;` in it first. The extension
must be in the `public` schema: if it is already installed in another
one, the migration fails; move it with `ALTER EXTENSION unaccent SET
SCHEMA public;`. Rolling the migration back (`migrate down`) drops the
index, and the extension too if the paperless-go database user owns it
(it created it) and nothing else uses it; one an administrator created
stays.

### Versions

Versions are named after the year and month of the release, plus a build
number that starts at 1 each month: `26.10-1` is the first release of
October 2026, `26.10-2` the second, `26.11-1` the first of November. They
are tagged `v26.10-1` in the repository. Each release publishes
`ghcr.io/pklnx/paperless-go` for `linux/amd64` and `linux/arm64` with three
tags:

| Tag | Points to |
|---|---|
| `26.10-1` | Exactly this build. The bundle's `docker-compose.yml` uses it. |
| `26.10` | The newest build of that month. |
| `latest` | The newest release. |

Set `PAPERLESS_GO_IMAGE` in `.env` to use another tag or your own build;
pinning an exact version is safer than `latest`, because migrations only run
with `./pgo migrate`. The images carry a provenance attestation and an SBOM.

The image is based on Debian and contains OCRmyPDF with Tesseract (German
and English), Poppler, img2pdf and libheif; it is about 145 MB to download.

Pull requests that change the database schema carry the label
`db-migration`. Back up the database before you upgrade past one of them.
`./pgo migrate status` lists applied and pending migrations.

## Monitoring

`GET /healthz` answers `200 ok` while the server runs and the database
answers, and `503` otherwise. It needs no login and contains no data.

## Logs

```sh
docker compose logs web --tail 100
```

`PAPERLESS_GO_LOG_LEVEL` (`debug`, `info`, `warn`, `error`) sets the detail.
