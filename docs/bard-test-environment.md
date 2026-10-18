# Docker test environment correction — 2026-09-27

This follow-up separates an environment prerequisite failure from a behavioral
regression. It does not modify the player or either acceptance test.

## Environment

The Dockerfile now defines `test-env` from `golang:1.27-bookworm`. It installs
FFmpeg using `--no-install-recommends`, removes apt package lists and proves
libopus works by encoding a short synthetic audio source. It then downloads Go
modules and copies the source. `build` inherits that exact environment and retains
the existing mandatory `go vet ./... && go test ./...` gate before compilation.

The runtime still starts independently from `debian:bookworm-slim`, with its own
existing runtime dependencies. Only `/out/bard` crosses from `build`; no compiler,
test tooling or build-stage package tree is copied into the final image.

Build the test target without starting Bard:

```sh
docker build --target test-env -t bard-test-env:audit .
docker run --rm --network none bard-test-env:audit go test ./internal/player -run TestTwoTracksNaturalEOFDeliverIndependentAudio -v -count=1
docker run --rm --network none bard-test-env:audit go test ./internal/player -run TestProviderChallengeDoesNotConsumeQueue -v -count=1
```

The target was built successfully as image `d65590e3e1fc`. FFmpeg
`5.1.9-0+deb12u1` reported libopus enabled and completed the encode probe.
Acceptance tests were executed first, in this image without network access or
Discord credentials. The two commands ran sequentially in one disposable
container to reuse its compilation cache, and their separate exit codes were
recorded. No service was started or restarted.

## Acceptance results and interpretation

| Test | Environment reached? | Behavioral result |
|---|---|---|
| TwoTracksNaturalEOFDeliverIndependentAudio/progressive=false | Yes | PASS: six distinct media frames per track, both natural completions |
| TwoTracksNaturalEOFDeliverIndependentAudio/progressive=true | Yes | PASS: same guarantees with controlled progressive matching |
| ProviderChallengeDoesNotConsumeQueue | Yes | FAIL: `attempts=6 retained=1 halted=true`; expected failed position and all successors retained |

**The two-playable-track test does not become red for a behavioral reason.**
Its real boundaries are GuildPlayer/run, queue mutation, fresh track contexts,
Streamer, os/exec process groups, pipes, FFmpeg/libopus, Ogg parsing, pacing,
natural EOF/teardown and actual frame handoff to a recording sink. The second
source helper checks a first-pipeline-completion sentinel. Frames are distinct
tones, and ending silence trailers are excluded from media evidence.

Its substituted boundaries are Spotify metadata/YouTube matching, yt-dlp network
extraction (a controlled local media helper), and Discord voice transport. It
does not run the command handler/voice join, Bot.play's lazy YouTube playlist
verification, real provider client behavior or pressure, real DAVE epochs/crypto,
UDP or a remote listener. It covers two short streams, not full-song network
lifetime. The progressive case uses a callback gated on the first media frame,
not the real global limiter and provider matching workers.

It closes the combined **local successor audio** gap. It does not close the
external-provider/Discord gap and does not prove the latest real Bard is correct.

The provider test now reaches its actual R3 assertion. It proves a current
deterministic queue-preservation defect under a controlled anti-bot challenge:
after track 1 finishes, the player consumes five failed positions before halting.
This is neither a missing-FFmpeg error nor proof of the user's latest runtime
cause. No production correction was made to turn it green, and no test is skipped.

## Why earlier tests were green

The [complete mapping](bard-test-map.md) remains applicable:

- Most queue/progressive tests substitute PlayFunc and assert selected track
  titles, using channels/nil returns as completion. They do not prove media.
- The process soak executes real children but uses a synthetic one-byte packet
  and a `cat` decoder. Its fake voice accepts those bytes, and isolated source
  failures are expected to consume positions.
- Existing real-FFmpeg tests exercise single streams and could skip when FFmpeg
  was absent in the build stage. Runtime-stage FFmpeg never satisfied that earlier
  stage's prerequisite.
- Metadata fixtures do not establish playback availability. DAVE readiness
  channels and local sinks do not establish encryption/transport success.
- The five-failure halt test encodes the current counter policy, not the provider
  unavailability requirement. The new red regression challenges that assumption.

## Verification

`gofmt -w cmd internal` completed without changing any Go file (fingerprints
checked). Host `go vet ./...`, `go build ./...` and `git diff --check` passed.
Host `go test ./...` and `go test -race ./...` failed only R3; no race report was
emitted. Docker-wide verification and the final Compose build are recorded below.

## Runtime evidence still required

The latest Arch-host failure remains unclassified. Historical September 24
challenge logs establish only a historical failure, not this latest reproduction
or deployed-source parity. Capture the real first-track EOF through successor
extraction/decoder/Opus/voice events using:

```sh
python3 scripts/capture-bard-audit.py > bard-audit.jsonl
```

Run from the host Compose checkout before submitting the reproducing playlist.
Let track 1 finish naturally, avoid control commands, wait 30 seconds after track
2 fails, then Ctrl-C the capture and return `bard-audit.jsonl`. Do not restart the
bot just to collect this. The file contains allowlisted lifecycle data and fixed
error flags, not raw credentials, headers or signed URLs. See the
[capture instructions](bard-reliability-audit.md#capture-the-latest-arch-host-failure).

## Reliability reviewer

One [Bard Reliability Agent](bard-reliability-agent.md) remains justified. This
environment failure adds a concrete responsibility: verify that required test
boundaries execute in the shipping build environment, and distinguish missing
prerequisites, behavioral failures and external-runtime evidence. It must not
weaken the tests or label local-sink success as a Discord fix. The agent remains
a documented design, not installed automation; its oracle remains the durable
[requirements](bard-requirements.md).
