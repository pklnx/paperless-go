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

Only in the wrapper:

| Command | Does |
|---|---|
| `./pgo backup DIR` | Writes a `pg_dump` of the database to `DIR`. Copy the data directory afterwards; see [Backups](../guide/operations#backups). |
