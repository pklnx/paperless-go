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
