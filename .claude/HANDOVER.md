# Handover

State of the work for the next session. Update before a session ends.

## Open PRs
- Users, passwords, sessions and login UI (#16, part 1 of #3): being implemented (migration 00004).

## Next tickets
- #2 document model: done (#22, #24, #25).
- #3 users and login: `ready`. #16 in progress; then #17 (audit log), #18 (TOTP, breaking), #19 (passkeys), #20 (API keys, security docs).
- #4 (upload) and #5 (processing, OCR): plans being written.
- Backlog: #6 to #12 (roadmap steps 5 to 11), #21 (transfer a user's data; conflict rules are a maintainer decision).

## Maintainer decisions (2026-10-21)
- Owner per document; tags, correspondents, document types and saved views per owner.
- Keep the original file next to the archived PDF.
- User IDs `BIGINT`; documents and metadata UUIDs.
- `serve` refuses to start without `PAPERLESS_GO_SECRET_KEY` once TOTP exists.
- Planners create sub-issues when a plan has parts. 2-3 agents may run at a time.
- Work only on paperless-go (not mail-archive).
- Saved-view names may repeat per owner (as in the app).

## Done (2026-10-21)
- #1: foundation (Go binary, migrations, web server, UI shell, Compose, CI, Action, docs, releases).
- #22: content-addressed blob storage (#13).
- #23: process docs (sub-issues), handover.
- #24: users, documents, tags, correspondents, document types (#14).
- #25: deadlines and saved views (#15).
- Pages enabled (https://pklnx.github.io/paperless-go/), `ANTHROPIC_API_KEY` set.

## Manual checks for the maintainer
- Ruleset for `main` with the required checks (test, lint, vulncheck, docker, compose, web).
