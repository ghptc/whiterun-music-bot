# CR-005 — Bard Command Expansion & Roots Help

The registry contains the eleven existing commands, exactly ten requested new command names, and `/help` (22 total). There are no other new commands. The implementation reuses the guild player, its mutex, its single playback goroutine, collection jobs, and SQLite. Spotify matching, Force Play, streaming, and fidelity verification are unchanged.

## Queue and cancellation

`/remove position:N`, `/move from:N to:N`, and `/jump position:N` use **one-based upcoming positions**, as shown by `/queue`. The current song is separate. Pending collection slots count as positions. Invalid positions leave state unchanged. Move and shuffle move whole entries, retaining the original collection job ID, resolver position, artwork, identity, and requester. Collection results find their original slots after reordering; removed slots cannot be resurrected by late results.

Shuffle permutes all upcoming entries, including pending entries. Removing or jumping past pending entries cancels their searches. Jump cancels the current stream and discards positions before the target atomically; a pending target waits for its own resolution. Playback failures retain the existing advancement behavior.

Replay clones the current resolved metadata to the front of the queue and cancels the active attempt. The existing playback goroutine completes teardown before invoking another stream. Replay resumes from the beginning even if the interrupted track was paused. Replay fails without changing playback if the queue has no room for reinsertion.

## Loop semantics

- `off`: normal advancement.
- `track`: a track that submitted audio and finished without error returns to the front.
- `queue`: that same successful track returns to the end, behind all upcoming entries.
- Skip and jump discard the interrupted track without repeating it. The chosen loop mode applies to subsequent successful tracks.
- Previous retains its existing behavior: select the previous started track and return the interrupted track immediately behind it. Loop then applies to successful natural completion.
- Clear removes upcoming entries, cancels pending jobs, and turns loop off while preserving current playback.
- Stop and leave turn loop off and retain their existing cancellation and cleanup behavior.
- Failures, cancellations, and attempts that never submitted audio are never repeated. Unavailable tracks therefore cannot create an infinite retry loop.
- Queue capacity remains 1,000 upcoming entries. If that capacity is filled concurrently at completion, no extra repeat entry is inserted.

Loop mode and autoplay preference are stored in the existing guild state JSON. Old databases need no migration. Recovery still requires explicit voice attachment and starts the recovered current track from the beginning.

## Explicit limitations

### Seek

The current pipeline pipes yt-dlp output into FFmpeg and sends paced Opus packets. It exposes neither a seekable source nor a tested offset contract through resolution, decoding, pause gates, and startup deadlines. This change does not introduce an unverified decoder restart or discard decoded audio while claiming reliable seeking.

`/seek` accepts `1:30`, `01:30`, and `HH:MM:SS`. It rejects signs, negative values, malformed components, values above 59 for non-leading components, values above 24 hours, and timestamps greater than a known duration. A valid timestamp returns an explicit unavailable message. Current playback and pause state remain unchanged. Seek is not operational in this release.

### Autoplay

The source investigation found no recommendation provider integrated into this repository. Spotify's [official Web API change notice](https://developer.spotify.com/blog/2024-11-27-changes-to-the-web-api) restricts Recommendations access for affected applications. Existing Spotify metadata credentials and YouTube song search do not establish access to a reliable recommendations endpoint. Random search results would not meet the requested compatibility and identity requirements.

`/autoplay enabled:true` persists guild intent but explicitly reports that recommendations are unavailable. False disables the preference. Empty queues remain empty, including after restart; no random songs or invented matches are inserted. There is no provider request, background retry, or fallback on provider failure. A working recommendation integration remains outstanding and requires an accessible provider and tests for seeded recommendations, compatibility, fidelity resolution, stale-result cancellation, and network failures. Saving this preference does **not** make autoplay operational.

### Lyrics

No licensed lyrics provider or display authorization is configured. [Musixmatch's official SDK](https://github.com/musixmatch/musixmatch-sdk) documents an API for lyrics, but API availability alone does not grant this application display rights. A future integration requires provider access and review of the applicable attribution, territorial, and display conditions.

`/lyrics` reports either no current track or unavailable licensed lyrics. It does not scrape sites, request external lyrics, or display copyrighted text. Actual lyrics retrieval remains unavailable.

## Permissions and help

New controls use the existing same-voice-channel gate. Any listener in the Bard's channel can control playback; no requester ownership or administrator requirement is added. History, lyrics, and help require no voice connection. Existing command permissions are preserved.

Help returns exactly one static ephemeral embed, with PLAYBACK, QUEUE, EXTRAS, and TAVERN fields. It has no interactive components, attachments, or pagination. It is handled before guild player creation and does not consult SQLite, a resolver, Spotify, YouTube, or FFmpeg. Short descriptions explicitly mark unavailable capabilities.

History queries the append-only SQLite playback events table by guild. It shows newest first, defaults to 10, allows 1–20, bounds Unicode output for Discord, and never mutates the player or its navigation history.

## Verification

New tests cover a reference model for queue edits, invalid and empty positions, snapshot mutation, metadata retention, pending resolution after move/remove, concurrent operations, jump cancellation, replay, loop completion/skip/failure, timestamp validation, unchanged paused playback, guild persistence across database reopen, empty autoplay, guild history isolation, handlers with SQLite, unavailable providers, permissions, and help completeness/Discord limits.

Run:

```sh
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
docker compose build
python3 scripts/check_queue_mutations.py
```

The mutation script builds disposable source copies and checks that queue tests reject four intentional defects: wrong removal position, lost moved entry, duplicated shuffled entry, and an off-by-one jump. It does not modify the working tree. This is a targeted mutation check, not an exhaustive mutation score.

All five commands passed locally on 2026-10-19. These are offline checks, including fake voice transports and actual SQLite. Discord E2E, audible playback, and live command publication were **not** validated. Autoplay recommendation-provider failures cannot be tested against an implementation because no provider is integrated; tests assert the explicit unavailable behavior instead.
