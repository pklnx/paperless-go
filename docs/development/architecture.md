# Architecture

One Go binary does everything: the command line and the web server with the
embedded UI. PostgreSQL holds metadata; document files live on disk. The
layout follows [mail-archive](https://github.com/pklnx/mail-archive), so its
code patterns (store, migrations, web middleware, login) carry over.

```
browser ──▶ internal/web (JSON API, UI from web/) ──▶ internal/store ──▶ PostgreSQL
                                                              ▲
cmd/paperless-go (CLI: migrate, serve) ───────────────────────┘
```

Planned, with the [roadmap](./roadmap):

```
upload, consume folder, CLI import ──▶ internal/ingest ──▶ internal/blobstore ──▶ data/documents/ab/cd/<sha256>
                                              │                ▲
                                              ▼                │
                                       job queue (PostgreSQL) ──▶ internal/ocr (OCRmyPDF, Tesseract; Mistral optional)
                                                                 internal/classify (matching rules, dates, Mistral)
```

## Packages

| Path | Purpose |
|---|---|
| `cmd/paperless-go` | Command line (cobra): `migrate`, `serve`, `user`, `audit export`, `prepare-data` (hidden, run by `./pgo migrate`). |
| `internal/auth` | Argon2id password hashes, session tokens, the failed-login limiter and a rate limit, all with an injectable clock. |
| `internal/config` | Configuration from `PAPERLESS_GO_*` environment variables. |
| `internal/blobstore` | Content-addressed document files: named by their SHA-256 without extension (`documents/ab/cd/<sha256>`), published and removed durably (see [Blob durability](#blob-durability)). |
| `internal/store` | PostgreSQL access: migrations (goose, embedded), queries (sqlc, generated into `internal/store/db`). |
| `internal/store/storetest` | A throwaway, migrated database per test. |
| `internal/web` | HTTP server: the route table with each route's access, login and sessions, user management, the Host and same-origin checks, `/healthz`. |
| `internal/web/ui` | Embeds the built web UI (`web/` → `internal/web/ui/dist`) and serves it with a strict CSP. |
| `web` | React UI (TypeScript, Vite, Tailwind CSS). |
| `docs` | This site (VitePress). |

## Data model

The first migration creates `installation`, one row with a random UUID
that identifies this server (for example as "created by" in exported archive
folders). The document model (`00002_documents.sql`, deadlines and saved
views in `00003_deadlines_saved_views.sql`) follows the Document
Archive app (`DocumentArchive/Model` there), so archives can move between the
app and the server through the app's archive folder format:

| App (SwiftData) | Server table |
|---|---|
| — | `users` (`BIGSERIAL` id, lowercase name, is_admin, password hash, lock, login times; `00004_users_login.sql`), `sessions` |
| — | `audit_log`, `audit_user_agents` (`00005_audit_log.sql`; see [Audit log](#audit-log)) |
| `Document`: id (UUID), title, documentDate, createdAt, modifiedAt, notes, isFavorite, isInbox, archiveSerialNumber, pageCount, fileSize, extractedText, source, originalFileName, textSource, contentHash | `documents`, with the UUID as primary key; the original file (`original_sha256`, size, MIME type) and the archive PDF (`archive_sha256`, size) |
| `Tag`: name, colorHex, symbol, parent, matchText, matchMode | `tags`, `document_tags` (nested through `parent_id`) |
| `Correspondent`, `DocumentType`: name, matchText, matchMode | `correspondents`, `document_types` |
| `Deadline`: kind, date, label, origin, isDone, addedToCalendarAt, addedToRemindersAt, createdAt, modifiedAt | `deadlines`, several per document |
| `SavedView`: name, symbol, sortIndex, createdAt, filterJSON | `saved_views`, with the filter as a `JSONB` object |

UUIDs as keys (not serials) keep imported and exported records stable across
the app and the server.

**Owner.** Every document, tag, correspondent, document type, deadline and
saved view has an `owner_id` (`ON DELETE RESTRICT`: a user with rows cannot
be deleted).
Metadata is per owner, and names are unique per owner, ignoring case (tags:
among siblings). Links use composite foreign keys `(owner_id, x_id) →
x (owner_id, id)`, so the database itself refuses a document that points at
another user's tag, correspondent or type. Every store call takes a
`store.Scope{UserID, All}`: a row outside the scope behaves as missing
(`ErrNotFound`), and `All` (admins) widens it to every owner. A link to a
row that is not the owner's gives `ErrOwnerMismatch`, whether the row
belongs to someone else or does not exist.

**Files.** A document keeps its original file next to the archive PDF. Both
live in the blob store, named by their SHA-256 without an extension; the
type is in the database. `original_sha256` is the uploaded file (the app's
`contentHash`) and detects duplicates per owner (`ErrDuplicate` names the
existing document; the same file for two owners is stored once on disk).
`archive_sha256` is the PDF with a text layer from OCR, `NULL` until
processed and equal to the original for a PDF that already has text. Export
writes the archive PDF, or the original when there is none. Deleting a row
leaves its files; the orphan sweep comes with the trash.

**Deletes.** Deleting a correspondent or document type leaves its documents
without one; deleting a tag removes it from its documents and makes its
children root tags (refused with `ErrTagNameConflict` when a child would
collide with a root tag of the same name; nothing is renamed). Deleting a
document removes its tag links and its deadlines. The trigger `tags_no_cycle` refuses a parent
that would make a tag its own ancestor (`ErrTagCycle`); a per-owner advisory
lock keeps two concurrent changes from building a cycle together.

**Deadlines.** A deadline belongs to one document and to that document's
owner: the composite foreign key `(owner_id, document_id) → documents
(owner_id, id)` (`ON DELETE CASCADE`) refuses a deadline whose owner differs
from its document's. `AddDeadline` gives the deadline the document's owner,
also for an admin; a document outside the scope gives `ErrOwnerMismatch`.
`kind` (`expires`, `due`, `noticeEnds`, `other`) and `origin` (`manual`,
`heuristic`, `ai`) are the app's raw strings without a `CHECK`, so values of
newer app versions survive a round trip. All dates are `TIMESTAMPTZ`
(microseconds), so the archive format's millisecond dates round-trip.

**Saved views.** A saved view is per owner; names need not be unique, as in
the app. `filter` holds the app's `filterJSON` as a `JSONB` object (a `CHECK`
refuses anything else; the store gives `ErrInvalidFilter`). The server does
not interpret it: unknown keys and values are kept, while whitespace and key
order are normalized by PostgreSQL. Lists are ordered as in the app: by
`sort_index`, then `created_at`, then `id`.

## Blob durability

`internal/blobstore` keeps document files under the data directory, named by
the SHA-256 of their content: `documents/ab/cd/<sha256>`, without an
extension (the type lives in the database). The same bytes are stored once,
whoever uploads them and whatever they are used for: an original file and an
archive PDF with the same content, or the same file uploaded by two users,
are one file. `Put` reports `created=false` for content that was already
there.

`blobstore.New`, `Put` and `Remove` return success only after the files and
directory entries their result depends on are synchronized with `fsync`.
Callers acknowledge nothing before that: an upload (#4) writes the file
first and inserts its row only after `Put` succeeded, so a row never names a
file that a crash can take away. An error can still leave the result
visible (a published file, a removed one); it counts as a failure, and the
retry completes it. A file being visible, `Stat` and `Exists` say nothing
about durability.

- **New** creates `documents/` and `tmp/` if needed and, on every call,
  synchronizes them, then the store root and each of its ancestors bottom up
  through `/`. It resolves symlinks in the root first, so the target's
  ancestry is synchronized, not the lexical parents of a link; `Root()`
  still returns the configured path. Every ancestor of the data directory
  must therefore be readable by the service user; one that is not fails
  with an `AncestorError` naming it. The configured path, its symlinks and,
  for a relative root, the working directory must not change while the
  process runs.
- **Put** writes into `tmp/put-*`, synchronizes and closes the file, and
  publishes it with a hard link under `documents/ab/cd/<sha256>`. A link
  never replaces an existing name, so of identical concurrent writers, also
  in other processes, exactly one publishes (`created`), and nobody replaces
  the file another writer is still synchronizing. Then it synchronizes
  `documents/ab/cd`, `documents/ab` and `documents`, also when they existed.
  A writer that finds the file (before or through a lost link race)
  synchronizes that file and the same directories itself before it returns
  `created=false`: the file may be what an earlier, failed call published.
  If a removal (the orphan sweep, #12) takes the file away between the
  lookup and its synchronization, `Put` publishes its own copy instead.
  After a failure the final file stays for the retry; the temporary name is
  removed, but not durably.
- **Remove** unlinks the file and synchronizes its directory, also when the
  file was already gone: an earlier call may have unlinked it and failed
  before the barrier. If shard directories are missing, it synchronizes the
  nearest existing one and its ancestors through the store root; a missing
  store root is an error. The caller makes sure no row refers to the file;
  deleting a document row leaves its file, and the orphan sweep that removes
  unreferenced files comes with the trash (#12).

Stale temporary names in `tmp/` are possible after a crash. A name left
after publication is a second hard link to a live blob. `Walk` reports every
file below `documents/` and `tmp/` (blobs, temporary files with their age,
and anything unexpected such as symlinks, wrong names or extensions) without
following symlinks, for the sweep and a later consistency check.

The guarantee holds on Linux, for a data directory on one local filesystem
that supports hard links and `fsync` on regular files and directories, on a
device that honours flushes. Storage without hard links or directory
`fsync` fails every write instead of falling back to weaker behaviour. A
container can only synchronize what it sees: the operator provisions the
host side of a bind mount or volume durably. File shares of Docker Desktop
(macOS, Windows) are not tested; if one lacks hard links or directory
`fsync`, writes fail. Other platforms build, without a durability claim.

The tests in `internal/blobstore` prove the order of the barriers, error
propagation, retries and concurrent writers, with failures injected per
store (`fsops`). None of them proves that data survives a power loss. That
needs a disposable VM or a fault-injecting block device, power cuts at the
publication and removal boundaries, a restart, and a check that every
acknowledged blob is still there and every acknowledged removal is still gone.

## Web server

`serve` refuses to start while migrations are pending or the data directory
is not writable. Every request passes `protect` (`internal/web/middleware.go`):
the `Host` must be allowed (DNS rebinding), and state-changing requests need
a same-origin `Origin` (or `Sec-Fetch-Site: same-origin`) and a JSON body
(CSRF). Then `authenticate` checks the login (see [Login](#login)). Unknown
`/api/` paths answer `404` in JSON after the login. `/healthz` pings the
database and answers `503` when it does not answer.

## Login

Ported from mail-archive (`internal/auth`, the auth parts of `internal/web`
and `internal/store`, `cmd/paperless-go/users.go`).

- **Route table.** `internal/web/routes.go` lists every route with its
  `access`: `accessPublic` (`GET /healthz`, `GET /{$}`, `GET /assets/`),
  `accessLogin` (`/api/session`: read, log in, log out) or `accessSession`.
  The zero value is `accessSession`, so a route that forgets it gets the
  strictest rule, and so does every path without a route (the JSON `404`
  under `/api/` comes only after the login). `authenticate` enforces the
  table; a test walks it and expects `401` without a session.
- **Sessions.** The cookie `paperless_go_session` (`__Host-` prefixed over
  HTTPS) holds a random token; `sessions` stores its SHA-256 with the user,
  creation, last use and absolute expiry. Sessions end after 7 days unused
  and after 30 days; `serve` deletes ended ones hourly. Every time a
  session check uses comes from the server clock, so tests move a clock
  instead of waiting.
- **Must change password.** A generated password (admin creates or resets a
  user) restricts the session to `PUT /api/profile/password`; everything
  else answers `403` with `passwordChangeRequired`.
- **Scope.** `requestScope` (`internal/web/auth.go`) is the one place that
  turns the request's principal into a `store.Scope`: the session's user,
  with `All` for admins. Handlers that touch documents or metadata take
  their scope from it.
- **Removing users.** Auth tables reference `users` with `ON DELETE
  CASCADE`, owned data with `ON DELETE RESTRICT`. `DeleteUser` counts the
  owned rows per table (documents, which cover their deadlines, tags,
  correspondents, document types, saved views) and refuses with the counts;
  a refusal by the database itself (`23001` from PostgreSQL 18, `23503`
  before) gives the same error. The last unlocked admin stays.
- **Client address.** The login limits count per user name and per client
  address; `X-Forwarded-For` counts only from `PAPERLESS_GO_TRUSTED_PROXIES`.

## Audit log

Ported from mail-archive (`internal/store/audit.go`, `internal/web/audit.go`,
`cmd/paperless-go/audit.go`, migration `00005_audit_log.sql`).

- **Table.** `audit_log` is partitioned by month (`at`, from the database
  clock). Each row has the actor (`session`, `api_key`, `cli`), the user's
  ID and name at the time, the action (`area.verb`), an optional target
  (`document`, `user`, `api_key`; a document target must be a UUID), client
  address, a user agent (stored once in `audit_user_agents`) and details as
  a JSON object. `user_id`, `api_key_id` and `target_id` have **no foreign
  keys**: removing a user keeps their rows, and the log never blocks a
  removal.
- **Append-only.** Row triggers refuse `UPDATE` and `DELETE` on the log and
  its partitions, statement triggers refuse `TRUNCATE`; all are `ENABLE
  ALWAYS`, so `session_replication_role = replica` does not skip them. The
  owner also revokes `UPDATE`, `DELETE` and `TRUNCATE` from itself (no
  effect for a superuser). The down migration refuses while entries exist.
- **Partitions.** `audit_log_ensure_partitions(n)` creates the current and
  the next `n` months, with the trigger and revoke partitions do not
  inherit; there is no default partition. The migration creates 3 months
  ahead, `serve` keeps 3 ahead (at start and hourly), every command 1. An
  insert that finds no partition creates it: on its own it retries once; in
  a transaction it fails and the next attempt works.
- **Same transaction.** `store.WithAudit(ctx, entry)` hands an entry to the
  next store transaction (`inTx`), which writes it after the change and
  before the commit. A change that fails writes nothing; an entry that
  cannot be written rolls the change back (`ErrAuditUnavailable`, `503` in
  the API, an error in the CLI). The user agent ID is looked up before the
  transaction starts, so a full pool cannot deadlock.
- **Events.** `auth.login`, `auth.logout` (same transaction as the session
  change; a logout without its entry still ends the session),
  `auth.login_failed` (best effort, with `reason` and, for an unknown user,
  the tried name only if it is a valid user name), `user.add`,
  `user.remove`, `user.lock`, `user.unlock`, `user.promote`, `user.demote`,
  `user.password_reset`, `user.password_set` (CLI), `profile.password_change`,
  `audit.export` (CLI, written before the export starts). Requests refused
  by the login limiter write nothing, so a flood cannot grow the log.
- **Reading.** `GET /api/audit` takes its scope from `requestScope`: users
  get their own rows (`audit_log_user_idx`), admins everyone's, optionally
  narrowed by `user` or `actor`. Paging is by a `(at, id)` cursor, newest
  first. Reading the log writes no entry.

## Container

The image is distroless and runs as UID 65532; `./pgo migrate` makes a fresh
data directory writable for it (`prepare-data`). OCR needs OCRmyPDF,
Tesseract and Ghostscript, so the OCR step of the roadmap moves the image to a
Debian base; that change gets its own plan.
