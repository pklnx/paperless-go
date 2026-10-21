# Follow-ups

Non-blocking findings from reviews that are not fixed in their PR. Read at session start; remove or mark done when handled.

| Source | Follow-up | Status |
|---|---|---|
| foundation PR | Open follow-ups of pklnx/mail-archive that apply to the copied workflow too: #128 (harden the Claude Action workflow), #129 (tool-neutral CLAUDE.md/briefs), #131 (`check-go-toolchain.sh` message). Port their fixes here when they land there. | open |
| PR #1 Action review, 5 | `scripts/compose-smoke-test.sh` waits for the web server with `curl --retry`: `web` has no healthcheck, so `up --wait` only waits for "running". Add a `paperless-go healthcheck` subcommand (the image is distroless) as the Compose healthcheck and rely on `up --wait`. | open |
| PR #24 Action review | `DeleteTag`: if a root tag is renamed concurrently so that the follow-up `TagRootNameConflict` lookup finds nothing, `TagNameConflictError` has an empty child; the refusal is still right. Handle the empty case in the UI or retry the lookup once. | open |
| PR #24 Action review | `TestConcurrentTagCycle` polls `pg_stat_activity` in a tight loop; add a short backoff or filter on the store connection's PID. | open |
| PR #25 reviews | Add `GetDeadline` when a caller needs it; test that deleting a user with deadlines or saved views is refused; the upcoming-deadlines query should match the partial index `WHERE NOT is_done`. | open |
| Process | PostgreSQL 16 locally, 18 in CI and Compose: SQLSTATEs can differ (ON DELETE RESTRICT: 23001 on 18, 23503 on 16). Assert on both. Docker Hub rate-limits this cloud environment, so a local PostgreSQL 18 container is not available. | note |
| Process | A PR with a merge conflict runs no `pull_request` workflows (no CI, no Action review). Before merging, check `mergeable_state` and the CI run of the head SHA, not only a check-suite notice. | note |
