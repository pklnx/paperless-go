# Roadmap

The order in which the server grows from this foundation to a document
archive with the features of the Document Archive app. Each step becomes an
issue with a plan (see [Issues, plans and releases](./process)); the plan
decides the details, this page only the direction. Decisions already taken:

- A server of its own, no live sync with the app. The data model follows the
  app's; import and export use the app's archive folder format.
- OCR runs locally with OCRmyPDF and Tesseract (German and English). Mistral
  OCR is optional, with an API key.
- Classification (title, correspondent, type, tags, date) is optional through
  an OpenAI-compatible API. An admin sets it up per server; it stays off for
  a user until that user agrees.
- Users and login as in mail-archive: several users, admins, password,
  passkeys, TOTP, API keys, audit log.
- One owner per document; admins see everything. Tags, correspondents,
  document types and saved views are per owner, with names unique per owner
  (decided in step 1, see [Data model](./architecture#data-model)).
- The original file is kept next to the archived PDF with its text layer.

| # | Issue | Step | Content | Open questions for the plan |
|---|---|---|---|---|
| 1 | #2 | Document model and file storage | Tables for documents, correspondents, document types, nested tags, deadlines and saved views (UUID keys, see [Data model](./architecture#data-model)); content-addressed files with durable writes (mail-archive's blobstore); duplicates by SHA-256. | Decided: original kept, owner per document, metadata per owner. |
| 2 | #3 | Users and login | Port users, sessions, Argon2id, TOTP, passkeys, API keys and the audit log from mail-archive. Before the server may leave `localhost`. |  |
| 3 | #4 | Upload and import | Upload in the web UI (drag and drop, several files), a consume folder, `./pgo import DIR`. PDFs and images (JPEG, PNG, HEIC); images become PDFs. | Allowed size per file. |
| 4 | #5 | Processing queue and OCR | A job queue in PostgreSQL (`FOR UPDATE SKIP LOCKED`), OCRmyPDF for scans and image-only PDFs, thumbnails, page count. The image moves to a Debian base. | Parallel jobs, retries, what the UI shows while a document is processed. |
| 5 | #6 | Full-text search | PostgreSQL full-text search over title, correspondent, type, tags, ASN and text, with German and English stemming (as in mail-archive). | Search syntax (`tag:`, `type:`, date ranges). |
| 6 | #7 | Documents in the web UI | Sidebar (inbox, favorites, tags, correspondents, types), list, detail with PDF viewer, editing, several documents at once, light and dark mode, German and English, phones. Name lists (tags as a tree, correspondents, types) sort with `collate.ts`, not by the server order. | PDF viewer: the browser's own or pdf.js. |
| 7 | #87 | Scanning with the iPhone | Scan with Files or Notes and send the file to the server with a Shortcut and an API key that can only upload (the `upload` scope); a guide with the Shortcut's steps, see [Scanning with the iPhone](../guide/iphone). No camera scanning in the web UI. | Decided: instructions in the docs, no `.shortcut` file; the title is the file name. |
| 8 | #88 | Running on the internet | Hardening and a guide for a public address behind a reverse proxy: host names, trusted proxies, login limits, proxy examples, logs and fail2ban; see [Running on the internet](../guide/internet). |  |
| 9 | #10 | Archive folder import and export | Read and write the app's archive folder format (`Documents/<uuid>.pdf` and `.json`, tags, correspondents, types, saved views, tombstones), so archives move both ways. | One-time import or a recurring exchange. |
| 10 | #8 | Automatic assignment | Matching rules (any, all, exact, regex), sender and issue date detection, ASN. |  |
| 11 | #9 | External OCR and classification (optional) | Mistral OCR, and classification of title, correspondent, type, tags and date through an OpenAI-compatible API; API keys encrypted with the secret key. Off by default; documents leave the server only when it is on. | Decided: per server by an admin, off until each user agrees. |
| 12 | #11 | Deadlines and notifications | Deadlines on documents, detection, reminders through ntfy, Gotify, Slack or a webhook (mail-archive's notifier). |  |
| 13 | #12 | Notes, saved views, trash | Markdown notes, saved views, deleted documents kept 30 days. |  |
| 14 |  | A native iPhone app, in its own repository | Scanning in the app and a device login instead of a pasted key, creating its own `read` and `upload` key through the API. [Document Archive](https://github.com/pklnx/document-archive) stays as it is. | Planned when the server side is done. |

Out of scope for now: e-mail import (mail-archive does mail), sharing with
people outside the server.
