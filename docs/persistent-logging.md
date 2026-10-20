# Persistent structured logging (CR-008)

The existing `slog` JSON console handler remains active. A second handler submits
sanitized copies to one bounded writer for **`bard-logs.db`**. The database is
separate from `bard.db`; no operational schema, player, UI, Force Play or Spotify
Fidelity code is changed. Events already emitted by the bot are reused: there is
no additional per-candidate logging or Discord-message-content capture.

## Configuration and lifecycle

| Setting | Default | Meaning |
|---|---|---|
| `BARD_LOG_DB_PATH` | `bard-logs.db` next to `BARD_DB_PATH` | Host log path |
| `BARD_LOG_DB_MAX_BYTES` | `524288000` | Physical budget: 500 MiB; minimum 1 MiB |

Compose fixes the path at `/home/bard/data/bard-logs.db` in the existing
`bard-data` volume and passes the configurable byte budget. The `.env.example`
relative path is for host execution; Compose deliberately keeps both databases
inside the persistent volume. Log files and WAL/SHM are restricted to mode 0600.
The existing volume owner (UID 10001 in the runtime image) must be able to write
the directory. No service, new volume or remote logging backend is required.

Startup initializes schema v1 in a transaction. An application ID identifies a
Bard log database. A foreign database, future schema, corrupt file, symlink,
second writer or path/hardlink alias to the operational DB is refused. Existing
log schema v1 is reopened without recreating rows. Initialization is bounded to
10 seconds (or the configured internal write timeout if larger). Failure emits a
fixed-category JSON diagnostic on the console and the bot continues with console
logging. A corrupt file is **preserved**, never silently deleted, renamed or
repaired. Stop the writer and recover it offline, or explicitly select a fresh
log path to resume persistence. The operational database is never repaired,
migrated or opened by this package.

## Async behavior and failure policy

- One worker, one SQLite connection, no goroutine per event.
- A nonblocking channel holds at most **512 events**, each at most **16 KiB** of
  encoded sanitized JSON: at most 8 MiB of buffered payload, plus bounded entry
  metadata, a batch and SQLite's page cache. Field names are bounded by the
  128-byte field-name limit and total encoded-record budget; attribute traversal is capped at 64 attributes,
  depth 4 and 32 elements per map/array. All nested values also share a
  per-event 256-node / 32 KiB string budget before JSON encoding.
- Batches hold at most 64 events and at most 1 MiB of JSON at the default quota
  (both limits shrink with small quotas). A batch is flushed every 250 ms or
  when a bound is reached. Small quotas reduce the batch count to avoid a single
  transaction consuming their safety reserve.
- Queue overflow **drops the new persistence copy**. The original console event
  has already been emitted. Oversized complete records are dropped explicitly;
  oversized individual values are replaced with omission/truncation markers.
- Each write/cleanup attempt has a two-second deadline and a 100 ms SQLite busy
  timeout. A batch that cannot be persisted is counted as lost; subsequent
  batches retry. There is no unbounded retry backlog. A failed checkpoint after
  a successful commit counts as a failure, **not** lost committed records.
- `Handle` never performs SQLite/file I/O or waits for the writer. It does bounded
  sanitization and a nonblocking enqueue. Console I/O retains its previous
  synchronous semantics; this change does not promise that stdout itself can
  never block. Malformed diagnostic values are contained, not propagated into
  playback.
- Shutdown stops admission, drains the queue and flushes for at most **5 seconds**
  after the existing bot shutdown. Expiry cancels the worker's active SQL context;
  remaining persistence copies may be lost. The Compose 30-second grace period
  covers the existing bot's 20-second close plus the log flush. The fatal startup
  path also flushes before returning a nonzero process exit.

WAL uses `synchronous=NORMAL`: committed WAL survives an ordinary process/container
crash. Power loss can lose recent transactions; this is diagnostic storage, not
an audit guarantee. In-memory queued events can be lost on SIGKILL.

## Physical retention and its margin

`BARD_LOG_DB_MAX_BYTES` covers the main file, **all indexes**, fragmentation,
`-wal`, `-shm`, and any `-journal` sidecar. Accounting sums the larger of each
file's logical length and allocated filesystem blocks. It is conservative for
sparse files and does not claim to include filesystem metadata or volume-wide
usage from unrelated files.

The budget is intentionally **not a 500 MiB payload allowance**:

1. Main database pages are capped with SQLite `max_page_count` at **40%** of the
   configured budget, rounded down to a 4096-byte page. WAL is checked/truncated
   before another transaction is admitted. Cache spilling is disabled, so a
   bounded transaction does not repeatedly spill rewritten pages to WAL.
2. Up to another 40% is reserved for the transaction's WAL; the remaining 20%
   covers SHM, frame/header/allocation overhead and safety margin. At 500 MiB,
   the main-page cap is 200 MiB, with another 200 MiB reserved for WAL and 100 MiB
   for overhead. This trades log capacity for safe transient headroom.
3. Before insertion, allocated live pages plus a conservative batch reserve must
   be below **75% of the main-page cap**. Otherwise the writer removes oldest
   IDs in batches of at most 64 (smaller near the end), preserving newer IDs.
   FIFO follows ingestion ID; query ordering follows event timestamp with ID as
   a stable tie-breaker. No age or guild-specific retention preference is applied.
4. `auto_vacuum=INCREMENTAL` is enabled before table creation. Cleanup executes
   `incremental_vacuum(64)` steps and explicit `wal_checkpoint(TRUNCATE)` between
   bounded transactions. Free pages left behind are reused by subsequent inserts.
   Partially empty pages still count toward the live-page estimate. There is no
   full `VACUUM` or database rebuild in the playback lifecycle.
5. If a reader prevents WAL truncation, **further write transactions stop** until
   a checkpoint succeeds. Producers keep their console output and may lose
   persistence copies. Read/lock/disk-full/cleanup failures cannot create an
   endlessly growing WAL.

An absolute filesystem quota cannot be enforced atomically by SQLite pragmas:
`max_page_count` caps the main file, and `journal_size_limit` is a truncation
setting rather than a hard WAL cap. File-size measurement also happens between
operations. The reserved WAL/overhead margin is therefore required; post-commit
physical checks suspend writes on a breach. Only this package may write the log
file (an advisory exclusive writer lock enforces this among Bard processes).
External tools that ignore that lock, an already oversized file after reducing
configuration, unusual allocation granularity, or externally modified sidecars
can exceed the requested total before maintenance finishes. Initialization must
successfully reclaim excess pages before new log events are accepted; if its
deadline expires it falls back to console. No automatic file rotation or extra
copy can multiply storage on such a failure.

These choices follow SQLite's [WAL/checkpoint behavior](https://www.sqlite.org/wal.html)
and its [page-limit and incremental-vacuum pragmas](https://www.sqlite.org/pragma.html).
Use short readers; do not hold diagnostic read transactions indefinitely.

## Stored records and musical correlation

Each row has an AUTOINCREMENT ID, UTC nanosecond timestamp, level, event, sanitized
JSON payload and nullable correlation columns:

`guild_id`, `interaction_id`, `track_id`, `spotify_track_id`, `youtube_video_id`,
`collection_id`, `position`, `error_category`, `elapsed_ms`.

Existing aliases are recognized (`spotify_id`, `video_id`, `selected_video_id`,
`category`); microsecond elapsed values are converted when needed. Missing fields
remain NULL. A structured `stage` is used as the event when supplied, with the
original message retained as `payload.msg`. Other fields stay in JSON. Existing
track duration, candidate decisions, rejection reasons, source and stream details
are preserved there after sanitization. Indexes cover timestamp and common
level/guild/event/track lookups, including event time and ID ordering.

Existing correlation IDs are preserved across logger `With` attributes and
track timing events: import, Spotify metadata, discovery, candidate decisions,
selection, stream startup, first frame, interruption, skip/transition and provider
errors are searchable **when the originating event already supplies the ID**.
A missing historical ID is not fabricated. This change does not backfill older
Docker stdout, infer identities from titles, or add a candidate-event flood.

For an Auto Pilot investigation, start with Spotify ID
`3hWu4Z5IkREyWNT4zuuXM5`, inspect `spotify_candidate_discovery`,
`spotify_candidate_decision`, `spotify_first_confirmed` and `spotify_resolution`,
then follow the stored interaction/collection/video IDs into the stream events.
The payload retains the concrete reason and elapsed time when emitted.

## Privacy

Persistence sanitization redacts token/secret/password/cookie/authorization/API
key/client-credential fields, including nested maps/groups. Configured secret
values from credential-related environment variables are removed from string
values and error messages. Bearer/Basic headers, quoted credential fragments and
JWT strings are scrubbed. HTTP(S) URLs lose userinfo, query and fragment; webhook
and token-bearing paths are masked. Raw provider payload/body/header fields,
Discord message/content fields, raw JSON and byte buffers are omitted. Arbitrary
provider structs are not serialized into the database. This is structured event
logging, not ingestion of private Discord chat or raw HTTP traffic.

The console keeps existing behavior; redaction is applied to the persistence
copy. Do not introduce raw provider bodies, private chat text or new secrets into
event messages under arbitrary aliases. Deleting old rows is FIFO retention,
not a promise of forensic secure erasure or encryption.

## CLI (JSONL, no Discord access)

```sh
# Existing compiled bard binary, or: go run ./cmd/bot logs ...
bard logs --last 100
bard logs --level ERROR
bard logs --guild 123456789
bard logs --track 3hWu4Z5IkREyWNT4zuuXM5
bard logs --since 1h --event spotify_resolution
bard logs --interaction 123456789 --since 2026-10-20T00:00:00Z
bard logs --db /home/bard/data/bard-logs.db --last 100

# Running service; no network/API request is made by this command.
docker compose exec -T bard bard logs --last 100
# Service stopped; uses the same persistent volume, does not start the bot.
docker compose run --rm --no-deps bard logs --last 100
```

Filters combine with AND; `--track` matches track, Spotify or YouTube ID.
`--last` selects the newest matching 1–10,000 records (default 100) and prints
chronologically, each as one valid JSON object. `--since` accepts a nonnegative
duration or RFC3339 UTC/offset time. Level and event comparisons are exact.
CLI connection uses SQLite **`mode=ro` and `query_only=1`**, performs no migrations,
and has a five-second query deadline. It cannot create a missing main database.
SQLite may need to create shared-memory/WAL coordination files in the directory
if none exist, so use the normal writable volume directory rather than assuming
a completely read-only filesystem mount will work. It never uses `immutable=1`,
which could hide an active WAL. No Discord token, Discord API, yt-dlp or FFmpeg
is needed by the query executable itself. Query errors go to stderr.

## Metrics and validation

`Store.Metrics()` exposes bytes, rows, written/removed records, failures, overflow,
oversized/lost events, batches, most recent flush and cleanup durations, and last
failure category. Every 30 seconds and at shutdown, a **console-only**
`log_persistence_metrics` JSON event also reports writes/second for that interval.
Using the original console logger avoids persistence recursion, even when the log
DB is unavailable. Counters are process-local; retained rows survive restarts.

Tests use actual SQLite files and cover persistence/reopen, a separate process
exiting with committed WAL, read-only queries, time ordering, filters, optional
fields, JSON/Unicode, concurrent producers, batches/overflow, deadlines and final
flush, real SQLITE_FULL, unavailable DB, blocked checkpoints, continuous FIFO,
page reuse/shrink, reduced quota on restart, cleanup failure/recovery, schema and
corruption refusal, credentials/private payload redaction, malformed values,
writer/path isolation and operational database byte/integrity preservation.
Read plans are checked for the expected lookup indexes.

```sh
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
docker compose build
# Explicit integration: default 500 MiB configuration, ~150 MiB live-page target,
# enough real data to trigger FIFO, latest record checked after 12,000 inserts.
BARD_LOG_FULL_QUOTA_TEST=1 go test ./internal/logstore \
  -run '^TestDefault500MiBSaturation$' -v -count=1 -timeout=120s
```

The default-quota saturation test is opt-in to avoid repeatedly allocating
~150 MiB in every Docker/race build. A smaller always-run representative test
uses the default quota and validates its SQLite cap; reduced-quota tests exercise
continuous eviction and physical limits in the ordinary suite.

### Results recorded on 2026-10-20

- `go test ./... -count=1`, `go test -race ./... -count=1`, and `go vet ./...`:
  passed, including operational storage, player, Force Play and Fidelity suites.
- `docker compose config --quiet`, `git diff --check`, and `docker compose build`:
  passed. Runtime image `3ab348162360`; binary SHA-256
  `1ded895df149f687210e7ce2bb56f7dc2cb9588bdd687d66f48ee2538c7d93de`.
  Docker used its classic builder because buildx is unavailable.
- Explicit real default-quota saturation: **12,000 written, 1,408 removed,
  10,592 retained**, latest ID 12,000 preserved, zero failures or lost events;
  measured main/sidecar accounting at completion **153,849,856 bytes**, below
  524,288,000. The 500 MiB configuration intentionally starts eviction before
  reaching the physical ceiling, as explained above.
- `python3 scripts/check_log_restart.py`: passed using a uniquely named temporary
  volume and the runtime image, with `--network none` and a read-only container
  root filesystem. First start stored IDs 1–2, second start of the same container
  preserved them and added IDs 3–4. Separate CLI containers read all four records
  without credentials/network. `PRAGMA integrity_check` returned `ok`, file mode
  was `0600`, and no operational DB was created. Temporary resources were removed.
  The intentionally unconfigured bot exits 1 after its startup error; that is the
  expected stimulus, not a failed acceptance assertion.
- A subprocess test also exits without closing SQLite after a committed WAL
  write; reopening recovers both rows. The real-container test validates clean
  exit/restart, while the subprocess test validates the uncheckpointed WAL case.

To repeat container acceptance after a build:

```sh
python3 scripts/check_log_restart.py
# Optional alternative already-built runtime image:
BARD_TEST_IMAGE=your-image python3 scripts/check_log_restart.py
```

No production service was started/restarted, no production volume was mounted
by these acceptance containers, and no real Discord playback claim is made.
