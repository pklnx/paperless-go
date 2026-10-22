# Contributing

## Setup

You need Go (version in `go.mod`), Docker, Node.js 26 with corepack for the
web UI and the docs, and optionally [golangci-lint](https://golangci-lint.run/)
v2. Node.js 26 no longer ships corepack; install it once with
`npm install -g corepack`.

```sh
make build              # bin/paperless-go (with the web UI if built)
make test-unit          # unit tests, no database needed
docker compose up -d postgres
make test TEST_DATABASE_URL='postgres://paperless:<password>@localhost:5432/paperless?sslmode=disable'
make lint
make vuln               # govulncheck
make web                # build the web UI
make docker-multiarch   # build amd64 + arm64 images
scripts/compose-smoke-test.sh   # end-to-end test of Compose and ./pgo (also in CI)
```

Running `serve` from a host build needs the OCR tools of the image
(poppler-utils, Tesseract with the configured languages): with processing on,
it refuses to start when `tesseract --list-langs` lacks one. Without them, set
`PAPERLESS_GO_OCR_WORKERS=0`; documents then stay queued until `serve` with
the tools or `process` runs. The tests with the real tools run in the image:
`docker build --target toolstest -t paperless-go:toolstest . && docker run --rm paperless-go:toolstest`.

Database tests need PostgreSQL 17 or newer: `docker compose up -d postgres`
gives the same `postgres:18` as Compose and CI, and on an older server every
database test fails in migrate (`PostgreSQL 17 or newer is required`). Any
`postgres:18` container works too, with the locale of new installations:
`docker run -d -p 127.0.0.1:5433:5432 -e POSTGRES_USER=paperless -e POSTGRES_PASSWORD=paperless -e POSTGRES_INITDB_ARGS='--locale-provider=builtin --locale=C.UTF-8' postgres:18`;
without `POSTGRES_INITDB_ARGS` it has the libc locale of existing ones.

Database tests create a throwaway database per test
(`internal/store/storetest`) and are skipped without `TEST_DATABASE_URL`; the
user in it needs `CREATEDB`. CI runs them all.

## Tests

**Tests are mandatory.** Every change to behavior comes with tests, and every
bug fix starts with a test that fails without the fix (a regression test). A
pull request without tests for the behavior it changes is not ready for
review.

- Tests never touch real documents, real credentials or the network. External
  services (Mistral, OpenAI-compatible APIs) are stubbed with `httptest` servers.
- Tests wait for signals, never for time: no `time.Sleep`, no fixed delays. A
  timeout is only the safety net for a signal that never comes.
- Test documents are generated or small files in `testdata/`, never real
  scans.

## Web UI

The UI lives in `web/`: React, TypeScript, Vite and Tailwind CSS, with pnpm
through corepack. `make web` builds it into `internal/web/ui/dist`, which is
embedded into the binary; a plain `go build` without it serves a hint page.

```sh
cd web
corepack enable
pnpm install
pnpm dev        # http://localhost:5173, proxies /api and /healthz to `serve` on :8080
pnpm test
```

UI texts are in `web/src/i18n.ts`, in English and German. The German
dictionary has the type of the English one, so a missing translation fails
the type check. Colors are semantic tokens in `web/src/index.css` (the same
design system as mail-archive), so light and dark mode need no per-component
colors.

pnpm refuses packages released less than a day ago (supply-chain protection).
Keep it that way instead of adding exceptions.

## Documentation

This site is built with [VitePress](https://vitepress.dev/) from `docs/` and
published to GitHub Pages on every merge to `main`. Pull requests that touch
`docs/` build it in CI, which fails on broken links.

```sh
cd docs
pnpm install
pnpm dev        # http://localhost:5173/paperless-go/
```

## Database migrations

Migrations are SQL files in `internal/store/migrations`, managed with
[goose](https://github.com/pressly/goose) and embedded in the binary. Each
file has an `Up` and a `Down` section:

```sql
-- +goose Up
ALTER TABLE documents ADD COLUMN archive_serial_number INTEGER;

-- +goose Down
ALTER TABLE documents DROP COLUMN archive_serial_number;
```

Name new files with the next number. Never edit a migration that has been
released; add a new one. The store tests roll every migration down and up
again, so a broken `Down` section fails CI.

## Database queries

Queries are plain SQL in `internal/store/queries/*.sql`.
[sqlc](https://sqlc.dev/) checks them against the schema and generates
type-safe Go code in `internal/store/db`, which `internal/store` wraps. After
changing a query or adding a migration:

```sh
make generate      # regenerate internal/store/db (commit the result)
make sqlc-check    # what CI runs: vet queries and fail on stale code
```

## Pull requests and labels

Changes start as an issue with an approved plan; see
[Issues, plans and releases](./process).

`main` is protected: changes go through pull requests, which can only be
squash-merged when all CI checks pass. A pull request is merged (squash) after
an approving review and green checks; auto-merge is not used. Agent rules are
in `CLAUDE.md` at the repository root.

| Label | Meaning |
|---|---|
| `feature`, `bug`, `chore`, `docs` | Kind of change (set by hand). |
| `dependencies` | Dependency update (set by Dependabot). |
| `db-migration` | Changes the database schema (set automatically). Back up before upgrading. |
| `breaking` | Needs manual steps when upgrading (set by hand). |
| `plan`, `review`, `ready` | Planning state of an issue; see [Issues, plans and releases](./process). |
| `second-review` | Risky pull request: needs a second, independent review in a Claude session (a marker; it triggers nothing). |

Labels are defined in `.github/labels.json` and synced to GitHub when that
file changes on `main`.

## Flaky tests

A rerun caused by a flaky test is logged in [Flaky tests](./flaky-tests).

## Dependency security

- CI runs `govulncheck`, `go mod verify`, `go vet`, golangci-lint (with
  `gosec`), tests with the race detector, and `pnpm audit` for the UI.
- Dependabot opens weekly update pull requests for Go modules, npm packages
  (UI and docs), GitHub Actions and Docker images. PostgreSQL major versions
  are excluded because they need a data migration.
- GitHub Actions are pinned to commit SHAs; workflows run with read-only
  permissions unless a job needs more.
- Direct Go dependencies are few: pgx, goose and cobra.
