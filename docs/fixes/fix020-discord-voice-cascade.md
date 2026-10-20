# FIX-020 — Discord Voice failure cascade

## Incident and cause

The persisted log database from the local `whiterun-music-bot_bard-data`
volume was copied into `/tmp` using SQLite backup with the original volume
mounted read-only. The bot was not started. The sanitized event extract is in
[evidence/fix020/incident.jsonl](evidence/fix020/incident.jsonl); original log
row IDs are retained for correlation.

All times below are UTC on 2026-10-20 (subtract three hours for São Paulo).

| Time | Original row | Observation |
| --- | --- | --- |
| 18:09:46.153 | 3349 | First Voice frame attempted for the active song; playback subsequently continued for about 41 seconds. |
| 18:10:27.666 | 3367 | First recorded abnormal Voice transition: WebSocket close **1006**, `unexpected EOF`. |
| 18:10:27.762 | 3368 | Library reconnection failed with **4006**, `Session is no longer valid`. |
| 18:10:27.776 | 3371 | UDP write deadline failed because the network connection had closed. Both producers were killed as cleanup consequences. |
| 18:10:27.777 | 3376 | Scheduler incorrectly marked the active recording unavailable and advanced. |
| 18:10:27.801 | 3389 | Health monitor reported main Gateway ready, cached membership in the same channel, Voice Gateway unavailable. Its grace period did not suspend playback. |
| 18:10:29.762 | 3408 | The following recording produced audio but failed at `Speaking`: `shard is not ready`. |
| 18:10:29.763 | 3413 | That recording was also marked unavailable and the scheduler advanced again. |

The database contains **27** `track_unavailable` events caused by Voice:
one `voice_send`, followed by **26** `voice_speaking` failures. Source resolution
and decoding were functioning. The first `shard is not ready` is a consequence,
not the initiating failure.

The initial EOF's physical cause cannot be established from these logs.
There is no preceding recorded heartbeat failure, main Gateway outage,
authoritative kick/move, or DAVE negotiation failure. Debug-level Voice State,
Voice Server, heartbeat payloads and MLS/UDP packet captures were not persisted;
claiming an earlier DAVE or network root cause would exceed the evidence.
What is established is the EOF → invalid-session response → closed UDP chain.

Code inspection of disgo v0.19.6 explains the cascade: `SetSpeaking` calls Voice
Gateway `Send`, which returns Discord's `ErrShardNotReady` outside `StatusReady`.
The stock gateway tries Resume using retained session data; after failure the
connection closes. Its heartbeat paths can also start reconnection with
`context.Background()`, independently of a guild's playback cancellation.
The old scheduler classified every stream error as a failed recording. The
health monitor neither canceled audio nor froze advancement during its 60-second
grace, and eventually cleared playback. Valid Spotify mappings could also be
invalidated by the transport error.

## Correction

* All `voice_speaking`, `voice_send`, and `voice_transport` errors, including
  nested/joined cleanup errors and health-monitor cancellation causes, are
  infrastructure failures. They never emit `track_unavailable`, increment the
  media failure count, or invalidate Spotify mappings. Genuine matching,
  unavailable-media, provider, decoder, and source errors retain their behavior.
* The guild circuit suspends scheduling and cancels active audio. The existing
  streamer tears down and reaps its processes before recovery starts. The final
  verified current recording is restored at the **front of the queue**, with its
  Spotify identity, YouTube locator, metadata and original playlist position.
  Upcoming entries retain their order. `State.VoiceSuspended` and playback state
  `SUSPENDED` expose the condition; there is no active streaming process while
  suspended. This also persists the retained recording with the queue.
* The sole scheduler owns recovery. There are at most **five attempts per
  incident**, separated by 1, 2, 4, 8, and 16 seconds plus 0–25% jitter. Each
  negotiation has a 15-second context deadline; failed-candidate cleanup has
  its own three-second ceiling. Enqueue, repeated health reports, and skip
  cannot reset this budget. A false recovery that fails before its first frame
  also cannot reset it.
* Recovery closes the old connection and asks the manager for a **new** one:
  Gateway, UDP and DAVE session are recreated. Readiness requires successful
  connection opening (UDP discovery and SessionDescription), Voice Gateway
  `StatusReady`, DAVE readiness, and a successful outbound Speaking control
  message. Normal streaming performs the actual first audio send.
* A small incident-owned Gateway replaces only the library's Voice Gateway
  reconnection path. It preserves Identify, heartbeat, UDP negotiation event
  delivery and DAVE opcode dispatch, but performs **no Resume or autonomous
  reconnect**. WebSocket loss, invalid ACK, or missed heartbeat terminates that
  session. Heartbeat state is synchronized and writes have deadlines. The guild
  circuit is the only recovery owner, so background reconnect loops cannot
  revive an invalid session or compete with a replacement.
* Stop/leave/shutdown cancel recovery and invalidate its epoch. A late success
  cannot restore playback. Skip during suspension explicitly removes the
  retained head; the next remaining recording waits for recovery. Orchestration
  lock acquisition remains cancellation-aware to avoid shutdown deadlocks.
* At most one interruption notification is emitted per incident. The first
  successfully transmitted frame produces the normal Now Playing update and
  closes the incident; there is no additional recovery announcement per track.

Recovery restarts the preserved song **from its beginning**; the current audio
pipeline does not support seeking. After five failed attempts, the queue remains
suspended indefinitely rather than consuming tracks. An explicit `/resume`
grants another bounded recovery budget without repeating the incident notice.
`/stop` clears it and prevents automatic playback; `/leave` explicitly clears
playback and detaches. No production Discord session was opened for validation.

## Reproduction and validation

Deterministic virtual-time tests cover Voice loss during playback, before the
first frame, during transition, recovery after availability returns at five
seconds, persistent failure, stop during negotiation, skip while suspended,
independent guilds, and a 300-recording queue. They assert identity/order
preservation, a single notice, no additional streaming while offline, bounded
attempts, and cancellation without resurrection. Another test deliberately
returns false readiness repeatedly and verifies that budget and notice remain
bounded.

Local TLS WebSocket fixtures exercise Identify, close 4006, missing heartbeat
ACK, explicit close, single close notification, and absence of Resume/background
reconnect. Adapter tests verify fresh connection replacement, cancellation while
waiting for orchestration, rejection of an unready Gateway, and distinct actual
DAVE session objects. Real local FFmpeg tests inject Speaking/send failures and
verify both yt-dlp fixture and FFmpeg PIDs have been reaped. Existing Spotify
Fidelity and legitimate media failure regression tests run in the full suite.

Commands and evidence:

```sh
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go test -race ./internal/player ./internal/discord -run 'TestVoice|TestIncidentGateway' -count=1 -v
docker build -t whiterun-music-bot:fix020 .
```

See [full suite](evidence/fix020/test.txt), [race detector](evidence/fix020/race.txt),
[vet](evidence/fix020/vet.txt), [targeted tests](evidence/fix020/targeted.txt), and
[Docker build](evidence/fix020/docker-build.txt). The first race pass found test
fixture counters shared across guilds/time advances; those counters were made
atomic before the final validation. No application race was reported in that
pass. The Dockerfile also runs vet, the full suite and the static binary build.

No deploy, service restart, or write to the original production volume was
performed.
