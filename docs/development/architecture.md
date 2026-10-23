# Architecture

One Go binary does everything: the command line and the web server with the
embedded UI. PostgreSQL holds metadata; document files live on disk. The
layout follows [mail-archive](https://github.com/pklnx/mail-archive), so its
code patterns (store, migrations, web middleware, login) carry over.

```
browser ──▶ internal/web (JSON API, UI from web/) ──▶ internal/store ──▶ PostgreSQL
                                                              ▲
cmd/paperless-go (CLI: migrate, serve) ───────────────────────┘
```

Planned, with the [roadmap](./roadmap):

```
upload, consume folder, CLI import ──▶ internal/ingest ──▶ internal/blobstore ──▶ data/documents/ab/cd/<sha256>
                                              │                ▲
                                              ▼                │
                                       job queue (PostgreSQL) ──▶ internal/process (OCRmyPDF, Tesseract; Mistral optional)
                                                                 internal/classify (matching rules, dates, OpenAI-compatible API)
```

## Packages

| Path | Purpose |
|---|---|
| `cmd/paperless-go` | Command line (cobra): `migrate`, `serve`, `keygen`, `user`, `audit export`, `prepare-data` (hidden, run by `./pgo migrate`). |
| `internal/auth` | Argon2id password hashes, session tokens, TOTP (RFC 6238) and recovery codes, the failed-login limiter and a rate limit, all with an injectable clock. |
| `internal/crypto` | AES-256-GCM sealing of small secrets (the TOTP secrets) with `PAPERLESS_GO_SECRET_KEY`. |
| `internal/config` | Configuration from `PAPERLESS_GO_*` environment variables. |
| `internal/blobstore` | Content-addressed document files: named by their SHA-256 without extension (`documents/ab/cd/<sha256>`), published and removed durably (see [Blob durability](#blob-durability)). |
| `internal/ingest` | Imports one file as a document: type detection from the content (PDF, JPEG, PNG, HEIC/HEIF), the size and 50-megapixel limits, then the blob store, the duplicate check and the row (see [Ingest](#ingest)). |
| `internal/consume` | The consume folder: polls `consume/<user>/`, imports settled files through `internal/ingest`, deletes or moves them (see [Consume folder](#consume-folder)). |
| `internal/sweep` | The trash's sweeper: deletes the documents due for purge and queues their files for removal (see [Trash](#trash)). |
| `internal/process` | The processing queue's worker: claims jobs, runs the tools (`Runner`) in a work directory, stores text, page count and archive PDF (see [Processing](#processing)). |
| `internal/store` | PostgreSQL access: migrations (goose, embedded), queries (sqlc, generated into `internal/store/db`). |
| `internal/store/storetest` | A throwaway, migrated database per test. |
| `internal/web` | HTTP server: the route table with each route's access, login and sessions, user management, the Host and same-origin checks, `/healthz`. |
| `internal/web/ui` | Embeds the built web UI (`web/` → `internal/web/ui/dist`) and serves it with a strict CSP. |
| `web` | React UI (TypeScript, Vite, Tailwind CSS). The view (sidebar filter, search, sort, page, an admin's "Everyone's documents") lives in the URL query (`urlState.ts`), so back, reload and bookmarks work and `GET /` serves every view. The sidebar's filters go to `GET /api/documents` as parameters (`tag`, `correspondent`, `type`, `inbox`, `favorite`), never as `q`, so only a typed search is a search for the audit log. `?doc=<id>` opens a document's detail with the PDF viewer (see [PDF viewer](#pdf-viewer)). |
| `docs` | This site (VitePress). |

## Data model

The first migration creates `installation`, one row with a random UUID
that identifies this server (for example as "created by" in exported archive
folders). The document model (`00002_documents.sql`, deadlines and saved
views in `00003_deadlines_saved_views.sql`) follows the Document
Archive app (`DocumentArchive/Model` there), so archives can move between the
app and the server through the app's archive folder format:

| App (SwiftData) | Server table |
|---|---|
| — | `users` (`BIGSERIAL` id, lowercase name, is_admin, password hash, lock, login times; `00004_users_login.sql`; 2FA columns in `00006_two_factor.sql`, `webauthn_handle` in `00008_passkeys.sql`), `sessions`, `two_factor_recovery_codes`, `two_factor_challenges`, `passkeys`, `webauthn_ceremonies` (network columns in `00014_ceremony_networks.sql`), `api_keys` (`00009_api_keys.sql`) |
| — | `audit_log`, `audit_user_agents` (`00005_audit_log.sql`; actor `system` in `00015_audit_system_actor.sql`; metadata targets in `00016_audit_metadata_targets.sql`, the saved-view target in `00020_trash.sql`; see [Audit log](#audit-log)) |
| `Document`: id (UUID), title, documentDate, createdAt, modifiedAt, notes, isFavorite, isInbox, archiveSerialNumber, pageCount, fileSize, extractedText, source, originalFileName, textSource, contentHash | `documents`, with the UUID as primary key; the original file (`original_sha256`, size, MIME type) and the archive PDF (`archive_sha256`, size) |
| `Tag`: name, colorHex, symbol, parent, matchText, matchMode | `tags`, `document_tags` (nested through `parent_id`) |
| `Correspondent`, `DocumentType`: name, matchText, matchMode | `correspondents`, `document_types` |
| `Deadline`: kind, date, label, origin, isDone, addedToCalendarAt, addedToRemindersAt, createdAt, modifiedAt | `deadlines`, several per document |
| `SavedView`: name, symbol, sortIndex, createdAt, filterJSON | `saved_views`, with the filter as a `JSONB` object |
| — | `jobs` (`00007_jobs.sql`; see [Processing](#processing)); `documents.thumbnail_sha256`, `documents.processing_note` |
| — | `documents.search_text`, `documents.search_links` (`00011_search.sql`; see [Search index](#search-index)) |
| — | `search_vocabulary` (`00018_search_vocabulary.sql`): per owner, the lexemes of `search_text` and how many documents hold each (see [Search index](#search-index), Vocabulary) |
| (the app's `Deleted/` folder, 30 days) | `documents.deleted_at`, `documents.purge_at`, `blob_removals` (`00020_trash.sql`; see [Trash](#trash)) |

UUIDs as keys (not serials) keep imported and exported records stable across
the app and the server.

**Owner.** Every document, tag, correspondent, document type, deadline and
saved view has an `owner_id` (`ON DELETE RESTRICT`: a user with rows cannot
be deleted).
Metadata is per owner, and names are unique per owner, ignoring case (tags:
among siblings). Links use composite foreign keys `(owner_id, x_id) →
x (owner_id, id)`, so the database itself refuses a document that points at
another user's tag, correspondent or type. Every store call takes a
`store.Scope{UserID, All}`: a row outside the scope behaves as missing
(`ErrNotFound`), and `All` (admins) widens it to every owner. A link to a
row that is not the owner's gives `ErrOwnerMismatch`, whether the row
belongs to someone else or does not exist.
A refused value gives a `*ValidationError` (`ErrInvalid`): `CreateDocument`
validates its input first, and any other check or not-null violation the
store has no specific error for is mapped to it, so no database message
reaches a caller. A new row whose owner does not exist (any more) gives
`ErrUnknownUser`. The web layer answers `ErrInvalid` with 400 and fixed
wording, and any store error it does not know with a generic 500 that is
only logged.

**Files.** A document keeps its original file next to the archive PDF. Both
live in the blob store, named by their SHA-256 without an extension; the
type is in the database. `original_sha256` is the uploaded file (the app's
`contentHash`) and detects duplicates per owner (`ErrDuplicate` names the
existing document; the same file for two owners is stored once on disk).
`FindDocumentByOriginal(ownerID, hash)` looks up a duplicate by the owner
the new document would get, never through a scope: an admin's upload is
checked only against the admin's own documents.
`archive_sha256` is the PDF with a text layer from OCR, `NULL` until
processed and equal to the original for a PDF that already has text. Export
writes the archive PDF, or the original when there is none. Deleting a
document moves it to the [trash](#trash); the sweeper's purge of the row
queues its files in `blob_removals`, and the file removal comes with #12,
part 2.

**Deletes.** Deleting a correspondent or document type leaves its documents
without one; deleting a tag removes it from its documents and makes its
children root tags (refused with `ErrTagNameConflict` when a child would
collide with a root tag of the same name; nothing is renamed). Deleting a
document moves it to the trash with everything it has; purging the row
(the sweeper) removes its tag links, its deadlines and its jobs. The trigger `tags_no_cycle` refuses a parent
that would make a tag its own ancestor (`ErrTagCycle`); a per-owner advisory
lock keeps two concurrent changes from building a cycle together.

**Deadlines.** A deadline belongs to one document and to that document's
owner: the composite foreign key `(owner_id, document_id) → documents
(owner_id, id)` (`ON DELETE CASCADE`) refuses a deadline whose owner differs
from its document's. `AddDeadline` gives the deadline the document's owner,
also for an admin; a document outside the scope gives `ErrOwnerMismatch`.
`kind` (`expires`, `due`, `noticeEnds`, `other`) and `origin` (`manual`,
`heuristic`, `ai`) are the app's raw strings without a `CHECK`, so values of
newer app versions survive a round trip. All dates are `TIMESTAMPTZ`
(microseconds), so the archive format's millisecond dates round-trip.

**Saved views.** A saved view is per owner; names need not be unique, as in
the app. `filter` holds the app's `filterJSON` as a `JSONB` object (a `CHECK`
refuses anything else; the store gives `ErrInvalidFilter`). The server does
not interpret it: unknown keys and values are kept, while whitespace and key
order are normalized by PostgreSQL. Lists are ordered as in the app: by
`sort_index`, then `created_at`, then `id`.

**Locale and text indexes.** Unique names (`users.name`, `lower(name)` of
tags, correspondents and document types, passkey and API key names) and a
few other text columns have B-tree indexes, sorted by the database's
collation. The Compose database is created with PostgreSQL's builtin
provider and `C.UTF-8` (code point order, PostgreSQL's own case mapping),
so no image or C library can change it. Databases created earlier, or on
your own server, use the C library's locale (`en_US.utf8`): a new libc
sorts differently and leaves those indexes out of order, so lookups miss
rows and unique indexes let duplicates in. musl (the former Alpine image)
reports no collation version, so PostgreSQL gives no warning when such a
database moves to glibc. `Store.Migrate` (`internal/store/collation.go`)
keeps a fingerprint of the locale (provider, collate, ctype, provider
version, PostgreSQL major) in `index_collation` and, when it changed or is
unknown under a libc or ICU locale, rebuilds every index with a collatable
key whose collation is not `C`/`POSIX` (`REINDEX INDEX`, one at a time
with a 30 s `lock_timeout`, before the migrations run; a failed unique
index is reported with all its duplicated keys after the others are
rebuilt), stores the fingerprint and refreshes `datcollversion` (only a
warning when the user does not own the database). PostgreSQL 17 or newer
is required (`pg_database.datlocale`); `migrate`, `serve` and `process`
check `server_version_num` first. `serve` and
`process` refuse to start while it is stale. The GIN search index needs no
rebuild: lexemes compare bytewise.

The database order of names depends on that locale, so the web UI sorts every
visible name list itself with `web/src/collate.ts`: `Intl.Collator` in the UI
language, numeric ("2" before "10"), and for strings the collator treats as
equal, UTF-16 code unit order. The order is the same on every installation.
Keyset-paged lists keep the server's order, and saved views keep their manual
order (`sort_index`).

## Search index

Full-text search (#6) runs in PostgreSQL. Part 1 (`00011_search.sql`) adds
the index and the SQL function that turns a parsed query into a `tsquery`;
the parser, the store search and the API build on it.

**Columns.** Two `tsvector` columns on `documents`, both `NOT NULL DEFAULT
''`, kept up to date by triggers (generated columns cannot read other tables
or fall back when a vector is too large):

| Column | Holds | Set by |
|---|---|---|
| `search_text` | Title (weight A; `paperless_simple`, `german`, `english`), ASN as `123` and `asn123` (A; `simple`), original file name with `_ . -` as spaces (C; `paperless_simple`), notes (C; all three), the first 300 000 characters of `extracted_text` (D; all three) | `BEFORE INSERT OR UPDATE OF title, archive_serial_number, notes, original_file_name, extracted_text`; skipped when none of them changed |
| `search_links` | One lexeme per linked correspondent (`c`), document type (`d`) and tag (`t`): a space, the letter and the UUID, at position 1, weight B. The parser never makes a lexeme with a space, so a typed word cannot match one | `BEFORE INSERT OR UPDATE OF correspondent_id, document_type_id` (also the `ON DELETE SET NULL` of a deleted correspondent or type); statement-level `AFTER INSERT` and `AFTER DELETE` on `document_tags` with transition tables, refreshing each affected document once per statement (a tag's delete reaches `document_tags` by its cascade) |

`documents_search_idx` is a GIN index on `(search_text || search_links)`;
queries use exactly this expression. `documents_owner_date_idx` on
`(owner_id, document_date DESC, id)` serves the date sort.

**Configurations.** `german` and `english` (Snowball) stem; `paperless_simple`
is `simple` with the `unaccent` dictionary and covers exact words, numbers
and prefixes. The German stemmer maps `ä ö ü` to `a o u` and `ß` to `ss`,
and `unaccent` does the same for the simple part, so `Gebühren`/`Gebuhren`
and `Straße`/`Strasse` match each other. `Gebuehren` matches them only
from PostgreSQL 18 on, whose German stemmer (Snowball 3) also folds `ae oe
ue`; the Compose image runs 18. `unaccent`
is a trusted extension (PostgreSQL 13+): the database owner creates it
without superuser rights, and it ships with `postgres:18`. It and
`paperless_simple` live in `public` and are always referenced
schema-qualified, so nothing depends on the caller's `search_path`.

**Cap.** A `tsvector` cannot exceed 1 MB, and the worker accepts 16 MiB of
text. `documents_search_text_vector` takes 300 000 characters of the text;
on overflow (SQLSTATE `54000`) it retries with 100 000, then without the
text, then with 100 000 characters of the text and of every other field,
then of the other fields only, then gives an empty vector. A built vector over 896 KiB counts as an overflow, which
leaves room for the link lexemes, so the index expression `search_text ||
search_links` cannot overflow either. Saving a document never fails
because of the index.

**Positions.** `a || b` shifts `b`'s positions by `a`'s largest, and
positions stop at 16 383. The short fields come first and the `german` copy
of the text right after them, so a phrase is found within about the first
16 000 words of the text; behind that, and in the `simple` and `english`
copies of a long text, every position is 16 383 and only single words
match.

**Names are resolved when a query runs.** `document_search_tsquery(owner,
terms[], is_phrase[], negated[], or_group[])` (`STABLE`) takes the parser's
free-text items as bound arrays. Each item becomes, joined by `|`:
`plainto_tsquery` (or `phraseto_tsquery`) in the three configurations; for
a word of 3 or more characters, a prefix query from the lexemes
`paperless_simple` makes of it, each quoted by a fixed escaper plus `:*`;
and the link lexemes of the tags (with their descendants), correspondents
and document types whose name contains the term, ignoring case and accents
(`ILIKE` over `unaccent`, wildcards escaped; a term under 3 characters
matches a whole name only; at most 1000 IDs per item, ordered by kind and
ID). Negated items get `!!`, items of one `or_group` are joined by `|`, the
groups by `&`. An item that yields nothing is dropped; with none left the
result is `NULL` (no full-text condition). The function is plpgsql and so never folded at plan time: a query
that calls it inline runs it for every row of a sequential scan (measured:
4.9 s instead of 22 ms for 20 000 documents), so the store search calls it
once, on its own, before the search (see below). Names are resolved within
`owner`, or among all owners when it is `NULL` (an admin). Because the
composite foreign keys keep a document's links within its owner, a user
cannot match another user's documents through a name or an ID. Renaming or
moving a tag, or renaming a correspondent or type, therefore rewrites no
document; only a document's own link changes touch its row.

**Concurrency.** `documents_search_links_vector` reads `document_tags` and
is `VOLATILE` (plpgsql) on purpose: each statement takes a fresh snapshot,
so a trigger that waited for the document's row lock sees tags committed
meanwhile. Every writer of `document_tags` holds the owner's write lock
(Write locking, below) and, except a tag's delete, locks the document row
first (`PatchDocument` through `LockDocument`, the bulk edit through
`LockDocuments`); automatic tagging must do the same, so two rebuilds of
one document never interleave. A tag's delete cascades to its
`document_tags` rows under the owner's write lock, so its trigger only
updates documents no other store writer touches meanwhile; this replaces
the order the comments in `00011_search.sql` describe. `document_tags`
rows are never updated, so there is no `UPDATE` trigger. Down drops
`unaccent` only when the current user owns it and nothing outside the
extension uses it.

**Vocabulary** (#56, part 1; `00018_search_vocabulary.sql`). `search_vocabulary
(owner_id, lexeme, n)` holds, per owner, every lexeme of 5 to 64
characters in the owner's `search_text` vectors and the number of the
owner's documents that hold it (`CHECK (n > 0)`, primary key `(owner_id,
lexeme)`). A GIN trigram index (`public.gin_trgm_ops`, extension
`pg_trgm`) on `lexeme` lets part 2 find the lexemes that contain a typed
word; part 1 changes nothing visible. The vocabulary is never returned by
any API.
- **One rule.** `search_vocabulary_lexemes(tsvector)` (immutable SQL) is the
  only definition of what the vocabulary holds: the distinct lexemes of
  `tsvector_to_array`, `char_length` 5 to 64 (characters, not bytes), in
  primary key order. Every configuration's lexemes go in, stems too; link
  lexemes live in `search_links` and never do.
- **Upkeep.** A row-level trigger function, `search_vocabulary_trigger`,
  on `AFTER INSERT OR DELETE` and on `AFTER UPDATE OF title,
  archive_serial_number, notes, original_file_name, extracted_text,
  search_text, owner_id` with `WHEN (OLD.search_text IS DISTINCT FROM
  NEW.search_text OR OLD.owner_id <> NEW.owner_id)`. The column list
  names the source columns because the `00011` BEFORE trigger sets
  `search_text`, and `UPDATE OF` looks only at the statement's SET list.
  The `WHEN` makes an update that leaves the vector alone (a patch that
  repeats the title, an OCR run with the same text) nearly free, and the
  bulk change (flags, links, tags) does not fire it at all. A new document
  adds 1 for each of its lexemes (`INSERT … ON CONFLICT DO UPDATE`), a
  deleted one subtracts 1, an update only the set difference. The
  decrement deletes rows with `n <= 1` and decreases the others (`n > 1`),
  so no path can violate the `CHECK`, also after a count drifted (raw
  SQL); a count that drifted is not repaired. Statement-level triggers
  with transition tables are not possible: PostgreSQL refuses them on a
  trigger with a column list.
- **Locking.** The trigger runs inside the writer's transaction, which
  holds the owner's write lock (Write locking, below), and writes only
  that owner's rows. So writers of one owner never touch the vocabulary at
  the same time, and writers of different owners touch disjoint rows.
  Safety comes from the owner lock alone: the add, subtract and delete
  statements each go in primary key order, but together their row locks
  are not taken in one monotone order. SQL outside the store that writes
  one owner's documents concurrently can therefore deadlock on vocabulary
  rows (`40P01`; a store writer it met gets `ErrRetry`).
  `TestSearchVocabularyConcurrentWriters` holds one writer after its lock
  (`WithAfterLock`), sees the same owner's second writer wait at the
  advisory lock and the other owner's finish without a lock wait, and
  checks the counts. `search_vocabulary` is in the guard's table list, so
  a store statement that ever wrote it directly would need the lock too.
- **Long transactions.** Each document's upsert rewrites the rows of its
  common lexemes. Within one transaction their old versions cannot be
  pruned, so a transaction that writes thousands of documents walks ever
  longer version chains (one statement adding 2 000 generated documents
  took 140 s instead of 6 s). The store writes one document per
  transaction. A future migration that rewrites `search_text` table-wide
  should disable the trigger and rebuild the vocabulary set-based, as the
  backfill does.
- **Backfill.** The migration takes `SHARE ROW EXCLUSIVE` on `documents`
  as its first statement (before `CREATE TABLE … REFERENCES users` locks
  `users`), so no writer can commit between the backfill's snapshot and the
  triggers (`TestSearchVocabularyMigrationWaitsForWriters`); readers go
  on. Then one `INSERT … SELECT … GROUP BY owner_id, lexeme` over
  `search_vocabulary_lexemes(search_text)`, then the primary key and the
  trigram index, then the triggers.
- **Collation.** `lexeme` keeps the database's default collation, so both
  indexes have a non-`C` `indcollation` and the rebuild after a locale
  change (Locale and text indexes, above) covers them: `pg_trgm` splits
  words by the database's ctype. `TestMigrateRebuildsIndexesAfterLocaleChange`
  lists both.
- **Cost** (`TestSearchBench`, 100 000 documents of one owner and 50 000
  of another, 2.1 and 2.0 million vocabulary rows, 4 CPUs, #114; before =
  the same generator on 00017). Per-document p95: a text update 5.2 →
  17.8 ms, `CompleteJob` with new text 6.7 → 18.3 ms, `CreateDocument`
  with text 6.0 → 13.9 ms, `DeleteDocument` 1.5 → 12.5 ms; an upload
  without text is unchanged. The generation (100 documents per
  transaction) took 35 instead of 10 minutes, a retitle of 1 000
  documents 4.5 instead of 3.1 s. The vocabulary with its indexes is 415
  MB (17 % of `documents`) when the backfill builds it and 623 MB (26 %)
  when the trigger builds it document by document; a `fillfactor` of 70
  made the generation 20 % faster and the table 15 % larger. The backfill
  took 1 min 40 s. Searches are unchanged.
- **`pg_trgm`** is a trusted extension like `unaccent` and must be in
  `public`; the migration checks `pg_extension.extnamespace` and fails
  with the fix in its message: the extension's owner or a superuser runs
  `ALTER EXTENSION pg_trgm SET SCHEMA public` (the app's role cannot), or
  drops it when nothing else uses it.
  Down drops the triggers, the function, the table and, under the
  conditions of `unaccent` in `00011`, `pg_trgm` (owned by the current user,
  nothing outside the extension depends on it; the `pg_amproc` entries of
  `pg_trgm`'s own operator families count as part of it).

**Reads.** No query reads the vectors back: `GetDocument` and
`ListDocuments` list their columns instead of `d.*`.

**Store search.** `internal/search` parses the query syntax
([user guide](../guide/search.md)) into free-text items and filters; it
never builds SQL. `store.SearchDocuments(ctx, scope, query)` turns them
into bound parameters and runs four steps, the last two only when needed:

1. `SearchTsquery` calls `document_search_tsquery` once and returns the
   `tsquery` as text (`''` when no item is left). Its text form reads back
   as the same `tsquery`.
2. `SearchTagIDs` and `SearchLinkIDs` resolve the names of `tag:`,
   `correspondent:` and `type:` to IDs in the scope (exact name ignoring
   case; tags with their descendants). Go combines the filters: tag
   filters become groups that each need one tag, the single-valued fields
   (correspondent, type, ASN) intersect, date filters intersect into one
   range plus excluded ranges, and `-date:<x` becomes `date:>=x`.
   Filters that contradict each other, or a positive filter with an
   unknown name, return an empty page without a query.
3. One query per sort order (`queries/search.sql`): relevance, added and
   date, the last two in both directions. A `CASE` in `ORDER BY` would
   keep the planner from walking an index in order. The filter block and
   the snippet are copied between `-- filters:begin/end` and `--
   snippet:begin/end`; `TestSearchFilterCopies` keeps the copies equal.
   The `tsquery` is a parameter there, so it is a constant: the GIN index
   serves it and nothing builds it per row
   (`TestSearchTsqueryOncePerSearch` counts the calls on forced sequential
   scans and checks the plan uses `documents_search_idx`).
4. The inner query finds the page's IDs, one row more than the page to
   tell whether another follows; the outer query reads their columns,
   tags and snippets, so these cost only the page.

The search queries run as unnamed statements
(`pgx.QueryExecModeCacheDescribe`): PostgreSQL plans each with its values,
so the `IS NULL` tests of unused filters fold away and `@@` can use the
index. A prepared statement may switch to a generic plan after five runs,
which cannot do either.

Relevance is `ts_rank_cd('{0.1,0.2,0.4,1.0}', search_text ||
search_links, q, 1)`, computed for every match. It runs in a read-only
transaction with `SET LOCAL enable_seqscan = off`: for a broad term
PostgreSQL tends to pick a sequential scan, which also detoasts the
vectors of every row that does not match, while the bitmap scan of
`documents_search_idx` reads only the matches. The benchmark
(`go test -tags searchbench -run TestSearchBench ./internal/store/`, 100 000
documents) has the numbers. Paging is by keyset over `(key, id)`: newest first
breaks ties by ascending ID, matching `documents_owner_created_idx` and
`documents_owner_date_idx`; oldest first walks them backwards. The cursor
is base64url JSON with the sort, the direction, the key (Unix microseconds,
or the exact bits of the `real` rank) and the ID; one from another sort, a
time before PostgreSQL's earliest `timestamptz` (4714 BC; near
`math.MinInt64` pgx's conversion would also wrap around) or a rank that is
not finite is `ErrInvalidCursor`. `SearchDocuments` also refuses a query
over the parser's limits with `search.ErrTooLong` (`Query.CheckLimits`),
so a query built without `search.Parse`, such as a saved view's, is held
to them too.

Snippets: `ts_headline` over the first 100 000 characters of the text,
with the private-use characters U+E000 and U+E001 around matches, two
fragments of up to 25 words. It tries `german` first, then `english`
(whose stems German parsing misses, such as `happiness` for `happy`), then
`paperless_simple` (accents, `cafe` for `Café`), and keeps the first that
marks a match. `ts_headline` leaves out what its parser takes for HTML
tags.

**Write locking** (#102). Every store transaction that writes
`documents`, `document_tags`, `tags`, `correspondents` or `document_types`
first takes the per-owner, transaction-level advisory lock of each owner
whose rows it writes; a transaction over several owners (an admin's bulk
change across owners) takes them in ascending owner id. Every row of these
five tables belongs to exactly one owner, `owner_id` is never updated, and
every link between them goes through a composite `(owner_id, …)` foreign
key, so a foreign-key check, a cascade or a `SET NULL` never reaches
another owner's row. All writes of one owner therefore serialise, no wait
between store writers can form a cycle, and deadlocks between them are
excluded by construction. This replaces the row-lock order of #99/#103
(key shares before the documents, deletes locking their documents
first) and its retry loop.

- **The key.** `hashtextextended('paperless-go:tags:' || owner_id::text,
  0)` with `pg_advisory_xact_lock` (`LockOwner` in `queries/locking.sql`;
  the prefix is `store.ownerLockPrefix`). It is the key the
  `tags_no_cycle` trigger already takes (`00002_documents.sql`), so the
  trigger's lock is folded in without a migration: inside a store writer
  the trigger re-acquires a lock its transaction holds (transaction-level
  advisory locks are re-entrant; `pg_locks` shows one row), and SQL
  outside the store keeps the trigger's own protection. It reads "tags"
  because the trigger came first; `TestOwnerLockMatchesTrigger` breaks if
  either side changes. The other advisory locks in the code base
  (`hashtext('audit_log_ensure_partitions')`,
  `hashtext('webauthn_login_ceremonies')`, goose's session lock) share the
  single-`bigint` key space; a collision has odds of about 2⁻³² and would
  only cause a false wait, never a cycle, since none of their holders
  holds an owner lock.
- **The helper.** `writeTx.lockOwners(ctx, owners...)`
  (`internal/store/locking.go`) dedupes and sorts the owners and takes one
  `LockOwner` per owner (a Go loop: SQL does not promise the order in
  which a function runs over rows). Only `ownerTx` calls it, as the first
  statement of its transaction and with the owners it gated (other owners
  are `errNotGated`, an internal error). It runs once per transaction (a
  second call is an internal error, so composed helpers cannot break the
  order) and never with an empty list: a writer answers an empty
  selection before (`ErrNotFound` for IDs that are all outside the scope,
  the empty-match answer of a bulk change by search). The lock is
  released at commit or rollback, also on an error or a cancelled
  context, so a pool connection never carries it into the next request.
- **The gate** (#117, `internal/store/ownergate.go`). A writer that waits
  at `pg_advisory_xact_lock` holds a pool connection, and the pool is
  pgxpool's default `max(4, NumCPU)`: a few waiting writers of one owner
  took every connection, so every other user, logins and `/healthz` waited
  for that owner's bulk change. So before its transaction (and so before
  it takes a connection) a writer takes the owner's token of the store's
  in-process gate, one one-slot channel per owner (entries live only
  while used), and gives it back after the commit or rollback. In one
  process at most one writer per owner holds a connection for that
  owner's lock; the others wait in Go, without a connection.
  `Store.ownerTx(ctx, owners, fn)` is the only entry point: gate, then
  `inTxWith`, then `lockOwners` over the same owners, then `fn`; the
  owners are known before (below). The gate is not reentrant: `fn` gets a
  context marked as gated, and a writer started with it fails
  (`errNestedOwnerTx`) instead of waiting for itself. One gate per
  `Store`, shared by the handlers and the worker inside `serve`; the
  advisory lock stays the guarantee across processes (an `import` run, a
  second server), each of which waits with at most one connection per
  owner. Deadlock freedom: gates are taken in ascending owner id like the
  locks; a writer waiting at the gate holds only lower gates, no
  connection and no advisory lock; a transaction never waits for a gate.
  Gate waits therefore form ascending chains ending at a writer that holds
  or waits for advisory locks, whose waits are acyclic as before, and a
  gate holder waiting for a pool connection waits for transactions that
  wait only for locks. `DisableOwnerLock` leaves the gate on; the
  test-only `DisableOwnerGate` turns it off.
- **Bounded wait.** `store.WithOwnerWait(ctx, d)` bounds the wait at the
  gate and at the owner lock together, from the start of `ownerTx`: at the
  gate by a timer, at the lock by `SET LOCAL lock_timeout` of the time
  left before each `LockOwner` (at least 1 ms; none left gives up without
  the statement) and `SET LOCAL lock_timeout TO DEFAULT` after the last,
  so row locks and triggers never run under it. Giving up is
  `store.ErrBusy` (`55P03` counts only at `LockOwner`); nothing was
  written. The web server sets 15 s on every request in `Handler()`
  (`ownerWait`, a constant), and `failStore` answers `503` with
  `Retry-After: 5` and `another change of these documents is still
  running; try again`. Without it a request's write waited as long as its
  context: about 30 s for most handlers (the request context ends with
  the `ReadTimeout`, see Web server), an hour for an upload. The worker
  (`CompleteJob`, limited by its lease), the consume folder and the import
  set no bound: they wait gated, without a connection, and failing an OCR
  attempt because the owner is busy would gain nothing.
- **Owners the writer does not know.** A writer of an existing row reads
  its owner in the scope first, on the pool before its transaction and
  the gate (`DocumentOwner`, `DocumentOwners`, `TagOwner`,
  `CorrespondentOwner`, `DocumentTypeOwner`; out of scope stays
  `ErrNotFound`, now answered without a transaction), takes the gate and
  the lock, then locks or writes the row and checks that the row's owner
  is one it locked (`writeTx.holds`; owners never change, so a mismatch is
  an internal error, never a silent write under the wrong lock). A row
  deleted between the read and the lock gives `ErrNotFound` from the row
  lock as before. A bulk change by search locks its one owner before the
  match; in an admin's scope over all owners the first match runs on the
  pool before the gate (the limit and an empty match answer there, as
  before without an audit entry), and the change gates and locks the
  owners of that match, whose locked documents are a subset of it (the
  recheck only removes; under READ COMMITTED the match inside the
  transaction ran on its own snapshot before the locks too).
- **Every writer:** `CreateDocument` (upload, consume folder, import),
  `DeleteDocument`, `PatchDocument`, `BulkUpdateDocuments` (also its
  trash actions), `TrashDocument`, `RestoreDocument`, `PurgeDocument`,
  `PurgeDueDocuments` (the sweeper, one owner per transaction),
  `Create/Update/Patch/Delete` of tags, correspondents and document
  types, and `CompleteJob` (OCR results; it reads the document's owner
  without a scope). The triggers (`tags_no_cycle`, the `00011` search
  triggers, the `00018` vocabulary trigger), the cascades to `document_tags` and `jobs` and the `SET
  NULL` of `documents.correspondent_id`/`document_type_id` and
  `tags.parent_id` run inside the writer's transaction, under its lock.
- **Not writers of the five tables** take no owner lock: the jobs writers
  (`EnqueueDocument`, `Reprocess`, `ClaimJob`, the lease updates,
  `ReapJobs`, `DeleteDoneJobs`), deadlines, users (`DeleteUser` refuses
  while the user owns rows), sessions, API keys, passkeys, saved views and
  the audit log. A jobs writer never waits for an advisory lock, and the
  owner-lock holders it can wait for (`CompleteJob`, `CreateDocument`,
  through the jobs unique index or a document's key share) never wait for
  a jobs writer, so no cycle closes through them. Two jobs writers can
  still deadlock with each other (two concurrent "reprocess all" runs on
  the jobs unique index): the store as a whole is not deadlock-free, only
  its writers of the five tables.
- **No slow work under the lock.** A store transaction does no I/O, no
  network and no blob-store access: uploads write the blob and detect the
  type before `CreateDocument`, OCR runs before `CompleteJob`, the audit
  user agent is looked up before the transaction begins. The longest
  holder is a bulk change over 5 000 documents.
- **What the row locks still do.** `LockDocument`/`LockDocuments` (`FOR
  NO KEY UPDATE`) return the owner, `modified_at` and the IDs in scope,
  and keep a document safe against SQL outside the store; `LockTag`,
  `LockCorrespondent`, `LockDocumentType` make the patches'
  read-modify-write safe; the bulk change's recheck after the lock
  (`stillMatching`) stays for an admin's change over all owners and SQL
  outside the store. No global row-lock order is needed any more.
- **The guard.** Every query of the store runs through `guardedDB`: the
  pool in `Open`, each transaction in `inTxWith` (`db.New` on the guarded
  transaction, never `WithTx`, which would put the raw transaction back)
  and the relevance search's read-only transaction (`readTx`). Before
  each statement it decides whether the statement writes or row-locks
  one of the five tables: a sqlc query by its name in `writerQueries`,
  any other SQL by the same regular expression (`writesOwnerTables`), so
  unnamed SQL fails closed. Such a statement outside a transaction, or in
  one before `lockOwners`, returns `errUnlockedWrite` (`500`). Three
  tests keep it complete: `TestWriterQueriesComplete` (the set equals the
  queries in `queries/*.sql` that write or row-lock the five tables),
  `TestEveryWriterLocksItsOwner` (a row per writer and admin variant with
  the owners it must lock, ascending, before its first guarded write; a
  reflection check fails for a new `*Store` method that is neither in the
  table nor on the list of non-writers) and
  `TestTransactionsOnlyThroughTheGuard` (a static check that `Begin`,
  `pgx.BeginFunc`, `WithTx` and `db.New` appear only in the guarded
  constructors, `pgx.Connect`, `pgx.ConnectConfig` and `Acquire` only in
  `ListenJobs`, and `lockOwners` only in `ownerTx`; `collation.go`'s
  `REINDEX` is allowed). The regular expression also matches quoted table
  names (`UPDATE "documents"`, `public."tags"`).
- **`ErrRetry` stays** (`40P01`, `503` with `Retry-After: 1`) as the net
  for SQL outside the store and for deadlocks among jobs-only writers;
  store writers of the five tables no longer retry.
- **Raw SQL and future code outside the store** (migrations, a data
  transfer between users) are not covered by the guard. The data transfer
  of #21 must lock both owners, ascending.
- **Tests.** The regression tests of the four rounds of deadlock holes
  (#84 opposite tag moves, the #99 pairs, review A's three-party
  `PatchTag`, review B's foreign-key re-check) hold one side after its
  locks (`WithAfterLock`), wait for the other at the advisory lock in
  `pg_stat_activity`, and give the side that would lose a deadlock a
  10 ms `deadlock_timeout` (the other an hour). The competing sides run
  on stores of their own (a gate each, as other processes would see it),
  so they really meet at the SQL lock; a second writer of the same store
  waits at the gate instead, which tests see through
  `store.WithGateWait` or `Store.OwnerGateWaiters`. With
  `store.DisableOwnerLock` (test-only; it also turns the guard off; a
  package-global switch, so no `t.Parallel`) the cases that can deadlock
  (the #99 pairs, review A's and review B's) fail with `ErrRetry`; the
  others (#84, parent and child delete, the serialisation tests) fail on
  their serialisation assertion. With the lock they pass.
  `TestOtherOwnerNotDelayedByWaitingWriters` (pool of 4, alice's patch
  held after its lock, 6 more of her writers waiting) checks that bob's
  patch, read and tag create and a ping go through and that at most one
  connection is in use; `PAPERLESS_GO_TEST_WITHOUT_GATE=1` runs it with
  `DisableOwnerGate`, where it fails (bob's calls run into their 5 s
  failure deadline).
- **Cost** (the load test of `scripts/loadtest`, before and after on one
  host, 4 CPUs, the server's default pool of 4 connections; #108): one
  user's concurrent writes (S2, 21 threads as one user) went from 22.7 to
  25.6 requests/s (bulk p99 3.6 s → 2.4 s, patch p99 3.4 s → 2.6 s), since
  the 56 deadlocks and their retries are gone; with two users (S1) from
  33.9 to 38.1 requests/s, 45 deadlocks and one `503` → none. An upload of
  the owner whose bulk changes over 5 000 documents loop waits for at
  most one of them (S3: p99 2.6 s, the bulk loop's p99 2.8 s; 8 ms p50
  alone). Another owner's request does not wait for the lock, and since
  the gate (#117) it does not wait for a pool connection either while any
  number of one owner's writers wait for each other: the pool fills up
  only with real concurrent work of different owners
  (`TestOtherOwnerNotDelayedByWaitingWriters`; before the gate four
  waiting writers of alice took bob's PATCH from 8 ms to 156 ms p50 and
  `/healthz` to its 5 s timeout). The accepted cost: one owner's
  concurrent writes serialise even when they touch different rows (review
  B of #116 measured an adversarial mix of one owner's non-overlapping
  writes about 15× slower than without the lock). The pool size is
  `pool_max_conns` in `PAPERLESS_GO_DATABASE_URL`
  (`docs/reference/configuration.md`).

## Trash

#12, part 1 (`00020_trash.sql`, `internal/store/documents_trash.go`,
`internal/sweep`). Deleting a document is two steps. **Trash** sets
`documents.deleted_at`: the row stays with everything it has (tags, ASN,
the unique `(owner_id, original_sha256)`, deadlines, the search vectors and
its words in the vocabulary), so a restore only clears the column and never
conflicts, and the duplicate check keeps finding it. The document leaves
every list, search, count (the sidebar's and the metadata lists', the tag
counts through a join to `documents`) and bulk match, and shows only in the
trash, which lists and searches with the same endpoint and filter code
(`trashed=true`). **Purge** happens when the document is *due*:
`coalesce(purge_at, deleted_at + retention) <= now`. "Delete permanently"
and "empty trash" set `purge_at = now`, which makes it due at once; the
retention is `PAPERLESS_GO_TRASH_RETENTION` (default 720 h). A due document
is hidden from every read (`GET` and the file routes are `404`, restore and
purge too), and the sweeper deletes its row.

- **One clock.** The store has `Now` (the wall clock in `serve`, injected
  in tests) and `TrashRetention` (every command sets it from the
  configuration). Every trash write takes `now` as a parameter (`deleted_at`,
  `purge_at`) and every read of the state compares against it, also the
  sweeper's scan, which gets the same clock and retention. The retention is
  read at every decision: shortening it applies at once to documents
  already in the trash; with `0` a trashed document is due at once (the
  UI's Undo then fails).
- **The filter** is two explicit branches in every copy of the search's
  filter block (the six search queries, `SearchDocumentsDeletedDesc` and
  `MatchDocumentIDs`; `TestSearchFilterCopies`): `(NOT trashed AND
  deleted_at IS NULL) OR (trashed AND deleted_at IS NOT NULL AND not
  due)`. The queries run as unnamed statements, so the constant folds one
  branch away: the trash list uses the partial index `documents_trash_idx
  (owner_id, deleted_at DESC, id) WHERE deleted_at IS NOT NULL`, and the
  live lists keep `documents_owner_created_idx`, `documents_owner_date_idx`
  and `documents_search_idx` (`TestTrashFilterUsesIndexes`, with
  `EXPLAIN`). The sort `deleted` (descending only, keyset over
  `(deleted_at, id)`) is the trash's default without ranked text and
  refused outside it.
- **Writers** (all through `ownerTx`, with the row lock and a state check
  under it): `TrashDocument` (a trashed one is `ErrInTrash`, `409
  in_trash`), `RestoreDocument` and `PurgeDocument` (a live one is
  `ErrNotInTrash`, `409 not_in_trash`: permanent deletion is always two
  steps); a due document is `ErrNotFound` for all three. None of them
  touches a column of the search triggers, and `modified_at` stays (the
  document did not change; a `PATCH` of a trashed document is refused, so
  the precondition loses nothing). `PatchDocument`, a bulk `update`, and
  `reprocess` of one document refuse a trashed one (`ErrInTrash`);
  `reprocess --all`/`--failed` skip trashed documents; a queued or running
  job of a trashed document finishes normally, so a restored document has
  its text. `GetDocument` returns a trashed document with `DeletedAt` and
  `PurgeAt` (the due time), so a link and the file and thumbnail routes
  keep working for the preview. `OwnedRowCounts` counts trashed rows: a
  user with documents in the trash cannot be deleted until they are
  purged.
- **Bulk.** `BulkSelection.Action` is `update` (the field change), `trash`,
  `restore` or `purge`; the trash actions take no fields and are one
  `UPDATE … WHERE id = ANY` each. By IDs every locked document must be in
  the action's state (else the whole change fails with `409`); by search
  the match must select the trash (`trashed`) for restore and purge and
  the live documents otherwise, and a purge by search needs an owner, so
  "empty trash" is always one user's trash, also for an admin who sees
  everyone's documents. The fingerprint (`v2.`) covers the action and the
  sorted IDs, so a dry run confirmed for `restore` is no confirmation of a
  `purge` of the same set; a `v1.` fingerprint never matches.
- **The sweeper** (`sweep.Sweeper`, in `serve` every minute and at once
  after a purge request in the same process, through `web.Options.SweepWake`).
  `RunOnce` loops `store.PurgeDueDocuments` in batches of 100 until a
  scan finds fewer: it scans due rows on the pool without a lock (`ORDER
  BY owner_id, id`), then per owner one `ownerTx` locks them, checks again
  that each is still due (a restore may have come first), deletes them
  (`RETURNING` the three hashes; cascades to `document_tags`, `deadlines`,
  `jobs`; the vocabulary trigger runs, about 12 ms per row, so the owner
  lock is held for about 1.5 s per batch at most), queues the original,
  archive and thumbnail hashes in `blob_removals` (`ON CONFLICT DO
  NOTHING`, `due_at = now + 1 h`) and writes `document.purge` by `system`
  with `reason: retention` and `ownerId` for each document whose retention
  passed (a requested purge was audited when it was asked for). Measured
  on the test machine: trashing 1 000 documents by search takes 91 ms,
  marking them for purge 104 ms, the row purge of all 1 000 330 ms.
- **Re-ingesting a trashed file.** `ingest.Import` reports a duplicate
  with `Existing.Trashed`; the callers decide (maintainer decision on
  #12). A web upload and a key with `read` answer `duplicate` with
  `trashed: true`, and the UI offers Restore. Where nobody sees a Restore
  button, the caller restores the document at once, audited as
  `document.restore` with `via`: an upload with a key without `read`
  (`upload`, the iPhone Shortcut; its answer stays `{"duplicate": true}`),
  the consume folder (`system`, then the file is deleted and counted under
  `restored`) and `import` (the operator; counted as `restored`). A
  duplicate whose document is due for purge is neither duplicate nor new:
  `store.ErrPurgePending`, a transient error on every path (`503` with
  `Retry-After: 60` for every upload, the consume folder keeps the file,
  `import` reports a failure), because a client that heard "duplicate"
  would not retry while the only copy goes. The same holds when the
  restore itself finds the document due (`ErrNotFound`, a window of at
  most a minute).
- **Not yet.** The files of a purged row stay until #12, part 2 removes
  the queued ones that no document references (with a per-hash lock
  against a concurrent upload of the same file). #10's export decides
  what to do with trashed documents (the app's format has `Deleted/`), and
  #11's upcoming deadlines must leave them out.

## Blob durability

`internal/blobstore` keeps document files under the data directory, named by
the SHA-256 of their content: `documents/ab/cd/<sha256>`, without an
extension (the type lives in the database). The same bytes are stored once,
whoever uploads them and whatever they are used for: an original file and an
archive PDF with the same content, or the same file uploaded by two users,
are one file. `Put` reports `created=false` for content that was already
there.

`blobstore.New`, `Put` and `Remove` return success only after the files and
directory entries their result depends on are synchronized with `fsync`.
Callers acknowledge nothing before that: an upload (#4) writes the file
first and inserts its row only after `Put` succeeded, so a row never names a
file that a crash can take away. An error can still leave the result
visible (a published file, a removed one); it counts as a failure, and the
retry completes it. A file being visible, `Stat` and `Exists` say nothing
about durability.

- **New** creates `documents/` and `tmp/` if needed and, on every call,
  synchronizes them, then the store root and each of its ancestors bottom up
  through `/`. It resolves symlinks in the root first, so the target's
  ancestry is synchronized, not the lexical parents of a link; `Root()`
  still returns the configured path. Every ancestor of the data directory
  must therefore be readable by the service user; one that is not fails
  with an `AncestorError` naming it. The configured path, its symlinks and,
  for a relative root, the working directory must not change while the
  process runs.
- **Put** writes into `tmp/put-*`, synchronizes and closes the file, and
  publishes it with a hard link under `documents/ab/cd/<sha256>`. A link
  never replaces an existing name, so of identical concurrent writers, also
  in other processes, exactly one publishes (`created`), and nobody replaces
  the file another writer is still synchronizing. Then it synchronizes
  `documents/ab/cd`, `documents/ab` and `documents`, also when they existed.
  A writer that finds the file (before or through a lost link race)
  synchronizes that file and the same directories itself before it returns
  `created=false`: the file may be what an earlier, failed call published.
  If a removal (the orphan sweep, #12) takes the file away between the
  lookup and its synchronization, `Put` publishes its own copy instead.
  After a failure the final file stays for the retry; the temporary name is
  removed, but not durably.
- **Remove** unlinks the file and synchronizes its directory, also when the
  file was already gone: an earlier call may have unlinked it and failed
  before the barrier. If shard directories are missing, it synchronizes the
  nearest existing one and its ancestors through the store root; a missing
  store root is an error. The caller makes sure no row refers to the file;
  purging a document row (the [trash](#trash)) queues its files in
  `blob_removals`, and the pass that removes unreferenced files comes with
  #12, part 2.

Every call that takes a hash (`OpenHash`, `Exists`, `Remove`) accepts only
a lowercase hex SHA-256 and builds the path itself, so no input reaches a
file outside `documents/`; a malformed hash gives `ErrNotHash` (`Exists`:
false). There is no access by path.

Stale temporary names in `tmp/` are possible after a crash. A name left
after publication is a second hard link to a live blob. `Walk` reports every
file below `documents/` and `tmp/` (blobs, temporary files with their age,
and anything unexpected such as symlinks, wrong names or extensions) without
following symlinks, for the sweep and a later consistency check.

The guarantee holds on Linux, for a data directory on one local filesystem
that supports hard links and `fsync` on regular files and directories, on a
device that honours flushes. Storage without hard links or directory
`fsync` fails every write instead of falling back to weaker behaviour. A
container can only synchronize what it sees: the operator provisions the
host side of a bind mount or volume durably. File shares of Docker Desktop
(macOS, Windows) are not tested; if one lacks hard links or directory
`fsync`, writes fail. Other platforms build, without a durability claim.

The tests in `internal/blobstore` prove the order of the barriers, error
propagation, retries and concurrent writers, with failures injected per
store (`fsops`). None of them proves that data survives a power loss. That
needs a disposable VM or a fault-injecting block device, power cuts at the
publication and removal boundaries, a restart, and a check that every
acknowledged blob is still there and every acknowledged removal is still gone.

## Ingest

`internal/ingest` turns one file into a document; the upload API, the
consume folder and the CLI import build on it. Per file, each step finishes
before the next starts:

1. **Detect** from the first 1 MiB, never from the name or a declared
   type: PDF (`%PDF-` within the first 1024 bytes), JPEG, PNG, or HEIC/HEIF
   (an `ftyp` brand `heic`, `heix`, `hevc`, `hevx`, `mif1` or `msf1`; any
   AVIF brand refuses the file). JPEG and PNG give their size through
   `image.DecodeConfig`, HEIC/HEIF through the `ispe` property of the
   primary item (a small box parser, no CGO); more than 50 megapixels, or a
   HEIC without a findable size, refuses the file. Nothing is stored for a
   refused file.
2. **Store** the file through the size limit (`PAPERLESS_GO_MAX_UPLOAD_SIZE`;
   exactly the limit passes) with `blobstore.Put`.
3. **Duplicate check** with `FindDocumentByOriginal` for the new document's
   owner.
4. **Insert** with `CreateDocument`, in the inbox; the title is the file's
   base name without extension (NFC, no control characters, at most 200
   characters). An `ErrDuplicate` from a concurrent import is a duplicate too.
   Titles and file names also lose bidirectional formatting characters
   (U+061C, U+200E/F, U+202A–E, U+2066–9), which could make a name display
   as something else.

The result is `Imported`, `Duplicate` (with the existing document's ID and
title) or `Refused` (`unsupported`, `too_large`, `too_many_pixels`,
`unreadable`); an error is transient, nothing was created and a retry may
succeed. Nothing calls `blobstore.Remove`: a blob left by a failure is
reused by the retry or swept as an orphan (#12). Images are stored as
originals; the CLI `import` walks a path (files only, symlinks skipped, sources
untouched), calls `Import` per file and writes one `document.import` audit
entry per run (the importer's per-file transactions have no audit entry of
their own); `--dry-run` uses `Detect` and `FindDocumentByOriginal` only. `CreateDocument` queues every new document for
[processing](#processing), which builds the archive PDF.

## Consume folder

`internal/consume` runs in `serve` when `PAPERLESS_GO_CONSUME_DIR` is set
(Compose: `/consume`, bind-mounted from `CONSUME_DIR`; `prepare-data`
creates it). One scan every 10 s (the trigger and the clock are injected):

- **Tree.** Everything is reached through an `os.Root` of the consume
  folder and checked with `Lstat`: only real directories are user folders,
  only regular files are inputs; symlinks are never followed (skipped, left
  in place, logged once). A folder or file opened after the check must be
  the same file (`os.SameFile`), and files are opened with `O_NONBLOCK`, so
  a FIFO swapped in cannot block the scan.
- **Users.** Each scan lists the users and creates the missing folders
  (with the consume folder's permissions). Names are matched exactly; names
  that are not a single safe component (`.`, `..`, a leading dot) get no
  folder. Unknown entries, loose files and locked users' folders are left
  alone with one warning each (a problem that goes away is forgotten).
- **Settling.** A file is imported when an earlier scan at least 2 s ago saw
  the same file (identity), size and modification time. Hidden names,
  `*.part`, `*.tmp`, `~$*`, subfolders and `failed/` are ignored.
- **Completeness.** A settled PDF also needs `%%EOF` in its last 1 KiB, a
  JPEG `FF D9` and a PNG its `IEND` chunk in the last 32 bytes (some padding
  allowed), so a writer that paused for longer than two scans does not get a
  truncated file imported and deleted. A file without the marker waits and
  is imported anyway after 10 minutes unchanged (`IncompleteTime`).
  HEIC/HEIF has no reliable marker and only settles.
- **Outcomes.** `Imported` and `Duplicate` delete the file, after the
  committed row, only if `Lstat` still shows the version that was read.
  A `Duplicate` whose existing document is gone (empty ID) keeps the file
  for the next scan, which imports it as new.
  `Refused` moves it to `failed/` under a free name (`name-N.ext`, checked
  together with its sidecar, with the stem shortened so that the sidecar's
  name fits in 255 bytes; the check and the rename are not atomic, the
  consumer is the only expected writer) and writes `<name>.error.txt`. A
  read error of the file itself leaves it in place (logged once, held like
  below) and the scan goes on; any other import error (database, blob store, shutdown)
  stops the scan and leaves the files for the next one. `ErrUnknownUser` (a
  user removed during the scan) skips the rest of that user's folder.
  `Run` logs a failing scan once until the error changes.
  A file that was read but cannot be deleted or moved (a folder that is not
  writable, `failed/` that is not a folder) is held for an hour unless it
  changes, so it is not read and stored again at every scan.
- **Overlap.** `serve` refuses a consume folder that is the data directory,
  inside it or around it (after resolving symlinks): the consumer would
  delete document files.
- **Limit.** A file that changes after it settled but while it is read may
  be imported in its partial state; it then stays and is imported again as
  a new document once it settles. The recheck before `Remove` and the
  `Remove` itself are not atomic.
- **Audit.** `Options.Audit` (in `serve`, `store.Audit`; nil writes
  nothing) gets one `document.consume` entry per user folder and scan that
  changed something: actor `system`, no user or client, target the user
  (ID and name), details `imported`, `duplicates`, `failed` and
  `interrupted: true` when the folder's pass stopped on an error or
  cancellation. Outcomes are counted, not attempts: a duplicate only once
  `remove` deleted it, a refused file only once it moved to `failed/`, so
  a held file tried again after `HoldTime` adds nothing. The entries are
  written at the end of `Scan` (deferred, so also after an error, an
  unknown user or shutdown), with `context.WithoutCancel` and a 10 s
  timeout. An entry that cannot be written is logged once (until the error
  changes) and kept in memory per user ID; every scan tries the kept
  entries again, adding its own counts, also when it changed nothing
  itself. They are lost only if the process exits first. The log has an
  info line per consumed file, with its name; the entry has none.
  Processing results are not audited (maintainer decision on #49): the job
  state and `processing_note` show them.

## Processing

A queue in PostgreSQL turns stored documents into searchable ones
(`internal/process`, `internal/store/jobs.go`, migration `00007_jobs.sql`).

- **Queue.** `jobs` has one row per attempt series of a document
  (`kind` `process`; `state` `queued`, `running`, `done`, `failed`;
  `attempts`, `max_attempts` 3, `run_after`, `lease_token`,
  `locked_until`, `last_error`, `finished_at`) and cascades with the
  document. A partial unique index allows one queued or running job per
  document, so enqueueing is idempotent. `CreateDocument` queues a document
  without an archive PDF in its own transaction, which also sends
  `pg_notify('paperless_go_jobs')`; the migration queued every document
  without one.
- **Claim and lease.** A worker claims the oldest due job with
  `UPDATE … WHERE id = (SELECT … FOR UPDATE SKIP LOCKED)`, counts the
  attempt and leases it for 5 minutes under a random token; a heartbeat
  renews it every third of that. The reaper (every minute) ends attempts
  whose lease expired: they count, so a job that keeps crashing its worker
  fails after the third. Every write of a result or an end checks the
  token, so a stale worker cannot overwrite anything. Times come from the
  worker's clock (tests inject one); a new job is due by the database's
  clock too.
- **Retries.** A failed attempt runs again after 1, then 5 minutes; the
  third failure fails the job. Errors a retry cannot fix (a damaged PDF,
  one that needs a password to open, an unsupported type, an original whose
  content no longer matches its hash) fail at once. `last_error` is a fixed
  message from a mapping of exit statuses; tool output (the last 64 KiB of
  standard error) goes only to the log. `PAPERLESS_GO_OCR_TIMEOUT` limits
  each attempt. At shutdown, running jobs are given back without counting,
  also when the shutdown comes while the result is stored; Compose gives
  `serve` 30 s for it (`stop_grace_period`).
  Done jobs are deleted after 7 days, and a document's older finished jobs
  when a new one is done; a failed job stays as the document's status.
- **Worker.** `serve` runs `PAPERLESS_GO_OCR_WORKERS` workers as goroutines;
  `process` runs the same code in the foreground. Idle workers wake on a
  notification, received by `LISTEN` on a connection outside the pool, and
  poll every minute for retries and missed notifications.
- **Tools.** `Runner` runs each tool with `exec.CommandContext` in its own
  process group (SIGTERM to the group on cancel, killed 10 s later), with a
  minimal environment (PATH, a UTF-8 locale, HOME and TMPDIR in the job's
  directory) and fixed file names as arguments. Each process has a
  directory `work/proc-*` in the data directory, locked with `flock`; a
  starting process removes the directories nobody holds a lock on. Each
  attempt copies the original there (checking its SHA-256) and removes it
  afterwards.
- **Pipeline.** `pdfinfo` gives the page count and whether the PDF is
  encrypted, `pdftotext -enc UTF-8` the text, pages separated by form
  feeds. Text is stored without NUL bytes and with invalid UTF-8 replaced;
  more than 16 MiB of text fails the job (a decompression bomb or a data
  dump, not a document).
  A page with fewer than 15 characters (white space does not count) needs
  OCR. With text on every page, the original is the archive and
  `text_source` is `PDF-Text`.
- **OCR.** The pages that need it go to `ocrmypdf --pages <those pages>
  --rotate-pages -l <PAPERLESS_GO_OCR_LANGUAGES> --output-type pdf
  --optimize 1 --jobs <n>`: a plain PDF (no PDF/A; the original is kept
  anyway) with an invisible text layer, the other pages unchanged. Pages
  without any text get `--skip-text`; when one of them has a little text
  (a scanner's header, a page number), `--redo-ocr` runs instead, because
  `--skip-text` would skip such a page; it keeps the visible text. `--jobs`
  is the usable CPUs (`GOMAXPROCS`) divided by the workers, and each
  Tesseract runs with `OMP_THREAD_LIMIT=1`. The OCRed PDF becomes the
  archive, stored content-addressed in the blob store before the result
  commits, and its text (`pdftotext` again, same cap) the document's text
  with `text_source` `Tesseract` (the server's own value next to the
  app's), but only when it has more characters on the OCRed pages than
  before; otherwise (blank back sides, nothing readable) the document
  stays as without OCR.
- **Encrypted PDFs.** One that needs a password to open fails at once. One
  that opens without a password (an empty user password) is OCRed when its
  permissions allow changes (`change:yes` in `pdfinfo`): OCRmyPDF opens it
  itself and writes the archive without encryption. When they forbid
  changes, the original is the archive, with a `processing_note`; so is a
  PDF that forbids copying text, and one OCRmyPDF refuses although poppler
  reads it (exit status 2, 6 or 8: a digital signature, input it cannot
  parse, earlier OCR, encryption). Its other exit statuses are retried.
- **Images.** JPEG and PNG become a one-page PDF with
  `img2pdf --imgsize <dpi>dpi --rotation=ifvalid`, which embeds the image without re-encoding.
  The DPI comes from the image (JFIF, then EXIF for JPEG; `pHYs` for PNG)
  when it is between 100 and 2400; otherwise (cameras write 72 or 96) it is
  300. It only sets the page size. HEIC/HEIF is decoded with `heif-dec`
  to a JPEG (quality 92, 300 DPI) first. Before any conversion the pixel
  limit of ingest (50 MP) is checked again on the stored file, and on the
  decoded HEIC once more. The converted PDF then goes through the PDF
  steps; it is the archive even when OCR finds no text. An image the tools
  cannot read (exit status 1) fails at once.
- **Thumbnail.** After the pipeline, `pdftoppm -f 1 -l 1 -singlefile
  -scale-to 512 -jpeg -jpegopt quality=80` renders the first page of the
  archive (the original PDF, OCRmyPDF's output or the image's PDF) as a
  JPEG whose longer side is 512 pixels. It must be at most 2 MiB, have a
  JPEG header of at most 512×512 pixels and then decode in full (a
  truncated file is refused). It is stored in the blob store before the
  result commits, like the archive, and `documents.thumbnail_sha256`
  points to it. `pdftoppm` has a limit of its own, 60 s, within the
  attempt's. A thumbnail that cannot be made or runs out of that limit is
  logged (with the tool's standard error) and left out: the result is
  stored without it, and an earlier thumbnail stays. Only the end of the
  attempt's context (shutdown, the attempt's limit, lost lease) ends the
  attempt there; it wins when both end at once. A PDF that makes
  `pdftoppm` hang costs the 60 s (plus the 10 s before the kill) on each
  processing, also on a reprocess.
- **Status.** The document JSON has `processing`: the state of the
  document's newest job (`queued`, `running`, `done`, `failed`; `done`
  when it has none, as done jobs are deleted after a week), `attempts`,
  `maxAttempts`, and `error` (the job's `last_error`, a fixed message) for
  a failed job and for a queued one that failed before, which also has
  `retryAt`. One query per page reads the newest jobs
  (`LatestDocumentJobs`, `DISTINCT ON`). The start page loads the list
  again every 5 s while one of its documents is queued or running, also
  after a load that failed (the error stays until a load succeeds).
- **Upgrade.** Migration `00010_requeue_ocr.sql` queues again the
  documents the version before OCR failed for good (scans and images,
  selected by its messages) and those it left with the old owner-password
  note.
- **Text rule.** The result replaces `extracted_text` and `text_source` only
  when the text is empty or came from processing (`PDF-Text`,
  `Tesseract`), so text imported from the app (`Mistral OCR`) or edited
  stays; an edit must set another `text_source`. `modified_at` is not
  touched.
- **Tests.** Unit tests use a fake `Runner`, the test database and an
  injected clock. The tests with the real tools have the build tag `tools`
  and run in the Dockerfile's `toolstest` stage as UID 65532 (CI job
  `toolstest`). They generate every document: PDFs in Go, scans rendered
  from them with `pdftoppm` (a Go-written image-only PDF, a JPEG, a PNG),
  HEIC with `heif-enc` (the x265 plugin is only in that stage); pikepdf,
  which comes with ocrmypdf, encrypts and marks a PDF as signed.

## Web server

`serve` refuses to start while migrations are pending, the text indexes
were built under another locale (see [Data model](#data-model)) or the data
directory is not writable. Every request passes `protect` (`internal/web/middleware.go`):
the `Host` must be allowed (DNS rebinding), and state-changing requests need
a same-origin `Origin` (or `Sec-Fetch-Site: same-origin`) and the body kind
of their route in the route table (CSRF; requests with an API key are
exempt, see [Login](#login)): `bodyJSON`, the zero value, takes
`application/json` only; `bodyFile`, only on `POST /api/documents`, takes a
raw file with a type from `uploadContentTypes` and never a form or text.
Then `authenticate` checks the login (see [Login](#login)). Unknown
`/api/` paths answer `404` in JSON after the login. `/healthz` pings the
database and answers `503` when it does not answer. A request has 30 s to
be read (`ReadTimeout`); upload and download extend both deadlines to an
hour, so for every other handler the request context is cancelled after
30 s, since in HTTP/1 the background read of a request ends at the read
deadline.

## Documents API

`internal/web/documents.go`; the scope comes from `requestScope`, so
another user's document answers `404` and an admin sees all.

- **`POST /api/documents`** streams the body through `ingest`
  (`http.MaxBytesReader` as a second cap; a `Content-Length` above
  `PAPERLESS_GO_MAX_UPLOAD_SIZE` answers `413` before reading). The read
  and write deadlines are extended to an hour for this request
  (`http.ResponseController`). `X-File-Name` is percent-decoded and reduced
  to a clean base name (`ingest.FileName`); without one, the title is
  `upload-<UTC time>`. The owner is the caller, also for an admin.
  `document.create` is written in the creating transaction
  (`store.WithAudit` through the importer's context). Answers: `201` with the
  document, `200` with `{"duplicate": true, "existing": {id, title}}`
  (`existing` left out if the lookup failed, and for an API key without
  `read`, so an `upload` key learns only that the file is there), `413` `too_large`, `415`
  `unsupported`, `422` `unreadable` or `too_many_pixels` (with `reason`),
  `400` for a body that broke off, `503` without the audit log, `503`
  with `Retry-After: 5` when another change of the caller's documents ran
  longer than 15 s (Write locking, bounded wait; the body must be sent
  again). A duplicate of a trashed document adds `trashed: true` for a
  caller that may read; for a key without `read` the server restores it
  instead (see [Trash](#trash)); one due for purge answers `503` with
  `Retry-After: 60`.
- **`GET /api/documents`** lists and searches the scope's documents
  through `store.SearchDocuments`, without their text, with
  `maxUploadSize` for the UI. Parameters (`listQuery`):

  | Parameter | Values |
  |---|---|
  | `q` | The [search syntax](../guide/search.md), parsed by `search.Parse` in the zone of `tz`. |
  | `tag`, `correspondent`, `type` | UUIDs, repeatable (at most 100 each), any of them; joined with `q` by AND. |
  | `inbox`, `favorite` | `true` or `false`: an `is:inbox` or `is:favorite` filter. |
  | `owner` | `me`: only the caller's own documents (an admin's default view; without it an admin lists everyone's, the "Everyone's documents" switch). For an admin also a user ID: only that user's. A non-admin may name only themselves (`403`). The same `listScope` as the metadata lists. |
  | `trashed` | `true`: the trash instead of the live documents (trashed documents not yet due for purge), with every other parameter. |
  | `sort` | `relevance` (the default with free text that is not only negated), `added` (the default otherwise: with only negated words every match ranks 0, and ranking them all took 4–5 s on 100 000 documents), `date`, or `deleted` (the trash only, descending; its default without ranked text). |
  | `order` | `desc` (default) or `asc`; relevance has no ascending order. |
  | `tz` | An IANA zone (`time.LoadLocation`, zones built in through `time/tzdata`) for the days of `date:` and `added:`; default UTC. `Local` is refused. |
  | `limit` | 1 to 100, default 100 (the start page asks for 10, the search for 25); more is capped. |
  | `cursor` | `nextCursor` of the previous page, with the same other parameters. |

  The answer adds `nextCursor` (left out on the last page). With free-text
  items each document has `snippet`, plain text with U+E000 and U+E001
  around the matches. Without parameters the answer is the list as before.
  Invalid input answers `400` with fixed wording: `invalid query`, `query
  too long` (over `search.MaxBytes` or `search.MaxItems`, also with the
  `inbox` filter added; the store checks a hand-built query the same way,
  `Query.CheckLimits`), `invalid cursor` (malformed, from another sort or
  order, or a time outside PostgreSQL's range), `invalid sort`, `invalid
  order`, `invalid time zone`, `invalid tag id` (likewise for
  `correspondent` and `type`), `too many tag ids`, `inbox must be true or
  false`, `limit must be a positive number`. `failStore` maps the store's
  `ErrInvalidCursor` and `ErrInvalidSort` to `400`, `ErrRetry` to `503`
  with `Retry-After: 1` and `ErrBusy` (a write that waited 15 s for
  another change of the same owner) to `503` with `Retry-After: 5`; any
  other error is a logged `500` with `internal
  error`, so no database message reaches the client.
  The first page of a typed search (`q` with free text or search syntax,
  no cursor) writes `document.search` after the search ran, with `q` as
  typed and the `tag`, `correspondent`, `type`, `inbox`, `favorite` and
  `owner` parameters; if the entry cannot be written the answer is `503`
  and no result. The filter parameters alone are browsing (a sidebar
  click) and write nothing, like further pages, plain lists and refused
  searches (maintainer decision on #7).
- **Document JSON** has `ownerId`; for admins also `ownerName`, the
  owner's user name, so the UI can mark other users' documents and load
  that owner's metadata. `deletedAt` and `purgeAt` (when it is removed for
  good) are `null` while the document is live.
- **`DELETE /api/documents/{id}`** (write) moves a document to the trash
  (`204`; `409` `in_trash` when it is there already), **`POST
  /api/documents/{id}/restore`** brings it back (`200` with the document
  as `GET`; `409` `not_in_trash`), **`POST /api/documents/{id}/purge`**
  asks for its permanent deletion (`204`; `409` `not_in_trash`) and wakes
  the sweeper. A document due for purge is `404` for all three. Audit:
  `document.delete`, `document.restore`, `document.purge`, target the
  document, `ownerId` for an admin's change of another user's (see
  [Trash](#trash)).
- **`GET /api/documents/{id}`** returns one document with its text.
- **`GET /api/documents/{id}/file?kind=original|archive`** sends the file
  from the blob store (`OpenHash`) with `Content-Disposition: attachment`
  (RFC 6266: an ASCII `filename` plus `filename*=UTF-8''`), `nosniff` and
  `Content-Security-Policy: sandbox`. The Content-Type is the stored type
  only if the importer produces it (PDF, JPEG, PNG, HEIC, HEIF), otherwise
  `application/octet-stream`; the file name is the stem of the original
  name with the stored type's extension, so `x.html` holding a PDF
  downloads as `x.pdf`. The archive PDF answers `404` until processing
  made one.
- **`GET /api/documents/{id}/thumbnail`** sends the thumbnail as
  `image/jpeg`, inline, with `Content-Security-Policy: sandbox`,
  `Cross-Origin-Resource-Policy: same-origin` and the blob's hash as a
  strong `ETag` (a matching `If-None-Match` answers `304`). The document
  JSON has `thumbnailUrl`, this path with `v=` and the first 16 hex digits
  of the hash, or `null` until processing made one. With the current `v`
  the answer is `Cache-Control: private, max-age=86400, immutable` (a new
  thumbnail gets a new URL); without it or with an old one, `private,
  no-cache`. `404` without a thumbnail.
- **`POST /api/documents/{id}/reprocess`** (write; a JSON body, which may
  be `{}`) queues one document of the scope again through
  `store.Reprocess` and writes `document.reprocess` in the same
  transaction, with the document as target and `selection: ids`, like
  `reprocess ID`. Answers: `202` with the document when it was queued,
  `200` when it was queued or running already, `404` outside the scope,
  `409` `in_trash` for a document in the trash, `503` without the audit
  log.
- **`PATCH /api/documents/{id}`** (write) changes only the fields sent
  (`store.PatchDocument`): `title` (at most 1 024 characters and no
  control characters (Unicode Cc: NUL, tab, line breaks, escape …), like
  the titles the import makes and metadata names; empty is allowed, the
  UI shows the file name), `documentDate`, `notes` (at most 100 000
  characters; of the control characters only tab, line feed and carriage
  return; the body may be up to 1 MiB), `isFavorite`,
  `isInbox`, `archiveSerialNumber`, `correspondentId`, `documentTypeId`
  (`null` clears these three) and `tagIds` (replaces the tags). `null` for
  any other field answers `400` `invalid_field`, an empty body `400`
  `nothing_to_change`. The optional `expectedModifiedAt` is a precondition,
  no field to change (not in `fields`, alone it is `nothing_to_change`):
  the document's `modifiedAt` exactly as `GET` or the last `PATCH`
  answered it (RFC 3339 with microseconds; the UI keeps the string and
  never passes it through a `Date`, which has milliseconds only). It
  reads the document's owner, then one transaction takes the owner's gate
  and write lock (Write locking, under Search index), locks the row (`LockDocument`,
  `FOR NO KEY UPDATE`: no key column changes, so a foreign-key check of a
  row referencing the document, such as a new job, does not wait; it also
  returns `modified_at`), checks the precondition after the lock (`404`
  first, then `409` `document_changed` with the current `modifiedAt`:
  nothing changes, no audit entry, no retry), updates the given
  columns (the search triggers follow), replaces the tags, advances
  `modified_at` and writes `document.update` (target the document, no
  target name; `fields`, the names of the fields sent; `ownerId` for an
  admin's change of another user's document). Processing writes only its
  own columns (text, archive, thumbnail), so an edit and a finishing job
  never undo each other. Answers: `200` with the document as `GET`,
  `400` `owner_mismatch` (a tag, correspondent or type that is not the
  document owner's, also for an admin), `invalid_title`, `invalid_notes`,
  `invalid_asn`, `404` outside the scope, `409` `asn_taken` with
  `documentId` of the document that has the number, `409`
  `document_changed` with `modifiedAt`, `409` `in_trash` for a document
  in the trash (`asn_taken` adds `trashed: true` when the document with
  the number is in the trash, where it keeps it), `503` without the
  audit log or after a deadlock with SQL outside the store (`Retry-After:
  1`), `503` with `Retry-After: 5` when another change of the owner ran
  longer than 15 s (nothing changes). Without
  `expectedModifiedAt` a change goes through as before (API clients that
  set one field need not read first); the UI always sends it, and on
  `document_changed` loads the document again and keeps the user's edits
  on top (`rebaseForm`: the user's changed fields, tags as added/removed),
  then asks to save again; it never saves by itself (#105).
- **`modified_at` advances strictly on every document write** (#105). A
  write of a field `PATCH` sends or of `document_tags` must advance
  `modified_at`, with `modified_at = GREATEST(now(), modified_at +
  interval '1 microsecond')`: `now()` is the transaction's start, so two
  writes in one transaction, in the same microsecond or after the clock
  stepped back would otherwise get the same value, and an equal value
  must mean "unchanged since read". `PatchDocument` and
  `BulkUpdateDocuments` use it;
  processing (`SetDocumentProcessed`) writes no such field and leaves
  `modified_at` alone, so OCR finishing causes no false conflict. A new
  writer (classification, an import into existing documents) must use the
  same form; `TestDocumentUpdatesAdvanceModifiedAt` checks every `UPDATE
  documents` in `internal/store/queries` with `SetDocumentProcessed` and
  the trash's three writes (`TrashDocuments`, `RestoreDocuments`,
  `PurgeDocuments`: no field of the document changes) as the only
  exceptions. Foreign-key actions (a deleted correspondent, type
  or tag) do not advance it: `PATCH` sends only changed fields, and a
  stale deleted tag ends in `owner_mismatch`.
- **`POST /api/documents/bulk`** (write) changes several documents, all
  or nothing (`store.BulkUpdateDocuments`). The selection is exactly one
  of:

  | Field | Selects |
  |---|---|
  | `ids` | These documents: at most 500 (`store.MaxBulkIDs`) after removing repeats, each in the scope, else `404` and nothing changes. An empty list is `400` `empty_selection`. |
  | `match` | Every match of a search across all pages: an object with the list's parameters `q`, `tag`, `correspondent`, `type` (arrays), `inbox`, `favorite` (booleans), `owner` and `tz`. The server reads it with `listQuery` and `ownerScope`, the code of `GET /api/documents`, and resolves it in the transaction with the search's own filter block (`MatchDocumentIDs`); a client-sent ID list plays no part. `owner` only narrows the match (`BulkSelection.Owner`); the change runs in the caller's scope, so the audit records `ownerId` for another user's documents with `owner=<id>` too. At most 5 000 matches (`store.MaxBulkMatches`), else `400` `too_many_documents` with `matched` and `limit`, and nothing changes. |

  The change: `addTags`, `removeTags` (UUID arrays; a tag in both is
  `400` `tag_add_and_remove`; IDs are compared as UUIDs, so repeats and
  another case count as the same tag), `correspondent`, `documentType` (a UUID,
  or `null` to clear), `inbox`, `favorite` (booleans). Adding a tag a
  document has, or removing one it lacks, is no error; a tag to add, a
  correspondent or type of another owner is `400` `owner_mismatch`.
  Tags, or a correspondent or type to set, need documents of one owner:
  an admin's selection across owners answers `400` `several_owners`;
  inbox, favourite and clearing work across owners. Nothing to change is
  `400` `nothing_to_change`. The answer is `{updated}`, `0` for a search
  whose dry run matched nothing either (which writes no entry).

  **Actions.** `action` `trash`, `restore` or `purge` (absent or `update`:
  the change above; another value is `400` `invalid_action`) takes no
  fields (`400` `invalid_field`). By `ids` every document must be in the
  action's state, else `409` `in_trash` or `not_in_trash` and nothing
  changes; `match.trashed` must be `true` for restore and purge and
  absent for the others, and a purge by `match` needs `match.owner`
  (both `400` `invalid_selection`). The dry run takes the `action` too:
  its fingerprint is for that action. Audit: `document.bulk_delete`,
  `bulk_restore`, `bulk_purge` as the summary and `document.delete`,
  `restore`, `purge` per document (see [Trash](#trash)).

  **Dry run.** `{"match": {...}, "dryRun": true}` changes nothing and
  answers `{matched, owners, limit, fingerprint}`: the number of matches
  (also above the limit), their owners (`0` above the limit), the limit,
  and the fingerprint of the matched set (left out above the limit, where
  the change is refused anyway; for no matches, the empty set's), so the
  UI shows the count and asks before it sends the change. The list itself
  has no total.

  **The confirmed set (#106).** A change by `match` must send the dry
  run's `fingerprint` (else `400` `invalid_selection`; a `fingerprint`
  with `ids` or with `dryRun` is refused the same way). It is applied
  only to exactly the documents the user confirmed: when the matches after
  the recheck under the lock (below) are not that set (one more, one less,
  or the same count with another document), nothing changes, no entry is
  written, and the answer is `409` `{"code": "match_changed", "matched":
  n}`. `n` is the number the change would cover now (also `0`), for
  information only: the client counts again with a dry run (which also
  gives `owners` and `limit`) and asks again; the UI never resends by
  itself. The fingerprint is opaque: `v2.` and the unpadded base64url
  (`RawURLEncoding`) SHA-256 of the action's name, a zero byte and the
  matched IDs, sorted by their 16 bytes and concatenated
  (`store.matchFingerprint`; `v1.` without the action is refused as
  `match_changed`); clients pass it back
  unchanged, never compute it, and compare nothing. It grants nothing (it
  only narrows a change the caller may make anyway), so it carries no key.
  It needs no client-sent ID list, so the server still resolves the
  search itself (maintainer decision 6 of #7). The limit is checked first:
  a set that grew past 5 000 is `too_many_documents`, not `409`.

  **Locking.** One transaction resolves the selection, locks the
  documents in id order (`LockDocuments`, `ORDER BY id FOR NO KEY
  UPDATE`, so two bulk changes over overlapping sets wait for each other),
  by `match` runs the search again with the documents locked and keeps
  only those that still match (the first match ran before the lock under
  READ COMMITTED, so a document a concurrent change took out of the match
  in between is left alone; `TestBulkUpdateByMatchRecheckedAfterLock`
  holds the bulk change between match and lock), removes and
  adds the tags (one `DELETE`, one `INSERT … ON CONFLICT DO NOTHING`),
  updates the columns and advances `modified_at` of every document. The
  recheck runs over the locked IDs only (`only_ids` in
  `MatchDocumentIDs`, outside the filter block), so it returns at most
  those and the limit cannot cut any off (#99,
  `TestBulkUpdateByMatchRecheckKeepsLockedMatches`). The selection is
  fixed at the first match, which the limit was checked on: documents
  that start matching between the match and the lock are neither added
  nor a reason to refuse. The locked documents that still match are then
  compared with the confirmed fingerprint (`confirmMatch`); a document
  that left the match in that window makes them differ, so the change is
  `409` instead of changing the rest (`TestBulkUpdateByMatchRecheckedAfterLock`;
  without the recheck the locked set would still fit the fingerprint and
  the document would be changed). Before its documents the bulk change
  takes the write locks of their owners (Write locking, under Search
  index): by IDs the owners of the IDs in the scope; by `match` in a
  scope of one owner that owner, before the match, so in that case no
  store writer can change the match before the documents are locked
  (only SQL outside the store); in an admin's scope over all owners the
  owners of the first match, after it (that match runs before the
  transaction). There is no retry: a deadlock with SQL outside the store
  answers `503` with `Retry-After: 1`; a change that waited 15 s for
  another change of the same owners answers `503` with `Retry-After: 5`
  and changes nothing.

  **Audit.** One `document.bulk_update` per change (no target; `count`,
  `fields`, `selection` `ids` or `match`, `ids` of the changed documents,
  `match` with the search's parameters as `document.search` records them,
  and `bulkRun`, a random ID) and one `document.update` per document (the
  document as target, `fields`, `bulkRun`, `ownerId` for another user's),
  all in the change's transaction (`InsertAuditEntries`, one statement).
  Field names only: never the tags, links or other values set.

## Metadata API

`internal/web/metadata.go`, on `store` functions in `metadata.go`. Tags,
correspondents and document types each have `GET` and `POST` on
`/api/tags`, `/api/correspondents` and `/api/document-types`, and `PATCH`
and `DELETE` on `/api/{kind}/{id}`. Reads are `accessRead`, changes
`accessWrite`; the scope comes from `requestScope`, so another user's row
answers `404` and an admin may change everyone's (the row keeps its owner).

- **Lists** hold `id`, `name`, `ownerId`, `createdAt`, `modifiedAt` and
  `documentCount`; tags add `parentId`, `color`, `symbol` and
  `subtreeCount`, the distinct documents of the tag and its descendants
  (what a `tag=` filter lists; summing the children's counts would count a
  document with a parent and a child tag twice). One aggregate query per
  list (`List*WithCounts`, the subtree by a recursive CTE). `owner=me`
  limits an admin to their own rows; `owner=<user ID>` (admins only; a
  non-admin may name only themselves, else `403`) to one user's, for the
  pickers of another user's document. For admins each row also has
  `ownerName` (one `ListUsers` per list, as for documents), so the
  metadata page can name the owner of another user's row; nothing for
  anybody else.
- **`POST`** creates a row owned by the caller: `name`, for tags also
  `parentId` and `color`. An admin may pass `ownerId` to create it for
  another user (inline creation on that user's document); anybody else
  gets `403` for another owner, an unknown owner `400` `unknown_owner`.
- **`PATCH`** changes only the fields sent: `name`, for tags also `color`
  and `parentId` (`null` makes a root tag). `matchText`, `matchMode` and
  `symbol` stay as stored; unknown fields and an empty patch answer `400`.
  `store.Patch*` takes the owner's write lock (Write locking, under
  Search index), locks the row (`SELECT … FOR NO KEY UPDATE`), merges and
  writes in one transaction, so a concurrent patch of another field is not
  lost. Opposite tag moves of one owner serialise at the owner's lock:
  one succeeds, the other answers `409` `tag_cycle`. A `POST` or `PATCH`
  that waited 15 s for another change of the owner answers `503` with
  `Retry-After: 5` and changes nothing.
  Only the fields sent are checked: a name of 1 to 128 characters after
  trimming, no control characters (Unicode Cc, also NUL; checked before
  trimming, so a trailing newline is refused) (`invalid_name`), a colour `#RRGGBB` (`invalid_color`); an
  imported name that is longer does not block a colour change.
- **`DELETE`** answers `204`; documents keep everything else, a deleted
  tag's children become root tags. The delete reads the row's owner,
  takes the owner's write lock (Write locking, under Search index) and
  deletes; the cascade, the `SET NULL` and the search triggers run under
  that lock. A deadlock with SQL outside the store answers `503` with
  `Retry-After: 1`; there is no retry. A delete that waited 15 s for
  another change of the owner answers `503` with `Retry-After: 5`.
- **Errors** carry `error` and `code`: `409` `name_taken` (case-insensitive
  per owner, per parent for tags), `409` `tag_cycle`, `409`
  `tag_name_conflict` (a child would clash with a root tag on delete;
  `childId` and `childName` when the lookup found it; nothing changes),
  `400` `owner_mismatch` (a parent of another owner, or no such tag).
- **`GET /api/documents/counts`** answers `{inbox, favorites, total,
  trash}` for the scope, with `owner` as for the lists: the first three of
  the live documents, `trash` the trashed ones not yet due for purge. The
  `documentCount` and `subtreeCount` of the metadata lists leave trashed
  documents out.
- **Audit.** `tag.create`, `tag.update`, `tag.delete`, likewise
  `correspondent.*` and `document_type.*`, in the transaction of the
  change (`503` and no change without the log). The target is the row's
  UUID and name; details hold `name`, `oldName` on a rename, `fields` (the
  names of the fields an update sent) and `ownerId` when the owner is not
  the acting user (an admin's change). Values other than names are never
  recorded.

## Login

Ported from mail-archive (`internal/auth`, the auth parts of `internal/web`
and `internal/store`, `cmd/paperless-go/users.go`).

- **Route table.** `internal/web/routes.go` lists every route with its
  `access`: `accessPublic` (`GET /healthz`, `GET /{$}`, `GET /assets/`),
  `accessLogin` (`/api/session`: read, log in, log out, and `/api/session/2fa`),
  `accessSession`, `accessRead`, `accessUpload` or `accessWrite` (a
  session, or an API key with that scope: the document and metadata
  routes; the upload alone is `accessUpload`, which a `write` key passes
  too).
  The zero value is `accessSession`, so a route that forgets it gets the
  strictest rule, and so does every path without a route (the JSON `404`
  under `/api/` comes only after the login). `authenticate` enforces the
  table; one test walks it and expects `401` without a session, another
  calls every route with a key of every scope combination.
- **Sessions.** The cookie `paperless_go_session` (`__Host-` prefixed over
  HTTPS) holds a random token; `sessions` stores its SHA-256 with the user,
  creation, last use and absolute expiry. Sessions end after 7 days unused
  and after 30 days; `serve` deletes ended ones hourly. Every time a
  session check uses comes from the server clock, so tests move a clock
  instead of waiting.
- **Must change password.** A generated password (admin creates or resets a
  user) restricts the session to `PUT /api/profile/password`; everything
  else answers `403` with `passwordChangeRequired`.
- **Scope.** `requestScope` (`internal/web/auth.go`) is the one place that
  turns the request's principal into a `store.Scope`: the session's or API
  key's user, with `All` for admins. Handlers that touch documents or metadata take
  their scope from it.
- **Removing users.** Auth tables reference `users` with `ON DELETE
  CASCADE`, owned data with `ON DELETE RESTRICT`. `DeleteUser` counts the
  owned rows per table (documents, which cover their deadlines, tags,
  correspondents, document types, saved views) and refuses with the counts;
  a refusal by the database itself (`23001` from PostgreSQL 18, `23503`
  before) gives the same error. The last unlocked admin stays.
- **Client address.** The login limits count per user name and per client
  address (IPv6 per `/64`; logs and the audit log keep the full address);
  `X-Forwarded-For` counts only from `PAPERLESS_GO_TRUSTED_PROXIES`, and
  `X-Forwarded-Proto` (`secureRequest`: the `__Host-` cookie with
  `Secure`) only from those or loopback. `Handler` logs one warning per
  `Server` (one per process) for an `X-Forwarded-For` from any other peer,
  loopback included, with the peer's address only.
- **API keys** (`internal/auth/apikey.go`, `internal/store/api_keys.go`,
  `internal/web/api_keys.go`, migrations `00009_api_keys.sql` and
  `00017_api_key_upload_scope.sql`). A key is
  `pl_` and 32 random bytes in lower-case base32; `api_keys` stores its
  SHA-256 (unique index; the lookup compares hashes), the last 4
  characters, name (unique among a user's keys that are not revoked),
  scopes (`read`, `upload`, `write`; `NormalizeScopes` stores a `write` key
  without `upload`, which `write` includes), creation, expiry, last use (written at most once
  a minute) and revocation, all on the server clock. `authenticate` treats
  every request with `Authorization: Bearer pl_…` as a key request and
  never reads its cookie; `protect` skips the Origin and content type checks
  for it. `authenticateKey` checks the key (invalid: `401`, 30 per client and
  minute, then `429`), the per-key rate (600 a minute), the owner's gates
  (must change password, 2FA required), and the route's scope
  (`requiredScope` and `APIKey.Allows`; `403` for every other access). The key's user becomes
  the request's session, so `requestScope` and the audit entry (actor
  `api_key`, `api_key_id`, `api_key_name`) follow. Creating a key
  (`POST /api/profile/api-keys`) checks password and second factor like a
  passkey registration and writes `api_key.create` in the insert's
  transaction; revoking (`DELETE /api/profile/api-keys/{id}`, admins
  `DELETE /api/api-keys/{id}`, `user revoke-api-keys`, a password reset)
  writes `api_key.revoke`. `serve` deletes keys revoked or expired 30 days
  ago hourly.
- **Two-factor login** (`internal/auth/totp.go`, `internal/store/two_factor.go`,
  `internal/web/two_factor.go`). Admins always need it,
  `PAPERLESS_GO_REQUIRE_2FA` extends it to everyone. A user who needs it but
  has none gets a session restricted to `GET /api/profile/2fa` and `POST
  /api/profile/2fa/setup|confirm` (`403` with `twoFactorSetupRequired`
  elsewhere, after a must-change password). With 2FA on, `POST
  /api/session` answers with a challenge (SHA-256 stored, 5 minutes on the
  server clock), and `POST /api/session/2fa` (`accessLogin`, same failure
  limits) checks the code, uses up the challenge, creates the session and
  writes `auth.login` in one transaction. `users.two_factor_version`
  changes with every 2FA change and invalidates pending challenges; the
  last accepted time step (`two_factor_last_counter`) stops replays. The
  secret is sealed with AES-256-GCM, associated data the user ID;
  recovery codes are HMAC-SHA-256 with a key derived from the secret key.
  Turning 2FA off and new recovery codes check the code in the same
  transaction as the change, so the audit entry records the factor used.
  `serve` refuses to start without the key.
- **Passkeys** (`internal/store/passkeys.go`, `internal/web/passkeys.go`,
  go-webauthn, migration `00008_passkeys.sql`). Off without
  `PAPERLESS_GO_PUBLIC_URL` (`503` from the endpoints); its host is the RP
  ID and an allowed host. `users.webauthn_handle` is a random 32-byte user
  handle made at the first registration. `webauthn_ceremonies` holds the
  library's session data of a registration or login under the SHA-256 of
  the client's token; it is used up in its own statement (a failed check
  still consumes it) and expires on the server clock. Registration
  (`/api/profile/passkeys/begin|finish`) checks password and second factor
  first and locks the user row (limit 10, unique name per user); the login
  (`/api/session/passkey/begin|finish`, `accessLogin`, discoverable, user
  verification required; at most 20 open logins per client (`client_key`:
  IPv4 address or IPv6 `/64`), 100 per `client_net_mid` (IPv6 `/56` or
  IPv4 `/24`), 250 per `client_net_wide` (IPv6 `/48`; none for IPv4,
  `00014_ceremony_networks.sql`) and 1000 in all, checked under an
  advisory lock in the insert's transaction; the ceremony cookie
  `paperless_go_passkey_login` names the browser's open login, which its
  next begin replaces in that transaction and a correct password deletes)
  locks user, then passkey row, rejects a sign
  count that did not increase, and stores the new count, the session and
  `auth.login` in one transaction. The session it creates is restricted
  like a password login's. Removing one passkey ends the user's other
  sessions; an admin's removal of all (`DELETE /api/users/{name}/passkeys`,
  `user remove-passkeys`) and a password reset (unless `removePasskeys:
  false` / `--keep-passkeys`) end all sessions.

## Audit log

Ported from mail-archive (`internal/store/audit.go`, `internal/web/audit.go`,
`cmd/paperless-go/audit.go`, migration `00005_audit_log.sql`).

- **Table.** `audit_log` is partitioned by month (`at`, from the database
  clock). Each row has the actor (`session`, `api_key`, `cli`, `system`), the user's
  ID and name at the time, the action (`area.verb`), an optional target
  (`document`, `user`, `api_key`, and since `00016` `tag`, `correspondent`,
  `document_type`; a document or metadata target must be a UUID), client
  address, a user agent (stored once in `audit_user_agents`) and details as
  a JSON object. `user_id`, `api_key_id` and `target_id` have **no foreign
  keys**: removing a user keeps their rows, and the log never blocks a
  removal.
- **Actors.** `user_id` is who acted. `cli` and `system` (the server on
  its own, `00015`) have no client IP or user agent
  (`audit_log_actor_no_client`); `system` has no user either
  (`audit_log_system_no_user`), and the user it concerns is the target, so
  that user sees it through the target branch of the log page. The checks
  are on the partitioned table and reach every partition, also later ones.
  The down migration of `00015` refuses while `system` rows exist (the old
  checks would refuse them, and the log is append-only).
- **Metadata targets.** `00016_audit_metadata_targets.sql` replaces the
  target-type check and the document UUID check of `00005`, which it finds
  on the parent table by what they are (exactly one each, or the migration
  stops), with `audit_log_target_type_check` and `audit_log_target_uuid`.
  The checks are on the partitioned table and reach every partition, also
  later ones. Its down migration refuses while metadata entries exist.
- **Append-only.** Row triggers refuse `UPDATE` and `DELETE` on the log and
  its partitions, statement triggers refuse `TRUNCATE`; all are `ENABLE
  ALWAYS`, so `session_replication_role = replica` does not skip them. The
  owner also revokes `UPDATE`, `DELETE` and `TRUNCATE` from itself (no
  effect for a superuser). The down migration refuses while entries exist.
- **Partitions.** `audit_log_ensure_partitions(n)` creates the current and
  the next `n` months, with the trigger and revoke partitions do not
  inherit; there is no default partition. The migration creates 3 months
  ahead, `serve` keeps 3 ahead (at start and hourly), every command 1. An
  insert that finds no partition creates it: on its own it retries once; in
  a transaction it fails and the next attempt works.
- **Same transaction.** `store.WithAudit(ctx, entry)` hands an entry to the
  next store transaction (`inTx`), which writes it after the change and
  before the commit. A change that fails writes nothing; an entry that
  cannot be written rolls the change back (`ErrAuditUnavailable`, `503` in
  the API, an error in the CLI). The user agent ID is looked up before the
  transaction starts, so a full pool cannot deadlock.
- **Events.** `auth.login`, `auth.logout` (same transaction as the session
  change; a logout without its entry still ends the session),
  `auth.login_failed` (best effort, with `reason` and, for an unknown user,
  the tried name only if it is a valid user name), `user.add`,
  `user.remove`, `user.lock`, `user.unlock`, `user.promote`, `user.demote`,
  `user.password_reset`, `user.password_set` (CLI), `user.2fa_reset` (web
  and CLI), `profile.password_change`, `profile.2fa_enable`,
  `profile.2fa_disable`, `profile.recovery_codes` (with the `factor` that
  confirmed it), `profile.passkey_add` (also with the confirming `factor`
  when 2FA is on), `profile.passkey_remove` (with the passkey's `name`),
  `user.passkeys_removed` (`count`; web and CLI), `api_key.create` (target
  the key, `scopes`, `expiresAt`), `api_key.revoke` (target the key and
  `owner`, or for all of a user's keys the user and `apiKeysRevoked`;
  `reason` `user`, `admin` or `cli`), `audit.export` (CLI,
  written before the export starts), `document.create` (upload, with
  `via: upload`; the target is the new document), `document.search` (the
  first page of a typed search, with `q` and the filter parameters; see
  [Documents API](#documents-api)), `document.update` and
  `document.bulk_update` (edits, with field names only; see [Documents
  API](#documents-api)), `document.import` (CLI),
  `document.consume` (system; see [Consume folder](#consume-folder)),
  `tag.*`, `correspondent.*`, `document_type.*` (see [Metadata
  API](#metadata-api)).
  A two-factor login records `method: 2fa` and its `factor` (`totp`,
  `recovery_code` with `recoveryCodesLeft`); a wrong code gives
  `auth.login_failed` with `reason: second_factor`. Requests refused
  by the login limiter write nothing, so a flood cannot grow the log.
- **Reading.** `GET /api/audit` takes its scope from `requestScope`: users
  get their own rows (`audit_log_user_idx`) and the rows about them
  (`target_type = 'user'`, partial index `audit_log_target_user_idx`) as a
  `UNION ALL` of two index scans, admins everyone's, optionally narrowed by
  `user` or `actor`. A row about a user made by someone else is marked
  `byOther` and reduced (`redactForeign`): no client IP, user agent or API
  key name, and only allowlisted detail keys; a new key stays admin-only
  until it is added to the allowlist. Paging is by a `(at, id)` cursor, newest
  first. Reading the log writes no entry.

## PDF viewer

The document detail (`web/src/DocumentDetail.tsx`) draws PDFs with pdf.js
(`pdfjs-dist`, maintainer decision on #7), never as a document of its own:
the file endpoint keeps `Content-Disposition: attachment` and
`Content-Security-Policy: sandbox`, and the PDF is fetched with the session
cookie and drawn into canvases.

- **Loading.** `pdfViewer.ts` is the interface (`loadPdf(url) → {numPages,
  pageSize, renderPage}`); it imports `pdfjsLoader.ts` with `import()`, so
  pdf.js (about 0.5 MB, plus its 1.3 MB worker) is a chunk of its own,
  loaded only when a detail opens.
- **Legacy build.** The loader uses `pdfjs-dist/legacy/build`. The modern
  build needs the newest JavaScript (`Map.prototype.getOrInsertComputed`)
  and draws nothing in browsers a year old, iOS Safari among them.
- **Same origin only.** The worker is a file of the build
  (`pdf.worker.min.mjs?url`). The cMaps, the standard fonts and the
  JavaScript decoders for JPEG 2000 and JBIG2 are copied by a plugin in
  `web/vite.config.ts` to `/assets/pdfjs-<version>/` (`/assets/` is cached as
  immutable, so the version is in the path) and served by `pnpm dev` too.
- **CSP unchanged.** Workers fall back to `script-src 'self'`. pdf.js 6 has
  no eval (`isEvalSupported` is gone). `pnpm check:dist`
  (`web/scripts/check-dist.mjs`, a step of CI's `web` job after the build)
  scans every `.js`/`.mjs` of the built `dist/assets` for `eval(` (also
  `(0, eval)(`) and `new Function(`, so a new dependency or a Vite
  transform is caught too; a missing build, main chunk, worker or fallback
  decoder fails it. `pdfViewer.test.ts` runs the same rules
  (`web/scripts/noEval.mjs`) over pdf.js's package files as a fast local
  check. The legacy build's core-js has only its `Function("return this")`
  fallback, reached without `globalThis`, which is allowed. WebAssembly is off (`useWasm: false`), so `'wasm-unsafe-eval'`
  is not needed: JPEG 2000, JBIG2 and CCITT images use the decoders'
  JavaScript versions, and ICC colour profiles fall back to their alternate
  colour space. `TestContentSecurityPolicy` (`internal/web/ui`) pins the
  policy.
- **What is drawn.** Pages are drawn while near the visible part (an
  IntersectionObserver), with a text layer of text nodes (select, find).
  No annotation layer, no XFA, no PDF scripts. Unprocessed JPEG and PNG
  originals show as a plain `<img>` of the file endpoint (`img-src 'self'`;
  the sandbox header does not apply to images); HEIC shows a note and the
  download.
- **Dependencies.** `pdfjs-dist` is pinned exactly, like every web
  dependency, and Dependabot updates it. Its optional dependency
  `@napi-rs/canvas` (native binaries, used only when pdf.js runs in Node)
  is removed by an override in `web/pnpm-workspace.yaml`, which the
  Dockerfile copies with the lockfile.

## Container

The runtime image is `debian:trixie-slim`, pinned by the digest of its
multi-arch index (Dependabot proposes updates). CI caches image layers in
the GitHub Actions cache; the release build always rebuilds the runtime
stages (`no-cache-filters: runtime-base,runtime`: the packages are in
`runtime-base`, which the `toolstest` stage shares), so each release
carries the current Debian security updates of the OCR tools. The build stages stay on
Alpine: the web UI is built with Node, and the Go binary is cross-compiled
with `CGO_ENABLED=0`, so it needs no system library and the arm64 image
builds without emulation. Debian replaced the earlier distroless base because
OCR needs external programs, installed with `--no-install-recommends`:

| Package | For |
|---|---|
| `ocrmypdf` (16.x in trixie), with Tesseract and Ghostscript | The text layer of scans |
| `tesseract-ocr-deu`, `-eng`, `-osd` | German and English, page orientation |
| `poppler-utils` | `pdfinfo`, `pdftotext` (text per page), `pdftoppm` (thumbnails) |
| `img2pdf` | JPEG and PNG to PDF without re-encoding |
| `libheif-examples`, `libheif-plugin-libde265` | `heif-dec` (HEIC to JPEG; trixie keeps `heif-convert` as a link to it) and its HEVC decoder plugin, which is not installed without recommended packages |
| `ca-certificates` | TLS to optional external services |

The image is about 145 MB compressed per architecture (about 540 MB
unpacked, measured for amd64). It runs as `nonroot`, UID and GID 65532, as
the distroless image did, so existing data directories keep working;
`./pgo migrate` makes a fresh data directory writable for it
(`prepare-data`, run with `--user 0:0`, which re-executes itself with the
numeric UID). The compose smoke test checks the tools as that user.
