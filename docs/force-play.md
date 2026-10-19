# CR-004 — Force Play / Exact YouTube Video

Use `/play query:https://youtu.be/abcdefghijk force:true` to queue exactly that
YouTube video. Confirmation: `Queued: <YouTube title> — Direct YouTube`.

Omitting force or selecting false preserves the existing resolution path.
Force accepts the single-video formats supported by `YouTubeURL()`, including
youtube.com/watch, youtu.be, music.youtube.com/watch, shorts, live and embed.
Parameters such as list, index and timestamps are discarded: a watch URL with a
video ID selects that video alone. A playlist URL without a video is rejected.
Spotify URLs, text searches, malformed URLs, lookalike domains, credentials and
explicit ports are rejected before extraction. Discord uses an optional native
boolean; there is no textual play-command parser or --force/-f alias.

## Audited flow and implementation

Before this change, slash play called CollectionResolver.Prepare, which routes
Spotify collections to progressive matching and other inputs to Resolve.
Resolver.Resolve already canonicalizes direct YouTube videos, extracts their
metadata with yt-dlp, and checks the returned video ID. Text searches use a
separate ranking path. Bot.play verifies Spotify identity or playlist metadata
before invoking the existing Streamer and FFmpeg.

PreparePlay delegates unchanged when force is false. When true, it first uses
YouTubeURL, then resolves only the canonical video with a resolver copy whose
Spotify Match is nil. No Spotify provider, candidate search, ranking, mapping
read/write or alternative recording participates. Metadata extraction keeps the
existing timeout, output limits, cancellation and subprocess handling. Playback
also checks the forced track's URL, source and video ID before streaming.

The same EnqueueMany and GuildPlayer handle playback, pause, resume, skip,
permissions and failures. Force does not prioritize the request. Voice membership
is checked before resolution and again before enqueue. The Now Playing embed
retains its YouTube thumbnail. Extraction or streaming failure never selects a
replacement; the normal failure notice and queue advancement policy apply.

ForcedYouTube is serialized in the existing SQLite queue/history metadata.
Video ID, canonical URL, title, duration, thumbnail and requester survive restart.
No schema migration or dependency was added. The provider catalog is currently
Spotify-keyed; direct YouTube metadata uses the existing memory cache and SQLite
queue/history, without writing Spotify catalog or fidelity mappings. The new
flag is omitted from ordinary tracks' JSON, preserving existing mapping keys.

## Deterministic coverage

| Contract | Tests |
| --- | --- |
| Force absent / false retain ordinary command behavior | TestForceCommandQueueAndControls |
| YouTube, short URL, Music, extra parameters | TestForceDirectSelection |
| Spotify, search, playlist, malformed, fake host and unsafe URLs rejected | TestForceRejectsInputsBeforeExtraction |
| No Spotify matching or alternative search; incompatible title/duration preserved | TestForceDirectSelection |
| Wrong ID, upcoming video, malformed JSON, extraction error, cancellation | TestForceFailureNeverSubstitutes |
| Normal queue position, requester, pause/resume/skip | TestForceCommandQueueAndControls |
| Streaming failure uses exact canonical URL once | TestForcedBotPlaybackFailureKeepsExactURL |
| Failure notice retains original identity and advances to queued successor | TestForcedFailureReportsOriginalAndAdvances |
| Thumbnail in Now Playing | TestForcedNowPlayingKeepsThumbnail |
| SQLite reopen and player recovery retain identity, order and artwork; Spotify tables remain empty | TestForcedYouTubeRecoveryAndSpotifyCacheIsolation |
| Other-channel requester denied | TestForceCommandRequiresSameChannel; TestChannelAuthorizationForEveryMutation |
| Native optional boolean command schema | TestForceSlashContract |

The adversarial fixture supplies Spotify's Paranoid by Black Sabbath (171 seconds)
as inherited matching context, while the selected YouTube video is an unrelated
17-second interview. Force preserves the interview's ID and metadata.
A mutation experiment removed `resolver.Match = nil`: all four direct-selection
cases failed with MATCH_REJECTED (version_or_semantic_mismatch). Restoring the
line restores success, demonstrating detection of an actual fidelity regression.

## Validation and limitations

Validated on 2026-10-19:

- `go test ./... -count=1`
- `go test -race ./... -count=1`
- `go vet ./...`
- `docker compose build`

The real `go run ./cmd/bot commands-sync` completed successfully against Discord
in global scope, including publication and readback comparison of all 11 command
contracts (`command_sync_complete`, 918 ms). The force option is an optional
Discord boolean (type 5). Global commands can take time to appear in clients.

No real Discord voice playback E2E was performed. Tests replace provider and
Discord I/O with local fixtures; real video availability, region/auth restrictions
and client propagation remain external acceptance checks. Building the image
does not restart the running service.
