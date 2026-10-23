# Issues, plans and releases

Work goes through four steps: **issue → plan → approval → pull request**.
Nothing is implemented before its plan is approved. The maintainer decides
what gets planned and what gets built; labels carry these decisions.

## 1. Issue

Every change starts as a GitHub issue:

- **Bug report** and **Feature request** templates ask for what is needed
  (steps and version for bugs, problem and "done when" for features).
- Findings from test reports become issues, one per topic, with a link to
  the report.
- Vulnerabilities are reported privately through GitHub private
  vulnerability reporting, never as a public issue (see `SECURITY.md`).
  The label `security` marks public hardening work only.

## 2. Plan

The maintainer labels an issue **`plan`** to ask for an implementation plan
from a Claude session. The plan is posted as a `## Plan` comment on the
issue, and the label changes to **`review`**. A plan covers:

- the approach and the files or components it touches,
- API, database or behavior changes (a schema change means `db-migration`),
- how it will be tested,
- open questions and decisions to make,
- the expected size (one pull request or several).

## 3. Review and approval

An independent plan-reviewer agent (never the planner) reviews the plan and
the maintainer may read it too; both can ask questions or for changes in the
issue, and the plan is updated in place. When the plan is good, the
reviewer or the maintainer replaces `review` with **`ready`**. Only `ready` issues are implemented.

A plan with several parts gets one sub-issue per part, titled
`<what> (#N, part K)`, created by the session planner when the plan is
posted. The `ready` on the main issue covers all parts, and the main issue
closes with the last part.

The order of `ready` issues is chosen during implementation, with a short
reason (dependencies between issues, risk, size). Changes to an approved plan
that come up during the work are noted in the issue before the pull request.

## 4. Pull request

Each issue (or part of a plan) gets its own branch and pull request. The
description says `Closes #N` (and "Part K of #N" for a part), so merging closes
the issue. Labels follow the [label table](./#pull-requests-and-labels). After
an approving review and green required checks, the coordinator merges with
squash; there is no auto-merge.

All reviews run in Claude sessions, local or cloud. Every pull request
with app code gets a session review. Risky pull requests (deletes, schema
changes, rewriting user data, sending documents to external services) also
get a second, independent session review: a separate reviewer agent that does
not see the first review beforehand. The coordinator adds the label
**`second-review`** to each of them, including every pull request that
changes `internal/store/migrations`; the label is a marker and triggers
nothing. Pull requests without app code (docs, CI, test-only) need one
session review and green required checks.

Pull requests that can weaken the secret scan are merged only after the
maintainer's explicit OK, noted on the pull request. This covers
`.gitleaksignore`, `.gitleaks.toml` and files it extends, a new
`gitleaks:allow` comment, `.github/workflows/secrets.yml`,
`scripts/secret-scan-*` and the gitleaks variables and targets in the
`Makefile`. The reviewer flags any such change as blocking until the OK is
there. The OK names the changed entries, never secret values.

Pull requests from forks get the same reviews. GitHub asks the maintainer to
approve workflow runs of outside contributors.

Go toolchain bumps change the `toolchain` line in `go.mod` and the `golang` tag
in the `Dockerfile` together, in one pull request: the image build fails when
they differ (`scripts/check-go-toolchain.sh`), also on a Dependabot bump of
only one of them.

## Labels

| Label | Set by | Meaning |
|---|---|---|
| `plan` | maintainer | Please write an implementation plan (Claude session). |
| `review` | planner | The plan is posted and waits for review. |
| `ready` | plan reviewer or maintainer | The plan is approved; the issue may be implemented. |
| `second-review` | coordinator or maintainer | Risky pull request: needs a second, independent session review (a marker; it triggers nothing). |
| `db-migration` | automatically | Schema change (see Releases). |
| `breaking` | by hand | Manual upgrade steps (see Releases). |
| `feature`, `bug`, `docs`, `chore` | template or by hand | Kind of change. |

Label changes do not notify anyone by themselves: after setting `plan` or
`ready`, say so in the chat with the implementer (for example "plan #18" or
"ready: #18, #19").

## Releases

Releases are published from `main` by hand: **Actions → Release → Run
workflow** (`.github/workflows/release.yml`), after CI on `main` is green. A
dry run builds everything without pushing an image or creating a release.

The version is `YY.MM-N`: year and month (UTC) and a build number that starts
at 1 each month (`scripts/next-version.sh`). The workflow builds the image for
`linux/amd64` and `linux/arm64`, pushes it to `ghcr.io/pklnx/paperless-go`
with the tags `YY.MM-N`, `YY.MM` and `latest`, creates the tag `vYY.MM-N` and
a GitHub release with the Docker Compose bundle (`scripts/release-bundle.sh`)
and notes grouped by label (breaking changes, database migrations, features,
fixes, documentation, dependencies, maintenance; see `.github/release.yml`).
Add upgrade notes for `breaking` and `db-migration` entries to the release
afterwards.

`./pgo --version` and `paperless-go --version` show the version, taken from
`git describe` when the image or binary is built from a clone.
