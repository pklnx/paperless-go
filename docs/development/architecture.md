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
| `cmd/paperless-go` | Command line (cobra): `migrate`, `serve`, `prepare-data` (hidden, run by `./pgo migrate`). |
| `internal/config` | Configuration from `PAPERLESS_GO_*` environment variables. |
| `internal/blobstore` | Content-addressed document files: named by their SHA-256 without extension (`documents/ab/cd/<sha256>`), published and removed durably (see [Blob durability](#blob-durability)). |
| `internal/store` | PostgreSQL access: migrations (goose, embedded), queries (sqlc, generated into `internal/store/db`). |
| `internal/store/storetest` | A throwaway, migrated database per test. |
| `internal/web` | HTTP server: routes, the Host and same-origin checks, `/healthz`. |
| `internal/web/ui` | Embeds the built web UI (`web/` → `internal/web/ui/dist`) and serves it with a strict CSP. |
| `web` | React UI (TypeScript, Vite, Tailwind CSS). |
| `docs` | This site (VitePress). |

## Data model

The first migration only creates `installation`, one row with a random UUID
that identifies this server (for example as "created by" in exported archive
folders). The document model follows the Document Archive app
(`DocumentArchive/Model` there), so archives can move between the app and the
server through the app's archive folder format:

| App (SwiftData) | Server (planned table) |
|---|---|
| `Document`: id (UUID), title, documentDate, createdAt, modifiedAt, notes, isFavorite, isInbox, archiveSerialNumber, pageCount, fileSize, extractedText, source, originalFileName, textSource, contentHash | `documents`, with the UUID as primary key and the file named by its SHA-256 |
| `Tag`: name, colorHex, symbol, parent, matchText, matchMode | `tags`, `document_tags` (nested through `parent_id`) |
| `Correspondent`, `DocumentType`: name, matchText, matchMode | `correspondents`, `document_types` |
| `Deadline`: kind, date, label, origin, isDone | `deadlines` |
| `SavedView` | `saved_views` |

UUIDs as keys (not serials) keep imported and exported records stable across
the app and the server.

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
acknowledged blob and every pending removal is still there.

## Web server

`serve` refuses to start while migrations are pending or the data directory
is not writable. Every request passes `protect` (`internal/web/middleware.go`):
the `Host` must be allowed (DNS rebinding), and state-changing requests need
a same-origin `Origin` (or `Sec-Fetch-Site: same-origin`) and a JSON body
(CSRF). Unknown `/api/` paths answer `404` in JSON. `/healthz` pings the
database and answers `503` when it does not answer.

## Container

The image is distroless and runs as UID 65532; `./pgo migrate` makes a fresh
data directory writable for it (`prepare-data`). OCR needs OCRmyPDF,
Tesseract and Ghostscript, so the OCR step of the roadmap moves the image to a
Debian base; that change gets its own plan.
