# Bard behavioral requirements

These invariants define expected behavior, independently of the implementation.
Added during the 2026-09-27 audit. They are acceptance criteria, not a claim of
external provider availability. R3/R4 now have deterministic local coverage for
a classified challenge, queue retention, background-resolution suspension, and
explicit same-position retry. R2 remains a separate capacity and request-budget
concern.

## Playback and transport

- **P1 — Start:** the first playable logical position starts without waiting for
  unrelated collection positions. Success requires media frames at the voice sink.
- **P2 — Natural completion:** after valid media and natural EOF, advance exactly
  once without another user command. Two and three independently playable tracks
  must deliver their own media frames, in order.
- **P3 — Ownership:** a successor owns a fresh playback lifetime. Finishing or
  canceling metadata work or its predecessor cannot cancel that successor.
- **P4 — Completion:** distinguish selection, extraction, decoder start, Opus
  production, successful transport submission, and audible reception. No stage
  may stand in as proof of a later stage. Silence trailers alone are not media.
- **P5 — Controls:** pause preserves the current stream and position; resume
  continues it. Skip advances the requested logical positions once, after old
  playback teardown. Stop cancels playback and clears upcoming work. Clear removes
  upcoming work and preserves current playback. Leave detaches voice and cleans
  playback and outstanding collection work; stale results cannot resurrect it.
- **V1 — Voice:** successful local playback requires actual media frame handoff;
  successful Discord submission requires a successful transport write. Audible
  reception requires a Discord smoke check. DAVE readiness failure must remain
  distinguishable from provider/decoder failure.

## Queue

- **Q1 — Order:** preserve collection order and duplicates. Pending positions
  occupy logical queue positions and capacity. Out-of-order resolution cannot
  allow later positions to overtake an unresolved earlier one.
- **Q2 — Mutations:** SkipN counts current plus upcoming logical positions,
  including pending ones. Canceled or discarded positions cannot reappear.
  Snapshots are consistent copies. Rejected batches do not partially mutate state.
- **Q3 — Concurrent play:** queue reservation/insertion is the ordering point for
  concurrent accepted commands. Whole collections remain contiguous. This does
  not promise gateway-arrival order before metadata completes.
- **Q4 — Failure accounting:** individual unavailable tracks may be skipped with
  explicit accounting. Provider-wide unavailability must not be interpreted as a
  run of individually bad tracks or silently consume positions.

## Providers and recovery

- **R1 — Metadata:** validate complete Spotify metadata before reserving its
  positions. Reject incomplete, mismatched or non-advancing collections atomically.
  Preserve source identity and recording/version intent.
- **R2 — Bounded work:** bound provider concurrency, request budgets and timeouts.
  Background matching must not exhaust capacity needed for playback. Capacity
  bounds alone are not rate limits or proof of provider availability.
- **R3 — Provider failure:** anti-bot challenges and rate limits must be
  distinguished from individual missing media. Suspend affected provider work
  without consuming the failed logical position or its successors. Do not retry
  the same provider across the rest of the queue as if each track were bad.
- **R4 — Recovery:** recovery must follow explicit, bounded policy with observable
  state and deterministic tests. Cooldown expiry, retry scope and manual resume
  must not create an uncontrolled request burst. Exact cooldown duration is a
  policy decision, not defined by these invariants.

## Concurrency and resources

- **C1 — Isolation:** guild controls and cancellation cannot mutate another
  guild's queue or voice session. Shared provider services still require explicit
  ownership and global resource limits.
- **C2 — Locks:** no process execution or wait under the critical player-state
  mutex. State remains inspectable during extraction and teardown.
- **C3 — Lifetime:** every worker, subprocess, pipe, watchdog and callback has an
  owner and a terminal path. Cancellation releases paused gates and limiter
  waiters. Process descendants cannot keep playback stuck after cancellation.
- **C4 — Concurrency validation:** race tests and bounded repeated lifecycle tests
  must pass. Absence of a race report does not prove behavior or absence of leaks.
- **C5 — Diagnostics:** retain stage/cause/exit evidence without credentials,
  cookies, tokens, private headers or signed media URLs.

## Evidence rules

Tests derive expectations from these invariants, not current counters, thresholds,
or return values. State exactly which boundaries are real and which are fixtures.
Keep deterministic player correctness separate from external provider availability.
Do not skip or weaken a failing invariant to report a green suite. Document an
unimplemented invariant and its failing test explicitly until a reviewed change
satisfies it.
