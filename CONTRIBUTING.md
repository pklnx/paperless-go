# Contributing

paperless-go is a hobby project and entirely AI-written (see the
[README](README.md)). Contributions and independent review are welcome.

## Bugs and features

Open an issue with the **Bug report** or **Feature request** template. Remove
passwords, API keys, personal data and document contents from logs and
screenshots first.

Work follows **issue → plan → approval → pull request**: the maintainer
decides what gets planned, and only issues labelled `ready` are built (see
[Issues, plans and releases](docs/development/process.md)). For anything
bigger than a small fix, open an issue before you write code.

## Security problems

Never open a public issue for a vulnerability. Report it privately through
GitHub private vulnerability reporting: Security tab → Report a
vulnerability (<https://github.com/pklnx/paperless-go/security/advisories/new>).
See [SECURITY.md](SECURITY.md).

## Pull requests

- One topic per pull request, with `Closes #N` in the description.
- Every behaviour change has tests; a bug fix has a regression test that
  fails without the fix. Tests never touch real documents, real credentials
  or the network, and they wait for signals, never for time.
- Before you push, run the checks of what you changed (see
  [Contributing](docs/development/index.md) for the toolchain):
  - `go test -race ./...` (`make test` with a test database for the
    PostgreSQL tests), `gofmt` and `make lint` (golangci-lint);
  - `pnpm test` and `pnpm typecheck` in `web/` for UI changes;
  - `pnpm build` in `docs/` for documentation changes.
- CI must be green: `test`, `lint`, `vulncheck`, `docker`, `toolstest`,
  `compose` and `web`, plus the docs build for documentation changes.
- The maintainer merges with squash after an approving review.

### Pull requests from forks

- GitHub asks the maintainer to approve workflow runs for outside
  contributors, so CI on a fork pull request starts only after that
  approval.
- Fork pull requests get the same reviews as any other pull request: a
  review in a Claude session, and a second, independent session review when
  the change is risky.

## License

By contributing you agree that your contribution is licensed under the
[MIT License](LICENSE).
