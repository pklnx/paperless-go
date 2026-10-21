# Handover

State of the work for the next session. Update before a session ends.

## Open PRs
- The foundation PR (branch `claude/bold-ride-dd4cn5`): repository set up after pklnx/mail-archive.

## Next tickets
- Create issues for the roadmap steps in `docs/development/roadmap.md`, starting with 1 (document model and file storage) and 2 (users and login), and label them `plan` or `needs-plan`.

## Done (2026-10-21)
- Foundation: Go binary (`migrate`, `serve`, `prepare-data`), PostgreSQL migrations (goose) and queries (sqlc), web server with Host and same-origin checks and `/healthz`, React UI shell, Docker Compose with `./pgo`, CI, Claude GitHub Action, docs site, release workflow, labels, Dependabot.

## Manual checks for the maintainer
- Repository secret `ANTHROPIC_API_KEY` for the Claude GitHub Action.
- Settings → Pages → Source "GitHub Actions" for the docs site (a private repository needs a plan with Pages for private repositories).
- Run "Sync labels" once (Actions → Sync labels → Run workflow), or it runs on the first change to `.github/labels.json` on `main`.
- Branch protection / ruleset for `main` with the required checks (test, lint, vulncheck, docker, compose, web), as in mail-archive.
- After the workflow is set up, test each Claude Action trigger once (`needs-plan`, `plan-review`, `second-review`, `@claude`).
