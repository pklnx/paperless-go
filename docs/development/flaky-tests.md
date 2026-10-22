# Flaky tests

Every rerun of a CI job because of a flaky test gets a row here. A test listed twice is fixed right away.

| Date | Test | CI run | Cause | Status |
|---|---|---|---|---|
| 2026-10-22 | Docs workflow `build` job, `pnpm audit` step | https://github.com/pklnx/paperless-go/actions/runs/39740813000 (#51, `7c71628`) | `ERR_PNPM_AUDIT_BAD_RESPONSE`: the npm audit endpoint timed out after 4 minutes; external, no test involved. The re-run passed. | rerun once |
| 2026-10-22 | `web/src/App.test.tsx` "shows a healthy server after login" | https://github.com/pklnx/paperless-go/actions/runs/39826152560 (#89, `874d09b`) | `waitFor` gave up after testing-library's default 1 s while the stubbed health check had not rendered yet (the run's jsdom setup took 65 s in total: a busy runner). | fixed in #89: `asyncUtilTimeout` 4.5 s in `web/src/testSetup.ts`, below vitest's 5 s `testTimeout` |
| 2026-10-22 | `web/src/MetadataPage.test.tsx` "opens from the sidebar and lists the tag tree…" | https://github.com/pklnx/paperless-go/actions/runs/39848601037 (#109, `10008d4`) | The test called `getByRole("tabpanel")` synchronously right after the sidebar click; on a busy runner the metadata view had not rendered yet. Not caused by #109 (passes locally on main and on the branch). | fixed: the test waits for the panel with `findByRole` |
