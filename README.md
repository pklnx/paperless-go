<img src="docs/public/logo.svg" alt="" width="80">

# paperless-go

> [!WARNING]
> **This project is entirely vibe-coded.** All code, configuration and
> documentation are written by an AI (Claude) under human direction, without
> line-by-line human review. Review it yourself before trusting it with
> important documents, and keep independent backups.

`paperless-go` is a self-hosted document archive in the style of
paperless-ngx: scan, recognize, tag and find your documents in a web UI. It
is the server version of [Document Archive](https://github.com/pklnx/document-archive),
the app for iPhone, iPad and Mac, with the same data model and an import and
export in the app's archive folder format.

**Documentation: <https://pklnx.github.io/paperless-go/>**

## Status

Early development. This version is the foundation: one Go binary with the
command line and the web server, PostgreSQL with migrations, the embedded web
UI shell, Docker Compose, CI, releases and login with user name, password,
two-factor login (TOTP) and passkeys. Documents, OCR, search and API keys
follow step by step; see the
[roadmap](https://pklnx.github.io/paperless-go/development/roadmap).

## Planned features

- **Plain files.** Documents are PDF files on disk; metadata and the search
  index live in PostgreSQL.
- **Text recognition** with OCRmyPDF and Tesseract (German and English) on
  your server; Mistral OCR and classification optional.
- **Correspondents, document types, nested tags, ASN** and matching rules for
  automatic assignment, as in the app.
- **Full-text search** with German and English stemming.
- **Users and login** with passkeys, TOTP and API keys, as in
  [mail-archive](https://github.com/pklnx/mail-archive).
- **Docker Compose** setup with images for amd64 and arm64 (Apple Silicon).

## Quick start

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
cp .env.example .env    # set POSTGRES_PASSWORD
./pgo keygen            # set PAPERLESS_GO_SECRET_KEY in .env to its output
./pgo migrate
docker compose up -d web
```

Create the first admin with `./pgo user add NAME --admin`, then open
<http://localhost:8080> and log in; admins set up two-factor login at the
first login. The web UI is only reachable from your own computer, and must
stay so until API keys and the security review are done; see
[Security](https://pklnx.github.io/paperless-go/reference/security).

More in the [getting started guide](https://pklnx.github.io/paperless-go/guide/getting-started).

## Development

See [Contributing](https://pklnx.github.io/paperless-go/development/) for
the toolchain, tests, migrations and the documentation workflow. The
documentation sources are in [`docs/`](docs/).

## License

[MIT](LICENSE)
