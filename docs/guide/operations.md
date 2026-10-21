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
