# Follow-ups

Non-blocking findings from reviews that are not fixed in their PR. Read at session start; remove or mark done when handled.

| Source | Follow-up | Status |
|---|---|---|
| foundation PR | Open follow-ups of pklnx/mail-archive that apply to the copied workflow too: #128 (harden the Claude Action workflow), #129 (tool-neutral CLAUDE.md/briefs), #131 (`check-go-toolchain.sh` message). Port their fixes here when they land there. | open |
| PR #1 Action review, 5 | `scripts/compose-smoke-test.sh` waits for the web server with `curl --retry`: `web` has no healthcheck, so `up --wait` only waits for "running". Add a `paperless-go healthcheck` subcommand (the image is distroless) as the Compose healthcheck and rely on `up --wait`. | open |
| PR #1 Action review, 1 | pklnx/mail-archive's `claude.yml` has the same gap fixed here: the `second-review` job lacks `actions: read`, so `gh pr checks` fails and the reviewer cannot read CI. | open, mail-archive |
