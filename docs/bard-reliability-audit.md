# Bard reliability audit — 2026-09-27

Follow-up: [Docker test environment correction](bard-test-environment.md) installs
and verifies build-stage FFmpeg/libopus and records acceptance results inside
Docker. Statements below about the missing build prerequisite describe the
initial audit snapshot, before that environment-only correction.

## Scope and result

Audited the working tree, including pre-existing modified and untracked files,
not just HEAD `027ef518594bf77ff160dc523678e640ec1aa42e`. Production files were not
changed by this audit. See [source fingerprints](bard-audit-source-sha256.txt),
[requirements](bard-requirements.md), [test mapping](bard-test-map.md), and
[sanitized runtime evidence](bard-audit-evidence.json).

**The historical September 24 second-track failure occurs at external YouTube extraction.**
Natural EOF and successor process creation both work in that recording. yt-dlp
rejects the successor with an anti-bot challenge before any successor Opus frame.
FFmpeg's invalid-input error is downstream of the failed extraction. The current
player then exhibits a separate, deterministic reliability defect: it treats a
provider challenge as an individual track failure, consuming five positions.

The original suite passed. A new two-track real-FFmpeg acceptance test also passes.
A new provider-challenge queue-preservation test fails, deliberately and without
a skip/build tag. This is **not a claim that real Bard playback is fixed**.

The user confirmed that the latest failing instance runs on their Arch Linux
host, outside this workspace, and no exact latest trace is available. **The latest
runtime root cause is unknown and was not reproduced here.** Historical container
evidence below must not be substituted for that missing capture.

Static/deterministic findings: sibling track-context ownership, successful local
two-track media delivery, and reproducible queue consumption under an injected
provider challenge. Runtime findings requiring the user's capture: the latest
failure category, its cancellation source, process exits, Opus/voice milestones,
and whether it has anything in common with the historical challenge.

## A. Current architecture

`cmd/bot/main.go` owns the signal context and checks yt-dlp, FFmpeg and Deno.
`discord.New` owns the shared metadata resolver/cache, four command-search slots,
one MatchLimiter and a map of guild sessions. Each guild owns a player, voice
connection, search-session context, announcement queue and orchestration mutex.

`handle` starts a tracked command goroutine. `command` acknowledges first, checks
guild/channel membership, opens voice if necessary, captures generation ticket
and search context, releases the guild lock, then calls `Prepare`. It rechecks
membership before insertion. The four command slots reject excess requests; they
are not a playback rate limiter. Progressive commands release their slot after
`job.First`, while background work continues.

Spotify playlist/album `Prepare` resolves **metadata only**. API calls obtain and
cache credentials under the Spotify mutex, paginate in batches of 50, filter
unavailable/local records, and cap collections at 500. Playlist access failures
403/404 can fall back to public HTML, then Embed, then anonymous occurrence
pagination plus up to four concurrent metadata hydration workers. Public paths
validate total/prefix/identity; anonymous pagination also validates offsets,
revision, unique occurrence IDs, order and a final first-page revision check.
Public metadata has a two-minute outer timeout and 15-second request bounds;
Prepare has a ten-minute metadata bound. The API path follows `next` presence
rather than verifying final accumulated count against a stable total/revision:
R1 is stronger than this API implementation, an additional coverage gap.

`BeginCollection` reserves all logical positions under `player.mu`, using a new
job ID. The first match is sequential, skipping errors. After its first success,
three workers resolve the rest. A coordinator publishes only contiguous ordered
prefixes into existing slots. Errors remove slots, regardless of whether they
are individual or provider failures. Cancellation removes unresolved slots and
retains already committed tracks. The job is not a track context.

`MatchSpotify` acquires one of four global match slots; background work also
acquires one of three background slots. The spare slot is for a **first match**,
not a reserved playback extraction slot. Spotify matching performs Music search,
regular search and up to four candidate verifications, sequentially per match.
Successful metadata is cached for ten minutes, at most 256 entries. Concurrent
identical misses share a separately owned flight, canceled when all waiters leave.
`selectSpotify` discards verification errors and can return `ErrSpotifyMatch`,
losing a provider cause. General discovery has a different error-preservation path.

YouTube playlist preparation uses flat metadata (up to 501 to detect overflow),
preserves IDs/order/duplicates, and marks entries `VerifyBeforePlay`. `Bot.play`
verifies these exact IDs at their turn before streaming. Spotify matching already
selects a canonical watch URL. No queue entry stores a signed stream URL.

One `GuildPlayer.run` goroutine serializes all playback for a guild. Under its
mutex it removes the ready head, creates a fresh child of `p.ctx`, installs a
pause gate and track completion channel, and increments a **sequence**. It then
releases the mutex and calls `Bot.play`/`Streamer.Play`. Generation tickets guard
stale queue insertion; generation is not the track sequence and does not advance
at natural EOF. Attach/stop/clear/leave invalidate tickets.

Streamer creates a child cancellation-cause context, startup and activity
watchdogs, an OS pipe, and separate process groups for yt-dlp and FFmpeg. FFmpeg
starts first. yt-dlp writes media through a Go copier to FFmpeg stdin. FFmpeg
converts it to 48 kHz stereo, 20 ms libopus packets in Ogg. The parser rebuilds
packets, discards Opus headers/tags, paces frames, waits at the pause gate and calls
Voice.WriteFrame. The first successful write triggers the start announcement.

`transport.WriteFrame` waits for DAVE readiness for up to 60 seconds when required,
checks cancellation, sets a three-second UDP write deadline, and writes through
DisGo. Installed DisGo `udp_conn.go:259` encrypts with the DAVE session, wraps RTP
transport encryption, writes UDP, and advances RTP sequence/timestamp. Those
transport counters and DAVE state survive track changes. Local sink success does
not exercise this encryption/network path or prove remote audibility.

There is **no audio prefetch, YouTube cooldown, rate limiter, provider circuit
breaker, or shared playback/background scheduler**. Progressive matching is
metadata lookahead across the entire remainder, not playable-stream prefetch.

### Async ownership and locks

| Component / creator | Owner and lifetime | Cancellation and join | State / locks | Survives track transition? |
|---|---|---|---|---|
| Signal context / main | Process | SIGINT/SIGTERM; Bot.Close | Bot lifetime | Yes |
| Command worker / handle | Bot.workers; one interaction | Bot context; scoped ack/reply/join deadlines; workers.Wait | b.mu for registration, g.mu for voice/insertion | May; background job is independent |
| Player run / player.New | Guild player | p.ctx; Close waits p.done | queue/current/jobs/tickets under p.mu | Yes |
| Track context / run | One playback invocation | skip/stop/leave/root cancel; run cleanup after return | trackCause/cancel/done and sequence under p.mu | No; next is a sibling |
| Stream context / Play | One pipeline | Track cancellation, startup/stall/drain errors; deferred cleanup | process handles and pipe ownership local | No |
| Startup watcher / Play | Pipeline | 30s extraction then 30s first Opus; exits on first Opus; monitors.Wait | milestone channels | No |
| Activity watcher / Play | Pipeline | Default 90s no successful frame; paused time refreshes clock; monitors.Wait | atomic timestamp and pause mutex | No |
| yt-dlp Wait goroutine / Play | Pipeline | Group SIGKILL; wait result channel; joined | atomic ytFinished; stderr written by exec copier | No |
| FFmpeg Wait goroutine / parser EOF | Pipeline | Shared two-second drain budget; group kill on cancel; joined | atomic ffFinished/result channel | No |
| exec I/O/context workers / Command | exec.Cmd | Cancel kills negative PID group; generic WaitDelay 2s; playback producer WaitDelay=0 | per-command stdout/stderr; no player lock | No |
| Force-cancel callback / Play AfterFunc | Pipeline | On stream cancellation kills unfinished groups, closes descriptors; deferred stop or waits callback completion | atomics/OS descriptors | No |
| Collection coordinator / BeginCollection | Player job; max ten minutes | search-session parent, player root AfterFunc, clear/stop/leave/job exhaustion; Close waits jobWorkers | job map/wanted/searches/progress and queue under p.mu | Yes |
| Three collection workers / job.run | Collection job | job context and per-position child cancellation; coordinator waits workers | buffered tasks/results; commits only coordinator, p.mu | Yes |
| Four synchronous matching workers / Resolve | Non-progressive collection resolution (not playlist Prepare) | collection deadline/context; WaitGroup | disjoint matched indexes | May, separate work |
| Shared cache flight / cache.resolve | All waiters, not first guild | WithoutCancel-derived work context; last waiter cancels; resolver 90s deadline | cache mutex and done channel | Yes |
| MatchLimiter waiters / MatchSpotify | Matching calls | Context selects release acquired tokens; no worker goroutine of its own | buffered channels, no player lock | Yes |
| Anonymous hydration workers / hydrateSpotifyTracks | One metadata call | first failure cancels peers; parent deadline; WaitGroup | disjoint slots, sync.Once firstErr | May, never track-owned |
| Announcement worker / setupAnnouncements | One per guild, bot lifetime | b.ctx; workers.Wait; each REST send 5s | bounded channel (32), announcement mutex, atomic epoch/channel | Yes |
| Voice health watcher / Start | Bot lifetime, one ticker | b.ctx, workers.Wait | b.mu snapshot then g.mu.TryLock; per-guild deadlines | Yes |
| Voice event worker / voiceStateChanged | Bot.workers | Finishes after guild lock/update/wake | cache, g.mu and wake channel | Yes |
| DAVE/voice gateway workers / library connection | Voice session | Conn.Close and dave.Close stop session watchdogs; library owns reconnect/heartbeat | gateway/UDP connection locks; DAVE RWMutex | Yes, until leave/reconnect |
| Pause gate / run | One track | Resume closes gate channel; wait also observes context | gate.mu; p.mu -> gate.mu only | No |

No process execution/wait is under `player.mu`. Guild voice orchestration does
hold `g.mu` during join/leave. `Leave` waits unconditionally for trackDone;
the passed cleanup context does not bound that wait. `Close` additionally joins
the player and collection workers. Stop/clear cancellation is asynchronous.

The announcement callback can block when its 32-slot queue is full: it selects
send or bot shutdown, not track cancellation. Thus the comment claiming complete
REST/audio isolation is stronger than code. This is a separate resource-risk
finding, not the observed anti-bot failure.

The voice watcher leaves after two minutes without listeners, not two minutes
without music. Known move/kick is terminal; transient unhealthy gateway/cache
state gets 60s grace. These operations reset search sessions, invalidate queue
tickets, cancel playback and close voice. DAVE readiness uses its own timeout;
it does not own a successor's process context.

## B. Concrete second-track execution path

1. `/play` Spotify playlist: acknowledge, join/check channel, acquire command slot,
   capture search context and generation ticket, call Prepare outside guild lock.
2. Metadata becomes trusted when Spotify.Collection returns its accepted complete
   snapshot and Prepare validates nonempty/limit/context. No YouTube work yet.
3. BeginCollection reserves placeholders. Job resolves position 1 using first
   match capacity. If it fails, the current implementation removes it and tries
   the next position, without provider classification.
4. Commit makes position 1 ready and wakes run. Run removes it and creates its
   track context from p.ctx. It calls play outside the mutex.
5. Job signals First and starts up to three background matches. Scheduling does
   not guarantee audio started before background work; first *commit* is the gate.
6. Bot.play calls Streamer; FFmpeg then yt-dlp start. Startup watcher ends at
   first Opus, before DAVE wait. Successful voice write records first audio.
7. Natural producer exit drains buffered data. yt.Wait closes the write pipe.
   Ogg parser sees EOF; pipeline waits for both processes within drain bounds,
   classifies errors, sends five silence trailers on success, turns Speaking off,
   stops monitors and returns. run logs EOF and cancels only the completed track.
8. run clears current state and closes trackDone. It selects the next ready head,
   or waits if it is pending/halted/empty/detached. It never reuses track 1 context.
9. Track 2 gets its own sequence, pause gate and context. YouTube playlist entries
   first get exact-ID metadata verification; Spotify entries proceed to a fresh
   yt-dlp full audio extraction. Metadata success is not stream availability.
10. On success: independent processes, Opus frames and voice submissions. On
    provider rejection: yt-dlp exits positive, FFmpeg receives invalid/empty input,
    no media frame reaches voice. On transport failure: Opus existed, WriteFrame
    fails, pipeline cancellation kills processes. These are different boundaries.
11. Current policy has no player retry. Any unexpected error increments failures;
    the popped track is lost and the next is tried. Five consecutive failures
    halt; pending jobs continue. Nil return or expected cancellation resets the
    streak. `/resume` from halt clears streak/halt; accepted enqueue/collection
    also clears it. No cooldown or automatic provider recovery exists.

## C. Reproduction evidence

No bot was started and no Discord messages were sent. The stopped Compose
container was inspected read-only. Its image is
`sha256:978fb1bdb8fe100c648fbca7ac90e425c6d2d36e25d9d669a4bdb66665556dca`,
created/started 2026-09-24 23:44:40 UTC and stopped 2026-09-25 10:28:41 UTC.
The deployed binary was **not proven identical to the current dirty working
tree**. The raw retained log was kept outside the repository with mode 0600;
the checked-in evidence contains only allowlisted fields and challenge booleans.

| UTC event on September 24 | Evidence |
|---|---|
| 23:45:10.327 | Sequence 1 selected; independent FFmpeg/yt-dlp startup follows |
| 23:45:12.567 | First Opus, first Discord send and first audio frame |
| 23:51:27.927 | Sequence 1 source exit: yt=0, FFmpeg=0, category=eof |
| 23:51:28.028560 | Track 1 ends EOF; cancel_reason=null |
| 23:51:28.028582 | Sequence 2, position 2 selected |
| 23:51:28.028821 | Track 2 FFmpeg starts; extraction start follows |
| 23:51:30.256 | Track 2 yt-dlp completes with failure |
| 23:51:30.258 | yt=1, FFmpeg=1; anti-bot diagnostic; no first Opus/send for track 2; parent cancel_reason=null |
| 23:51:40.936 | Positions 2–6 failed, player halted; ready=137, pending=155 |

The retained record contains 51 source exits: 32 EOF, 12 generic yt-dlp failures,
four HTTP 403, three explicit cancellations. Later sequences 34–36 and 38–40
show consecutive natural completions. Thus “every second track always fails”
is not supported by this record; intermittent provider availability is.

During sequence 1, 753 metadata extraction starts occurred: 465 candidate
verifications, 145 Music searches and 143 video searches. These are subprocess
starts, not an HTTP request count. Background matching continues after halt.

Current-code deterministic replay uses a controlled provider process returning
the anti-bot failure text after the first natural playback, real FFmpeg, real
player/queue/contexts/process groups and a recording voice sink. Result:
`attempts=6 retained=1 halted=true` for a seven-track queue. No external network
is involved. This reproduces the failure handling at the established boundary.

## D. Classification and uncertainty

**Established for recorded sequence 2:** external extraction rejection. It starts
after successful predecessor EOF, has a fresh pipeline and an uncanceled track
context, and never reaches Opus/voice. FFmpeg invalid input is a consequence.
Not evidence of stale track context, generation mismatch, queue reorder, limiter
starvation, or DAVE failure in this sequence. Playback doesn't acquire MatchLimiter.

**Established in current code and replay:** provider challenge lacks a distinct
policy and burns queue positions. `sourceFailure` recognizes 403/410/expired/
broken-pipe strings but not challenge/429; `run` does not branch on category anyway.
Joined errors can expose an earlier parser/no-audio cause as the top category,
so all error components/process exits matter, not just the headline category.

**Plausible, unproved trigger:** sustained background metadata pressure contributes
to provider challenge. No controlled counterfactual or provider-side evidence
establishes causation. IP reputation, client/extractor behavior, provider changes,
or combinations remain possible. An HTTP 403 alone does not prove an anti-bot cause.

Host yt-dlp is absent. No fresh authenticated Spotify/YouTube/Discord session was
run. The deterministic current-code result plus retained production evidence
demonstrates the historical external boundary; it is not a fresh live reproduction
on a byte-identical deployed build.

## E. Exactly why existing tests pass

There are three distinct substitutions, not one generic “mock problem”:

1. `player.New(..., PlayFunc)` transition and progressive tests replace the entire
   extraction/FFmpeg/parser/voice path with channel notification and nil/error
   return. Their `started <- title` means selection, not audio. EOF is a channel
   value or function return. A successor with permanently broken yt-dlp still
   satisfies those assertions.
2. Process tests execute actual shell children and exercise cancellation, pipes
   and waits. The soak's source emits a tiny hand-built Ogg header and one byte;
   its decoder is `cat`, and fakeVoice accepts any bytes. It counts successful
   first-frame announcements but does not decode valid successor audio. Injected
   403s are isolated and explicitly expected to advance, reinforcing the wrong
   policy for provider-wide failures.
3. The real-FFmpeg tests feed local synthetic media to one Streamer invocation.
   They cover real encoding, pacing, cancellation and draining, but neither real
   provider extraction nor the combined player transition. They skip when FFmpeg
   is absent. Docker's build-stage tests run before runtime FFmpeg installation,
   so that stage does not guarantee these tests execute.

Metadata resolver tests return fixed JSON from executable scripts; HTTP tests
inject RoundTrippers/fixtures; readiness tests replace DAVE epochs with a channel.
MetadataArgs intentionally uses skip-download/ignore-no-formats-error and different
extractor options from playback. Resolving a watch URL can succeed even when later
full playback extraction is challenged. No previous test modeled “metadata still
works, first stream works, subsequent provider extraction is globally rejected.”

`TestConsecutiveFailuresHaltAndResume` expects exactly five generic errors to be
consumed and three survivors out of eight. It verifies the current threshold,
not the requirement to preserve tracks on provider unavailability. Green is
consistent with the observed failure; the missing assertion is precisely R3.

## F. Weak tests and false-positive routes

- `TestQueueAdvanceSkipFailureAndStop`, `TestBatchOrderClearAndCapacity`, command
  collection tests, progressive first-start/order/failure tests: successor
  selection is strong queue evidence but no audio evidence. Many use Skip to
  traverse fixtures, not natural EOF.
- `TestNaturalEOFRacesWithSkipAndStop`: synthetic EOF and `successors <= 1`; zero
  successors is accepted, and Close cancels before the final assertion. Suitable
  for “no duplicate/stuck state,” insufficient for liveness or audio.
- `TestPipelineSoak125Transitions`: real child PIDs/reaping and frame announcements
  are useful. One identical invalid-as-Opus byte per track, no per-track media
  identity, no real decoder, no progressive worker pressure. FD/goroutine baselines
  are aggregate and allow one extra; they are not a proof of all descendant leaks.
- `TestStreamerLocalFFmpeg`: `frames >= 10` includes five appended silence frames.
  Its 100 ms fixture yields about six media frames. It does not establish ten
  media frames or any second-track media. New acceptance excludes trailers.
- `TestStreamerFailuresAndCancellation`: mostly nonnil error and elapsed <1s;
  not exact root classification or queue preservation. Real group kill happens,
  but these assertions alone cannot detect misdiagnosed provider errors.
- `TestSourceSurvivesAudioEOFCleanup` and helper-leader cleanup tests: useful real
  exec boundaries; neither models a provider challenge or a second voice stream.
- `TestProgressive123StartupMeasurement`: fake 10 ms metadata/search and one fake
  frame, compares elapsed time with old path; load-sensitive and not production
  latency evidence. Most tests do use gates/Done/WaitGroups; this is not a blanket
  claim that all async tests are unsynchronized.
- Limiter canceled-waiter test cancels immediately after spawning; it does not
  prove the worker first blocked inside acquire. It tests cancellation/release
  outcomes, not playback priority or fairness under long-lived background load.
- `TestConcurrentQueueAccess` ignores several errors and has no behavior oracle;
  useful for race/deadlock exercise only. Cache coalescing waits for waiter counts,
  but its work is a channel callback, not os/exec.
- `TestDiscoveryWithCompleteMetadataMatchesExistingSelection` and captured
  selection comparison calculate expected selection using another production
  selector. Shared ranking mistakes can pass both sides. Fixed expected-ID
  canonical fixtures provide more independent expectations.
- Announcement no-duplicate check uses a 20 ms quiet window; it cannot exclude
  later duplicates. DAVE recovery tests use 1–200 ms scaled deadlines and fake
  readiness; they do not perform real MLS recovery or UDP encryption.
- Public Spotify queue test still calls synchronous Resolve + EnqueueMany. Its
  “atomic queue” assertion does not cover production playlist Prepare + progressive
  BeginCollection. Public metadata tests themselves do validate useful invariants.
- Synthetic Ogg tests do not validate CRCs/full Opus headers or decode packets.
  They test the parser's accepted grammar. No dedicated graceful SIGTERM test
  exists: Command intentionally sends SIGKILL, not TERM-then-KILL.

## G–H. Closed and remaining test boundaries

Added `internal/player/reliability_test.go`:

- `TestTwoTracksNaturalEOFDeliverIndependentAudio`: ready and progressive cases;
  tone-distinct valid PCM source helpers, real FFmpeg/libopus, production player
  and Streamer, six recorded media frames per track, exact two invocation results,
  natural zero-error completion with live parent contexts, process-start records,
  source natural-exit records and a first-pipeline-completion sentinel required
  by the second extractor. FFmpeg starts are recorded through an exec wrapper.
  First helper stays alive 80 ms after emitting media. No user controls advance it.
- `TestProviderChallengeDoesNotConsumeQueue`: the same real pipeline, then
  deterministic provider challenge, proves first media/EOF, second independent
  failure with live parent and zero successor media. Expected two total attempts
  and six retained positions starting at track 2; actual six attempts and one
  retained position. **This regression is red on current production code.**

The new acceptance tests require FFmpeg/libopus and fail rather than skip when
it is missing. The unchanged Docker build stage does not install that prerequisite;
a future CI/build adjustment must supply it or run a clearly required separate
integration job. No Dockerfile change was made during this audit.

The successful two-playable-track test should not be forced to fail when the
recorded failure is external provider rejection. It closes the combined local
audio guarantee; the red test captures the actual current recovery deficiency.
It remains a local sink, not Discord. Progressive matching in that test is a
controlled callback gated on first media, not real Spotify/YouTube work.

Remaining deterministic gaps: real command -> Prepare -> progressive resolver ->
real helper extraction -> voice sink in one test; three distinct tracks; global
provider classification/cooldown and queue preservation during background errors;
API early-final-page completeness; all controls on the combined process/player
harness; saturated announcement cancellation; real voice-adapter error injection;
SIGTERM at application shutdown; stronger descendant/FD checks and cancellation
at every startup step. External challenge frequency cannot be a deterministic test.

## I–J. Dedicated reliability agent decision and testing pyramid

**Justified: one Bard Reliability Agent**, because coverage currently spans useful
components but reports have overstated their combined guarantee, and at least one
test codifies the behavior that violates the provider requirement. Agent design
is documented in [bard-reliability-agent.md](bard-reliability-agent.md). No agent
configuration or automatic invocation was installed during this audit.

The durable requirement document is the oracle. The reviewer must ask: “How can
this implementation violate a Bard requirement while every current test passes?”
It may edit tests/fixtures and report risk; it must not implement product features,
weaken requirements, or claim local sink results prove Discord audibility.

Recommended layers:

1. Unit: queue positions, ranking, state transitions, cancellation decisions,
   provider classification, deterministic virtual-clock cooldown rules.
2. Process: real exec with controlled helpers for delayed output, EOF, malformed
   input, crashes, hangs, pipe-owning descendants and cancellation signals;
   FFmpeg/libopus must be installed and checked in this job, not silently skipped.
3. Player integration: two/three distinct tracks; real queue/run/Streamer, local
   provider helpers, real FFmpeg and recording sink; progressive work plus all
   controls and races. Assert separate media frames, not selection or trailers.
4. Provider contract/smoke: opt-in external Spotify and YouTube, version/network
   provenance, strict request budget; failures classified separately from player
   correctness. Existing live tests are metadata-only and gated.
5. Discord smoke: explicitly gated test guild/channel; join, play two tracks,
   natural first EOF, second successful UDP submissions and listener confirmation.
   Record image/source hashes, tool versions, stage timestamps and sanitized
   causes. No credentials or signed stream URLs in artifacts.

## K. Production correction proposal — not implemented

The initial instruction is to stop implementing fixes. There is therefore no
production before/after claim. Evidence supports a future minimal policy change:
preserve a typed provider challenge/rate-limit cause through extraction and
matching, retain the failed queue position and successors, suspend affected
background provider work, and use explicit bounded recovery. Do not merely raise
the failure threshold, lengthen timeouts, or retry each successor.

That would fix queue destruction, not guarantee YouTube availability. Request
budget/priority changes need separately tested policy; reduction in challenge
frequency requires a measured live comparison. The same red regression must
become green without changing its expectation when that work is authorized.

## L. Verification

Host: Go `go1.27.0-X:nodwarf5 linux/amd64`, FFmpeg `n9.0.1`. Baseline before
new tests: `go test ./...`, `go test -race ./...`, `go vet ./...` passed (some
baseline package results were cached).

After adding tests, fresh `go test ./... -count=1` and
`go test -race ./... -count=1` fail **only** the new R3 regression with
`attempts=6 retained=1 halted=true`. No race report was emitted. `go vet ./...`
passes. These failures are intentional retained evidence, not a fixed suite.

Repeated checks:

- Natural EOF vs skip/stop, progressive out-of-order prefix, halt/resume,
  exited-source pipe-holder cleanup: 100 runs each, pass.
- MatchLimiter and independent/all-waiter cache cancellation: 100 runs each, pass.
- New two-track acceptance plus EOF/skip/stop and progressive prefix under race:
  10 runs each, pass.
- New two-track acceptance: 100 runs (both ready/progressive cases each run),
  pass in 114.556s; six distinct media frames per track in the initial verbose run.
- Provider-challenge preservation: 100 runs, all fail the same R3 assertion
  (`attempts=6 retained=1 halted=true`); no alternate failing test or timeout.

Go cache access initially hit sandbox restrictions on some reruns; approved
escalated reruns completed. No container was rebuilt, started or deployed.

## M. Remaining external and operational risks

YouTube challenge/403 availability, request-pressure causality, exact deployed
binary parity, Discord/DAVE network behavior, and audibility remain outside the
new deterministic guarantee. Shared provider/cache/limiter state means guild
queue isolation is not external-provider isolation. Existing diagnostics redact
URLs in Streamer but metadata error paths sometimes return raw bounded stderr;
SafeDiagnostic is URL redaction, not a general token/header sanitizer. No new
diagnostics expose these fields. A green unit suite must not be used to certify
any of these external guarantees.

## Capture the latest Arch-host failure

Copy/update `scripts/capture-bard-audit.py` into the host checkout. From the same
directory where the host's `docker compose` commands normally run, start:

```sh
python3 scripts/capture-bard-audit.py > bard-audit.jsonl
```

Start this **before submitting the playlist**. In Discord submit the playlist or
multiple tracks that reproduce the issue, let track 1 finish naturally, and do
not skip/stop/pause/resume during the transition. After track 2 fails, keep the
capture running for 30 more seconds (or until the halt is visible), then press
Ctrl-C in the capture terminal. This stops log following only, not Bard.
Return **bard-audit.jsonl**; no diagnosis or raw-log redaction is needed.

The script follows service `bard`, includes the preceding minute, and exports
only allowlisted event names, numeric timings/counts, booleans, known categories
and fixed error flags. It omits raw errors, stderr, titles, queries, URLs, headers,
cookies, tokens and environment values. It performs no Discord/provider requests.
If it reports zero lifecycle records, return the file and that message. A missing
event is not proof that an older deployed build emitted that event: build/logging
parity must still be checked before interpreting absence.
