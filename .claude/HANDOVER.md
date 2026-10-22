# Handover

State of the work for the next session. Update before a session ends.

## Open PRs
- #47 consume folder (#31, part 4 of #4): both reviews approve; the data-loss fixes (duplicate without a document keeps the file, completeness check before import) are under re-review. Needs a merge of main after #46 before it can merge.
- API keys and security docs (#20, part 5 of #3): being implemented (migration 00009).

## Next tickets
- #3 users and login: parts 1–4 merged (#29, #38, #40, #45); #20 is the last part.
- #4 upload and import: parts 1–3 and 5 merged (#41, #43, #44, #46); #31 (consume folder) in review.
- #5 processing and OCR: parts 1–2 merged (#37, #42); next #35 (OCR, image to PDF; see its comments: requeue the jobs part 2 failed, `heif-dec`, `libheif-plugin-x265` in `toolstest`), then part 4 (thumbnails).
- #39 (admin changes in the user's own log), #48 (passkey ceremony caps), #49 (`system` audit actor): need plans.
- Backlog: #6 to #12 (roadmap steps 5 to 11), #21 (transfer a user's data).

## Maintainer decisions (2026-10-21)
- Owner per document; tags, correspondents, document types and saved views per owner.
- Keep the original file next to the archived PDF; the archive is a plain PDF, not PDF/A.
- User IDs `BIGINT`; documents and metadata UUIDs.
- `serve` refuses to start without `PAPERLESS_GO_SECRET_KEY`.
- Upload limit 100 MiB, configurable (`PAPERLESS_GO_MAX_UPLOAD_SIZE`); images at most 50 megapixels.
- Consume folder: `consume/<username>/`; imported files and duplicates are deleted; failures go to `failed/`.
- Classification (#9) through an OpenAI-compatible API; OCR stays Tesseract or Mistral OCR; set up per server by an admin; off until each user agrees.
- Users see admin changes to their own account in their log (#39).
- Saved-view names may repeat per owner.
- Planners create sub-issues when a plan has parts. 2-3 agents may run at a time. Work only on paperless-go.

## Done (2026-10-21/22)
- #1 foundation; #22–#25 document model (#2); #29 users and sessions (#16); #37 Debian image with OCR tools (#33); #38 audit log (#17); #40 TOTP and secret key (#18, `breaking`); #41 store hardening (#27); #42 processing queue (#34); #43 ingest (#28); #44 CLI import (#30); #45 passkeys (#19); #46 upload API and UI (#32).

## Manual checks for the maintainer
- Ruleset for `main` with the required checks (test, lint, vulncheck, docker, compose, web; `toolstest` recommended).
- After updating: `./pgo keygen` into `.env`, then `./pgo migrate`. Try TOTP with a real authenticator app, and a passkey at an HTTPS `PAPERLESS_GO_PUBLIC_URL`.
- Drag and drop in Safari and Firefox; a ~100 MB upload through a reverse proxy.

## Notes for sessions
- golangci-lint matching CI: `GOTOOLCHAIN=$(go env GOVERSION) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0`, then `~/go/bin/golangci-lint run ./...`.
- Local PostgreSQL: `TEST_DATABASE_URL='postgres://paperless:paperless@localhost:5432/paperless?sslmode=disable'`.
- Web: use `pnpm` in `web/`; never `npx prettier` or `tsc -b` (they rewrite or emit files).
