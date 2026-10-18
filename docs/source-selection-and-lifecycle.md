# Source intent and playback lifecycle

## Evidence and scope (2026-09-24)

The existing running container was inspected read-only and was **not restarted**.
Its logs contain two mid-track cancellations followed by `voice disconnected;
cleared playback` (2026-09-23 23:29:34 UTC and 2026-09-24 03:22:44 UTC).
The old watcher called `leave` for *any* non-ready main/voice gateway or absent
cache entry. That destroys the entire queue even during a transient reconnect.
The old log does not identify which predicate fired, so it cannot establish
whether those particular disconnections were transient, kicks, or transport loss.
The new watcher gives uncertain state 60 seconds to recover, logs the predicates,
and immediately honors a confirmed move/removal on a ready main gateway.
Auto-leave's independent two-minute empty-channel policy is retained.

A separate, concrete stuck-Current failure was reproduced before modification:
`TestSourceSurvivesAudioEOFCleanup` supplied a decoder that emitted valid Opus
and exited, with a source that remained alive. The old streamer blocked on
`<-ytDone` after the startup watchdog had retired. The test reached its parent
500 ms deadline instead of advancing. The corrected test uses a 30 ms cleanup
deadline and returns `source_after_eof` without parent cancellation.

A second reproducer, `TestExitedSourceLeaderCannotLeakPipeHoldingHelper`,
confirmed cancellation could also hang after the source **leader had exited**:
a helper retained its stdout, Go had retired the command's context watcher,
and `WaitDelay=0` left the copier blocked. The pre-fix test required explicitly
killing the helper at 400 ms. The pipeline now owns a separate cancellation
callback through the end of process I/O; it kills unfinished process groups,
closes blocked pipes, and joins the callback. The Compose configuration already
uses `init: true` to reap orphaned helpers after termination.

Logs also contain yt-dlp HTTP 403 failures followed by successful later tracks.
These are source access failures, not evidence of a dead player loop. A 403 alone
is not proof of URL expiry. We retain that distinction and introduce no source
retry, alternate upload substitution, cookies, or API key dependency.

## Lifecycle and recovery

Each guild has one playback loop. Every selected track has a monotonically
increasing sequence plus its original collection position/total. INFO events
cover `next_track_selected`, `track_playback_start`, `audio_source_start`,
`first_opus_packet`, `next_track_start` (first successful voice submission),
`audio_source_exit`, `track_playback_end`, and `player_transition`.
Process IDs/exit codes, video ID, guild, elapsed time, cancellation reason,
consecutive failures, ready/pending counts, and next-pending state correlate the
transition. Per-track timing children prevent an entire playlist sharing one
first-send marker. Resolver progress remains separately visible.

Startup retains the 30-second resolution and first-audio deadlines. After
startup, 90 seconds without audio progress cancels the pipeline. Pausing suspends
this inactivity deadline; finite recordings and livestreams have no overall
playback-duration deadline. At audio EOF, both process waits share a two-second
cleanup deadline. Overdue processes are canceled as groups and reaped. All
watchdog goroutines are joined. Normal successful source exit can still drain
buffered audio during a long pause.

Errors distinguish source HTTP 403, gone/expired diagnostics, broken pipes,
yt-dlp/FFmpeg exits, voice sends/speaking, stalls, overdue EOF cleanup,
unexpected cancellation, and recovered callback panics. Diagnostics retain at
most 512 bytes per stderr tail with URLs redacted. A panic in a track callback
is converted into a logged track failure; streamer-owned children are reaped.
A collection matcher panic fails its position without killing its coordinator.

An isolated failure advances once to the next logical position after cleanup.
No track is retried automatically. Five consecutive failures halt selection,
clear Current, and retain the queue. `/queue` exposes the halt; `/resume` or an
accepted `/play` releases it. Successful EOF and intentional cancellation reset
the streak. Stop/clear/leave still cancel pending collection work.

## Source selection policy

| Input | Policy |
| --- | --- |
| Explicit YouTube video (`watch`, `youtu.be`, `embed`, `shorts`, `live`) | Extract and play exactly that ID. No music/category/duration/ranking filter. A video URL with `list=` still means that video. |
| Explicit `/playlist?list=...` | Flat-extract up to the existing collection limit, preserve IDs/order/duplicates, skip known unavailable entries, verify each exact ID at playback. No replacement searches. Audio starts without resolving all entries' full metadata. |
| Direct currently live video | Exact ID, unknown duration allowed, actual live title/status retained. Fresh stream extraction at playback; EOF ends that live session and advances, without reconnect loops. Upcoming broadcasts remain unavailable. |
| Spotify | Validate complete metadata first, then reproduce the recording with the dedicated strict policy below. Never stream Spotify audio. |
| Free text | Existing bounded music discovery and explicit variant intent policy; separate from Spotify's strict metadata policy. |

### Pinned yt-dlp research

The Docker pin is **2026.08.19**. Implementation was checked against that tag,
not assumed from a generic search API:

- [Search extractors](https://github.com/yt-dlp/yt-dlp/blob/2026.08.19/yt_dlp/extractor/youtube/_search.py): `ytsearch10:` and `https://music.youtube.com/search?q=...#songs`.
- [Tab/flat extractors](https://github.com/yt-dlp/yt-dlp/blob/2026.08.19/yt_dlp/extractor/youtube/_tab.py): ordinary video summaries may expose channel/uploader, duration, availability, live status, and verification. Music song summaries may expose **only ID and title**. Playlist entries retain selected video IDs.
- [Full video extractor](https://github.com/yt-dlp/yt-dlp/blob/2026.08.19/yt_dlp/extractor/youtube/_video.py): `artists`, `track`, `album`, categories, channel identity/verification, duration, and live status when available. Auto-generated release descriptions supply recording metadata. Fields are optional; no invented Official Artist Channel flag is used.

Spotify matching fetches at most ten Music song summaries and ten normal video
summaries, deduplicates by ID, and verifies at most four candidates. Two shortlist
places are reserved for each pool when available; unused places go to the other
pool. This prevents sparse Music summaries being starved by richly described
reuploads. Extractor calls remain sequential within each match, under the shared
four-match/three-background limiter and 90-second per-match deadline. No new API
or credentials are required.

### Matching and ranking

1. Unicode compatibility decomposition, accent normalization, punctuation and
   apostrophe normalization handle spelling presentation differences. `feat.`,
   `ft.`, and `featuring` clauses are separated from the core title. Queries
   include all credited artists where absent from the title.
2. Require all meaningful requested title tokens and independent primary-artist
   evidence. Reject substantial extra title content (precision below 0.65), such
   as a concert venue/date without the word “live”. Explicit artist arrays must
   contain the requested credits; absent secondary metadata can contribute less
   evidence but cannot create an official-source classification.
3. Bidirectional version checks cover live/concert/unplugged, remix, acoustic,
   remaster, instrumental, karaoke, slowed/reverb, sped-up, nightcore, 8D, bass
   boost, cover, and mashup. Requested remix identities and remaster years remain
   title requirements. Album version metadata also participates. Artist names
   such as “2 Live Crew” are removed before candidate version detection.
4. Reject unsolicited reaction, tutorial, interview, compilation/full-album,
   documentary, medley, extended, and edit markers. These are token/phrase checks,
   not substring matching.
5. Known Spotify duration requires verified candidate duration. Differences over
   `max(15 seconds, 8%)` reject; smaller differences reduce ranking weight.
6. Among compatible recordings, bounded source bonuses prefer a matching verified
   artist, matching Topic/auto-generated release, then matching VEVO/artist-named
   sources. “Official” words in a title grant no Spotify authority bonus. A
   verified unrelated channel is not a verified artist. Exact non-official audio
   beats any incompatible official version.
7. All hard gates plus a ranking floor of 650 are required. Scores are arbitrary
   ordinal weights, **not statistical confidence**. Missing/inconsistent metadata
   can produce false negatives deliberately; no acceptable candidate means the
   Spotify position fails and ordered progressive loading skips it.

DEBUG `spotify_candidate` events contain similarities, title precision, duration
delta, source classification, compatible/mismatched versions, ranking score, and
accept/reject reason. INFO logs only the final selected recording and summary
progress. These remain heuristics, not proof that two recordings are identical.

## Public Discord state

`/play` acknowledgements and success responses, `/queue`, and `/queue page:N` are
public. Other command response visibility is unchanged. Accepted `/play` chooses
the text channel for future track announcements, in acceptance order; failures do
not change it. Only channel/session identifiers are retained, never interaction
objects. Leaving invalidates unsent announcements from the old session.

One public Now Playing/Next announcement is emitted after the first successful
voice frame for each track. Resolution alone emits none. Pending Next is displayed
as `resolving…`; live tracks show `YouTube Live`. Spotify-originated recordings are
labeled `Spotify metadata • YouTube audio`. Position/total appears for collections.
One bounded REST worker per guild serializes messages with a five-second request
timeout. Send failures are logged without retrying or duplicating transitions.

## Deterministic verification

The 125-track soak uses two real local fake executables per track, injecting
source HTTP 403 and decoder exit failures. It checks transition/event counts,
first-frame announcements, no stuck Current, all 250 PIDs reaped, bounded
file descriptors, and goroutine return after shutdown. Separate tests exercise
EOF races with skip/stop, pending collections, cancellation, pause/resume,
inactivity/cleanup deadlines, panic cleanup, failure halt/resume, transient
voice reconnection, public commands, and exactly-once transition announcements.
All run under the race detector. Spotify metadata 50/50 and 123/123 offline
regressions and existing DAVE/auto-leave/isolation tests remain in the suite.

These checks prove the reproduced cleanup failure and deterministic transitions;
they do not establish live Discord audibility or guarantee YouTube availability.
No deployment or live playback verification was performed during this pass.

## Files changed in this pass

Earlier uncommitted work was retained. This pass changed:

- `Dockerfile`, `README.md`, `go.mod`, `go.sum`, `docs/source-selection-and-lifecycle.md`.
- `internal/discord/bot.go`, `commands.go`, `commands_test.go`, `idle.go`, `idle_test.go`, `announcements.go`, `announcements_test.go`.
- `internal/media/resolver.go`, `resolver_test.go`, `collection.go`, `collection_test.go`, `process.go`, `timing.go`, `spotify_rank.go`, `source_intent_test.go`, `canonical_test.go`, `ranking_test.go`, `spotify_public_test.go`.
- `internal/player/player.go`, `queue.go`, `pause.go`, `collection.go`, `stream.go`, `lifecycle.go`, `lifecycle_test.go`.

The final image build exposed the same environment-specific Go/EDNS lookup
failure already addressed in the runtime stage: host DNS resolved
`proxy.golang.org`, but containerized `go mod download` failed, including with
host networking. Applying `GODEBUG=netedns0=0` to that build step resolved the
download. No running container or host DNS configuration was changed.
