# Handover

State of the work for the next session. Update before a session ends.

## Open PRs
- #66 PostgreSQL `postgres:18` (Debian) instead of Alpine, with an automatic index rebuild on a collation change (migration 00013, `second-review`, `breaking`, minimum PostgreSQL 17). Both first reviews asked for changes; fixed in `8db3171`; a round-2 review is under way.

## Next tickets
- #3 users and login: done (#29, #38, #40, #45, #51).
- #4 upload and import: done (#41, #43, #44, #46, #47).
- #5 processing and OCR: parts 1–3 merged (#37, #42, #55); part 4 is #36 (thumbnails).
- #6 full-text search: done (#58, #64, #67). Follow-ups: #56 (compound words), #68 (UI sort with `Intl.Collator`), and the follow-ups in the #67 decision comment.
- #39 done (#60).
- #48 (passkey ceremony caps), #49 (`system` audit actor): need plans.
- Backlog: #7 to #12 (roadmap steps 6 to 11), #21 (transfer a user's data).

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

## Maintainer decisions (2026-10-22)
- Search (#6): sort by relevance by default, except a query with only excluded words (newest added); `date:`/`created:` = document date, `added:` = upload; day bounds in the browser's time zone, else UTC; `document.search` is audit-logged with the query; broad relevance at about 2 s is accepted until #7. Compound words later (#56).
- #39: users see all entries whose target is themselves, details reduced to an allowlist; `audit export --user` unchanged.
- #55: a PDF that opens without a password and allows changes is OCRed; its archive is unencrypted.
- The repository is public (#61); no release yet (#63).
- No Claude GitHub Action any more (#65): plans, plan reviews and reviews run in Claude sessions on the subscription. Risky PRs get a second, independent session reviewer; `second-review` is a marker. The Action ran Claude Code on the API key, and the plan's API credits do not cover Claude Code.
- PostgreSQL: Debian image `postgres:18` (musl ranked full-text search about 4× slower); minimum PostgreSQL 17; existing databases keep their sort order, new ones use builtin `C.UTF-8` (#66).

## Done (2026-10-22)
- #51 API keys (#20); #55 OCR and images (#35); #57, #59, #62 Action fixes, then #65 removed the Action; #58, #64, #67 search (#52–#54); #60 admin changes in the user's log (#39); #61 public repository; #63 no-release note.

## Done (2026-10-21)
- #1 foundation; #22–#25 document model (#2); #29 users and sessions (#16); #37 Debian image with OCR tools (#33); #38 audit log (#17); #40 TOTP and secret key (#18, `breaking`); #41 store hardening (#27); #42 processing queue (#34); #43 ingest (#28); #44 CLI import (#30); #45 passkeys (#19); #46 upload API and UI (#32); #47 consume folder (#31).

## Manual checks for the maintainer
- Ruleset for `main` with the required checks (test, lint, vulncheck, docker, compose, web; `toolstest` recommended): part of the public-switch steps; confirm it is active.
- After the first release: set the ghcr.io package to public, then remove the "no release yet" notes (#63).
- Trusted proxies behind Tailscale or Caddy (`PAPERLESS_GO_TRUSTED_PROXIES`) so the `__Host-` cookie is kept (#51).
- After updating: `./pgo keygen` into `.env`, then `./pgo migrate`. Try TOTP with a real authenticator app, and a passkey at an HTTPS `PAPERLESS_GO_PUBLIC_URL`.
- Drag and drop in Safari and Firefox; a ~100 MB upload through a reverse proxy.

## Notes for sessions
- golangci-lint matching CI: `GOTOOLCHAIN=$(go env GOVERSION) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0`, then `~/go/bin/golangci-lint run ./...`.
- PostgreSQL 17 or newer is required (#66). The cloud sandbox's local PostgreSQL is 16 and can no longer run the store tests: start `dockerd` and run `postgres:18` (Docker Hub rate-limits; `mirror.gcr.io/library/postgres:18` works), then point `TEST_DATABASE_URL` at it.
- Cloud sessions have no `gh`; the coordinator subscribes to PR activity and arms one safety check-in.
- Web: use `pnpm` in `web/`; never `npx prettier` or `tsc -b` (they rewrite or emit files).
