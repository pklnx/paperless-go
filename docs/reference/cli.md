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

Only in the wrapper:

| Command | Does |
|---|---|
| `./pgo backup DIR` | Writes a `pg_dump` of the database to `DIR`. Copy the data directory afterwards; see [Backups](../guide/operations#backups). |
