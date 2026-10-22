# Command line

With Docker Compose, run commands through the wrapper, `./pgo <command>`.
Without it, the binary is `paperless-go <command>`. `--help` works on every
command.

| Command | Does |
|---|---|
| `migrate` (`migrate up`) | Applies all pending database migrations. `./pgo migrate` also makes a fresh data directory writable for the container user. |
| `migrate status` | Lists applied and pending migrations. |
| `migrate down --yes` | Rolls back the most recent migration. Usually deletes data; for development. |
| `serve [--listen ADDR]` | Runs the web server (default `127.0.0.1:8080`; `:8080` in Compose). Refuses to start without `PAPERLESS_GO_SECRET_KEY` and while migrations are pending. Also processes the queue and, with `PAPERLESS_GO_CONSUME_DIR`, imports the [consume folder](../guide/import#consume-folder). |
| `process [--until-empty]` | Processes queued documents in the foreground (see [Processing](#processing)); with `--until-empty` it stops when none is due. |
| `reprocess [--failed \| --all \| ID...]` | Queues documents for processing again. |
| `keygen` | Prints a new random secret key for `PAPERLESS_GO_SECRET_KEY` (32 bytes, base64). Run it once at the first install; changing the key later breaks every two-factor login. |
| `import PATH [--user NAME] [--dry-run]` | Imports the PDF, JPEG, PNG and HEIC/HEIF files in `PATH` (a folder, searched recursively, or one file) as new documents in a user's inbox; see [Importing files](../guide/import). |
| `--version` | Prints the version. |

## Users

Users log in to the web UI. Names are lowercase (`a-z`, `0-9`, dot, hyphen,
underscore; `Kim` becomes `kim`); passwords need at least 12 characters.
Without `--password-stdin`, a terminal asks for the password twice;
otherwise one line is read from stdin. Admins manage users in the web UI and
see every user's documents.

| Command | Does |
|---|---|
| `user add NAME [--admin] [--password-stdin]` | Creates a user. Create the first admin with `./pgo user add NAME --admin`. |
| `user list` | Lists users with role, state (active, locked, must change password), two-factor login (on, off), passkeys, documents, creation and last login. |
| `user set-password NAME [--password-stdin]` | Sets a new password and logs the user out everywhere. |
| `user reset-password NAME [--keep-passkeys]` | Generates a password, prints it once and logs the user out everywhere; the user must choose their own at the next login. The user's passkeys are removed too, unless `--keep-passkeys` is given: after a compromise, a new password alone would not lock the attacker out. |
| `user promote NAME` (`user admin`) | Makes a user an admin. |
| `user demote NAME` (`user unadmin`) | Takes the admin role away. |
| `user lock NAME` | Stops a user from logging in and logs them out everywhere. |
| `user unlock NAME` | Lets a locked user log in again. |
| `user reset-2fa NAME` | Turns the user's two-factor login off: deletes the TOTP secret and the recovery codes and logs them out everywhere. Works for the last admin too, for an admin who lost both the authenticator and the recovery codes; admins set it up again at the next login. |
| `user remove-passkeys NAME` | Removes all passkeys of a user, for a lost or stolen device, and logs them out everywhere. They log in with their password (and second factor) again. |
| `user remove NAME` | Deletes a user and their logins. Refused while the user owns documents, tags, correspondents, document types or saved views: the message names how many of each. Delete them first, or lock the user instead. |

The last admin who can log in (not locked) can be neither demoted, locked
nor removed.

Every command that changes a user writes an [audit log](../guide/audit-log)
entry together with the change; if the entry cannot be written, nothing
changes. The entry names the command, the operating system user and, when
set, `PAPERLESS_GO_OPERATOR`: `./pgo` sets it to the user who runs it on
the host. Neither name is verified.

Only in the wrapper:

| Command | Does |
|---|---|
| `./pgo backup DIR` | Writes a `pg_dump` of the database to `DIR`. Copy the data directory afterwards; see [Backups](../guide/operations#backups). |

## Import

`import PATH` reads the files below `PATH` and never changes, moves or
deletes them; symbolic links are skipped. It prints one line per file
(`imported`, `duplicate of "TITLE"` or `failed: REASON`) and a summary, and
exits with status 1 if any file failed. Running it again reports the imported
files as duplicates.

| Flag | Does |
|---|---|
| `--user NAME` | The owner of the documents. Required when there is more than one user; an unknown or locked user is refused. |
| `--dry-run` | Checks type, size (`PAPERLESS_GO_MAX_UPLOAD_SIZE`), pixels (50 megapixels at most) and duplicates, and stores nothing. |

`./pgo import DIR` takes a folder on the host (relative to where you run it)
and mounts it read-only into the container. A file instead of a folder is
refused. Each run that imports something writes a `document.import` entry to
the audit log.

## Audit log

| Command | Does |
|---|---|
| `audit export --since DATE --format csv\|jsonl [--until DATE] [--user NAME] [--action A]... [--out FILE]` | Writes the entries of a period, oldest first, to stdout or a new file `FILE` (mode `0600`, refused if it exists). `DATE` is `YYYY-MM-DD` (midnight UTC) or RFC 3339; a plain `--until` date includes that day. `--action` takes a group (`login`, `users`, `profile`, `export`) or an action such as `auth.login`, and repeats. `--user` also finds a removed user's entries, by the name they keep. The export itself is recorded as `audit.export`. In CSV, a cell starting with `=`, `+`, `-`, `@`, a tab or a carriage return gets a leading `'` so spreadsheets do not run it as a formula. |

With `./pgo`, `--out` must point into a directory the container can write,
for example the data directory (`/data/...`); stdout works anywhere:
`./pgo audit export --since 2026-01-01 --format csv > audit.csv`.

## Processing

Every new document is queued for processing: its text is extracted, its
pages counted and its archive PDF made. `serve` processes the queue in the
background (`PAPERLESS_GO_OCR_WORKERS`). A failed attempt is retried twice,
after 1 and 5 minutes; a damaged PDF or image, one that needs a password to
open, or an image with more than 50 megapixels fails at once. The error stays with the document.

| Command | Does |
|---|---|
| `process` | Processes the queue in the foreground until stopped. It can run next to `serve`: each document goes to one of them. |
| `process --until-empty` | Processes until no queued document is due, then prints how many were processed, are waiting for a retry, or failed. |
| `reprocess --failed` | Queues the documents whose processing failed. |
| `reprocess --all` | Queues every document. |
| `reprocess ID...` | Queues the documents with these IDs. An unknown ID queues nothing. |

Processing again replaces a document's extracted text, page count and
archive PDF. Text that did not come from processing (`PDF-Text` or
`Tesseract`), such as text imported from the app or edited by hand, is kept.
`reprocess` writes a `document.reprocess` entry to the audit log.

A PDF keeps its own text layer on pages with at least 15 characters; the
other pages are OCRed with OCRmyPDF and Tesseract (`PAPERLESS_GO_OCR_LANGUAGES`),
and the archive PDF is the original with an invisible text layer (a plain
PDF, not PDF/A). JPEG, PNG and HEIC images become a one-page PDF that is
OCRed the same way. The original is always kept. A PDF whose permissions
forbid changes, or that is digitally signed, is not OCRed: it keeps the
original as its archive, and the document gets a note that says so.
