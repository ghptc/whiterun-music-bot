# CR-002: audit before corrections

> Historical CR-002 audit. Its inline embeds, HTML/TXT exports and presentation measurements were superseded by [CR-006 UI V2](bard-ui-v2.md). Cache/fidelity investigation results remain relevant.

Audit baseline: `762fbb5`, 2026-10-18. No running Compose service or local runtime logs were available.

## Confirmed findings

- `commands()` already declares optional native integer `limit`, min 1, without `page`. Handler takes one player snapshot, excludes current from the limit, and exports a bounded ten-card preview plus HTML/TXT in one response edit.
- Live Discord REST audit: global `/queue` ID `1548720833114153074`, version `1561545613315809411`, has integer (type 4) `limit`, min 1; no `page`. Both accessible guilds have **zero** guild commands. A stale guild override and failure to publish the current global schema are therefore ruled out at audit time. The original outdated interaction was not captured; client/version skew remains unconfirmed. This cannot honestly be called a reproduced root cause. Do not delete/recreate commands to guess a fix.
- Startup uses bulk replacement (`SetGlobalCommands`/`SetGuildCommands`), potentially deleting unrelated commands, without readback or per-command diagnostics.
- Start announcements use plain `Content`, never an embed. Artwork cannot appear despite metadata being present. Events already follow the first successful voice frame and are isolated per guild.
- `762fbb5` added safe artwork URL selection (Spotify, YouTube, validated video ID fallback), propagated image metadata, native per-row queue thumbnails, escaped HTML and complete TXT fallback. These should be retained.
- SQLite migrations 001/002 contain guild state/settings, history, content-hashed tracks, mappings and playback events. Artwork survives JSON persistence. `tracks` includes request/session data in its hash and is not a reusable Spotify catalog. `mappings` already stores candidate evidence, thumbnail, checked timestamp and 24h expiration with a unique identity key; do not duplicate it.
- Mapping read/write failures currently abort media resolution. Expired availability triggers extraction; failed extraction can leave a stale mapping. Spotify metadata is always fetched before lookup. Stream failure does not invalidate cached candidates.

## Validation boundary

REST schema inspection is real API validation, not Discord E2E. Client rendering, invocation of `/queue`, audio and rapid skips in real voice must be tested by an authenticated Discord user. No such E2E execution has been claimed.

## Corrections

- Registry publication now inventories, skips identical definitions, upserts only owned names/types, and reads back every contract. It never bulk-deletes commands. The configured scope is explicit; an operator switching scopes should inspect old guild registrations rather than erase all commands. `bard commands-sync` performs this operation without gateway/voice or media dependencies. Sync has a 90-second overall budget and structured success/failure events; gateway connection has its own deadline.
- The first live publication attempt exposed rate-limit waits exhausting the former 30-second deadline (`publish leave: context deadline exceeded`). The corrected inventory/upsert/readback path then completed successfully against Discord: 11 global contracts verified, 656 ms. `/queue` already had the correct global schema before either attempt. This fixes a demonstrated synchronization weakness, not a proven explanation for the originally reported client error.
- Now Playing uses one native embed: title, artist/channel fallback, source, requester, collection position, next track and right-side thumbnail. Spotify artwork wins over YouTube thumbnail/video-ID fallback. No image fetch/upload precedes audio. Failure notices remain text.
- Late playback verification supplies final display/locator metadata to the player before streaming. First-frame events copy that actual current track; pending resolutions cannot emit starts. A per-guild worker drops superseded queued starts and isolates REST latency from streaming. An already submitted REST request cannot be recalled on a simultaneous skip.
- Queue presentation preserves CR-001's snapshot/limits/order and ten total preview cards, including current. HTML generation failure retains the complete TXT fallback. Existing bounded multipart response edits and real disgo 429 retry handling remain.

## Evidence and measurements

On AMD Ryzen 7 9800X3D, Go 1.27.1, local SQLite (benchmarks, not Discord network latency):

| Operation | Result |
| --- | --- |
| 500-track queue render including gallery/TXT | 2.197 ms/op |
| HTML / TXT attachment sizes for fixture | 133,260 / 57,966 bytes |
| SQLite catalog hit / miss | 5.677 / 3.769 microseconds/op |

`queue_render` records combined render time, separate gallery duration, attachment bytes, preview count and missing/invalid artwork metadata; `queue_delivery_complete` records response generation/delivery duration. `playback_announcement_delivery` records first-frame-to-REST-completion latency, artwork metadata presence and invalid artwork metadata. Actual CDN image-load success and perceived audio latency cannot be observed without the Discord client; missing/invalid metadata is handled locally without fetching images.

Validation executed: `go test ./... -count=1`, `go test -race ./... -count=1`, `go vet ./...`, Docker build with real FFmpeg/Opus checks. Migration 003 was applied to a **copy** of the existing Docker-volume DB: SQLite integrity OK; one guild snapshot decoded and round-tripped. The deployed volume was not migrated by this audit. Fresh database/reopen, constraints, concurrency and isolation also use real on-disk SQLite tests.

Two regression tests copied into an isolated `762fbb5` tree fail there and pass after correction: cache read/write errors interrupt confirmed resolution; failed revalidation leaves the old mapping. Network extraction is controlled in these tests; SQLite, subprocess handling, REST serialization and rate limiter integration are real components, not live provider E2E.

## Regression coverage map

| Requested cases | Coverage |
| --- | --- |
| 1–3 registry / page removal / native limit | commands tests; CR-002 global/guild HTTP inventory, POST and readback tests, permission/mismatch errors; live global REST check |
| 4–9 no limit, 1/10/50, empty, 123/500 | `TestQueueCompleteAndLimitedSnapshots`, handler tests |
| 10–12 partial delivery, 429, duplicates | queue delivery tests using real disgo multipart/limiter and single response edits |
| 13–18 artwork priority, invalid/absent/pending | artwork tests, queue tests, `TestNowPlayingEmbedArtworkAndNativeLimits` |
| 19–20 skip and simultaneous guilds | lifecycle tests; superseded-start worker test; simultaneous guild final-metadata/embed test |
| 21 artwork after restart | storage artwork round-trip tests; catalog reopen test |
| 22–26 hit/miss/restart/shared/isolated | storage mapping/catalog integration and guild durable-state tests |
| 27–29 expiration/unavailable/artwork refresh | mapping lease/invalidation tests; catalog expiry/artwork upsert tests |
| 30–32 concurrent requests, I/O failure, migrations | real SQLite concurrent upserts/conditional invalidation/constraints; closed-DB fallback; cache failure regressions; existing DB upgrade acceptance |
| 33 no external media calls from queue | queue handler tests with provider/resolver traps, pure snapshot renderer |

## Remaining real Discord acceptance

Run `/queue`, `/queue limit:1`, `/queue limit:10`, `/queue limit:50` from a freshly selected command; inspect artwork, download HTML/TXT and check 123/500-track order. Play Spotify with/without art and YouTube `Black Sabbath - Paranoid (Official Audio)`, skip rapidly, and repeat in a second guild. Confirm thumbnails and actual audio. If “outdated” recurs, capture time, selected command ID/version and whether `command_received` exists. A rejection before delivery has no bot interaction log. No client invocation/rendering/voice E2E has been executed in this change.
