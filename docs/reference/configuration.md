# Configuration

The server reads its settings from environment variables. With Docker
Compose, set them in `.env`; `docker-compose.yml` passes them on.

## `.env` (Docker Compose)

| Variable | Default | Meaning |
|---|---|---|
| `POSTGRES_PASSWORD` | (required) | Password of the bundled PostgreSQL. |
| `PAPERLESS_GO_SECRET_KEY` | (required) | See below; Compose passes it to the web server. |
| `DATA_DIR` | `./data` | Where document files are stored on the host. |
| `WEB_BIND` | `127.0.0.1` | Address the web UI is published on. See [Security](./security#network-access). |
| `WEB_PORT` | `8080` | Port the web UI is published on. |
| `POSTGRES_PORT` | `5432` | Port PostgreSQL is published on, on `127.0.0.1` only. |
| `PAPERLESS_GO_IMAGE` | the bundle's version | Image of the web server and the CLI. |

## Server

| Variable | Default | Meaning |
|---|---|---|
| `PAPERLESS_GO_DATABASE_URL` | (required) | PostgreSQL URL. Compose sets it. |
| `PAPERLESS_GO_DATA_DIR` | `./data` (`/data` in the image) | Directory of the document files. |
| `PAPERLESS_GO_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `PAPERLESS_GO_ALLOWED_HOSTS` | `localhost,127.0.0.1` | Host names accepted in the `Host` header, comma-separated, without port. See [DNS rebinding](./security#dns-rebinding). |
| `PAPERLESS_GO_TRUSTED_PROXIES` | (none) | Reverse proxies whose `X-Forwarded-For` names the client, as CIDR networks or addresses, comma-separated (for example `172.16.0.0/12`). The login limits then count per client instead of per proxy. Without it, the header is ignored. |
| `PAPERLESS_GO_SECRET_KEY` | (required) | 32 random bytes, base64: encrypts the TOTP secrets (AES-256-GCM) and keys the hashes of the recovery codes. Generate it once with `./pgo keygen` (`paperless-go keygen`). `serve` refuses to start without a valid key. Keep it with your backups and never change it: with another key, nobody who uses two-factor login can log in until an admin runs `user reset-2fa`. See [Secret key](./security#secret-key). |
| `PAPERLESS_GO_REQUIRE_2FA` | `false` | `true` makes two-factor login required for every user; admins always need it. Other values are refused. |
| `PAPERLESS_GO_OPERATOR` | (none) | Who runs a command, recorded in the [audit log](../guide/audit-log) (not verified). `./pgo` sets it to the host user. |
| `PAPERLESS_GO_MAX_UPLOAD_SIZE` | `100MiB` | Largest file imported by upload, consume folder and CLI import: bytes, or a number with `KiB`, `MiB` or `GiB`. A file of exactly this size passes. Images may also have at most 50 megapixels. |
