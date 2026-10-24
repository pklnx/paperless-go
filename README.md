<h1>
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/public/paperless-go-lockup-dark.svg">
    <img src="docs/public/paperless-go-lockup-light.svg" alt="paperless-go" height="48">
  </picture>
</h1>

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

Early development. The first release has one Go binary with the command
line and the web server, PostgreSQL with migrations, Docker Compose, login
with user name, password, two-factor login (TOTP), passkeys and API keys,
uploads, OCR, full-text search, the document list and detail views, bulk
changes, notes, saved views and a trash. Archive import and export,
automatic assignment, external OCR and classification, and deadlines follow;
see the [roadmap](https://pklnx.github.io/paperless-go/development/roadmap).

## Planned features

- **Plain files.** Documents are PDF files on disk; metadata and the search
  index live in PostgreSQL.
- **Text recognition** with OCRmyPDF and Tesseract (German and English) on
  your server; optional Mistral OCR, and optional classification through an
  OpenAI-compatible API.
- **Correspondents, document types, nested tags, ASN** and matching rules for
  automatic assignment, as in the app.
- **Full-text search** with German and English stemming.
- **Users and login** with passkeys, TOTP and API keys, as in
  [mail-archive](https://github.com/pklnx/mail-archive).
- **Docker Compose** setup with images for amd64 and arm64 (Apple Silicon).

## Quick start

Download the Docker Compose bundle of the latest release, check it and
extract it. The first `./pgo` command pulls the images from `ghcr.io`
without a sign-in, as does `docker pull ghcr.io/pklnx/paperless-go:latest`
without `docker login` (this needs the package to be public).

```sh
curl -fLO https://github.com/pklnx/paperless-go/releases/latest/download/paperless-go.tar.gz
curl -fLO https://github.com/pklnx/paperless-go/releases/latest/download/SHA256SUMS
sha256sum -c SHA256SUMS   # macOS: shasum -a 256 -c SHA256SUMS
tar -xzf paperless-go.tar.gz
cd paperless-go
cp .env.example .env    # set POSTGRES_PASSWORD
./pgo keygen            # set PAPERLESS_GO_SECRET_KEY in .env to its output
./pgo migrate
docker compose up -d web
```

Create the first admin with `./pgo user add NAME --admin`, then open
<http://localhost:8080> and log in; admins set up two-factor login at the
first login. The web UI is only reachable from your own computer; to use it
from your network or a VPN, put HTTPS in front as described in
[Security](https://pklnx.github.io/paperless-go/reference/security#network-access).
For a public address, read [Running on the
internet](https://pklnx.github.io/paperless-go/guide/internet) first.

More in the [getting started guide](https://pklnx.github.io/paperless-go/guide/getting-started).

## Development

Contributions are welcome; read [CONTRIBUTING.md](CONTRIBUTING.md) first.
See [Contributing](https://pklnx.github.io/paperless-go/development/) for
the toolchain, tests, migrations and the documentation workflow. The
documentation sources are in [`docs/`](docs/).

## License

[MIT](LICENSE). The logo and app icons are not under the MIT License; they are
covered by [BRAND-ASSETS.md](BRAND-ASSETS.md).
The web UI bundles third-party packages and the IBM Plex fonts; their licenses
are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
