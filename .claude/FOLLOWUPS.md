# Follow-ups

Non-blocking findings from reviews that are not fixed in their PR. Read at session start; remove or mark done when handled.

| Source | Follow-up | Status |
|---|---|---|
| foundation PR | Open follow-ups of pklnx/mail-archive that apply to the copied workflow too: #128 (harden the Claude Action workflow), #129 (tool-neutral CLAUDE.md/briefs), #131 (`check-go-toolchain.sh` message). Port their fixes here when they land there. | open |
| PR #1 Action review, 5 | `scripts/compose-smoke-test.sh` waits for the web server with `curl --retry`: `web` has no healthcheck, so `up --wait` only waits for "running". Add a `paperless-go healthcheck` subcommand (or, since #37 the image has a shell, a curl-free check) as the Compose healthcheck and rely on `up --wait`. | open |
| PR #24 Action review | `DeleteTag`: if a root tag is renamed concurrently so that the follow-up `TagRootNameConflict` lookup finds nothing, `TagNameConflictError` has an empty child; the refusal is still right. Handle the empty case in the UI or retry the lookup once. | open |
| PR #24 Action review | `TestConcurrentTagCycle` polls `pg_stat_activity` in a tight loop; add a short backoff or filter on the store connection's PID. | open |
| PR #25 reviews | Add `GetDeadline` when a caller needs it; test that deleting a user with deadlines or saved views is refused; the upcoming-deadlines query should match the partial index `WHERE NOT is_done`. | open |
| Process | PostgreSQL 16 locally, 18 in CI and Compose: SQLSTATEs can differ (ON DELETE RESTRICT: 23001 on 18, 23503 on 16). Assert on both. Docker Hub rate-limits this cloud environment, so a local PostgreSQL 18 container is not available. | note |
| Process | A PR with a merge conflict runs no `pull_request` workflows (no CI, no Action review). Before merging, check `mergeable_state` and the CI run of the head SHA, not only a check-suite notice. | note |
| PR #43 review | HEIC detection is tested only on generated headers (including an iPhone-style grid); check a real iPhone HEIC once. A JPEG with more than 1 MiB of metadata before the image header is refused as unreadable. | open |
| PR #44 review | `import --dry-run` can name a different refusal reason than the real import (size before type); no tests for the `./pgo import` argument checks (`:`/`,`, a file, relative DIR). A symlinked PATH root is followed on purpose. | open |
| PR #46 reviews | `maxUploadSize` comes with `GET /api/documents`; a file dropped before the list loads is checked only by the server. A "documents" group for the log filter. | open |
| PR #42 reviews | `TestWorkerTimeout` stops before a tool runs; tool cancellation is covered by `TestToolsCancel` only. | open |
| Process | Push before opening a PR only what `git status` shows as intended: a stray `paperless-go` binary was committed once (#44); `/paperless-go` is now ignored. | note |
