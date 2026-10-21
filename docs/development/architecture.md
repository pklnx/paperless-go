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
upload, consume folder, CLI import ──▶ internal/ingest ──▶ internal/blobstore ──▶ data/documents/ab/cd/<sha256>.pdf
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
| `internal/store` | PostgreSQL access: migrations (goose, embedded), queries (sqlc, generated into `internal/store/db`). |
| `internal/store/storetest` | A throwaway, migrated database per test. |
| `internal/web` | HTTP server: routes, the Host and same-origin checks, `/healthz`. |
| `internal/web/ui` | Embeds the built web UI (`web/` → `internal/web/ui/dist`) and serves it with a strict CSP. |
| `web` | React UI (TypeScript, Vite, Tailwind CSS). |
| `docs` | This site (VitePress). |

## Data model

The first migration creates `installation`, one row with a random UUID
that identifies this server (for example as "created by" in exported archive
folders). The document model (`00002_documents.sql`) follows the Document
Archive app (`DocumentArchive/Model` there), so archives can move between the
app and the server through the app's archive folder format:

| App (SwiftData) | Server table |
|---|---|
| — | `users` (`BIGSERIAL` id, name, is_admin; minimal until users and login add the rest) |
| `Document`: id (UUID), title, documentDate, createdAt, modifiedAt, notes, isFavorite, isInbox, archiveSerialNumber, pageCount, fileSize, extractedText, source, originalFileName, textSource, contentHash | `documents`, with the UUID as primary key; the original file (`original_sha256`, size, MIME type) and the archive PDF (`archive_sha256`, size) |
| `Tag`: name, colorHex, symbol, parent, matchText, matchMode | `tags`, `document_tags` (nested through `parent_id`) |
| `Correspondent`, `DocumentType`: name, matchText, matchMode | `correspondents`, `document_types` |
| `Deadline`: kind, date, label, origin, isDone | `deadlines` (planned) |
| `SavedView` | `saved_views` (planned) |

UUIDs as keys (not serials) keep imported and exported records stable across
the app and the server.

**Owner.** Every document, tag, correspondent and document type has an
`owner_id` (`ON DELETE RESTRICT`: a user with rows cannot be deleted).
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
document removes its tag links. The trigger `tags_no_cycle` refuses a parent
that would make a tag its own ancestor (`ErrTagCycle`); a per-owner advisory
lock keeps two concurrent changes from building a cycle together.

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
