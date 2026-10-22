# Getting started

paperless-go runs with Docker Compose: one container for PostgreSQL, one for
the web server, and a one-shot container for commands.

::: warning Early development
Uploads and OCR work; search and the full document views follow. See the
[roadmap](../development/roadmap). The web UI is published on `localhost`
by default; to reach it from your network or a VPN, follow
[Network access](../reference/security#network-access).
:::

## Requirements

- Docker with a current Docker Compose plugin. Images are published for
  `linux/amd64` and `linux/arm64`, so Apple Silicon Macs and a Raspberry Pi
  with a 64-bit system run them natively.

## Install

Download the Docker Compose bundle of the latest release and extract it.

The repository and its images on `ghcr.io` are private. Downloads need the
[GitHub CLI](https://cli.github.com/) signed in to an account with access,
with the `read:packages` scope for the images:

```sh
gh auth login
gh auth refresh -s read:packages
gh auth token | docker login ghcr.io -u "$(gh api user -q .login)" --password-stdin
```

Then:

```sh
gh release download --repo pklnx/paperless-go --pattern paperless-go.tar.gz --clobber && tar -xzf paperless-go.tar.gz
cd paperless-go
cp .env.example .env
```

The bundle holds `docker-compose.yml`, the `./pgo` wrapper, `.env.example`
and a `README.txt`. Its `docker-compose.yml` uses the image of that release,
`ghcr.io/pklnx/paperless-go:<version>`; nothing is built. See
[Versions](./operations#versions) for the version numbers.

Edit `.env` and set `POSTGRES_PASSWORD` to any long random password. Then
generate the secret key that encrypts the two-factor secrets, and put the
line it prints into `.env` as `PAPERLESS_GO_SECRET_KEY=...`:

```sh
./pgo keygen
```

The web server refuses to start without it. Keep the key with your backups
and never change it (see [Secret key](../reference/security#secret-key)).

Then create the database schema and start the web server. The first `./pgo`
command downloads the images:

```sh
./pgo migrate
docker compose up -d web
```

Create the first admin; the command asks for a password (at least 12
characters):

```sh
./pgo user add NAME --admin
```

Open <http://localhost:8080> and log in. Admins must set up two-factor
login first: scan the QR code with an authenticator app (any TOTP app, for
example the one built into your password manager), enter the code and save
the recovery codes it shows once. Every later login asks for a code. Admins
add more users on the
**Users** page or with `./pgo user add`; see [Command line](../reference/cli#users).

::: tip The `./pgo` wrapper
`./pgo` runs `docker compose run --rm paperless-go …`. It starts PostgreSQL
when needed and hides Docker Compose's progress output. Every `./pgo`
command also works as `docker compose run --rm paperless-go …`, except that
only `./pgo migrate` makes the data directory writable for the container and
`./pgo backup` exists only in the wrapper.
:::

::: details From source instead
To run the code of a branch, or to change it, clone the repository; its
`docker-compose.yml` builds the image locally, and `./pgo` rebuilds it after
code changes:

```sh
gh repo clone pklnx/paperless-go   # the repository is private; uses the gh sign-in above
cd paperless-go
cp .env.example .env
```

The rest is the same. Upgrades then start with `git pull`; see
[Upgrades](./operations#upgrades).
:::

<a id="linux-hosts"></a>

::: info Linux hosts
The containers run as UID 65532. `./pgo migrate` creates `data/` (or
`DATA_DIR`) and makes it writable for that user. Docker Desktop on macOS
needs nothing.

Only a directory that already holds files, for example after a restore,
needs a fix by hand. `./pgo migrate` then prints a warning with the command:

- Docker: `sudo chown -R 65532:65532 data`
- Rootless Docker, where UID 65532 in the container is another UID on the
  host: `docker run --rm --user 0 -v "$PWD/data:/data" alpine chown -R 65532:65532 /data`
- Podman (untested): `podman unshare chown -R 65532:65532 data`
:::
