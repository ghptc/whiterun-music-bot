# First uncached /play: lightweight discovery

2026-09-13. This supersedes the first optimization report in `play-latency.md`.

Fresh text queries reached the **real local queue in 2.99s / 2.87s / 3.42s** for the requested normal/live/unplugged inputs. Direct video metadata took 1.56s; the normalized repeat took 0.015ms. These are real YouTube/FFmpeg measurements, not synthetic timing claims. **Discord REST delivery and audible Discord playback were not measured**: no Discord messages were sent. The same production resolver and GuildPlayer were exercised with a local discard voice transport.

## Exact root cause

The previous “search” process was not lightweight. It made a search/config request and then processed ten video pages serially, even though formats/JS/manifests had already been reduced. The captured baseline is `play-flat-baseline-trace.jsonl`; it includes the exact argv, UTC times, one parent PID, and nested extraction start/end markers.

For `Arctic Monkeys 505`, the search/config phase reached the first candidate at **1,537.259ms**. The whole yt-dlp process ended at **12,339.895ms**. The following operations were inside that one process, **not separate subprocesses**:

| Video ID | Start from process launch (ms) | Extraction duration (ms) |
| --- | ---: | ---: |
| CKI8iQTgZKU | 1537.259 | 981.671 |
| bhk-yqnGlpM | 2519.611 | 1221.252 |
| iIRnWv1k-js | 3741.593 | 1026.242 |
| MrmPDUvKyLs | 4768.559 | 1217.593 |
| iIfl5k2nQBQ | 5986.845 | 1021.004 |
| J7K6EbcWhig | 7008.454 | 1101.562 |
| fz0TC9Q4fhw | 8110.722 | 1141.684 |
| VcV00GDAjaY | 9253.115 | 1486.793 |
| 1Jd2DysfCW8 | 10740.597 | 786.919 |
| dvw3v2ibQUo | 11528.209 | 789.977 |

The trace reports web-client configuration and search API downloads, then ten video-page downloads (plus any additional extractor messages visible in the raw log). “Downloading item” messages mark playlist iteration, not additional HTTP requests. This is extractor-level tracing, not a packet capture. Ranking was sub-millisecond. Playback then used one separate yt-dlp process and one FFmpeg process; there was no third yt-dlp process in the old flow.

## New flow and music safeguards

1. Immediately acknowledge `/play` with the existing custom ephemeral message.
2. Run **one `ytsearch10:` search with `--flat-playlist`**, skip download and emit JSON. Flat mode never enters each video's extractor. It fetches neither candidate formats nor candidate thumbnail/subtitle/comment resources.
3. Filter obvious non-music, invalid IDs, live streams and unsuitable durations; score the remaining lightweight candidates in Go with the existing relevance, canonical-source and variant weights.
4. If the leading result has insufficient music evidence, fetch metadata for **that candidate only**, using the existing cheap metadata flags. A verified, query-relevant channel may also need this check to confirm the artist-channel tier. Verification by itself never establishes that it is an official artist channel.
5. Apply the unchanged `IsMusic` and `Score` rules to the verified result. Re-rank if the provisional official-source bonus was not confirmed. If verification fails or the result is not music, try the next ranked result. There is no “accept unknown as music” shortcut and no arbitrary shortlist truncation.
6. Queue stable metadata and edit the interaction to “Queued”. At playback time the player performs unrestricted stream extraction and pipes audio through FFmpeg. In the existing pipe design FFmpeg starts first and waits for yt-dlp's audio.

The [yt-dlp documentation](https://github.com/yt-dlp/yt-dlp#usage-and-options) explicitly notes that flat extraction skips URL-result extraction and omits some metadata. That is why uncertain music results still need a selected-video check. For flat results already establishing music (for example Topic/VEVO or an official-audio title), no extra check is needed unless artist-channel ranking is uncertain.

Full metadata for every unselected result is no longer fetched. Thus fields visible only in a discarded video's full metadata cannot affect discovery ranking. This is an information tradeoff of lightweight search, not a change to the music gate or scoring weights. The captured three-query regression fixture uses the same ten IDs/order on both sides and verifies the **same selected video** as the previous full-metadata ranking, with one verification per query. It is evidence for those cases, not proof of identical rankings for every possible YouTube search.

`Track` now also carries VideoID and channel alongside its existing canonical URL, title and duration. No signed media URL is stored in the queue or cache. Stream resolution was **already** at playback time and remains there; playback failure still logs and advances the queue.

## Fresh measurements

Source: `play-flat-final.jsonl`. A new profiler process started with an empty cache. The first four inputs are distinct misses; the fifth is a case/whitespace-normalized repeat in the same process. No concurrent baseline search ran during this final measurement. Runtime: yt-dlp 2026.08.19, FFmpeg 5.1.9, Deno 2.5.6. Single samples vary with YouTube/network conditions; this is not a p95 guarantee.

All durations below are milliseconds. Queue insertion measured below the logger's 1µs resolution in these samples. Ranking includes provisional scoring and re-ranking, excluding extraction.

| Query | Flat search | Ranking | Selected/direct metadata | Queue insertion | Request → queue ready |
| --- | ---: | ---: | ---: | ---: | ---: |
| Arctic Monkeys 505 | 1357.143 | 0.288 | 1627.360 | <0.001 | 2985.475 |
| Arctic Monkeys 505 live | 1460.256 | 0.543 | 1411.227 | <0.001 | 2872.681 |
| Alice in Chains Nutshell unplugged | 1594.155 | 0.367 | 1826.321 | <0.001 | 3421.245 |
| https://www.youtube.com/watch?v=qU9mHegkTc4 | 0.000 | 0.000 | 1557.212 | <0.001 | 1557.465 |
| arctic   monkeys 505 | 0.000 | 0.000 | 0.000 | <0.001 | 0.024 |

| Query | Stream resolution | Playback start → first local frame | Request → first local frame | Discord says “Queued” |
| --- | ---: | ---: | ---: | --- |
| Arctic Monkeys 505 | 2287.540 | 2455.869 | 5441.397 | Not measured |
| Arctic Monkeys 505 live | 2049.482 | 2105.865 | 4978.555 | Not measured |
| Alice in Chains Nutshell unplugged | 2458.151 | 2728.848 | 6150.103 | Not measured |
| https://www.youtube.com/watch?v=qU9mHegkTc4 | 3104.597 | 3297.330 | 4854.804 | Not measured |
| arctic   monkeys 505 | 2902.639 | 2953.113 | 2953.144 | Not measured |

The first-frame request age is total local playback-start latency, including resolution and queue insertion. `profile_playback_complete` also includes teardown and is not the first-frame timestamp. The profiler intentionally cancels after the first frame, so child-process `signal: killed` and `playback_complete: context canceled` are expected; all five final runs reported `frame_received: true` and exited successfully.

Compared with the previous task's final samples:

| Query | Previous metadata resolution | New metadata resolution |
| --- | ---: | ---: |
| Arctic Monkeys 505 | 12,583.426ms | 2,985.460ms |
| Arctic Monkeys 505 live | 10,815.725ms | 2,872.666ms |
| Alice in Chains Nutshell unplugged | Not previously measured | 3,421.227ms |
| Direct video qU9mHegkTc4 | 1,593.953ms | 1,557.449ms |
| Repeated text | 0.012ms | 0.015ms |

That is approximately **76% / 73% less uncached resolution time** for normal/live. Selected IDs were `CKI8iQTgZKU`, `aZv8tmvCGPE`, `9EKi2E9dVY8`, and `qU9mHegkTc4`, respectively.

## Exact subprocess counts

Counts include playback of the track. “Metadata” is a non-flat selected/direct-video extraction with JS and manifest work skipped; “full stream” is unrestricted extraction/download at playback. Counting all non-flat extractions as “full” therefore means **metadata + full stream**, not just the last column.

| Path | Search processes | Selected/direct metadata processes | Full stream processes | Total yt-dlp | FFmpeg |
| --- | ---: | ---: | ---: | ---: | ---: |
| Previous text search | 1, extracting all 10 videos internally | 0 separate | 1 | 2 | 1 |
| New measured normal/live/unplugged | 1, flat | 1 | 1 | 3 | 1 |
| New text result with sufficient flat evidence | 1, flat | 0 | 1 | 2 | 1 |
| Direct URL, before and after | 0 | 1 | 1 | 2 | 1 |
| Repeated normalized query | 0 | 0 | 1 | 1 | 1 |

The measured text path uses **one extra yt-dlp process**, but reduces video metadata extraction from **ten videos to one**. This avoids hiding network work inside subprocess counts. The extra selected-video check is the cost of retaining the current music gate with incomplete search metadata. It still meets the local ≤4s queue-ready target. Uncertain/rejected leading results can require further checks; the resolver retains the existing overall 90s bound and does not extract candidates concurrently.

## Cache and duplicate coalescing

The existing 256-entry, 10-minute successful-metadata cache remains. Text keys now lowercase, trim and collapse whitespace. Variant words and punctuation remain, and canonical URL keys retain case-sensitive video IDs. A second video-ID index was unnecessary for these improvements.

A small in-flight map coalesces normalized duplicate misses. All waiters share one search, but each can cancel independently. The last departing waiter cancels the subprocess work; the leader's cancellation does not disrupt another guild's waiter. Failures are not cached and stale cancelled flights cannot delete a replacement flight. No dependency, worker pool or persistent storage was added. Existing bot-level concurrency limits still apply.

## Instrumentation and reproduction

`yt_dlp_start/complete` identify purpose (`flat_search`, `selected_candidate_verification`, `direct_video_metadata`, `playback`), start time, elapsed time, candidate count and target/video ID. FFmpeg startup, ranking, verification, queue insertion, actual interaction edit completion, stream-resolution marker and first successful frame write remain separately instrumented. Production events carry interaction/guild IDs. The profiler's `discord_queued_ms` is explicitly null.

```sh
go build -o /tmp/bard-profile ./cmd/profile
docker run --rm \
  --mount type=bind,src=/tmp/bard-profile,dst=/tmp/profile,readonly \
  --entrypoint /tmp/profile whiterun-music-bot-bard:latest -playback \
  'Arctic Monkeys 505' 'Arctic Monkeys 505 live' \
  'Alice in Chains Nutshell unplugged' \
  'https://www.youtube.com/watch?v=qU9mHegkTc4' 'arctic   monkeys 505'
```

The final raw log includes the full successful local request traces. `play-flat-first-run.jsonl` retains the earlier experiment that unnecessarily verified unrelated verified channels; it is not the final implementation's measurement. The baseline trace's argv can reproduce the old extractor behavior without changing production code.

Remaining latency is the search config/API round trips (~1.4–1.6s here), one metadata verification where necessary (~1.4–1.8s), and fresh playback extraction plus initial audio delivery (~2.1–3.3s here). These are measured costs of the current yt-dlp path, not a claim that they are fundamentally impossible to optimize. Discord REST, voice joining and queue wait add their own latency in real use and are instrumented but unmeasured here.

## Validation and files

Offline tests cover the existing ranking/filter/variant rules, canonical ranking after verification, unknown/non-music rejection, fallback on verification failures, one selected extraction, normalization, expiry/eviction, coalescing with independent cancellation, and the fixed full-vs-flat candidate fixtures. Existing queue and playback tests remain.

All required checks passed: `gofmt`, `go vet ./...`, `go test ./...`, `go test -race ./...`, `go build ./...`, and `docker compose build`. Go cache writes required sandbox escalation; Docker uses the classic builder because buildx is unavailable.

Changed in this follow-up:

- `internal/media/resolver.go`: flat discovery, targeted verification, stable Track fields, external-operation traces.
- `internal/media/discovery.go`, `discovery_test.go`: provisional ranking, unchanged music gate before acceptance, selective verification and regression tests.
- `internal/media/ranking.go`: share the existing scoring and shape checks without changing weights or rejection terms.
- `internal/media/cache.go`, `cache_test.go`: normalized keys and cancellation-aware coalescing.
- `internal/media/resolver_test.go`, `testdata/discovery.json`: subprocess-shape assertions and captured fixed-candidate fixtures. Fixture descriptions retain only the phrase consumed by the filter; unused metadata is omitted.
- `internal/player/stream.go`: subprocess purpose/start/count logging.
- `cmd/profile/main.go`: measure the actual local GuildPlayer queue before streaming.
- `docs/play-flat-*.jsonl`, this report: raw measurements and findings.

Earlier acknowledgement/timing changes from the previous task remain in the working tree. No deployment or Discord message sending was performed.
