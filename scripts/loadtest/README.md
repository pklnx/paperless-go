# Load test

`scripts/loadtest` drives a local paperless-go server with concurrent writes
and checks the database afterwards. It rebuilds the load test of review B of
PR #103 and adds the scenarios of the #102 plan (section 6), so the locking
can be measured before and after a change on the same host.

It is a tool for developers, not a test: it is built only with the
`loadtest` build tag, so `go build ./...`, `go vet ./...`, `go test ./...`
and CI never compile it.

**Never point it at a real server or database.** It creates users, writes
thousands of documents and deletes metadata. It refuses:

- a server or database host that is not a loopback address or a Unix socket
  (`-allow-non-loopback` overrides; there is no reason to use it on a real
  installation);
- a database that already has users or documents: it only runs on a fresh,
  throwaway database that it fills itself.

## Scenarios

| | What runs | What it measures |
|---|---|---|
| S1 | Review B's mix: users `alice` (3 000 generated documents) and `bob` (1 000); 21 threads (two of three are alice's) for 240 s | Comparability with review B's numbers |
| S2 | The same mix, all 21 threads as alice | One user's throughput when their writes serialise (#102) |
| S3 | `alice` (5 000 documents, out of the inbox) and `bob` (1 000) each upload 50 PDFs of 20 KiB, one every 200 ms: first alone (baseline), then while a loop of alice's bulk changes by match over her 5 000 documents runs (`inbox: false`; favourite on, add a tag, favourite off, remove the tag) | How long an upload of the same owner and of another owner waits behind a bulk change |

Review B's mix, per thread and step: 30 % bulk changes (half by match:
`inbox`, `q=invoice`, a tag or everything; half by 20–500 random IDs; with
`addTags`, `removeTags`, correspondent or type set or cleared, `addTags` +
correspondent, or `favorite`), 35 % single PATCHes (`tagIds`,
`correspondentId`, `documentTypeId`, set or null), 35 % metadata work
(replenishing 6 top-level tags × 3 sub-tags, 5 correspondents and 5 types per
user through the API; concurrent parent + child tag deletes; single tag,
correspondent and type deletes; renames; tag moves, including some into the
tag's own sub-tag, which answer `409 tag_cycle`). Every user's metadata is
created through the API before the load starts. Each thread has its own
session cookie.

Expected 4xx are part of the mix: `400 owner_mismatch` (the target was
deleted meanwhile), `404` (already deleted), `409 tag_cycle`,
`409 tag_name_conflict`, `409 name_taken`, and in the fingerprint flow
`409 match_changed` (the match changed between the dry run and the change).
The summary lists every 4xx by code.

## Bulk flow

- `-bulk-flow fingerprint` (default): every bulk change by match first sends
  the dry run `{match, dryRun: true}` and passes the answer's `fingerprint`
  with the change, as the UI does after #106. A server before #106 answers
  without a fingerprint; the change is then sent without one, so this flow
  works against both. Dry runs are counted on their own (`bulk_dry_run`) and
  not in the request total.
- `-bulk-flow legacy`: the change by match is sent alone, without a dry run.
  This is the flow of review B and of servers before #106; a server with #106
  refuses it (`400 invalid_selection`, and the summary says so).

Compare before and after with the same flow on both sides when both servers
support it; the #102 before numbers on `8f16b58` use `legacy`.

## Pinned setup

| | Value |
|---|---|
| PostgreSQL | `mirror.gcr.io/library/postgres:18@sha256:edb0c08dc33befd0262ab3b5cf1c4f3f8cfd8b09e88c05b45adee29d6761ddb6` (18.6), started with `-c log_lock_waits=on`, otherwise defaults |
| Server | the real `paperless-go serve` with `PAPERLESS_GO_OCR_WORKERS=0`, no consume folder, its default database pool |
| Seed | `-seed 1` (the request mix; the documents are generated the same way on every run) |
| S1/S2 | `-threads 21 -duration 240s` |
| S3 | `-uploads 50 -upload-size 20480 -upload-interval 200ms` |

The report records the host (CPU, cores, RAM, kernel, load average at the
start), the PostgreSQL version and settings, and every parameter. Run before
and after on the same host in the same session (plan review of #102,
must-do 7), and keep the host otherwise idle: other builds or databases on
the machine change the numbers.

## Running it

Start a throwaway PostgreSQL on a loopback port:

```sh
docker run -d --name pgload -e POSTGRES_USER=loadtest -e POSTGRES_PASSWORD=loadtest \
  -p 127.0.0.1:55432:5432 \
  mirror.gcr.io/library/postgres:18@sha256:edb0c08dc33befd0262ab3b5cf1c4f3f8cfd8b09e88c05b45adee29d6761ddb6 \
  -c log_lock_waits=on
```

Build the server to measure and the script:

```sh
git worktree add /tmp/pg-before 8f16b58            # or any commit
(cd /tmp/pg-before && go build -o /tmp/paperless-go-before ./cmd/paperless-go)
go build -tags loadtest -o /tmp/loadtest ./scripts/loadtest
```

### Managed mode (recommended)

The script creates a fresh database `loadtest_s1`, `loadtest_s2`,
`loadtest_s3` per scenario on the cluster (dropping an old one of that name),
migrates it with the binary (`migrate up`), starts `serve` on
`127.0.0.1:18080` (`-listen`) with a temporary data folder and a throwaway
secret key, runs the scenario, and stops the server before it reads the
statistics:

```sh
/tmp/loadtest \
  -bin /tmp/paperless-go-before \
  -pg-admin-url 'postgres://loadtest:loadtest@127.0.0.1:55432/postgres?sslmode=disable' \
  -pg-log-cmd 'docker logs pgload' \
  -pg-image 'mirror.gcr.io/library/postgres:18@sha256:edb0c08dc33befd0262ab3b5cf1c4f3f8cfd8b09e88c05b45adee29d6761ddb6' \
  -label 'before: 8f16b58' -bulk-flow legacy \
  -out before.json
```

The three scenarios take about 9 minutes. The databases stay for inspection
until the next run; the server logs are in `-workdir` (a new temporary folder
by default), named `s1/server.log` and so on.

Then the after run, with the baseline for the S2 check:

```sh
/tmp/loadtest -bin /tmp/paperless-go-after -pg-admin-url '…' -pg-log-cmd 'docker logs pgload' \
  -label 'after: <sha>' -bulk-flow legacy -baseline before.json -out after.json
```

### External mode

Against a server you started yourself on a fresh, migrated database
(`-url` and `-db`, one scenario per run and per fresh database):

```sh
/tmp/loadtest -url http://127.0.0.1:8080 \
  -db 'postgres://loadtest:loadtest@127.0.0.1:55432/fresh?sslmode=disable' -scenarios S1
```

The server needs `PAPERLESS_GO_OCR_WORKERS=0` and must accept the host of
`-url`. In this mode the server keeps running, so `pg_stat_database.deadlocks`
may lag by a few seconds (PostgreSQL flushes a backend's statistics when it
goes idle or exits); the log count from `-pg-log-cmd` does not.

## Output

The human summary goes to stdout, progress to stderr, and with `-out` the
whole result as JSON (the input of a later `-baseline`). Per scenario:

- requests (without dry runs), 2xx, 4xx by code, 500, 503 by operation,
  transport errors;
- per operation: requests, p50/p99/max latency and answers;
- successful bulk changes and documents changed;
- deadlocks: the delta of `pg_stat_database.deadlocks`, and from the
  PostgreSQL log the `deadlock detected` lines, the logged lock waits
  (`log_lock_waits`) and the sqlc queries of each deadlock's processes;
- `level=ERROR` lines of the server log (managed mode);
- review B's consistency checks (rows that must not exist): `search_links`
  that differ from `documents_search_links_vector`, `document_tags` whose
  owner differs from the document's or the tag's, documents with another
  owner's correspondent or type, links to missing rows, sub-tags with a
  missing parent;
- the audit log of the test users since the load started against the
  client's answers: `document.bulk_update` rows and the sum of their `count`
  against the bulk answers with `updated > 0` and the sum of `updated`, bulk
  runs whose `count`, `ids` and per-document entries differ,
  `document.update` without `bulkRun` against PATCH 200s, and every metadata
  create, update and delete, and `document.create` against upload 201s.

Then the checks, each `PASS`, `FAIL`, `SKIP` (input missing) or `INFO`:

- per scenario: no deadlocks (both counts 0), no 500 and no 503, every
  consistency check 0, the audit log matches the answers;
- S2: requests per second at least half of the `-baseline` run's S2 (the
  maintainer's criterion on #102);
- S3: alice's upload p99 during the bulk loop ≤ her baseline p99 + the bulk
  loop's p99 (an upload waits at most one bulk change); bob's p99 with and
  without the loop as information.

The exit code is 0 when the run completed, whatever the checks say (a before
run is expected to fail some), and 2 when it could not run.
