# Importing files

There are two ways to add many files at once: the [consume
folder](#consume-folder), which imports what you put into it while the
server runs, and [`./pgo import DIR`](#import-a-folder) for a folder you
import once.

## Consume folder

Put a file into your folder in the consume folder and the server imports it
into your inbox, then deletes it. Scanners, sync tools and a network share
can write there.

```text
consume/            CONSUME_DIR in .env, default ./consume
  anna/             one folder per user, created by the server
    Rechnung.pdf    imported for anna, then deleted
    failed/         files that could not be imported
      notes.txt
      notes.txt.error.txt   why
  bert/
```

- **Your folder.** The server creates `consume/<user>/` for every user,
  also for users added later (within one scan). The folder name is exactly
  the user name: on a file system that ignores case (Docker Desktop on
  macOS), `Anna/` is not `anna/` and is left alone. Files directly in
  `consume/`, folders that belong to no user, and the folders of locked
  users are left alone; the server log names each once. A user name that
  starts with a dot gets no folder.
- **When.** The server looks every 10 seconds. A file is imported once its
  size and modification time have not changed for two looks at least 2
  seconds apart, so a file that is still being copied waits. A PDF, JPEG or
  PNG must also be complete: a PDF needs `%%EOF` in its last kilobyte, a
  JPEG its end-of-image marker and a PNG its `IEND` chunk at the end. A file
  without it waits, in case its writer only paused, and is imported anyway
  once it has not changed for 10 minutes (some real files lack the marker).
  HEIC/HEIF has no such marker and is imported once it settles.
- **Which files.** PDF, JPEG, PNG and HEIC/HEIF directly in your folder;
  the type is decided from the content. Not touched: hidden files (starting
  with a dot), `*.part`, `*.tmp`, `~$*` (Office lock files), subfolders,
  `failed/` and symbolic links (which are never followed).
- **After the import** the file is deleted, and so is a duplicate (a file
  you already have; it is not imported twice). It is deleted only after the
  document is stored, and only if the file has not changed in the meantime;
  a changed file stays and is imported again once it settles.
- **Files that cannot be imported** (another type, larger than
  `PAPERLESS_GO_MAX_UPLOAD_SIZE`, an image with more than 50 megapixels or
  one whose size cannot be read) move to `failed/`, next to a `<name>.error.txt` that says
  why. A name that is taken there gets a number: `notes-1.txt`. Fix the
  file and put it back into your folder to try again.
- **Errors that are not the file's** (the database is down, the server
  stops) leave the file where it is; the next look tries again. A file that
  cannot be read, deleted or moved (for example, the folder is not writable
  for the server) stays where it is with one warning in the log and is
  tried again after an hour, or as soon as it changes.

With Docker Compose the consume folder is on: `./pgo migrate` creates it
and makes it writable for the container user, UID 65532, which must be able
to create, delete and move files in it. To drop files from the host as your
own user, give your user write access too, for example on a computer only
you use:

```sh
sudo chmod -R a+rwX consume
```

The server must also be able to read each file: a file you save with mode
`0600` (readable only by you) cannot be read by UID 65532 and stays where it
is, with one warning in the log. Save files readable for others, or give
them to the server's group. The consume folder must not be the data
directory or lie inside it (or the other way round): `serve` refuses to
start then.

New user folders get the permissions of the consume folder. To turn the
consume folder off, set `PAPERLESS_GO_CONSUME_DIR=` (empty) in `.env`.
Without Compose, set `PAPERLESS_GO_CONSUME_DIR` to the folder for `serve`.

## Import a folder

`./pgo import DIR` adds every PDF and image in a folder to a user's inbox. Use
it for the first load of an existing scan folder.

```sh
./pgo import ~/Documents/scans --dry-run   # check first, store nothing
./pgo import ~/Documents/scans             # import
```

- **Which files.** PDF, JPEG, PNG and HEIC/HEIF, in `DIR` and all folders below
  it. The type is decided from the content, not the name, so a PDF called
  `scan.dat` is imported and a `.pdf` that is really text is not. Symbolic
  links are skipped. Images may have at most 50 megapixels, and files at most
  `PAPERLESS_GO_MAX_UPLOAD_SIZE` (default 100 MiB).
- **Your files stay where they are.** The command only reads them: nothing is
  moved or deleted. With `./pgo`, the folder is mounted read-only into the
  container.
- **Whose documents.** With one user, that user owns the documents; with more
  you name the owner: `--user alice`. A locked user is refused.
- **Result.** One line per file, then a summary:

  ```text
  invoice-2026.pdf: imported
  sub/copy.pdf: duplicate of "invoice-2026"
  notes.txt: failed: unsupported file type
  imported: 1, duplicates: 1, failed: 1
  ```

  A duplicate is a file the user already has (the same content, whatever the
  name). The exit status is 1 if any file failed.
- **Running it again is safe.** Files that were imported are then reported as
  duplicates and nothing is added twice. After fixing the files that failed,
  run the same command again.
- **`--dry-run`** runs all checks, including the duplicate check, and lists
  `would import`, `duplicate` or `failed` per file without storing anything.

New documents are queued for [processing](../reference/cli#processing) like
every other new document. `serve` does that in the background, or run
`./pgo process --until-empty`. Each run that imports something is recorded in
the [audit log](./audit-log) as `document.import`.

Without Docker Compose, run `paperless-go import PATH` on the server.
