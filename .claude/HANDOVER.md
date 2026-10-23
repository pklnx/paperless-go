# Handover

State of the work for the next session. Update before a session ends.

## Open PRs
- #100 (#92, iPhone guide): approved; waits for the maintainer's device checks listed in the PR body (raw bytes vs multipart is the key one). Don't merge before.

## Next tickets
- First release v26.10-1 published 2026-10-23 (dry run, then release; ghcr.io package public, #149 removed the "no release yet" notes). Every further release needs the maintainer's explicit OK; run a dry run first. The release body has only the generated notes (no tool here edits releases).
- Follow-up ticket: a search time limit for the bulk match (`MatchDocumentIDs`), which runs under the owner lock (maintainer: after the release).
- Backlog (step numbers as in #100's roadmap, valid once #100 merges; main and the bodies of #8–#11 still use the old numbers): roadmap 9 archive import/export (#10), 10 automatic assignment (#8), 11 external OCR/classification (#9), 12 deadlines (#11), 14 native iPhone app in its own repository (later); #21 (transfer a user's data). #12 (notes, saved views, trash) is done.

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
- The repository is public (#61); no release yet (#63, superseded by v26.10-1 on 2026-10-23).
- No Claude GitHub Action any more (#65): plans, plan reviews and reviews run in Claude sessions on the subscription. Risky PRs get a second, independent session reviewer; `second-review` is a marker. The Action ran Claude Code on the API key, and the plan's API credits do not cover Claude Code.
- PostgreSQL: Debian image `postgres:18` (musl ranked full-text search about 4× slower); minimum PostgreSQL 17; existing databases keep their sort order, new ones use builtin `C.UTF-8` (#66).

## Maintainer decisions (2026-10-22, evening)
- #7 web UI: no delete before the trash (#12); admins see their own documents with an "Everyone's documents" switch and may edit others' (audited, field names only); `document.search` only for typed queries; bulk also over "all matches" with a dry-run count and confirmation; pdf.js.
- #49 server audit entries: summary per scan; "interrupted" hint shown; late entries keep the write time (both chat decisions, recorded in #85's body).
- Metadata names refuse control characters (#84).
- iPhone (#87): Apple's scanner + Shortcut upload; instructions plus an optional iCloud link; scope `upload` (reads nothing; `write` implies it); scans land in the inbox, title = file name; an upload-only key's duplicate answer is `{"duplicate": true}` only (chat decision, recorded on PR #96). A native app comes later in its own repository; pklnx/document-archive stays as it is.
- Public internet (#88): keep the per-name block (passkeys still work); one audit row per address and window for unknown keys; fail2ban and Caddy only documented.
- Locking: after four rounds of deadlock holes, rebuild on a per-owner advisory lock (#102); serialisation cost accepted within the limits above.
- #105: on a conflicting save, reload and keep the user's edits on top; #106: several owners are explained before sending.
- Model use (chat): Fable for large plans, Opus for routine plans, building and reviews.

## Coordinator decisions (night 2026-10-22/23, delegated by the maintainer; reversible)
- #56: plan's recommendations (trigram vocabulary per owner, no compound dictionary, infix from 4 letters, cap 100 words per term, false hits like "Berechnung" for "rechnung" accepted), comment on #56.
- #117: 15 s bound for API requests, `Retry-After: 5`; background writers unbounded; no separate pool; gate per process (comment on #117).
- Merge order kept: #109 → #108 → #117 → #114.

## Maintainer decisions (2026-10-23)
- #120: the missed plan thresholds are accepted. The night's coordinator decisions are confirmed.
- #12 trash: not in normal search, only in the trash view. A duplicate of a trashed file (consume, import and upload-only keys) restores it automatically. Retention 30 days, configurable (`PAPERLESS_GO_TRASH_RETENTION`, default 720h). A single delete has an undo notice, no dialog. Emptying a trash over 5 000 documents: a hint, delete in parts.
- Notes autolinks like the app; bare domains without `www` are not linked; `&amp;` in URLs is decoded after the check.
- #115: a cap of about 250 compounds per search and a 30 s limit answering "search too broad"; stem prefixes stay off; the 32-word AND case (about 3.3 s, the parser's maximum) is accepted; a time limit for the bulk match is a follow-up.
- Release after #120/#115 only with the maintainer's explicit OK.

## Done (2026-10-23)
- Release v26.10-1; #149 (no-release notes removed).
- #118 (#117), #119, #120 (#114), #122, #123 (#121 `./pgo restore`), #129 (#128 Markdown notes), #130 (smoke cleanup), #132 (#124 trash store/API), #133 (#131 autolinks), #135 (#126 trash UI), #136 (#125 blob removal, `./pgo sweep`), #139 (#127 saved views), #141 (#137 flaky test), #144 (#134), #145 (#142), #146 (#138), #147 (#143), #140 (#115; #56 closed).
- #12 closed (all 5 parts).

## Done (night 2026-10-22/23)
- #109 (#105), #116 (#108, #102 closed: per-owner advisory lock, 0 deadlocks in S1–S3), #118 (#117: per-owner in-process gate + bounded wait), #119 (flaky DocumentList poll test).

## Done (2026-10-22, evening)
- #81 (#49), #82 (#75), #83/#89/#86/#104/#101 (#7 parts 1–5, #7 closed), #90 (#84), #95 (#85), #96 (#91), #97/#98 (#88 closed), #103 (#99), #110 (test fix), #111 (#106), #112 (#107).
- Earlier that day, after #69: #70 thumbnails (#36), #72 UI collation (#68), #73 passkey caps (#48), #74 thumbnail timeout (#71).

## Done (2026-10-22)
- #51 API keys (#20); #55 OCR and images (#35); #57, #59, #62 Action fixes, then #65 removed the Action; #58, #64, #67 search (#52–#54); #66 PostgreSQL Debian image, index rebuild on collation change, minimum PostgreSQL 17; #60 admin changes in the user's log (#39); #61 public repository; #63 no-release note.

## Done (2026-10-21)
- #1 foundation; #22–#25 document model (#2); #29 users and sessions (#16); #37 Debian image with OCR tools (#33); #38 audit log (#17); #40 TOTP and secret key (#18, `breaking`); #41 store hardening (#27); #42 processing queue (#34); #43 ingest (#28); #44 CLI import (#30); #45 passkeys (#19); #46 upload API and UI (#32); #47 consume folder (#31).

## Manual checks for the maintainer
- Ruleset for `main` with the required checks (test, lint, vulncheck, docker, compose, web; `toolstest` recommended): part of the public-switch steps; confirm it is active.
- Trusted proxies behind Tailscale or Caddy (`PAPERLESS_GO_TRUSTED_PROXIES`) so the `__Host-` cookie is kept (#51).
- After updating: `./pgo keygen` into `.env`, then `./pgo migrate`. Try TOTP with a real authenticator app, and a passkey at an HTTPS `PAPERLESS_GO_PUBLIC_URL`.
- Drag and drop in Safari and Firefox; a ~100 MB upload through a reverse proxy.

## Notes for sessions
- golangci-lint matching CI: `GOTOOLCHAIN=$(go env GOVERSION) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0`, then `~/go/bin/golangci-lint run ./...`.
- PostgreSQL 17 or newer is required (#66). The cloud sandbox's local PostgreSQL is 16 and can no longer run the store tests: start `dockerd` and run `postgres:18` (Docker Hub rate-limits; `mirror.gcr.io/library/postgres:18` works), then point `TEST_DATABASE_URL` at it.
- Cloud sessions have no `gh`: use the GitHub MCP tools from the GitHub operations table in `CLAUDE.md`; the coordinator subscribes to PR activity and arms one safety check-in.
- Web: use `pnpm` in `web/`; never `npx prettier` or `tsc -b` (they rewrite or emit files).
- The container can restart: docker stops (start `dockerd` again), /var/tmp worktrees survive.
- Docker Hub can answer 500 on image pulls in CI (seen 2026-10-23 on main, compose job); rerun only per the flaky-test rule.
