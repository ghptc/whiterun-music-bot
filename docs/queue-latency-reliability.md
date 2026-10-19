# Queue latency and interaction reliability

CR-006.2 separates the Discord acknowledgement deadline from queue delivery. The reported production interval, 22:11:20.338 to 22:11:22.339, matches the former two-second local acknowledgement timeout. Snapshot, PNG rendering and artwork downloads occurred after acknowledgement in the original code; they cannot directly explain that failed acknowledgement. Shared CPU/network pressure remains possible and requires production measurements.

## Acknowledgement and delivery

Discord requires an initial response within three seconds and permits followups for fifteen minutes ([official API documentation](https://github.com/discord/discord-api-docs/blob/main/developers/interactions/receiving-and-responding.mdx)). The new absolute deadline is receipt at the bot handler plus 2.7 seconds, leaving 300ms for transit/processing. Scheduler delay consumes this budget. There is no application retry of the initial callback. Disgo rate-limit handling remains bounded by the same context; a 60-second 429 is rejected without waiting or bursting. A network error stops command processing, even if delivery to Discord is uncertain.

The player snapshot occurs only after acknowledgement, under its own mutex. It copies queue entries and tracks; image encoding and HTTP do not hold that mutex or the guild command mutex. Spotify matching runs outside the player mutex with three collection workers. Audio continues through the existing playback callback. Synchronous persistence in player mutations can still delay snapshots; the separate snapshot metric exposes that delay without delaying acknowledgement.

Each delivery has a twelve-minute lifetime, each image has a five-second admission/render budget, and each Discord send has a twenty-second budget. PNG encoding checks cancellation before publishing its bytes; Go PNG encoding itself is not preemptively interrupted mid-call. Two render slots bound CPU and image allocations; BestSpeed reduces encoding cost. Subsequent blocks release their PNG buffers after successful sends. A replacement request cancels the previous delivery in the same guild/channel. Canceled deliveries no longer overwrite a newer response with their failure notice.

Rendering uses cached artwork only and never searches music or downloads an image. Cache misses immediately use a local placeholder. Separate nonblocking artwork warming admits at most two tasks globally, each with a two-second budget and sequential HTTP requests (800ms client timeout). An already delivered placeholder is not retroactively replaced. Existing positive/negative cache expiry, 256-entry bound, two-MiB download cap, four-million-pixel decode cap and provider host restrictions remain. Decode and resize are synchronous but limited by these caps.

Failed initial PNG rendering sends an explicit error message and records unsuccessful queue delivery. Partial sends record only confirmed positions. A failed completion edit also records unsuccessful delivery. There are no application retry loops; disgo's finite rate-limit retries share each request deadline. Successful HTTP completion confirms API acceptance, not visibility in a user's Discord client.

## Measurements

Local measurements on AMD Ryzen 7 9800X3D, linux/amd64, five benchmark iterations. The baseline is the repository HEAD before this change. The cold-artwork fixture has ten unique provider URLs, each returning 404 after an artificial 10ms. It excludes Discord and Spotify network traffic, background warming and player snapshot time.

| Measurement | Before | After |
| --- | --- | --- |
| Cold artwork block render | 111.41ms/op | 6.06ms/op |
| Allocated bytes per block | 4,116,980 | 3,834,404 |
| Allocations per block | 764 | 497 |
| Ack budget | 2 seconds | 2.7 seconds from handler receipt |
| Artificial 2.1-second ack | Exceeds old budget | Accepted in regression |
| Permanently blocked ack | 2-second cutoff | Stops within 3-second window |

Reproduce rendering with `go test ./internal/discord -run '^$' -bench '^BenchmarkQueueColdArtwork$' -benchtime=5x -count=1`. These are synthetic local results, not production latency percentiles. There is no production CPU profile, HTTP latency distribution, scheduler profile or Discord E2E measurement for this change.

## Operational metrics

Correlate by `interaction_id`:

- `command_received`: command receipt; log timestamp plus command identity.
- `command_ack_start`: elapsed microseconds since handler receipt, including scheduling.
- `command_ack_http_connection`: connection acquisition elapsed time and connection reuse.
- `command_ack_http_first_byte`: HTTP first-byte elapsed time from ack start.
- `command_ack_complete`: ack duration and cause, including network errors, HTTP errors and deadline expiry.
- `queue_snapshot`: consistent snapshot duration and entry count.
- `queue_plan`: plan construction duration, message/position counts and missing artwork metadata.
- `queue_png`: per-block render duration including slot wait and error cause.
- `queue_response`: per-request Discord send duration and error, including followups and completion edits.
- `queue_delivery_complete`: delivery duration, confirmed messages/positions, success and render/send causes.
- `command_total`: total duration from handler receipt, including failed acknowledgements.

Connection acquisition versus first-byte timings distinguish connection/pool pressure from server/network response delay. Snapshot and PNG timings distinguish player contention from rendering pressure. These are structured log metrics; no production histogram or CPU measurements are fabricated.

## Regression coverage and validation

The real disgo HTTP client and rate limiter are exercised with immediate acknowledgement, 2.1-second artificial latency, network failure, blocked HTTP and a 429 exceeding the remaining budget. Failed acknowledgement never enters guild work. Renderer admission timeout verifies no partial PNG or slot leak. Artwork cancellation verifies release of its task slot. Existing tests cover 10, 123 and 500 positions, image decode failures, bounded cache, partial delivery and short/long 429 handling.

The concurrent import regression uses 123 Spotify metadata entries with three blocked matching workers, a simulated active voice callback, skip, occupied render slots and a second guild requesting its queue. It verifies immediate acknowledgement, independent guild progress, resumed rendering and cleanup of collection/delivery resources. This is integration with the real player and queue renderer using simulated providers and voice transport; it does not validate real Spotify throughput, Discord voice packets or production CPU saturation.

Validation passed: `go test ./... -count=1`, `go test -race ./... -count=1`, `go vet ./...`, `docker compose build`. The final artwork-cancellation regression also passed separately with and without race detection. Discord E2E remains unvalidated until exercised against the real API with a live bot and users.

## Force Play verification before feature freeze

The CR-006.2 hotfix includes verification that `/play force` remains optional. No production code change was necessary: `ApplicationCommandOptionBool.Required` already has its Go zero value `false`, the interaction decoder returns `false` for an omitted boolean, and `PreparePlay(..., false)` directly delegates to the existing `Prepare` path. Only `true` selects Force Play, which validates a direct YouTube video URL before extraction and rejects Spotify, text searches and playlist-only URLs.

On 2026-10-19, a read-only authenticated GET to the real Discord API for the configured application's global commands confirmed a registered `/play` with exactly one `force` option, type `5` (boolean), and `required` absent, meaning optional. No guild scope was configured locally. No command was republished and no playback interaction was sent for this verification.

`TestForceCommandQueueAndControls` exercises absent, explicit `false` and `true` through the command handler and player. `TestForcePublishedJSONContract` verifies the serialized publication contract for both global and guild scopes; the existing native option contract also checks `Required == false`. `TestBardSpotifyResourceIntent` now compares `PreparePlay(..., false)` with standard preparation for Spotify tracks, albums, playlists with duplicates and partially available albums. `TestPlayCollectionAndClearCommands` imports Spotify playlists through `/play` with no `force` field. The media suite also retains the Spotify Fidelity and Force Play rejection/isolation regressions.

The live API check confirms the published command contract. Spotify and playback behavior remain covered by local fixtures, not a new real Discord playback E2E run.

Complement validation passed: `go test ./... -count=1`, targeted Force Play/Spotify Fidelity/playlist integration regressions with `-race`, `go vet ./...` and `git diff --check`. This complement changes tests and documentation only.
