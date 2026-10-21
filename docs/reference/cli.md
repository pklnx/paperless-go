# Command line

With Docker Compose, run commands through the wrapper, `./pgo <command>`.
Without it, the binary is `paperless-go <command>`. `--help` works on every
command.

| Command | Does |
|---|---|
| `migrate` (`migrate up`) | Applies all pending database migrations. `./pgo migrate` also makes a fresh data directory writable for the container user. |
| `migrate status` | Lists applied and pending migrations. |
| `migrate down --yes` | Rolls back the most recent migration. Usually deletes data; for development. |
| `serve [--listen ADDR]` | Runs the web server (default `127.0.0.1:8080`; `:8080` in Compose). Refuses to start while migrations are pending. |
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
| `user list` | Lists users with role, state (active, locked, must change password), documents, creation and last login. |
| `user set-password NAME [--password-stdin]` | Sets a new password and logs the user out everywhere. |
| `user reset-password NAME` | Generates a password, prints it once and logs the user out everywhere; the user must choose their own at the next login. |
| `user promote NAME` (`user admin`) | Makes a user an admin. |
| `user demote NAME` (`user unadmin`) | Takes the admin role away. |
| `user lock NAME` | Stops a user from logging in and logs them out everywhere. |
| `user unlock NAME` | Lets a locked user log in again. |
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

## Audit log

| Command | Does |
|---|---|
| `audit export --since DATE --format csv\|jsonl [--until DATE] [--user NAME] [--action A]... [--out FILE]` | Writes the entries of a period, oldest first, to stdout or a new file `FILE` (mode `0600`, refused if it exists). `DATE` is `YYYY-MM-DD` (midnight UTC) or RFC 3339; a plain `--until` date includes that day. `--action` takes a group (`login`, `users`, `profile`, `export`) or an action such as `auth.login`, and repeats. `--user` also finds a removed user's entries, by the name they keep. The export itself is recorded as `audit.export`. In CSV, a cell starting with `=`, `+`, `-`, `@`, a tab or a carriage return gets a leading `'` so spreadsheets do not run it as a formula. |

With `./pgo`, `--out` must point into a directory the container can write,
for example the data directory (`/data/...`); stdout works anywhere:
`./pgo audit export --since 2026-01-01 --format csv > audit.csv`.
