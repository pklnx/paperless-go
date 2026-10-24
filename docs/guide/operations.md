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

Stop the web server and keep it stopped until the dump and the data
directory are both back, so nothing writes in between:

```sh
docker compose stop web
./pgo restore --yes backups/paperless-YYYYMMDDTHHMMSSZ.dump
# now restore the data directory from the same backup
./pgo migrate
./pgo sweep --dry-run   # check: no warning about newer files
docker compose up -d web
```

`./pgo restore` loads the dump into a new database and swaps it in only
when the whole dump is in. If it fails (a damaged dump, a full disk),
nothing was changed: it says so, and the old database is still in use.
It refuses while the web server runs or restarts, or while another `./pgo`
command (an import, `process`, a cron job) uses the database, and checks
that again before the swap. Without `--yes` it only says what it would do.

If a restore is interrupted (Ctrl-C) while it loads the dump, the old
database is unchanged and a partial copy, `paperless_restore`, is left; the
next `./pgo restore` drops it. Interrupted during the swap, PostgreSQL
finishes the swap on its own; the command says how to check. If the web
server or `./pgo migrate` ever fails with `database "paperless" is not
currently accepting connections`, a swap was cut short: run the restore
again (it opens the database first), or open it by hand:

```sh
docker compose exec postgres psql -U paperless -d postgres -c 'ALTER DATABASE paperless ALLOW_CONNECTIONS true'
```

The database it replaces stays, renamed to
`paperless_replaced_YYYYMMDD_HHMMSS` (the time in UTC), and the command prints its name and
the command that drops it:

```sh
docker compose exec postgres dropdb -U paperless paperless_replaced_YYYYMMDD_HHMMSS
```

Until then it takes disk space, and the restore needs free space for one
more copy of the database (it prints the size of the current one). If the
disk fills up during the restore, the restore stops and the old database
stays in use; PostgreSQL may restart.

**The data directory and the restored database.** The data directory holds
a marker file, `installation`, with the ID of the installation it belongs
to (written by `./pgo migrate` into a fresh data directory, or by `./pgo
sweep --bind`). The daily [file sweep](#file-removal) only removes files
the database does not name in a data directory whose marker names the
database's installation. A restore with the data directory of the same
backup keeps them matching: the dump holds the installation's ID and the
data directory its marker, so the sweep works as before. A database
restored into another installation, or a data directory of another one, is
refused with an error in the log, and nothing is removed: neither the
daily walk nor the per-minute pass touches a data directory whose marker
names another installation. A data directory
copied before the marker existed stays unbound (a warning in the log) until
`./pgo sweep --bind`. So a database restore never removes files of the
restored state: the walk refuses a data directory that is not the
installation's, and the per-minute pass removes only files the restored
database itself queued.

**An older dump with the newer data directory.** Keeping the current data
directory with an older dump of the same installation makes the files that
only the newer documents needed unreferenced. The walk keeps them: a file
no document names that is newer than the newest document change in the
database (a document created or edited, a processing job ended) is not
queued, and the walk warns instead, in `serve`'s log ("kept files no
document names that are newer than the newest document change", with the
count) and in `./pgo sweep`:

```text
walk: warning: kept 19 unreferenced files that are newer than the newest document change. ...
```

`./pgo sweep --dry-run` before starting the web server shows it, as in the
steps above.

The warning has harmless causes too, in which case `./pgo sweep --force`
queues the files once:

- A data directory that was copied without keeping the file times (`cp`
  without `-p`, `rsync` without `-t` or `-a`): every file then looks as new
  as the copy. Back the data directory up with a tool that keeps file
  times (`rsync -a`, `cp -a`, Time Machine, restic).
- Purging the newest documents: the newest change then goes back to an
  older document, and files no document names that were written in
  between (left by a failed upload) count as newer until the next change.
- A file left by an upload or a processing job that failed after the last
  document change.

Otherwise put back the data directory of the same backup. If the
files are really not needed (you restored on purpose and want the newer
documents gone), `./pgo sweep --force` queues them once. The check only
keeps files, it never removes one, and it is not watertight: once the
restored server creates or edits a document, files written before that
count as older again, and the next daily walk queues them (they go an hour
later). So check right after the restore, and restore the data directory
together with the dump, as above.

Move the current data directory aside instead of overwriting it, and keep
it until the restored server works: going back to the replaced database
needs the files that belong to it. To go back, stop the web server and
swap the names again:

```sh
docker compose stop web
docker compose exec postgres psql -U paperless -d postgres -v ON_ERROR_STOP=1 -1 \
  -c 'ALTER DATABASE paperless RENAME TO paperless_unwanted' \
  -c 'ALTER DATABASE paperless_replaced_YYYYMMDD_HHMMSS RENAME TO paperless'
```

Then move the old data directory back and start the web server.

## File removal

Files are shared: the same file uploaded by two users, or an archive PDF
that equals its original, is stored once. `serve` removes a file only when
no document names it any more:

- **Every minute** it removes the files of purged documents, an hour after
  the purge (so disk space comes back about an hour and a minute after
  "delete permanently" or the end of the retention), unless a document
  names them again by then.
- **Once a day** (and at start) it walks the data directory and queues the
  files no document names (left by refused uploads, failed imports or a
  crash), which then go an hour later, and removes temporary files older
  than a day. Anything it does not recognize (symbolic links, other names)
  is logged and left alone.

The daily walk refuses, logs an error and removes nothing when the data
directory is not bound to this installation (see the marker under
[Restore](#restore)), when the database has no documents but the data
directory has files, or when it would queue more than 10 % of the files
(and more than 100). These are the signs of a wrong
`PAPERLESS_GO_DATABASE_URL`, a restore into the wrong database or a
reinstall that kept the data directory. It also keeps, with a warning, the
unreferenced files that are newer than the newest document change, the
sign of an older dump restored with a newer data directory (see
[Restore](#restore)). Rarely, a file left by an upload that failed after
the last document change is kept that way too; it is queued once a later
document change is newer.

```sh
./pgo sweep --dry-run   # what one pass and one walk would do
./pgo sweep             # do it now; files queued now still wait an hour
./pgo sweep --force     # once, after checking: override the 10 % and empty-database stops, queue newer files
./pgo sweep --bind      # bind the data directory to this installation (see the upgrade note)
```

Only one sweep runs at a time: `./pgo sweep` while `serve`'s sweeper is
busy (for a moment every minute) says "another sweep is running" and exits
with status 1; run it again. A file that cannot be removed (a read-only or
failing disk) is logged as an error and tried again an hour later.

An upload of a file whose last document was just purged can meet its file
being removed: it answers `503` with `Retry-After` (1 second, or up to 6
minutes while the removal of a sweep that was interrupted is pending), and
the retry stores the file again. The consume folder and `./pgo import` treat it the same way
(the file is kept, or reported as failed, for the next run).

## Upgrades

Back up first (see [Backups](#backups)) and read the release notes of every
version you skip: they list database migrations and manual steps. Check
`.env` for a `PAPERLESS_GO_IMAGE` line first (see the note below the steps).
Then, in the directory that holds your `paperless-go/` directory:

```sh
curl -fLO https://github.com/pklnx/paperless-go/releases/latest/download/paperless-go.tar.gz
curl -fLO https://github.com/pklnx/paperless-go/releases/latest/download/SHA256SUMS
sha256sum -c SHA256SUMS    # macOS: shasum -a 256 -c SHA256SUMS
tar -xzf paperless-go.tar.gz
cd paperless-go
./pgo migrate              # apply new database migrations with the new image
docker compose up -d web   # restart the web server with the new image
./pgo --version            # the version you are running
```

The new bundle replaces `docker-compose.yml`, `./pgo`, `.env.example` and
`README.txt` and keeps `.env` and `data/`. Compare `.env.example` with your
`.env` for new settings. A specific version instead of the latest:
`https://github.com/pklnx/paperless-go/releases/download/v1.0.0/paperless-go.tar.gz`
(and `SHA256SUMS` from the same release). The web server refuses to start
while migrations are pending.

A `PAPERLESS_GO_IMAGE` line in the kept `.env` (see [Versions](#versions))
overrides the new bundle's image: the web server and the CLI (`./pgo`) go on
using the image it names, and `./pgo --version` shows that image's version.
Before `./pgo migrate`, remove the line (unless it names a newer build of
your own) or set it to the new release's exact image, for example
`ghcr.io/pklnx/paperless-go:X.Y.Z`. If you ran the steps above with the
line in place, change it as described, run `./pgo migrate`, redo any manual
steps from the release notes that ran with the old image, and run
`docker compose up -d web` again.

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

**Thumbnails, status and Retry (#5).** Processing now also renders a
thumbnail of the first page, and the start page shows it next to each
document, with a badge while a document is queued or processed and the
error and a Retry button when processing failed. Documents processed by an
earlier version have no thumbnail (an empty frame) until they are processed
again; `./pgo reprocess --all` does that for all of them, with the cost of
running OCR again on every scan.

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

**PostgreSQL image: Debian instead of Alpine; PostgreSQL 17 or newer.**
Compose now runs `postgres:18` instead of `postgres:18-alpine`: on Alpine's
C library (musl) broad full-text searches ranked about four times slower
(2.3 s instead of 0.6 s on 30 000 documents). paperless-go now needs
PostgreSQL 17 or newer: `migrate`, `serve` and `process` refuse an older
server with `PostgreSQL 17 or newer is required, found …`. Compose runs 18;
with your own server, upgrade it first.

The data volume stays; no dump is needed. But the database sorts text with
the C library's locale (`en_US.utf8`), and glibc sorts differently from
musl, so the indexes on names (users, tags, correspondents, document types,
passkeys, API keys) and a few other text columns are out of order on the
new image: lookups miss rows and unique names can be added twice.
PostgreSQL does not warn about it. `./pgo migrate` rebuilds those indexes
(seconds; the search index is not affected), and the web server and
`process` refuse to start until it has. When Compose recreates the
database container with the new image, it restarts a running web server too,
which then refuses to serve until `./pgo migrate` has run. Stop the web server first,
so nothing is written in between:

```sh
docker compose stop web
./pgo migrate              # starts postgres:18, prints "rebuilt index ..." per index
docker compose up -d web
```

With a clone, run `git pull` and then these three lines; `docker compose up
-d` alone does not rebuild anything.

If something wrote to the database on the new image before the rebuild,
two rows may now have the same name. `./pgo migrate` then still rebuilds
the other indexes and stops with `these keys are duplicated`, listing
every duplicated key of each affected index, for example
`(name)=(alice)`. Rename one row of each with
`docker compose exec postgres psql -U paperless -d paperless` (renaming is
safer than deleting: deleting a user or tag in psql also deletes what
depends on it, without an audit log entry) and run `./pgo migrate` again.
If it stops because a table stayed locked, something still uses the
database: stop the web server and `process` and run it again.

The same rebuild runs once after every later change of the C library
(a new `postgres:18` image on a database with the libc locale) and after
every PostgreSQL major upgrade; migrate does it, and the web server waits
for it.

On your own PostgreSQL server, the first `./pgo migrate` of this version
rebuilds the same indexes once, as it does not know which C library built
them. If the database belongs to another role than paperless-go's user,
migrate cannot record the new collation version after a C library update
and prints `warning: … have an administrator run: ALTER DATABASE …
REFRESH COLLATION VERSION;`. The indexes are correct by then; run that
statement as the owner so PostgreSQL stops warning about a mismatch.

Lists sorted by name (tags, correspondents, users) now follow glibc's
order, which ignores case and accents (`apfel`, `Ärzte`, `Zebra`) instead
of musl's byte order (`Zebra`, `apfel`, `Ärzte`). A new installation uses
PostgreSQL's own locale instead (`POSTGRES_INITDB_ARGS` in
`docker-compose.yml`): code point order, the same as before, and no image
or C library can change it. To move an existing database to it, recreate
the database volume from a backup. Stop the web server first, so the
backup holds every change:

```sh
docker compose stop web
./pgo backup ./backups
docker compose rm -sf postgres
docker volume rm paperless-go_pgdata   # the database; ./backups has it
docker compose up -d --wait postgres   # a new, empty database with the new locale
./pgo restore --yes backups/paperless-YYYYMMDDTHHMMSSZ.dump
./pgo migrate
docker compose up -d web
```

If the restore fails, nothing was restored; the error names the cause.
The database it replaces is the empty one of the new volume: drop it right
away with the `dropdb` command it prints. The data directory stays as it
is.

To go back to an older version on the Alpine image, first roll back this
version's migration with this version, as the older one refuses a newer
schema: run `./pgo migrate down --yes` until it reports `rolled back
00013_index_collation.sql` (`./pgo migrate status` lists the applied
migrations). Then extract the older bundle (or check out the older
commit), which brings back `postgres:18-alpine`, and rebuild the indexes
by hand, as the older version does not do it:

```sh
docker compose stop web
docker compose up -d --wait postgres
docker compose exec -T postgres psql -U paperless -d paperless -c 'REINDEX DATABASE paperless'
docker compose up -d web
```

Restoring a backup made before the upgrade works too and needs no
`REINDEX`.

**Passkey login caps (#48).** Run `./pgo migrate` before `docker compose
up -d web`. Open passkey logins are now also capped per network: 100 per
IPv6 `/56` or IPv4 `/24` and 250 per IPv6 `/48`, on top of 20 per client
and 1000 in total. The browser keeps its open passkey login in a cookie
(`paperless_go_passkey_login`, `__Host-` over HTTPS), so reloading the
login page no longer piles up open logins. When a request carries
`X-Forwarded-For` from a peer that is not a trusted proxy (also from
loopback), the web server logs one warning, once after each start: behind
a reverse proxy set `PAPERLESS_GO_TRUSTED_PROXIES` (see [Trusted
proxies](../reference/security#trusted-proxies)), else all clients share
one login limit. Rolling the migration back (`migrate down`) only drops the
network caps.

Behind a proxy without `PAPERLESS_GO_TRUSTED_PROXIES` all browsers share
the limit of 30 passkey-login starts per minute for one address; with the
cookie this is now the first limit users hit (`429` "too many attempts").
The fix is the trusted-proxy setting. The warning is logged once per
start, and any client that sends `X-Forwarded-For` straight to the server
can trigger it first: the line then names that client's address, not the
proxy's. So when the warning names an unexpected address, check the proxy
setup anyway.

**Audit entries by the server (#49).** Run `./pgo migrate` before `docker
compose up -d web`. The consume folder now writes a `document.consume`
entry to the audit log for each scan that imported, deleted or moved files
in a user's folder; the Log page names the server as who made it. Going
back to an older version after the first such entry needs a [backup
restore](#backups) from before the upgrade: the log is append-only, so the
migration (`00015_audit_system_actor.sql`) cannot be rolled back while it
holds entries by the server, and `migrate down` refuses with that hint.
Before the first one, `migrate down` rolls it back.

**Tags, correspondents and document types in the API (#7).** Run `./pgo
migrate` before `docker compose up -d web`. The migration lets the audit log
record changes of tags, correspondents and document types. Rolling it back
(`migrate down`) works only until the first such change: the log is
append-only and the old checks would refuse those entries, so after that,
going back needs a restore of a backup made before the upgrade.

**Hardening for the internet (#93).** Every refused login is now logged as
one line, `login refused`, with `method`, `reason`, `addr` and, where
known, `user` (see [Refused logins in the log](../reference/security#refused-logins-in-the-log)).
It replaces the lines `login failed`, `login failed: second factor`,
`login failed: unknown passkey` and `login failed: passkey`; log filters
that match those need the new line. An unknown API key is recorded in the
audit log once per client address and minute, no longer on every request.
Over HTTPS the server sends `Strict-Transport-Security: max-age=31536000`.
A request now has 30 seconds for its body instead of 5 minutes (uploads
and downloads keep an hour) and at most 64 KiB of headers. Compose runs
`web` and the command line with `no-new-privileges`; pull the new
`docker-compose.yml` with the release.

**Upload-only API keys (#87).** Run `./pgo migrate` before `docker compose
up -d web`. API keys get a third scope, `upload`: it adds documents and
reads nothing, for a phone or a scanner. `write` includes it, so existing
keys keep their rights. Rolling the migration back
(`migrate down`, `00017_api_key_upload_scope.sql`) removes `upload` from
keys that have another scope too and deletes the keys that have only
`upload`; their scripts need new keys after going back.

**Search vocabulary (#56, `00018_search_vocabulary.sql`).** The migration
collects the words of every document's search index into a per-user list
with a trigram index, which the search will use to find words inside
longer words (`rechnung` in `Stromrechnung`); this version changes nothing
visible yet. Plan on about a minute per 100 000 documents of about 5 000
characters (measured: 1 min 40 s for 150 000 documents with 4.1 million
distinct words on 4 CPUs), during which documents cannot be written (the
web server does not run while migrations are pending), and on disk space
of about a sixth of the documents table: 415 MB next to 2.4 GB in that
measurement. As documents are added and changed afterwards, the list grows
to about 1.2 times its size after the migration. Saving a document's text
now also updates the list: an upload's OCR result, an import with text or
a delete takes about 10 ms longer, and `import` (one document per
transaction) spends about 2.2 times as long in the database. The
benchmark's figures of 3.4 times the time and a list of 26 % of the
documents table come from adding 100 documents per transaction.

It creates the PostgreSQL extension `pg_trgm`, a trusted extension like
`unaccent` (#6) that needs only the `CREATE` privilege on the database: in
the Compose setup the database user creates it itself. With your own
PostgreSQL server and a user without that privilege, have an administrator
run `CREATE EXTENSION pg_trgm SCHEMA public;` in the database first. The
extension must be in the `public` schema: if it is already installed in
another one, the migration stops with `the extension pg_trgm is installed
in schema …, it must be in public`. Moving it needs the extension's owner,
usually a superuser, not the paperless-go user: have them run `ALTER
EXTENSION pg_trgm SET SCHEMA public;` and migrate again. If nothing else
in the database uses `pg_trgm`, its owner can instead run `DROP EXTENSION
pg_trgm;` and the migration creates it in `public`. Rolling the migration back (`migrate down`)
drops the list, and `pg_trgm` too if the paperless-go database user owns
it (it created it) and nothing else uses it; one an administrator created
stays. No document data changes either way.

**Trash (#12, `00020_trash.sql`).** Run `./pgo migrate` before `docker
compose up -d web`. The migration adds two columns to `documents`, three
indexes (one on the trash, two on the file hashes for the file
removal) and the queue table `blob_removals`, and lets the audit log record
saved views; no row is rewritten. Building the two hash indexes reads the
documents table once, with writes to it waiting meanwhile (the web server
does not run while migrations are pending anyway). Deleting a document now moves it to the trash; after
`PAPERLESS_GO_TRASH_RETENTION` (default 30 days), or at once after "delete
permanently", `serve` removes the row within a minute and records it in
the audit log; its files go about an hour later (see the next note). A user who has
documents in the trash cannot be removed until they are purged. An invalid
`PAPERLESS_GO_TRASH_RETENTION` (negative or unreadable) stops every
command, not only `serve`: `migrate`, `user`, `import` and the others read
it too. Rolling the migration back (`migrate down`) brings every trashed
document back as a live one, including those whose "delete permanently"
was asked for but not yet carried out by the sweeper (their
`document.purge` stays in the log): in the old version they show up again
in every list and search, so empty the trash and let the sweeper finish
(a minute) before going back if that matters. It forgets the queued file
removals (the files stay), and works only until the first audit entry
about a saved view, after which going back needs a backup restore.

**File removal (#12, part 2).** `serve` now removes the files that no
document names any more: the files of purged documents an hour after the
purge, and once a day the files the walk of the data directory finds
unreferenced (see [File removal](#file-removal)). No migration. The walk
works only in a data directory bound to the installation: `./pgo migrate`
binds a fresh data directory (one without document files) by writing the
file `installation` into it, so `migrate` must run with the data directory
mounted, as `./pgo` does. An existing installation stays unbound, and the
log warns about it at every start, until you bind it once after checking
that `DATA_DIR` and the database belong together:

```sh
./pgo migrate
./pgo sweep --bind      # needs documents in the database; --force replaces another installation's marker
docker compose up -d web
```

While unbound, the files of purged documents are still removed (they are
known to the database); only the daily walk is off. Back up the data
directory with the marker, and restore both together (see
[Restore](#restore)).

**Words inside compounds (#56, `00021_search_infix.sql`).** The search
uses that list: a word of 4 or more characters also finds the longer words
that contain it (see [Search](./search), "Compound words"). The migration
only replaces a function and takes no noticeable time. Rolling it back
(`migrate down`) restores the search without compound matches; the list
stays until `00018` is rolled back too. Words of trashed documents stay in
the list until the document is purged, so the trash's own search finds
them inside compounds too; live searches never show trashed documents.

**Session cookie of the request's scheme (#156).** Nothing to run. When a
request counts as HTTPS (`X-Forwarded-Proto: https` from loopback or a
[trusted proxy](../reference/security#trusted-proxies)), only the
`__Host-paperless_go_session` cookie logs in; over plain HTTP, which
includes a proxy that does not count, only `paperless_go_session`. Before,
either name counted, so a plain cookie planted over plain HTTP or from
another host name of the domain could log a browser into someone else's
session. Most users notice nothing. A user logs in once more after the
update in these cases:

- `PAPERLESS_GO_TRUSTED_PROXIES` was set (or the proxy moved to loopback)
  after the user logged in while the proxy did not count yet: the plain
  cookie no longer counts over HTTPS. The next login sets the `__Host-`
  one.
- The variable was removed, or the proxy's address changed so that it no
  longer counts, after the user logged in with the `__Host-` cookie: that
  cookie no longer counts. The next login sets the plain one and expires
  the `__Host-` one.
- A browser logged in over plain HTTP under the host name it now uses over
  HTTPS.

From now on, any later change of `PAPERLESS_GO_TRUSTED_PROXIES` that
changes whether requests count as HTTPS means one more login for everyone.
A login and a logout still end the sessions of both cookies the browser
sends. The warning about an ignored `X-Forwarded-For` is now logged only
for requests with an allowed host name, so a client that sends a wrong
`Host` can no longer use it up.

### Versions

Versions follow [semantic versioning](https://semver.org/), `X.Y.Z`, from
`1.0.0` on:

- a **major** release (`2.0.0`) has a breaking change that needs manual steps
  when upgrading (see the release notes);
- a **minor** release (`1.5.0`) adds features and may change the database
  schema; then it needs `./pgo migrate`;
- a **patch** release (`1.4.3`) normally has fixes only, without a migration
  or a breaking change.

The maintainer can release with a lower version than the labels propose, so
read the release notes: they list database migrations and breaking changes
under their own headings.

Releases are tagged `v1.0.0` in the repository. Each release publishes
`ghcr.io/pklnx/paperless-go` for `linux/amd64` and `linux/arm64` with four
tags:

| Tag | Points to | Brings |
|---|---|---|
| `1.4.2` | Exactly this build. The bundle's `docker-compose.yml` uses it. | Nothing new. |
| `1.4` | The newest `1.4.x` release. | Patch updates: normally no migration and no breaking change. |
| `1` | The newest `1.x.y` release. | Features, and migrations when there are any, so run `./pgo migrate` after each update; normally no breaking change. |
| `latest` | The newest release. | Everything, breaking changes included. |

The release before 1.0.0 was named after the year and month, plus a build
number: `26.10-1`; its tag and images stay. The image tag `26.10` stays on
`26.10-1` and gets no more updates. Tools that update images automatically
(Watchtower, Renovate, Diun) may rank `26.10` above `1.0.0`: switch them once
to an exact version or `1.0` (patch releases, which normally have no
migration). `1.0` gets nothing new once a newer minor or major release is
out. Then move the tag to the newest `X.Y` tag (for example `1.1`, or `2.0`
after the major release's upgrade steps) after reading that release's notes:
set `PAPERLESS_GO_IMAGE=ghcr.io/pklnx/paperless-go:1.1` in `.env`, back up and
run `./pgo migrate` as in [Upgrades](#upgrades), then
`docker compose up -d web`. `1` also installs minor releases, which may
include a migration; until `./pgo migrate` runs, the web server does not
start. A tool on `latest` installs breaking changes without their upgrade
steps.

Set `PAPERLESS_GO_IMAGE` in `.env` to use another tag or your own build;
pinning an exact version is safer than `latest`, because migrations only run
with `./pgo migrate`. Do not update images automatically (Watchtower, a
floating tag): the server refuses to start while migrations are pending. If a
tool updates the image anyway, pin an exact version or an `X.Y` tag, as
described above. See [Running on the internet](./internet#updates). The
images carry a provenance attestation and an SBOM.

The image is based on Debian and contains OCRmyPDF with Tesseract (German
and English), Poppler, img2pdf and libheif; it is about 145 MB to download.

Pull requests that change the database schema carry the label
`db-migration`. Back up the database before you upgrade past one of them.
`./pgo migrate status` lists applied and pending migrations.

## Monitoring

`GET /healthz` answers `200 ok` while the server runs and the database
answers, and `503` otherwise. It needs no login and contains no data.

For a site on the internet, also watch the audit log and the refused
logins in the server log, and keep the system and the images updated; see
[Running on the internet](./internet#logs-and-the-audit-log).

## Logs

```sh
docker compose logs web --tail 100
```

`PAPERLESS_GO_LOG_LEVEL` (`debug`, `info`, `warn`, `error`) sets the detail.
