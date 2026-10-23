# Audit log

paperless-go records who logged in and who changed which login: in the web
UI and on the command line. The log is append-only: the database refuses to
change or delete entries.

## What is recorded

| Action | When |
|---|---|
| `auth.login`, `auth.logout` | A user logs in or out. A two-factor login records the factor: the authenticator app (`totp`) or a recovery code, with how many are left. A passkey login records the passkey's name. |
| `auth.login_failed` | A login is refused: wrong password, unknown user, a locked user, a wrong second factor, or a passkey that is not registered or not accepted. For an unknown user the tried name is kept only if it is a valid user name. Attempts the login limit refuses are not recorded. A refused API key is recorded with `method: api_key`, the reason (`invalid`, `revoked`, `expired`, `locked`) and, for a key that exists, its name and owner; never the key. |
| `user.add`, `user.remove` | An admin adds or removes a user. |
| `user.lock`, `user.unlock` | An admin locks or unlocks a user. |
| `user.promote`, `user.demote` | An admin grants or takes away the admin role. |
| `user.password_reset` | An admin generates a new password for a user; with how many passkeys it removed and API keys it revoked. |
| `user.passkeys_removed` | An admin, or `user remove-passkeys`, removes all passkeys of a user. |
| `user.password_set` | `user set-password` sets a password on the command line. |
| `user.2fa_reset` | An admin, or `user reset-2fa`, turns a user's two-factor login off. |
| `profile.password_change` | A user changes their own password. |
| `profile.2fa_enable`, `profile.2fa_disable` | A user turns their two-factor login on or off. |
| `profile.recovery_codes` | A user creates new recovery codes. |
| `profile.passkey_add`, `profile.passkey_remove` | A user adds or removes one of their passkeys, with its name; adding one with two-factor login on records the factor that confirmed it. |
| `profile.2fa_failed` | A wrong code or password when a user sets up, turns off or renews their two-factor login, adds a passkey or creates an API key. |
| `api_key.create` | A user creates an API key, with its name, scopes and expiry. |
| `api_key.revoke` | A user revokes one of their keys, an admin any key (with its owner), or `user revoke-api-keys` all of a user's keys (with how many). |
| `audit.export` | `audit export` writes the log out. |
| `document.create` | A user uploads a document in the web UI or with an API key (with the title). |
| `document.search` | A user searches their documents, in the web UI or with an API key: the first page of each typed search, with the search text and the filters. Choosing a tag, correspondent, type, the inbox or the favourites in the sidebar is browsing and is not recorded, nor are further pages and the plain list of recent documents. |
| `document.update` | A user changes a document in the web UI or with an API key, with the names of the changed fields (such as `title`, `notes`, `tagIds`), never their values. An admin's change of another user's document records that user's ID (`ownerId`). A change of several documents at once writes one per document. |
| `document.bulk_update` | A user changes several documents at once: the number of documents, their IDs, the names of the changed fields, and for "all matches of this search" the search. The per-document entries carry the same `bulkRun` ID. |
| `document.reprocess` | `reprocess` queues documents for processing again (with the selection and how many it queued), or a user clicks Retry on a document in the web UI or calls the API with a key (with the document). |
| `tag.create`, `tag.update`, `tag.delete` | A user creates, changes or deletes a tag in the web UI or with an API key, with its name; a change records the old name on a rename and the names of the changed fields (`name`, `color`, `parentId`), never their values. An admin's change of another user's tag records that user's ID (`ownerId`). |
| `correspondent.create`, `correspondent.update`, `correspondent.delete` | Likewise for a correspondent. |
| `document_type.create`, `document_type.update`, `document_type.delete` | Likewise for a document type. |
| `saved_view.create`, `saved_view.update`, `saved_view.delete` | A user saves, changes or deletes one of their saved views, with its name; a change records the old name on a rename and the names of the changed fields (`name`, `filter`, `sortIndex`), never the filter itself. |
| `document.import` | `import` added documents: one entry per run that imported or restored something, naming the user who owns them, the path and the counts of imported, duplicate, restored and failed files, and "interrupted" on the Log page when the run stopped early. |
| `document.delete` | A user moves a document to the trash, in the web UI or with an API key. An admin's change of another user's document records that user's ID (`ownerId`), as for the entries below. |
| `document.restore` | A user brings a document back from the trash. A file of a trashed document brings it back too, with `via`: `upload` (an upload with a key that cannot read, such as a phone's upload key; by that key), `consume` (the consume folder; by the server, with the user's ID as `ownerId`) or `import` (`import`; by the command line). |
| `document.purge` | A user deletes a document from the trash permanently ("delete permanently", "empty trash"), or the server removes one whose time in the trash is up (by the server, with `reason: retention` and the owner's ID). |
| `document.bulk_delete`, `document.bulk_restore`, `document.bulk_purge` | A user moves several documents to the trash, restores or deletes them permanently at once: like `document.bulk_update`, with one `document.delete`, `restore` or `purge` per document. |
| `document.consume` | The [consume folder](./import#consume-folder) changed a user's folder: one entry per scan and user that imported, deleted or moved files, made by the server, naming the user and with the counts: files imported, duplicates deleted, documents restored from the trash, files moved to `failed/`, and `interrupted` when the scan stopped early (the server stopped, or the database was down); the Log page shows it as "interrupted" ("abgebrochen") after the counts, to admins and to the user. File names are only in the server log. If the entry cannot be written, the files are handled anyway and the counts go into the user's next entry. |

Each entry has the time, the user (by the name they had then), how they
acted (a session, an API key with its name, the command line, or the
server on its own, as for the consume folder), the
client address and browser in the web UI, the user an admin action was about, and
details such as a failed login's reason. Passwords, TOTP secrets and codes
and API keys are never recorded. Reads are not recorded, except searches
(`document.search`): a typed search can say what someone looked for.
Commands record the command, the operating system user and the operator
`./pgo` passes (the user on the host); neither is verified, because whoever
can run commands holds the database password.

A change and its entry are written together: if the entry cannot be
written, the change is not made and the web UI answers with an error. A
search whose entry cannot be written shows no results.
Removing a user keeps their entries.

## Reading the log

The **Log** page in the web UI shows your own entries, newest first, for the
last 30 days by default; choose a period and a kind of action to see others.
It also shows the entries about you that an admin, the command line or the
server made: your account being locked, unlocked, promoted or demoted, your
password or two-factor login reset, your passkeys removed, documents
imported into your account, files imported from your consume folder and
exports of your log. Such an entry names the admin, or says "command line"
or "by the server", and shows only counts, the format and the period from its
details: never the admin's address, browser or API key, or who ran the
command and with which paths.
Admins see every user's entries and can narrow them to one user, to the
command line, to requests made with API keys or to the server. A removed user's entries stay, under the name the user had, and
the filter by name still finds them.

Admins can export a period as CSV or JSON lines:

```sh
./pgo audit export --since 2026-01-01 --format csv > audit-2026.csv
./pgo audit export --since 2026-10-01 --until 2026-10-31 --user alice --action login --format jsonl > alice.jsonl
```

`--user` selects the entries that user made (by `user_id`), not the entries
about them; to find changes made to a user, export `--action users` without
`--user`. See [Command line](../reference/cli#audit-log) for all options.

## Storage

The log is partitioned by month. `serve` creates the partitions three months
ahead, every command one month ahead. Entries are kept for good; there is no
retention yet. Back up the database as usual: the log is part of the dump.
