# Flaky tests

Every rerun of a CI job because of a flaky test gets a row here. A test listed twice is fixed right away.

| Date | Test | CI run | Cause | Status |
|---|---|---|---|---|
| 2026-10-22 | CI `build` job, `pnpm audit` | https://github.com/pklnx/paperless-go/actions/runs/39740813000 (#51, `7c71628`) | `ERR_PNPM_AUDIT_BAD_RESPONSE`: the npm audit endpoint timed out after 4 minutes; external, no test involved. The re-run passed. | rerun once |
