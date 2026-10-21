# Configuration

The server reads its settings from environment variables. With Docker
Compose, set them in `.env`; `docker-compose.yml` passes them on.

## `.env` (Docker Compose)

| Variable | Default | Meaning |
|---|---|---|
| `POSTGRES_PASSWORD` | (required) | Password of the bundled PostgreSQL. |
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
