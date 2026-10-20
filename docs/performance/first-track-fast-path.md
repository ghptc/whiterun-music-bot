# PERF-018 — First track fast path

## Critical path recorded before implementation

At baseline `0665076`, `commandAt` acknowledges, connects Voice/DAVE, then calls
`PreparePlay → Prepare → spotifyMetadata → Spotify.Collection`. Only after the
complete collection returns does `BeginCollection` reserve positions and match
the first track. The background player already follows logical queue order with
three workers, six-position lookahead and a reserved foreground MatchLimiter slot.

For public playlists the provider validates HTML count/prefix, Embed metadata,
then paginates occurrence IDs with one revision. For 500 tracks, Embed supplies
100 tracks, but `anonymousPlaylist` additionally hydrates 400 individual track
Embeds with four workers, then rechecks revision, before returning any track.
PERF-017 observed 45.8–51.7 seconds here. Warm mapping/cache hits do not bypass
this collection import. Matching, yt-dlp extraction, FFmpeg/Opus and actual
Discord UDP send follow; the Queued response does not measure playback.

The queue contract reserves all occurrences atomically, uses stable job indexes
through shuffle/move/removal, and persists unresolved Spotify tracks for restart.
The minimal safe optimization is to retain the validated occurrence manifest but
hydrate metadata on demand in the existing resolution window. This avoids a
second importer, duplicate caches and unknown-identity placeholders. Pagination
and revision validation still precede reservation; this residual dependency must
be measured and must not be described as constant-time collection loading.

## Provider failures

Existing evidence identifies Spotify API 403 (public metadata fallback), and a
separate tail-hydration HTTP 504. HTTP 596 has not been reproduced. These are
not evidence of expired YouTube audio URLs or an extractor/client defect. No
access restriction bypass or additional retry is justified.

## Implemented strategy

`Prepare` opts into `Spotify.CollectionForPlayback`; synchronous `Collection` and
`Resolve` keep their complete-metadata contracts. The public path retains all
count, prefix, revision, offset and occurrence-UID checks. It reserves all known
Spotify IDs, with `MetadataPending` for entries outside the Embed. These entries
carry exact track URLs and source positions, not anonymous sessions or audio URLs.

`MatchSpotify` enriches pending metadata before constructing a search, under the
existing four-slot limiter (three background). It reads the existing SQLite
catalog first, otherwise fetches that exact Spotify track Embed and validates its
identity. Completed metadata is persisted in the existing catalog. Pending
placeholders cannot overwrite catalog rows. Catalog recovery restores complete
metadata and artwork before reservation when available, with one shared 200 ms
budget. No database or external requests were added to `/queue`.

The player itself is unchanged: its three workers enrich the next six logical
positions, including a shuffled/moved/jumped head. Thus metadata enrichment is
bounded by demand instead of importing 400 unnecessary Embeds before playback.
Known occurrences remain durable and can hydrate on playback after restart.
Failures affect the pending occurrence; stop/cancel cannot resurrect it.

Files: `internal/media/{progressive,spotify_anonymous,playback_metadata,resolver,
artwork,catalog}.go`, `internal/storage/catalog.go`; focused regression/benchmark
files and the existing Discord harness. Commit `af6c619` contains the production
change and deterministic regressions.

## Deliberate limits and rejected changes

This is a substantial optimization of the observed public-provider bottleneck,
but **not a fully streaming occurrence import**: the manifest and final revision
check still precede playback. The authenticated API path also still paginates
before reservation. Those dependencies grow with page count. The strict
acceptance requirement of starting as soon as first-track metadata exists is
therefore not fully met; release remains blocked on the real latency gate.
A subsequent change would need a durable partial-manifest import contract,
including recovery and revision changes, rather than silently dropping these
integrity checks. No constant-time claim is made here.

Metadata for distant entries is now loaded as the existing player window advances,
not eagerly for the entire collection while the first song plays. `/queue` shows
“Loading Spotify metadata…” for a verified occurrence not yet enriched; cached
metadata is reused. A distant track can still fail at enrichment or matching.
Collection edits after the validated snapshot do not reorder the captured IDs.

No additional cache, singleflight, workers, retries, prestarted FFmpeg, extractor
client switch, signed audio URL persistence or yt-dlp disk-cache change was added.
These would not address the measured 400-request metadata barrier. Voice still
connects before preparation; parallel preparation needs a separate cancellation
and admission change, and was not bundled without comparative evidence. DAVE and
Spotify identity verification remain intact.

## Reproduction

```sh
go test ./internal/media -run '^$' -bench '^BenchmarkPlaybackMetadata500$' -benchtime=30x -count=3
go test ./internal/media -run 'TestPlayback' -count=1
go test ./internal/player -run 'TestPERF018|TestFIX014|TestReorderedPendingHead|TestShuffleWhileResolution' -count=1
```

`TestPERF018ProviderMetadata` is opt-in (`BARD_PERF018_PROVIDER=1` plus Spotify
credentials); it measures one real paired import with synchronous and deferred
hydration, including CPU and allocations, without Voice. Do not mix it with the
synthetic benchmark or interpret a successful import as first audio.
`TestInstantPlaybackDiscordLive` reuses the authorized PERF-017 harness; the added
`BARD_LIVE_DISCONNECT=1` closes only its own connection before each sample.
The harness stimulates `commandAt` synthetically and confirms real DAVE/UDP sends;
its ACK is simulated and does not qualify as full real-interaction release proof.

## Measurements

Environment: same local Ryzen 7 9800X3D (8 CPUs available), Go 1.27.1, isolated
Docker runtime with the existing yt-dlp/FFmpeg/Deno installation and the authorized
Discord test channel from PERF-017. No production volume or deployment was used.
The source playlists remain externally mutable; track counts were checked by the
harness for every successful sample. Cold means a new SQLite media catalog and
SearchCache; warm primes the first metadata/match outside the timer. It does not
mean a cached collection manifest or cached signed audio URL.

### Local provider fixture (no network or Voice)

Three benchmark runs, 30 imports each, same 500-position fixture and same complete
manifest/revision validation. Synchronous path: 4.277–4.301 ms/op,
8.139–8.152 MB allocated/op, 47,331–47,351 allocations/op. Deferred path:
1.260–1.268 ms/op, 2.651–2.664 MB/op, 10,608–10,624 allocations/op.
Approximately 70.5% less elapsed time and 67.4% fewer allocated bytes. These are
benchmark means, not playback latency percentiles or peak resident memory.

### Paired real Spotify metadata diagnostic

One eager/deferred pair: **43,428.81 → 4,106.47 ms**, both succeeded. First Embed
metadata was available at 2,154.19 / 1,964.04 ms respectively; deferred preparation
still waited another ~2,142 ms for occurrence pagination/revision validation.
Self CPU: **248.46 → 26.42 ms**; allocated bytes: **66.70 → 13.59 MB**.
Sampled heap at completion: 2.19 / 2.16 MB (not peak RSS). The eager request paid
400 individual track Embeds; the deferred result retained 400 pending identities.
The paired diagnostic overlapped the connected playlist-123 probe; it is evidence
of the removed barrier, not a controlled latency distribution or release proof.

The current real probe reproduced **Spotify API 403**, followed by successful
public fallback. It produced no 596, 504 or YouTube stream failure. This does not
prove those transient failures are fixed; hydration failures are now isolated to
the relevant pending track instead of preventing the initial song.

### Real first Discord send

Three cold and three warm attempts for each of nine source scenarios: **54/54
actual UDP sends, zero observed failures**, versus 51/54 in PERF-017. No-audio
attempts remain in the baseline reliability denominator. This small sample is not
a reliability guarantee. For 500 cold, median TTFA fell **53.832 → 8.952 s (83.4%)**;
metadata preparation fell from the prior 45.8–51.7 s range to 4.10–4.32 s.
500 warm now sent in all three attempts (median 6.370 s, max 7.227 s); previously
all three metadata warmups failed.

The following table reports descriptive medians/maxima only. Three samples cannot
establish p95/p99; legacy harness nearest-rank fields are retained in raw evidence
and are explicitly insufficient for release. The initial cold YouTube row includes
one new Voice connection; the other connected observations reused Voice.
Concurrent local validation work and external network variance also prevent
attributing small changes in unaffected scenarios to this patch.

| Scenario | Cache | Sends/attempts | TTFA before p50 / max (ms) | TTFA after p50 / max (ms) | Metadata after p50 (ms) |
|---|---|---:|---:|---:|---:|
| youtube-single | cold | 3/3 | 3582 / 15806 | 3321 / 5289 | 1414 |
| youtube-single | warm | 3/3 | 2088 / 2308 | 1919 / 2075 | 0 |
| youtube-force | cold | 3/3 | 3238 / 3287 | 3398 / 3804 | 1325 |
| youtube-force | warm | 3/3 | 2281 / 2543 | 1858 / 1908 | 0 |
| spotify-single | cold | 3/3 | 5316 / 6586 | 4637 / 5708 | 187 |
| spotify-single | warm | 3/3 | 1788 / 1990 | 1617 / 2047 | 0 |
| spotify-album | cold | 3/3 | 5068 / 5338 | 4666 / 5306 | 505 |
| spotify-album | warm | 3/3 | 2747 / 2926 | 2267 / 2351 | 518 |
| playlist-10 | cold | 3/3 | 5532 / 6158 | 6014 / 7920 | 987 |
| playlist-10 | warm | 3/3 | 2935 / 3133 | 2679 / 4052 | 991 |
| playlist-50 | cold | 3/3 | 7030 / 7251 | 6743 / 6923 | 1679 |
| playlist-50 | warm | 3/3 | 3533 / 3555 | 3610 / 3814 | 1652 |
| playlist-123 | cold | 3/3 | 9433 / 9913 | 7443 / 8408 | 2653 |
| playlist-123 | warm | 3/3 | 6621 / 7315 | 4499 / 4517 | 2745 |
| playlist-302 | cold | 3/3 | 8188 / 8331 | 7949 / 8262 | 3246 |
| playlist-302 | warm | 3/3 | 4999 / 5094 | 5153 / 5193 | 3018 |
| playlist-500 | cold | 3/3 | 53832 / 56637 | 8952 / 9002 | 4253 |
| playlist-500 | warm | 3/3 | no audio | 6370 / 7227 | 4304 |

### Deterministic TTFA gate

A targeted rerun of the existing dispatcher/real-FFmpeg/simulated-UDP gate covered
single Spotify, playlists 10/123/302/500, Voice disconnected, restart, shuffle,
progressive resolution and ready-track transition: **19 scenarios × 100 samples,
1,900/1,900 sends, PASS**. Original thresholds and minimum sample count are
unchanged. The provider fixture here is controlled; the separate media tests
exercise the new public-provider parser and deferred enrichment. This gate
cannot approve network latency. The full earlier concurrency baseline remains
versioned; no new load, soak, fuzz or mutation campaign was run.

| Deterministic scenario | Mode | n | p50 ms | p95 ms | p99 ms | Max ms | Result |
|---|---|---:|---:|---:|---:|---:|---|
| spotify-single | cold | 100 | 21.83 | 22.30 | 22.67 | 23.77 | PASS |
| spotify-single | warm | 100 | 19.68 | 20.54 | 21.85 | 21.96 | PASS |
| playlist-10 | cold | 100 | 22.64 | 23.25 | 23.40 | 23.60 | PASS |
| playlist-10 | warm | 100 | 20.46 | 20.89 | 21.00 | 21.15 | PASS |
| playlist-123 | cold | 100 | 28.82 | 29.61 | 29.93 | 30.33 | PASS |
| playlist-123 | warm | 100 | 27.05 | 28.00 | 28.27 | 28.42 | PASS |
| playlist-302 | cold | 100 | 38.36 | 39.18 | 39.41 | 39.49 | PASS |
| playlist-302 | warm | 100 | 37.66 | 38.56 | 39.24 | 39.59 | PASS |
| playlist-500 | cold | 100 | 48.57 | 49.54 | 49.97 | 50.09 | PASS |
| playlist-500 | warm | 100 | 48.94 | 49.80 | 51.60 | 52.44 | PASS |
| voice-disconnected | cold | 100 | 21.90 | 22.71 | 23.11 | 23.22 | PASS |
| voice-disconnected | warm | 100 | 19.56 | 21.13 | 23.69 | 24.68 | PASS |
| after-restart | cold | 100 | 21.92 | 22.78 | 22.93 | 23.31 | PASS |
| after-restart | warm | 100 | 19.60 | 20.27 | 20.61 | 20.66 | PASS |
| after-shuffle | cold | 100 | 25.42 | 26.25 | 26.79 | 27.17 | PASS |
| after-shuffle | warm | 100 | 23.43 | 24.17 | 24.49 | 24.68 | PASS |
| progressive-active | cold | 100 | 49.39 | 50.39 | 51.10 | 51.30 | PASS |
| progressive-active | warm | 100 | 49.22 | 50.56 | 53.17 | 53.58 | PASS |
| ready-transition | ready | 100 | 24.61 | 26.44 | 27.95 | 29.55 | PASS |

### Validation and release decision

- Focused media regressions: 500 occurrences with unavailable tail, no tail calls
  before first match, exact identity/position/requester propagation, wrong identity
  rejection, existing catalog hit, cancellation and later-page failure.
- Player regressions: first playback with 499 deferred positions, shuffle while
  enrichment is blocked, stop/cancel, durable restart and provider timeout.
- Catalog regression: pending metadata cannot overwrite a complete catalog row.
- Existing FIX-014 reordered-head/EOF and stop regressions passed.
- `go test ./... -count=1`, `go test -race ./... -count=1`, `go vet ./...`: PASS.
- Docker Compose build: PASS, image built only; no service started or deployed.

**Release decision: NOT APPROVED.** Keep the deterministic gate and the real
homologation gate unchanged. The real gate remains FAIL: external collection
pagination plus yt-dlp extraction alone exceed the warm 2 s budget; cold matching
adds roughly another 2.8 s. Small external samples and simulated ACK independently
prevent approval. No missing/audio-failed attempts are removed to manufacture PASS.
The strict first-metadata-before-manifest acceptance requirement remains open.

Evidence: [local benchmark](evidence/first-track-metadata-bench.txt),
[paired provider diagnostic](evidence/first-track-provider-paired.json),
[connected real observations](evidence/first-track-real-observations.json),
[real gate FAIL](evidence/first-track-real-gate.json),
[deterministic gate](evidence/first-track-deterministic.json).
Raw local runs are under `.qa-runs/perf018` (ignored); versioned evidence contains
only sanitized measurements. No credentials or anonymous session material is saved
in these reports.

### Initially disconnected Voice diagnostic

Ten additional attempts (single Spotify and playlists 10/123/302/500, cold/warm),
with explicit disconnect before **each** command: 10/10 actual sends. One observation
per combination; no p95/p99 or reliability approval is inferred. These are separate
from the 54 connected/reused-Voice observations above.

| Source | Cache | TTFA ms | Voice connected at ms | First metadata at ms | Manifest/preparation at ms |
|---|---|---:|---:|---:|---:|
| spotify-single | cold | 6374.2 | 524.5 | not instrumented on this provider path | 4044.5 |
| spotify-single | warm | 3418.1 | 1208.9 | not instrumented on this provider path | 1209.3 |
| playlist-10 | cold | 6785.3 | 1095.7 | not instrumented on this provider path | 2309.9 |
| playlist-10 | warm | 3389.8 | 616.5 | not instrumented on this provider path | 1688.3 |
| playlist-123 | cold | 8596.5 | 1173.4 | 3379.1 | 4265.9 |
| playlist-123 | warm | 4740.7 | 756.4 | 2108.6 | 2730.4 |
| playlist-302 | cold | 7244.3 | 1095.5 | not instrumented on this provider path | 3521.0 |
| playlist-302 | warm | 5527.8 | 485.1 | not instrumented on this provider path | 3754.9 |
| playlist-500 | cold | 10100.8 | 609.1 | 2792.4 | 5691.5 |
| playlist-500 | warm | 5257.0 | 703.8 | 2154.3 | 3817.1 |

[Disconnected observations](evidence/first-track-disconnected-observations.json) retain
all attempts, timestamps and failures. API 403 still falls back normally; no
new 596/extractor diagnosis can be inferred from successful sends.

### Resource interpretation

The controlled 500 warm gate reported self CPU 6,208.8 → 5,670.4 ms per 100
trials; sampled heap peak 6.43 → 4.29 MB, max processes 2 and max concurrent
matches 3, with goroutines 3 before / 4 after (inside the existing cleanup bound).
Total allocation was **1,882.4 → 1,921.8 MB** per 100 trials (about +2.1%,
including fixture/catalog/persistence setup). This fixture supplies full metadata;
it does not measure the 400 deferred network requests. Small gate resource
differences are descriptive, not attributed wins. The real paired metadata probe
above supplies direct evidence for reduced enrichment CPU/allocations. No new
workers, parallel streams or production RSS measurements are claimed.
