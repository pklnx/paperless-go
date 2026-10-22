# Workflow

Roles: maintainer (human) decides, sets `plan` and may set `ready`; coordinator (Claude, Opus) plans the work, delegates to subagents, merges; implementers (Opus for features, Sonnet for mechanical work) build one PR each; reviewers (Opus for risky/large, Sonnet for small) review only; an independent plan-reviewer agent (never the planner) reviews plans. All Claude work (planning, plan reviews, implementation, reviews) runs in Claude sessions, local or cloud; there is no Claude GitHub Action.

Language: chat with the maintainer in German; everything on GitHub and in code in English. Every GitHub text ends with `— Claude, <role>` (no model names on GitHub).

Cloud sessions have no `gh` CLI: the GitHub MCP tools replace the `gh` commands, and the coordinator is woken by the PR activity subscription instead of a background `gh pr checks --watch`.

## Product
A self-hosted server version of the Apple app [pklnx/document-archive](https://github.com/pklnx/document-archive) (paperless-ngx style): Go, PostgreSQL, document files on disk, React web UI. Its own server, no live sync with the app; the data model follows the app's, and import/export uses the app's archive folder format (`DocumentArchive/Services/ArchiveFolderFormat.swift` there). OCR runs locally (OCRmyPDF/Tesseract, German and English); Mistral OCR is optional with an API key; classification is optional through an OpenAI-compatible API, set up per server by an admin and off until each user agrees. Users, login and security follow [pklnx/mail-archive](https://github.com/pklnx/mail-archive), whose code patterns are reused where they fit. Roadmap: [`docs/development/roadmap.md`](docs/development/roadmap.md).

## Ticket flow
1. Issue (template). 2. Maintainer labels `plan` (session planner) → `## Plan` comment (approach, files, data model, external services, tests, open questions, size), label becomes `review`. 3. Independent plan review: a plan-reviewer agent in a session (never the planner); an approving reviewer or the maintainer replaces `review` with `ready`; only `ready` issues are built.
4. Plan with several parts: one sub-issue per part, created when the plan is posted (maintainer rule) by the session planner; title "<what> (#N, part K)", linked as GitHub sub-issue (`gh api -X POST repos/pklnx/paperless-go/issues/N/sub_issues -F sub_issue_id=<issue .id>`; in cloud sessions `issue_write` with `parent_issue_number`). The main issue's `ready` covers them; it closes with the last part.
5. One branch + PR per part; body "Closes #<sub>." and "Part K of #N.".
6. Review: session reviewer always for app code; risky PRs (deletes, schema changes (`db-migration`), rewriting user data, sending documents to external services) get a second, independent session reviewer, a separate reviewer agent that does not see the first review beforehand: the coordinator adds `second-review` to every risky PR (also every migration PR) as a marker; the label triggers nothing. PRs without app code (docs, CI, test-only): one session review + green required checks.
7. Merge: approving review + green required checks; `gh pr merge <n> --squash --match-head-commit <sha>`. Never auto-merge, never change rulesets or repo settings.

Blocking in reviews only: crash, data loss/corruption, hang, wrong result in a main function, security/privacy problem, broken main flow, missing/failing test for new behaviour, violation of plan or maintainer decision. Everything else is a follow-up.

## Rules
- Tests never touch real user data, real documents, real credentials or the network (no real Mistral or OpenAI-compatible calls: stub the API); secrets only in the secret store, never in chat or files.
- Every behaviour change has tests; a bug fix has a regression test that fails without the fix. Tests wait for signals, never for time (no sleeps/fixed delays).
- Locally run only the tests of what changed; CI runs everything.
- Agents never watch CI: push, open the PR, report. The coordinator gets woken per PR by a background `gh pr checks <n> --watch --interval 120 >/dev/null 2>&1; gh pr checks <n>` (wait until the checks appear first).
- ≤3 tickets in parallel (2-3 agents at a time), ≤2 PRs in CI at a time.
- A flaky-test rerun gets a row in `docs/development/flaky-tests.md`; a test listed twice is fixed right away.
- After 3 review rounds that keep finding new holes in a heuristic: stop patching, rebuild it on a principle or give the maintainer options.
- Ask instead of guessing on product decisions (via AskUserQuestion, recommendation first).

## Session handover
At session start read `.claude/HANDOVER.md` and `.claude/FOLLOWUPS.md`. Update `HANDOVER.md` before a session ends.

Pointers: [`docs/development/process.md`](docs/development/process.md), [`docs/development/architecture.md`](docs/development/architecture.md), [`.claude/briefs/implementer.md`](.claude/briefs/implementer.md), [`.claude/briefs/reviewer.md`](.claude/briefs/reviewer.md).
