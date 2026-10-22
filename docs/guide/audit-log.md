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
| `document.reprocess` | `reprocess` queues documents for processing again (with the selection and how many it queued). |
| `document.import` | `import` added documents: one entry per run that imported something, naming the user who owns them, the path and the counts of imported, duplicate and failed files. |

Each entry has the time, the user (by the name they had then), how they
acted (a session, an API key with its name, or the command line), the
client address and browser in the web UI, the user an admin action was about, and
details such as a failed login's reason. Passwords, TOTP secrets and codes
and API keys are never recorded. Reads with an API key are not recorded.
Commands record the command, the operating system user and the operator
`./pgo` passes (the user on the host); neither is verified, because whoever
can run commands holds the database password.

A change and its entry are written together: if the entry cannot be
written, the change is not made and the web UI answers with an error.
Removing a user keeps their entries.

## Reading the log

The **Log** page in the web UI shows your own entries, newest first, for the
last 30 days by default; choose a period and a kind of action to see others.
Admins see every user's entries and can narrow them to one user, to the
command line or to requests made with API keys. A removed user's entries stay, under the name the user had, and
the filter by name still finds them.

Admins can export a period as CSV or JSON lines:

```sh
./pgo audit export --since 2026-01-01 --format csv > audit-2026.csv
./pgo audit export --since 2026-10-01 --until 2026-10-31 --user alice --action login --format jsonl > alice.jsonl
```

See [Command line](../reference/cli#audit-log) for all options.

## Storage

The log is partitioned by month. `serve` creates the partitions three months
ahead, every command one month ahead. Entries are kept for good; there is no
retention yet. Back up the database as usual: the log is part of the dump.
