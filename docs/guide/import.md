# Importing files

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
