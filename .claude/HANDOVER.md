# Handover

State of the work for the next session. Update before a session ends.

## Open PRs
- Users, documents, tags, correspondents and document types (#14, part 2 of #2): being implemented.

## Next tickets
- #2 document model: part 1 (#13) merged; part 2 (#14) in progress; part 3 (#15) after #14.
- #3 users and login: plan revision 2 posted, waiting for the plan review (`plan-review`). Parts #16 to #20; #16 needs #14.
- Backlog: #4 to #12 (roadmap steps 3 to 11), #21 (transfer a user's data; conflict rules are a maintainer decision).

## Maintainer decisions (2026-10-21)
- Owner per document; tags, correspondents, document types and saved views per owner.
- Keep the original file next to the archived PDF.
- User IDs `BIGINT`; documents and metadata UUIDs.
- `serve` refuses to start without `PAPERLESS_GO_SECRET_KEY` once TOTP exists.
- Planners create sub-issues when a plan has parts. 2-3 agents may run at a time.
- Work only on paperless-go (not mail-archive).

## Done (2026-10-21)
- #1: foundation (Go binary, migrations, web server, UI shell, Compose, CI, Action, docs, releases).
- #22: content-addressed blob storage (#13).
- Pages enabled (https://pklnx.github.io/paperless-go/), `ANTHROPIC_API_KEY` set.

## Manual checks for the maintainer
- Ruleset for `main` with the required checks (test, lint, vulncheck, docker, compose, web).
