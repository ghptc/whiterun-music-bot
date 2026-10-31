# OPT-026 — YouTube request efficiency and musical identity

Date: 2026-10-30. Baseline: `bf09d1c13a7a13f563e454cbfc7870b5fac1d671`.
Scope: three P0 improvements, offline only. No EC2 commands, YouTube requests,
deployment, restart, network changes, or authentication workaround were performed.
Reference: `docs/investigations/ec2-youtube-access-root-cause.md` (existing local
investigation; not included in this commit).

## 1. Findings and scope

The existing SearchCache already coalesced identical discovery/matching requests.
It did not share `VerifySpotify` extraction across callers, renew the caller's
proof, or persist renewal. Restored checkpoints therefore lost useful in-memory
proof even when SQLite still held a fresh exact mapping. Provider recovery budgets
belonged to individual GuildPlayers. CollectionJob prefetched six upcoming entries
per collection without considering other collections or the time to playback.
At 210 seconds per song, that window could outlive the ten-minute proof lease.

The existing MatchLimiter, Spotify cache, MappingStore, queues, CollectionJob
workers, matching policy, checkpoint policy and storage limits remain in use.
There is no new database, queue, service, scheduler, or retry goroutine. No matching
score or threshold was changed. Unrelated dirty files were excluded by developing
and validating an isolated archive of the baseline revision; overlapping resolver
edits are merged back while staging only this change.

## 2. Exact changes

### Shared provider admission

`internal/media/provider_admission.go` owns one process/egress admission gate.
Bot configuration passes the same pointer to metadata resolution, YouTube playlist
extraction, streaming, and every GuildPlayer. Defaults are four concurrent startup
operations and at least 250 ms between starts. Metadata holds capacity through
extraction; streaming releases it after the first successfully sent frame, so
long-running healthy audio does not monopolize startup slots.

Only classified bot/authentication challenges and rate limiting open the global
breaker. A plain HTTP 403, timeout, malformed JSON, missing extractor, unavailable
video or age-specific authentication does not establish a global restriction.
Age-restricted classification retains the authentication category for existing
consumers but carries video-specific scope; it cannot open this breaker.

Existing recovery callers drive the shared policy: 15/30/60-second waits, three
controlled half-open trials, only one trial token at once. Other guilds wait rather
than start their own budget. No /play, /resume, /leave or metadata-only success
resets the budget. Sustained newly admitted media (250 frames, approximately five
seconds) can clear it; audio admitted before the outage cannot. Cancellation before
an external start refunds a trial; attempts that reached extraction remain spent.
Concurrent failure generations prevent a successful trial from erasing another
confirmed failure. Notifications use the shared incident ID and a per-guild last
notification ID. Global exhaustion is explicitly described as requiring an operator.

The gate never cancels healthy streams or local playback, and never calls Voice
Leave. A paused healthy stream can resume even when global recovery is exhausted,
without resetting that budget. Each player retains its own queue, checkpoint, and recovery progress policy.
Legacy standalone/local-error recovery remains in the existing player path; it does
not run alongside a global trial for the same provider incident.

### Share extraction, compare each intent

`mapping.go`, `cache.go`, `spotify_rank.go`, `resolver.go`, and the Bot play path
share raw full metadata through existing bounded SearchCache/singleflight.
Keys include the exact **case-sensitive** video ID and immutable extraction context
(binary and metadata arguments). Consumers cancel independently; the final departing
consumer cancels and reaps the extraction. Failed extraction is never cached.
Raw candidates and private proof slices are deep-copied for caller ownership.

Every caller still executes `CompareSpotify` against its own requested identity.
Raw extraction may be reused even when another caller rejects it: musical acceptance
is never shared across different intents. Freshness is capped by the ten-minute
proof lease. `VerifiedSpotify` returns renewed caller proof and persists it through
existing MappingStore; `VerifySpotify` remains a compatibility wrapper. A fresh
SQLite mapping is also compared before use after checkpoint restoration. Storage
renewal failure does not turn a successful identity comparison into playback failure.
Known corrupt/different-video mappings retain the existing safe invalidation/fallback.
Signed media URLs are not cached; media extraction still happens at playback.

### Playback horizon

`internal/player/horizon.go` uses current confirmed progress and the actual guild
queue, including earlier collections. Existing CollectionJob workers consider only
positions estimated to play within eight minutes, also retaining the six-entry cap
and three-worker cap. A one-minute freshness allowance fits inside the ten-minute
proof lease. Cached proof that would expire before the estimate is refreshed once
using the exact mapping, without another search.

Unknown/live duration is a conservative barrier after the next eligible occurrence.
Pause prevents new speculative resolution, including rechecking the horizon after
a backoff wait; resume, queue mutations, skip/stop and
existing five-second checkpoint wakeups reevaluate the horizon. A distant collection
publishes a deferred first-result response so a command does not wait hours or hold
its admission slot. Positions and duplicate occurrences stay reserved in order.
Standalone GuildPlayer embedders retain legacy count-only prefetch unless explicitly
configured; production Bot initialization always supplies the bounded horizon.

## 3. Offline measurements

All counts below are **extractor process invocations or mocked matcher operations**,
not inferred HTTP requests. A single yt-dlp invocation may perform many HTTP requests.
No HTTP traffic reduction is claimed. Probe sources and outputs are in
[`evidence-opt026`](evidence-opt026/measurement-optimized.txt).

| Metric and controlled scenario | Baseline | Optimized |
|---|---:|---:|
| Cold track, discovery + full verification + media process | 3 | 3 |
| Warm track, media process only | 1 | 1 |
| Five concurrent expired-proof checks, same video/context | 5 | 1 |
| Two sequential checks of the same expired proof | 2 | 1 |
| 123 distinct cold tracks, all eventually resolved, metadata processes | 246 | 246 |
| 123 warm tracks, additional metadata processes | 0 | 0 |
| Two guilds, automatic outage recovery trials | 6 | 3 |
| Same outage, initial + recovery actual matcher admissions | 8 | 4 |
| 123 × 210-second playlist, before first EOF, matcher calls | 7 | 3 |
| Second 123-position playlist behind it, early matcher calls | 6 | 0 |
| Cold matching latency p50 / p95, milliseconds | 31.889 / 32.251 | 31.861 / 32.330 |
| Warm matching latency p50 / p95, milliseconds | 0.017 / 0.020 | 0.017 / 0.024 |
| Incorrect selections, new 31-negative identity corpus | 0 | 0 |

Latency uses 25 synthetic identities, a local Python extractor fixture, no admission
pacing, and no network; the small differences are noise, not a latency improvement
claim. Production pacing intentionally adds waiting under load. Count probes were
run against the baseline and optimized archive on the same machine. The baseline
corpus result is additionally checked against the unchanged comparator. The full
flow probe uses real SQLite reopen, real FFmpeg/Opus, and simulated Voice frames.
It confirms cold/warm/reopened playback counts, never real Discord audio.

For 30-second tracks the count cap still allows seven initial matches; 20-minute
and unknown-duration tracks allow one. This is intentional: the optimization removes
premature work, not necessary future verification. Pauses, long stalls and actual
availability changes may still require renewal. The older audit's 120-extra-proof
figure for 123 × 210-second tracks is an analytical projection, not a measured
HTTP count or a claim that this change eliminates every future verification.

## 4. Identity fidelity

The added corpus has 14 positive and 31 negative pairs, with zero false positives.
It covers studio/live, original/remix/cover, radio/extended, remaster 2021/original,
acoustic, instrumental/vocal, explicit/clean, individual/full-album and featured
artist credit mismatches. Named fixtures include Pursuit Of Happiness — Extended
Steve Aoki Remix, Heads Will Roll — A-Trak Remix, Beelzeboss and Fuck Her Gently.
These are synthetic metadata fixtures, not downloaded recordings. Existing quality
and hardening corpora continue exercising primary artist, album, duration, producer,
edition and insufficient-evidence decisions. Missing duration stays MATCH_UNCERTAIN;
age restriction/unavailability is classified and preserved without attempted bypass.

## 5. Concurrency, cancellation and recovery evidence

- Five consumers share one extraction; all receive the correct raw metadata with
  independent copies. One cancellation leaves four successful consumers; canceling
  all five terminates and reaps the subprocess. A subsequent success in the same
  previously failing context works; different binary contexts and case-sensitive
  IDs do not collide.
- Rate and concurrency tests verify paced starts, bounded simultaneous capacity,
  idempotent release, breaker wakeup and cancellation without capacity leaks.
- Two simulated guilds retain all 246 positions throughout one initial failure and
  three global trials, then no additional attempts during a simulated hour.
  Repeated resume cannot renew the budget. There is one notice per guild, zero
  Voice Leave and independent recovery of position one after explicit operator rearm.
- Global recovery at 10%, 49%, 50%, 75% and 99% preserves FIX-025: resume below
  50%; exactly one advancement at or above 50%. Existing EOF/cancellation race,
  watchdog and notification regressions remain enabled.
- Already healthy audio continues through another guild's incident. Real local
  FFmpeg/Opus playback completes while the global YouTube breaker is open.
- The 123-position and second-playlist tests cover duration horizons, unknown
  durations, duplicates, proof expiration, pause/resume, skip and stop. No queue
  position is reordered, resurrected, or silently discarded by deferred work.

## 6. SQLite and existing guarantees

A real SQLite regression expires a proof, renews it once, closes/reopens storage,
then verifies without a second extraction; resume offset is preserved and health
checks succeed. No schema/migration or storage policy changes were made. Existing
DB-022 physical/logical quota, WAL/crash/atomic import, 10 GiB catalog defaults,
500 MB log retention and Discord alert regressions run in the complete suite.
No production database was opened or mutated. FIX-025, FIX-025.1 and REL-024 tests
remain in the suite, including queue preservation and no duplicate advancement.

## 7. Validation

Commands executed on the isolated intended commit tree:

```sh
go test ./... -count=1
go vet ./...
go test -race ./internal/media ./internal/player ./internal/discord ./internal/storage
go test -race ./internal/media ./internal/player ./internal/discord ./internal/storage -run '^TestOPT026' -count=1 -v
docker build -t bard-opt026:validation .
```

Final full suite and vet passed. Full and targeted race results are recorded
in the accompanying evidence files, alongside the diagnosed intermediate failure.
Docker build passed; local inspected image ID is
`sha256:045af4700739273724204eb1477dbed000267770eff5fac70508ad3ee56cb08c`.
The image remains local and was not deployed. Docker's
existing build also runs vet, the full suite and the FFmpeg/Opus encoder check.
Compose configuration was validated with dummy credentials and `config --quiet`.
No production credentials were used.

A full race run initially passed; a subsequent validation failed the pre-existing
`TestREL024HealthDoesNotWaitForDeadlockedWorker` immediate TryLock assertion, with
no data race warning. Both baseline and optimized 100-run checks passed, but a
fixed 10,000-run baseline race stress reproduced the same assertion failure.
After unlocking, the real worker can legitimately acquire the mutex before Health.
The test now allows eventual responsiveness within one second, retaining the
immediate nonblocking check while the lock is held. No production watchdog code
was relaxed. The same 10,000-run race stress passed after this test-only correction;
the failed run and baseline reproducer are preserved in evidence. This was diagnosis
and correction, not repeated execution until green. Existing live Spotify, YouTube
and real Voice tests remain opt-in/skipped; this report does not claim them.

## 8. Operational configuration

| Variable | Default | Validation / scope |
|---|---|---|
| `YOUTUBE_MAX_CONCURRENT` | `4` | 1–32; shared startup/extraction capacity |
| `YOUTUBE_ADMISSION_INTERVAL` | `250ms` | At least 1 ms through application config; shared spacing |
| `YOUTUBE_PREFETCH_HORIZON` | `8m` | Positive, maximum 8m; per-guild actual queue ETA |

These are startup-only and wired through Compose and `.env.example`. The Go
admission constructor permits zero interval for deterministic tests/embedders;
production environment parsing does not. Match scoring and proof lease are unchanged.

After three global trials, automatic attempts stop. `ProviderAdmission.Rearm()` is
an explicit process-level operator API, not bound to public Discord controls.
This patch deliberately adds no new administrative command or second retry loop.
There is currently no remote owner rearm endpoint: after investigating restored
provider access, operational rearm requires a separately authorized integration or
controlled application lifecycle action. Guild resume does not reset the budget.
The incident/budget is process-local and naturally does not survive process restart;
SQLite queues/checkpoints still do. No restart is performed by this work.

## 9. Risks and limitations before deployment

- One gate coordinates one Bot process/egress, not multiple replicas or other
  applications sharing an address. No distributed infrastructure was added.
- A full metadata trial can perform multiple extractor operations sequentially;
  only one logical trial is admitted globally. Actual HTTP fan-out is not measured.
- Metadata success does not prove media-server access. The incident budget remains
  until sustained newly admitted media confirms recovery. A local failure during a
  trial conservatively leaves the global breaker suspended and spends that admitted
  trial; it is not classified as a new global restriction.
- Freshness is estimated using durations and confirmed offsets; unknown/live songs
  deliberately suppress speculative work. Queue mutation may leave an already
  started extraction in flight; its result remains guarded by existing cancellation
  and position/generation checks.
- Cache limit remains 256 entries shared between matching and raw proof, so eviction
  can reduce reuse under large/many-guild workloads. Mappings preserve durable proof.
- Provider authentication, PO-token/client requirements and cloud restrictions are
  external constraints. This optimization supplies no authorization or access bypass.
- Deployment requires separate review/authorization and real playback verification.
  Passing offline tests does not restore or homologate YouTube access on EC2.

## 10. Decision

Retain the existing process and SQLite architecture for this targeted change.
Coordinate external admission, reuse exact-video evidence while comparing each
musical intent, and resolve only the approaching playback horizon. The measured
benefit is fewer redundant extractor invocations and less premature work with the
same matching thresholds and ordered queues. YouTube availability remains unresolved
by optimization alone. Nothing in production was modified.
