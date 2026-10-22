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

| # | Step | Content | Open questions for the plan |
|---|---|---|---|
| 1 | Document model and file storage | Tables for documents, correspondents, document types, nested tags, deadlines and saved views (UUID keys, see [Data model](./architecture#data-model)); content-addressed files with durable writes (mail-archive's blobstore); duplicates by SHA-256. | Decided: original kept, owner per document, metadata per owner. |
| 2 | Users and login | Port users, sessions, Argon2id, TOTP, passkeys, API keys and the audit log from mail-archive. Before the server may leave `localhost`. | |
| 3 | Upload and import | Upload in the web UI (drag and drop, several files), a consume folder, `./pgo import DIR`. PDFs and images (JPEG, PNG, HEIC); images become PDFs. | Allowed size per file. |
| 4 | Processing queue and OCR | A job queue in PostgreSQL (`FOR UPDATE SKIP LOCKED`), OCRmyPDF for scans and image-only PDFs, thumbnails, page count. The image moves to a Debian base. | Parallel jobs, retries, what the UI shows while a document is processed. |
| 5 | Full-text search | PostgreSQL full-text search over title, correspondent, type, tags, ASN and text, with German and English stemming (as in mail-archive). | Search syntax (`tag:`, `type:`, date ranges). |
| 6 | Documents in the web UI | Sidebar (inbox, favorites, tags, correspondents, types), list, detail with PDF viewer, editing, several documents at once, light and dark mode, German and English, phones. Name lists (tags as a tree, correspondents, types) sort with `collate.ts`, not by the server order. | PDF viewer: the browser's own or pdf.js. |
| 7 | Automatic assignment | Matching rules (any, all, exact, regex), sender and issue date detection, ASN. | |
| 8 | External OCR and classification (optional) | Mistral OCR, and classification of title, correspondent, type, tags and date through an OpenAI-compatible API; API keys encrypted with the secret key. Off by default; documents leave the server only when it is on. | Decided: per server by an admin, off until each user agrees. |
| 9 | Archive folder import and export | Read and write the app's archive folder format (`Documents/<uuid>.pdf` and `.json`, tags, correspondents, types, saved views, tombstones), so archives move both ways. | One-time import or a recurring exchange. |
| 10 | Deadlines and notifications | Deadlines on documents, detection, reminders through ntfy, Gotify, Slack or a webhook (mail-archive's notifier). | |
| 11 | Notes, saved views, trash | Markdown notes, saved views, deleted documents kept 30 days. | |

Out of scope for now: a mobile app of its own (the web UI works on phones),
e-mail import (mail-archive does mail), sharing with people outside the
server.
