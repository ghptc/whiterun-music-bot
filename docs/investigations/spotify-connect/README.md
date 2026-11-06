# BARD-SPOTIFY-001: native Spotify Connect

Status: experimental, opt-in; live audio acceptance incomplete. No production changes.

## Authorization and delivery status

On 2026-11-03 the operator explicitly confirmed that authorization had been
verified and approved for broadcasting Spotify audio to other Discord users,
and instructed implementation of the full integration. This records the
operator's attestation; no authorization document or credentials are stored here.
The runtime path is available only when `BARD_SPOTIFY_CONNECT_ENDPOINT` is set.
The example environment leaves it disabled. This branch must not be treated as
production-ready until the remaining live tests below pass.

Work started in `/tmp/bard-spotify-001`, branch `feat/bard-spotify-001`, from
latest `origin/main` (`aa861cdeea156247f4941fb8685a1e603aaab38f`). Existing changes
in the original checkout were preserved. EC2, COD4, production SQLite, images,
branch protection and release workflow configuration were not changed.

## Investigation and decision

| Existing component | Integration point |
| --- | --- |
| `internal/media/spotify.go`, public/anonymous Spotify providers | Metadata, track IDs, duration, ordered album/playlist expansion |
| `internal/media/progressive.go`, `collection.go` | Native mode uses complete Spotify metadata and returns ready entries without YouTube matching; duplicates and source positions remain intact |
| `internal/media/resolver.go` | Persistent Track descriptor; `RequestedSource=spotify_connect`, `URL=spotify:track:<id>`, `SpotifyID`; original relink identity retained separately |
| `internal/discord/bot.go` | Native descriptors enter Streamer directly; restored legacy Spotify descriptors are converted without a YouTube request |
| `internal/player/stream.go`, `ogg.go` | Existing FFmpeg Opus encoder, packet parser, pacing, Voice callbacks, watchdogs and process-group cleanup |
| `internal/player/pause.go` | Event-driven source Pause/Resume and acknowledged delivery gate |
| `internal/player/player.go`, `durable.go` | Sole playback worker, exactly one completion return, confirmed outbound progress, retained failed native entries |
| `internal/storage/sqlite.go` | Existing JSON queue/checkpoint persistence; no schema or database change |
| `internal/discord/voice_join.go`, `voice_recovery.go`, `voice_gateway.go` | Existing Discord encryption, frame sending and bounded transport recovery |
| `internal/media/extraction_error.go`, `provider_admission.go` | Native Spotify errors bypass YouTube classification/admission; YouTube request protections remain in force |

Decision: control a separately operated, dedicated go-librespot process over its
loopback REST API and WebSocket event stream. Do not link its GPL executable
into Bard, rewrite the Discord player, or introduce another queue. The sidecar's
source and license remain upstream; local setup builds that separate executable.
No new Go module dependency was added.

The raw PCM producer replaces yt-dlp in the existing stream pipeline. A small
FFmpeg producer stream-copies PCM into Matroska, giving the existing decoder a
track-scoped input. This extra process avoids changing the established Opus and
Voice implementation; it does not perform another lossy encode. Both FFmpeg
process groups remain owned and reaped by the existing stream supervisor.
Bard does not own or automatically restart the independent daemon.

Rust librespot was considered as the alternative. It would require a separate
control bridge instead of go-librespot's existing REST/event contract. Similar
audio-key refusals have also been reported upstream there; switching is not an
evidenced resolution of the observed denial.

## Verified upstream contract

The local executable is built from published non-prerelease **v0.10.5**, commit
`90a4fdaf0314269e3fa25698a5f4270c026d2c03`, released 2026-10-16. GitHub release
metadata and the exact tag commit were checked. This is source/release provenance
verification, not a claim of an independently verified signed release artifact.
The initially inspected master commit was
`522adcae8d65974352ed8c3f9c8676c33642e6aa`; the relevant API documentation, pipe
implementation, player implementation and credential store were compared with
the selected tag. The local source checkout is clean. The setup script checks
both the exact commit and clean source before building, and writes a local binary
SHA-256 manifest.

Pinned source references:

- [README and supported device authorization](https://github.com/devgianlu/go-librespot/blob/90a4fdaf0314269e3fa25698a5f4270c026d2c03/README.md)
- [OpenAPI paths and payloads](https://github.com/devgianlu/go-librespot/blob/90a4fdaf0314269e3fa25698a5f4270c026d2c03/api-spec.yml)
- [REST dispatch and WebSocket events](https://github.com/devgianlu/go-librespot/blob/90a4fdaf0314269e3fa25698a5f4270c026d2c03/daemon/api_server.go)
- [Playback events and controls](https://github.com/devgianlu/go-librespot/blob/90a4fdaf0314269e3fa25698a5f4270c026d2c03/daemon/controls.go)
- [Pipe open lifecycle](https://github.com/devgianlu/go-librespot/blob/90a4fdaf0314269e3fa25698a5f4270c026d2c03/output/driver-pipe-unix.go)
- [Sample conversion, buffering, pause and EOF](https://github.com/devgianlu/go-librespot/blob/90a4fdaf0314269e3fa25698a5f4270c026d2c03/output/driver-pipe.go)
- [Sample rate/channel constants and decoder checks](https://github.com/devgianlu/go-librespot/blob/90a4fdaf0314269e3fa25698a5f4270c026d2c03/player/player.go)
- [Authentication and session ownership](https://github.com/devgianlu/go-librespot/blob/90a4fdaf0314269e3fa25698a5f4270c026d2c03/daemon/app.go)
- [Atomic credential persistence](https://github.com/devgianlu/go-librespot/blob/90a4fdaf0314269e3fa25698a5f4270c026d2c03/cmd/daemon/file_state_store.go)
- [Configuration schema](https://github.com/devgianlu/go-librespot/blob/90a4fdaf0314269e3fa25698a5f4270c026d2c03/config_schema.json)
- [Published release](https://github.com/devgianlu/go-librespot/releases/tag/v0.10.5)
- [Alternative client](https://github.com/librespot-org/librespot)

The adapter uses `POST /player/play` with `uri`, `position` (milliseconds) and
`paused`; `POST /player/pause`, `/player/resume`, `/player/stop`; `GET /status`;
`GET /events` (WebSocket). It explicitly disables daemon repeat/shuffle through
`/player/repeat_track`, `/player/repeat_context`, `/player/shuffle_context`.
There is no invented completion or authentication-expired WebSocket event.
Upstream `not_playing` is the natural completion signal; `inactive` indicates
lost ownership; `playback_error` includes stage/kind/unplayable evidence.
HTTP 204 means **no session**, including on controls; it is never success.
A `session` failure alone is not proof of expired credentials.

## Identity, queue and concurrency policy

Only exact track URIs with a 22-character alphanumeric ID are accepted by the
adapter. Album/playlist expansion stays in Bard. Returned relink identities are
retained and rejected if they differ; a single-track response must match the
requested ID. Player `will_play`, `metadata`, `playing` and `not_playing` URIs
must match. No YouTube locator or recording substitution is used in this path.
Unavailable metadata entries retain existing explicit skipped-track reporting.

One account/device has one active track session across all guilds. A mutex
protects the process-wide lease. Occupied requests receive a clear response;
if simultaneous requests race admission, only one stream can start and the
other guild retains its entry in a halted queue. It requires explicit `/resume`.
The lease is released after teardown; it does not reserve an idle device for a
guild. Guild queues remain isolated, and intentionally queued duplicates remain
separate occurrences. Do not run multiple Bard processes against this daemon.

Bard resets daemon queue options and configures autoplay/crossfade off. Natural
completion requires both a matching event and drained audio. Duplicate events
cannot create extra workers or call the guild completion transition twice.
Unexpected EOF, session loss, identity mismatch, process failure and rate limits
retain the native entry and confirmed outbound progress, halt the guild queue,
and require explicit recovery. No native media failure triggers automatic replay.
Existing bounded Discord transport reconnection remains owned by GuildPlayer.

## Audio report and lifecycle incompatibilities

- Selected pipe format: interleaved signed 16-bit little-endian PCM (`s16le`),
  44,100 samples/second, two channels, four bytes per stereo sample frame.
  This follows actual player constants and pipe conversion code.
- Upstream also offers `s32le` and `f32le`. The selected format is explicit,
  rather than inferred from source codec or guessed from a FIFO extension.
- Upstream reads 4096 float samples at a time. PCM passes through bounded kernel
  FIFOs/pipes and FFmpeg buffers; no application audio archive is created.
- The pipe backend pauses on decoder EOF and keeps the FIFO open. Transport EOF
  therefore cannot identify track completion. Bard waits for `not_playing`, then
  drains through a 100 ms quiet-read deadline. Each read renews that deadline so
  a paused downstream pipeline cannot expire it before buffered tail samples
  are read. A duration-plus-five-second byte ceiling guards runaway output.
- Pause holds the upstream output lock; a blocking FIFO write can delay Pause.
  Bard continues draining until the source acknowledges Pause, then gates Voice
  delivery. Resume likewise releases delivery after source acknowledgement.
  This prevents the circular wait caused by pausing downstream first. The actual
  live pause latency remains to be measured.
- Cancellation closes the pollable FIFO reader before waiting for producer IO,
  kills the existing FFmpeg process groups, and joins adapter event/control
  workers. Stop is bounded. Failed Stop poisons the lease until operator recovery
  rather than allowing an overlapping stream with unknown device state.
- WebSocket ping/pong liveness is bounded; there is no status-polling playback
  loop and no automatic reconnect/replay. A lost event stream fails closed.
- Setup requests preferred bitrate 320 kbps, disables FLAC, disables crossfade
  and caching, uses initial volume 100 and normalization with zero pregain.
  This is a configuration preference, **not evidence of a delivered 320 kbps
  stream**. Live load failed before usable source metadata/audio was available.
- The existing encoder performs the required 44.1-to-48 kHz conversion and
  encodes stereo Opus at its unchanged 96 kbps setting, 20 ms frames. Packet
  pacing and confirmed-progress accounting remain unchanged. This is lossy
  playback; no lossless claim is made. PCM remuxing adds no lossy conversion.
- Full-track clipping/underrun measurements, live startup latency, and active
  playback CPU/memory consumption remain unverified because of the audio-key
  denial. Passing synthetic silence through the pipeline proves mechanics,
  not musical quality or audible commercial playback.

## Local Arch Linux setup and operation

The existing machine already supplied Go and `pkg-config` libraries `ogg`,
`vorbis`, `flac`, `libmpg123`, and `alsa`; no system package was installed.
On a fresh Arch environment the corresponding development/runtime libraries,
Git, Go, FFmpeg and a working local audio device are prerequisites. Setup fails
if libraries are missing; it never invokes a package manager or installs a service.

From the feature checkout:

```sh
scripts/spotify-connect/setup-local.sh
scripts/spotify-connect/run-local.sh
```

Default runtime: `$PWD/.local/spotify-connect` (ignored by Git). Override with an
absolute `BARD_SPOTIFY_LOCAL_ROOT` outside production data. Directories are 0700,
FIFO/config/credential files are private, audio caching is disabled, discovery is
disabled, and REST binds exclusively to `127.0.0.1:3678`. No Docker or service
configuration is changed. The daemon is a foreground process; Ctrl-C stops it.
Alternatively, `scripts/spotify-connect/stop-local.sh` verifies the recorded PID's
executable and config arguments before sending SIGTERM. It never uses `pkill`.

Device pairing is completed only in the operator's local terminal/browser:

```sh
curl -fsS http://127.0.0.1:3678/auth/code
```

Visit the returned Spotify URL and enter the code. Do not paste this response,
state.json, tokens or authorization headers into tickets/chat/log evidence.
The daemon's local terminal can also display pairing instructions. Avoid debug
or trace logging. Bard never reads the stored credential file or requests the
API's token endpoint during normal operation. The unauthenticated local API can
control the account and expose a token, so it must never be publicly bound,
forwarded, proxied or exposed to untrusted local users.

For health, `GET /` reports bootstrap readiness; `GET /status` returns HTTP 200
for an authenticated session and HTTP 204 with no active session. Do not publish
raw status responses, which include account identifiers. The private `state.json`
is atomically replaced from a 0600 temporary file by upstream.

For expired/revoked credentials: stop this local daemon, move `state.json` aside
within its private directory, restart and repeat pairing. Do not add passwords,
cookies, credential fixtures, or automatic authentication bypasses. If Bard's
lease was poisoned by failed cleanup, stop/recover the daemon and restart the
local Bard before explicitly resuming the retained queue.

Run the independent local sound test (no Discord and no saved audio):

```sh
go run ./cmd/spotify-connect-check \
  -pipe "$PWD/.local/spotify-connect/run/pcm" \
  -uri spotify:track:0DiWol3AO6WpXZgp0goxAV
```

The command validates identity, pauses/resumes the actual source, checks paused
position, plays through local `ffplay`, and requires the matching completion
event. It prints only sanitized evidence. Do not run it while Bard owns the device.

For local Bard testing, set these alongside locally provisioned Discord settings:

```sh
export BARD_SPOTIFY_CONNECT_ENDPOINT=http://127.0.0.1:3678
export BARD_SPOTIFY_CONNECT_PIPE="$PWD/.local/spotify-connect/run/pcm"
```

Use a separate local SQLite path and log database. Spotify metadata credentials
remain the existing separately configured metadata mechanism; Connect pairing
credentials are not copied into those environment variables. With native mode
enabled, startup requires FFmpeg, without requiring yt-dlp or deno. Explicit
YouTube requests still use their original protected provider path.

## Local proof-of-concept evidence

Observed on 2026-11-03, with no EC2 access:

| Check | Result |
| --- | --- |
| Published release/tag and clean exact source | Verified v0.10.5 / 90a4fda |
| Build with local codec dependencies | Passed |
| Loopback-only listener | Verified 127.0.0.1:3678 |
| Credentials absent before pairing | HTTP 204 status, pending device code |
| Operator interactive pairing | Operator reported paired; status became HTTP 200; auth/code became HTTP 204 |
| Credential/FIFO permissions | state.json 0600, config directory 0700, FIFO 0600 |
| Exact track requested | spotify:track:0DiWol3AO6WpXZgp0goxAV |
| Independent local audio | Failed before audio: playback_error / load / restricted / unplayable=true; audio-key code 1 |
| Premium verification via profile API | Inconclusive: HTTP 429; not retried |
| Full song, local pause/resume, transition/completion | Not passed; blocked by load rejection |
| Live expiry/restart/recovery | Not claimed; deterministic failure tests cover adapter handling |
| Discord listening confirmation | Not performed; final acceptance outstanding |

No alternate recording was selected and no commercial audio was saved.
The load failure was reproduced once for sanitized diagnosis; there is no
background retry. Authentication success is not proof of a playable Premium
session. The operator was asked to confirm Premium and official-app playback of
this exact URI. The error code alone does not establish account entitlement,
regional restriction, protocol compatibility or DRM as its cause.

Related first-party issue reports are useful diagnostic context, not proof of
this session's cause: [go-librespot #235](https://github.com/devgianlu/go-librespot/issues/235)
and [librespot #1649](https://github.com/librespot-org/librespot/issues/1649).
Do not bypass the denial or use another recording to claim success.

## Validation and remaining acceptance

Default automated tests use only fake loopback REST/WebSocket servers and
synthetic PCM. Evidence summaries are in `evidence/`. They cover URI rejection,
reported identity mismatch, relinking, ordering/duplicates, no YouTube execution,
HTTP no-session/authentication and rejected/rate-limited failures, timeout,
event disconnect/takeover, one active session, pause during startup, command
coalescing, cancellation, FIFO security, natural completion, queue retention,
confirmed progress, source failure and child-process reaping. Existing suites
cover skip/stop/Voice recovery, persistence, race and performance contracts.
This does not replace the outstanding native live Voice acceptance scenarios.

Required next acceptance sequence after the local denial is resolved:

1. Independently play the entire exact URI locally; measure actual format,
   startup, pause/resume, tail drain, CPU and memory. Verify device visibility
   in the official app, explicit stop, transition, expiry and process recovery.
2. Start local Bard with separate local data and an operator-selected test guild.
3. Use `/play` on a Spotify URL and confirm the entire exact song is heard in
   Discord. Record the operator's listening confirmation explicitly.
4. Validate `/pause`, `/resume`, `/skip`, `/stop`, `/queue`, duplicates, ordered
   album/playlist playback, Voice reconnect, process failure, unexpected EOF and
   retained confirmed progress on that live path.
5. Wait for mandatory PR CI, review the remaining limitations and merge only
   through the existing protected-main workflow. No deployment is authorized
   by this report; no production image was manually published.
