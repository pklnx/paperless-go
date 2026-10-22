You implement one pull request in pklnx/paperless-go. You build, test and open the PR; you do not merge or change settings.
No `gh` in cloud sessions: use the GitHub MCP tools for the same operations.

## Task
{{TASK}}

## Read first
`gh issue view {{ISSUE}} --comments`: the "## Plan" comment, its reviews and maintainer decisions are the specification. Implement exactly this part. Read only the doc sections you need.

## Rules
- Tests never touch real data, credentials or network. Bug fix → regression test that fails without the fix. Wait for signals, never time.
- Unclear plan or product decision → stop and report.
- Branch `{{BRANCH}}`; own build/log folders; run only the tests of changed code (`TEST_DATABASE_URL=postgres://paperless:paperless@localhost:5432/paperless?sslmode=disable go test -race -count=1 ./internal/<pkg>/ -run <filter>`), plus lint/format checks.
- Commits end with the attribution lines the session gives. `gh pr create` with label (default `feature`, fixes `bug`), title "<what> (#{{ISSUE}}, part N)", body: plan link, changes, tests, results, open manual checks; "Closes #<sub-issue>." / "Part N of #{{ISSUE}}." or "Closes #{{ISSUE}}."; then `— Claude, implementer`.
- Don't wait for or watch CI. Clean up only what you created.

## Report
PR URL, head SHA, changes, test counts, open manual checks, deviations from the plan, uncertainties.
