# Playback recovery investigation — 2026-09-14

## Cause established from code and production trace

The 10-second deadline was in `internal/discord/bot.go`:
`transport.WriteFrame -> context.WithTimeout(ctx, 10*time.Second) -> dave.WaitReady`.
It was not a yt-dlp resolution deadline.

The Ogg parser invokes WriteFrame only after reconstructing an audio packet.
WriteFrame's deadline error propagated through that callback as
`audio stream: context deadline exceeded`. Streamer then canceled its process
context, killing FFmpeg and yt-dlp. Thus this error path establishes that media
bytes and an Opus packet had arrived, despite the misleading final error label.

Production timestamps (UTC):
- 17:25:30.833 playback processes started.
- 17:25:33.509 extraction marker emitted.
- 17:25:43.574 playback failed after the 10-second voice readiness wait.
- 17:25:58 DAVE recovered, about 15 seconds too late for the abandoned track.

The stream pipe was connected correctly. The first-frame log previously came
after WriteFrame succeeded, hiding the already-working decoder while DAVE held
the packet. Backpressure while the send waited could also stall the producers.

Git history: the same 10-second wait and Duration+2-minute lifetime context
already exist in initial commit b4901a2. The latency commit ae8cb48 added the
stderr extraction marker and timing events, without adding a startup context.
No available commit is independently established as a known-good audible build.
Faster resolution can expose the existing readiness window sooner; this is an
inference, not proof that the latency commit introduced a pipe regression.

## Changes

- Playback subprocesses use a track-owned cancellation context, without a fixed
  duration deadline.
- Separate 30-second extraction and first-Opus watchdog phases. They end on the
  first Opus frame, before a voice readiness wait.
- DAVE readiness has its own 60-second wait, allowing the observed watchdog and
  recovery cycles; it remains interruptible by track cancellation. Exhaustion
  reports `DAVE readiness` explicitly.
- Removed the generic producer idle timer that also ran during voice waits.
- One-time timings cover process start, URL resolution, producer bytes, FFmpeg
  input pipe write, FFmpeg output, Ogg packet, Opus packet, voice attempt and
  successful Discord UDP submission. First input means bytes accepted by the
  FFmpeg input pipe; FFmpeg output independently confirms consumption.
- yt-dlp stdout remains binary-only; a bounded-memory Go copier observes its
  first write and closes the pipe after process output is drained.
- Process exits and cancellation causes are reported separately.
- Playback yt-dlp disables the shared two-second post-exit WaitDelay: a fast
  producer may finish while its Go output copier still drains into paced audio.
  Live tests exposed this instrumentation-specific drain error; a five-second
  real FFmpeg test covers complete draining.
- `cmd/profile -playback -play-for=61s` exercises paced local audio without Discord.

No search subprocess or extraction settings were added or reverted.

## DAVE dependency findings

The installed versions match the Go module proxy's latest tagged versions:
dave-go v0.5.1, mls-go v1.6.0, DisGo v0.19.6.

The installed dave-go source documents ShouldHoldFrames as a handshake gate:
frames sent before readiness would be unusable by E2EE receivers. WaitReady
survives epoch resets. Its commit watchdog initiates recovery after an
unconfirmed commit; production logs show a recovery cycle taking another 15s.
These warnings represent recoverable failures, not evidence that recovery is
instant or that every session will recover. We retain the library's recovery
implementation and do not send around its readiness gate.

References:
- https://github.com/thomas-vilte/dave-go/blob/v0.5.1/session/state.go
- https://github.com/thomas-vilte/dave-go/blob/v0.5.1/session/mls.go
- https://github.com/thomas-vilte/dave-go/blob/v0.5.1/CHANGELOG.md

## Measurements and validation

Direct URL: https://www.youtube.com/watch?v=2KSpDNlsVF4

61-second local probe, timings relative to playback extraction start:
- URL resolution: 1.935s.
- First yt-dlp byte: 1.991s.
- First FFmpeg input pipe write: 1.991s.
- First FFmpeg output / Ogg packet: 1.992s.
- First Opus packet / local voice attempt: 1.998s.
- 3,051 paced frames over 61 seconds.
- Processes canceled intentionally by the probe at about 63s total.
- Discord send and audible playback: NOT measured by the local sink.

Separate local queries:
- Metallica The Unforgiven: first Opus in 2.532s after playback start.
- The Strokes Sunday Morning: first Opus in 2.041s; search still selected
  Why Are Sundays So Depressing. Preserve this secondary ranking finding.

Regression tests cover startup timeout phases, watchdog removal once Opus is
ready, a voice wait outliving the audio watchdog, single stage events, DAVE
recovery/readiness failure/cancellation, and existing skip/stop/leave/queue
lifecycle tests. Existing ranking changes and tests were retained.

Validation commands: gofmt, go vet ./..., go test ./..., go test -race ./...,
go build ./..., docker compose build.

## Outstanding acceptance checks

The instrumented bot was rebuilt and restarted. A human listener is still
needed for direct-URL audible playback lasting at least 60 seconds, both named
queries, skip-to-next, stop, and play again. Live Discord tests subsequently recorded:
- 505: first yt-dlp bytes 2.025s, FFmpeg output 2.027s, Opus 2.039s,
  successful Discord UDP send 2.039s after playback start.
- Is This It (Home Recording): first Discord send at 17:33:57.793 UTC;
  parent cancellation at 17:34:59.777 UTC, about 62 seconds later.
- Someday started after that cancellation and sent its first Discord packet
  in 3.350s.

These establish actual sends and queue advancement, but a listener has not yet
confirmed audibility or all requested manual steps. The live query set differs
from the requested direct-URL/Metallica/Sunday Morning acceptance sequence.
