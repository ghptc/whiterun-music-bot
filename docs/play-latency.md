# /play latency investigation — 2026-09-13

The custom Discord acknowledgement is now the first network operation. Fresh searches still exceed the 2–4 second target in this environment. The music filter, canonical scoring, variant preferences, ten-candidate search, and queue lifecycle are preserved.

## Original path and measured bottleneck

Baseline source: `b4901a2`, with timing instrumentation added before changing extraction or acknowledgement behavior.

1. Discord deferred an ephemeral response immediately, leaving generic “thinking…” visible.
2. Guild/voice checks and, when necessary, voice connection opening.
3. One yt-dlp process searched `ytsearch10:` and fully extracted **all ten videos**, including formats, client API responses, JavaScript and manifests. `--skip-download` did not skip extraction.
4. JSON parsing, music filtering and ranking ran in Go.
5. Track metadata entered the queue; Discord received “Queued”.
6. The existing player started FFmpeg and a **second** yt-dlp process for the selected track, piping audio into FFmpeg and paced Opus into the voice transport.

There was no third yt-dlp process, and playback extraction was already outside the command's resolution path. The instrumented baseline spent 15,529 ms / 13,983 ms in yt-dlp for normal/live searches, versus roughly 4 ms parsing and under 0.3 ms filtering/ranking combined. A direct URL spent 1,997 ms in yt-dlp. These observations locate the resolver bottleneck; they do not attribute an unmeasured Discord session's entire delay to that stage.

## Changes

- `/play` immediately creates the ephemeral message “The Bard searches Skyrim for your song...” and edits it with the result. Other commands keep their deferred responses.
- Discovery uses `youtube:player_client=web;player_skip=js;skip=hls,dash,translated_subs` plus `--ignore-no-formats-error`. This avoids extra playback client, JavaScript and manifest work while keeping full metadata for all candidates. Playback retains the original unrestricted client selection and format handling.
- Flat extraction was deliberately not adopted: categories, artist/track and full description are inputs to the music filter and canonical scoring. In particular, category-only concert uploads must remain eligible for explicit live requests. No candidate limit or scoring rule was changed.
- A shared in-memory cache holds at most 256 successful metadata results for 10 minutes. Keys are exact trimmed queries; variants stay distinct. It stores canonical video URLs, never signed streams, failures or request traces. Expired entries are removed and the oldest entry is evicted when full. Cancelled searches cannot return cached results.
- No extra workers, parallel candidate subprocesses or persistence were introduced. Existing search concurrency limits, queue tickets, cancellation, failure logging and automatic advancement remain in place.

The extraction options are documented in the [yt-dlp README](https://github.com/yt-dlp/yt-dlp#youtube). Their behavior was also checked against the installed 2026.08.19 extractor and live metadata.

## Measurements and limitations

All recorded YouTube runs used the existing Docker runtime dependencies: yt-dlp 2026.08.19, Deno 2.5.6 and FFmpeg 5.1.9, on the same host/network. These are individual samples, not percentiles or a guarantee. Container startup is excluded. Searches can return different candidates between calls.

The first instrumented comparison is preserved in `play-latency-before.jsonl` and `play-latency-after.jsonl`:

| Input | Before metadata resolution | After metadata resolution | After playback start → local first frame |
| --- | ---: | ---: | ---: |
| Arctic Monkeys 505 | 15,533.584 ms | 13,646.848 ms | 2,343.465 ms |
| Arctic Monkeys 505 live | 13,986.814 ms | 11,558.898 ms | 2,263.318 ms |
| https://www.youtube.com/watch?v=qU9mHegkTc4 | 1,997.830 ms | 1,647.492 ms | 2,646.846 ms |
| Repeated Arctic Monkeys 505 | — | 0.013 ms | 2,342.229 ms |

Normal selection remained `CKI8iQTgZKU` (“Arctic Monkeys - 505”). Both live selections were live performances; their IDs differed as the returned candidates/order changed. Direct selection remained `qU9mHegkTc4` (“505”). A separate field comparison (`play-metadata-parity.jsonl`) found **zero differences** in the 12 fields consumed by `Candidate` for all common IDs: nine of ten candidates for each search, and the direct video. This sample supports metadata preservation, but is not a claim about all YouTube videos.

An additional sequential before/after run, including playback for both versions, is preserved in `play-latency-before-playback.jsonl` and `play-latency-after-final.jsonl`. That run measured 18,242 / 14,798 / 2,875 ms before versus 11,990 / 11,766 / 1,341 ms after for normal/live/direct metadata resolution. The direct video then failed to stream with HTTP 403, and the profiler correctly exited with status 1; the earlier direct-video playback had succeeded. This external failure is retained in the raw evidence, not discarded from the report.

The initial `stream_resolve_complete` events in those earlier artifacts matched yt-dlp's diagnostic rather than the actual marker line: a bare marker was interpreted as an output field. Their resolver and first-frame measurements remain valid, but the intermediate stream-resolution timestamps should not be treated as exact. The final code uses a template containing the video ID and matches only a marker at the start of a line; its regression test rejects diagnostic/partial matches. `play-latency-verified.jsonl` contains the corrected run. All three inputs produced audio in that run, including the direct video that previously returned HTTP 403:

| Input | Final metadata resolution | Stream extraction | Playback start → first local frame |
| --- | ---: | ---: | ---: |
| Arctic Monkeys 505 | 12,583.426 ms | 2,650.591 ms | 2,698.139 ms |
| Arctic Monkeys 505 live | 10,815.725 ms | 2,304.263 ms | 2,346.979 ms |
| Direct video qU9mHegkTc4 | 1,593.953 ms | 2,047.280 ms | 2,085.448 ms |
| Repeated Arctic Monkeys 505 | 0.012 ms | 2,254.915 ms | 2,307.327 ms |

Relative to the first instrumented baseline, the final normal/live/direct samples reduced resolution time by approximately 19% / 23% / 20%. Network and search-result variability prevent treating those percentages as stable performance guarantees.

The final profiling utility exits unsuccessfully if no frame arrives. It deliberately cancels after the first frame; `playback_complete` can therefore record `context canceled` while `profile_playback_complete` records successful frame receipt. Older profiler logs report this expected cancellation directly.

**These are not live Discord end-to-end timings.** The profiling utility passes real decoded audio into a local discard transport. It does not join a guild, insert into a real queue, call Discord REST or measure Discord delivery/audibility. Those stages are instrumented in the bot and need a real `/play` session to measure. The acknowledgement ordering is covered by a local interaction test. No messages were sent to Discord during this work.

Subprocess counts per successfully played track:

| Path | yt-dlp processes | FFmpeg processes |
| --- | ---: | ---: |
| Before, search or direct URL | 2 | 1 |
| After, uncached search or direct URL | 2 | 1 |
| After, cached query | 1 | 1 |

The optimization reduces work **inside** discovery, not its cold subprocess count. It also avoids discovery JavaScript helper work. Stream extraction remains at playback time and is repeated on every play. Fresh discovery still makes per-video metadata requests for ten candidates; preserving that coverage limits the speedup.

## Timing logs

Every real `/play` trace carries `interaction_id` and `guild_id`. `elapsed_ms` measures its stage; `request_elapsed_ms` measures age since request start, so queue waiting is distinguishable from playback startup. Measurements use Go's monotonic `time.Now`/`time.Since` and retain sub-millisecond precision.

Events: `play_request_start`, `discord_acknowledged`, `voice_join_complete` (when joining), `resolver_start`, `youtube_search_start/complete`, `candidate_parsing_complete`, `music_filtering_complete`, `ranking_complete` (searches), `candidate_selected`, `resolver_cache_hit` (hits), `resolver_complete`, `queue_inserted`, `discord_response_complete`, `play_request_total`, `playback_start`, `ffmpeg_start`, `stream_resolve_start/complete`, `first_audio_frame`, `playback_complete`.

`play_request_total` ends after the final interaction edit attempt, including error paths. It does not wait for playback or queue drain. The first-frame event fires once after a successful `WriteFrame`, not per packet. yt-dlp's `before_dl` marker is directed to stderr and reports completion of stream extraction without exposing signed URLs or contaminating the audio pipe. Extraction failures also report `stream_resolve_incomplete` and the player logs/advances normally.

## Tradeoffs

Metadata resolution no longer requires usable playback formats. An unavailable song can therefore be queued and fail later; existing playback failure handling logs and skips it. The custom acknowledgement also appears briefly for invalid requests before being edited with the validation error. Cache metadata/results can be stale for ten minutes. Playback quality, retries, idle watchdog, process-group cancellation and queue ordering were not relaxed.

## Reproduce

With yt-dlp, Deno and FFmpeg on PATH:

```sh
go run ./cmd/profile -playback \
  'Arctic Monkeys 505' \
  'Arctic Monkeys 505 live' \
  'https://www.youtube.com/watch?v=qU9mHegkTc4' \
  'Arctic Monkeys 505'
```

Without local media dependencies, use the built runtime image:

```sh
go build -o /tmp/bard-profile ./cmd/profile
docker run --rm \
  --mount type=bind,src=/tmp/bard-profile,dst=/tmp/profile,readonly \
  --entrypoint /tmp/profile whiterun-music-bot-bard:latest -playback \
  'Arctic Monkeys 505' 'Arctic Monkeys 505 live' \
  'https://www.youtube.com/watch?v=qU9mHegkTc4' 'Arctic Monkeys 505'
```

Omit `-playback` to measure only queue metadata resolution. Repeating a query in the same invocation exercises the shared cache. The baseline logs came from a saved instrumented binary before the optimization, not a production flag that changes resolver behavior.

## Validation and changed files

Ran `gofmt`, `go vet ./...`, `go test ./...`, `go test -race ./...`, `go build ./...`, and `docker compose build`. Go checks needed build-cache access outside the filesystem sandbox. Docker used its classic builder because buildx was unavailable. Tests cover official ranking, variants, non-music rejection, queue advancement/cancellation, acknowledgement ordering, cache expiry/eviction/concurrency/isolation, marker parsing and local FFmpeg playback. Unit tests make no live YouTube requests.

- `internal/discord/commands.go`, `commands_test.go`: custom acknowledgement, command timings and ordering test.
- `internal/discord/bot.go`: shared metadata cache wiring.
- `internal/media/resolver.go`, `resolver_test.go`: extraction options, stage timings and cache use.
- `internal/media/timing.go`: immutable request timing context carried into playback.
- `internal/media/cache.go`, `cache_test.go`: bounded TTL metadata cache and tests.
- `internal/player/stream.go`, `stream_test.go`: extraction marker, startup/first-frame timings and tests.
- `cmd/profile/main.go`: standalone real-media profiling utility without Discord.
- `docs/play-latency.md`, `docs/play-*.jsonl`: investigation and captured measurement evidence.
