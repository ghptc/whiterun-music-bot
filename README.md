# The Bard, Slave of Whiterun

A small Go Discord music bot. Music discovery follows source intent; explicit YouTube links retain the selected video. State lives in memory, independently per server. No database or Redis.

## Setup

Prerequisites: Docker Engine running with Docker Compose, a Discord server where you can install a bot, and outbound HTTPS/WebSocket and UDP access. No host Go, Python, or FFmpeg installation is needed.

1. Create an application in the [Discord Developer Portal](https://discord.com/developers/applications), named **The Bard, Slave of Whiterun**. On its Bot page, create/reset the bot token.
2. Copy the **Application ID** from General Information. This is not the public key or client secret.
3. Under Installation / OAuth2 URL Generator, choose **Guild Install**, scopes `bot` and `applications.commands`, and permissions **View Channels**, **Send Messages**, **Connect**, and **Speak**. Open the generated link and install in your server. Ensure channel overrides also allow these permissions. Administrator permission is unnecessary.
4. No privileged intents are needed. The bot requests only Guilds and Guild Voice States; Message Content can remain disabled. Leave the Interactions Endpoint URL empty because commands arrive over the gateway.
5. Configure and start:

   ```sh
   cp .env.example .env
   # Edit .env with your token and application ID.
   docker compose up --build
   ```

   `.env` is excluded from Git and Docker's build context. Never share the token.

| Variable | Meaning |
| --- | --- |
| `DISCORD_TOKEN` | Required bot token |
| `DISCORD_APPLICATION_ID` | Required application ID matching the token |
| `DISCORD_GUILD_ID` | Optional server ID for fast, server-scoped command registration |
| `YTDLP_VERSION` | Optional Docker build override; defaults to `2026.08.19` |

Enable Discord Developer Mode to copy a server ID. With no guild ID, commands register globally and may take time to appear. Startup synchronizes the Bard's 22 commands in the selected scope. Use a dedicated application; changing scope does not remove commands previously registered in another scope.

Logs: `docker compose logs -f bard`. Shut down: `docker compose down`. To update yt-dlp, change its version in `.env` and rebuild. Rebuild the image rather than updating a running container.

## Commands

Join a regular voice channel first. Commands that change playback require you to be in the Bard's channel; `/queue`, `/now`, `/history`, `/lyrics`, and `/help` can be used by anyone in the server. Playback control responses are visible only to the invoker; `/play` and `/queue` responses are public.

| Command | Behavior |
| --- | --- |
| `/summon` | Join your voice channel |
| `/play query:505 Arctic Monkeys` | Find and enqueue a song; join automatically if necessary |
| `/play query:505 Arctic Monkeys live Reading 2009` | Prefer the requested live performance |
| `/play query:Nutshell Alice in Chains unplugged` | Prefer the unplugged version |
| `/play query:https://www.youtube.com/watch?v=VIDEO_ID` | Play that exact video, including unofficial uploads and live streams |
| `/skip [count]` | Skip 1–1001 tracks atomically, including the current track; default 1 |
| `/queue [limit] [from] [to]` | Show current and future positions within one message; all options are optional positive integers |
| `/now` | Show current artwork, title, artist, duration and next song in one compact card |
| `/pause` | Hold the current track and keep the queue intact |
| `/resume` | Continue paused audio, or release a consecutive-failure halt |
| `/clear` | Clear upcoming tracks and cancel their searches without interrupting the current song; disable loop |
| `/previous` | Replay the previous song; return the interrupted song to the queue |
| `/remove position:N` | Remove a future position, including unresolved collection entries |
| `/move from:N to:N` | Move a future position with its metadata intact |
| `/shuffle` | Shuffle future positions only |
| `/jump position:N` | Skip directly to a future position |
| `/replay` | Restart the current song after stream teardown |
| `/loop mode:off/track/queue` | Repeat successful tracks; skip bypasses repeat |
| `/seek position:MM:SS` | Validate a timestamp and explicitly report unavailable seeking; playback stays unchanged |
| `/autoplay enabled:true/false` | Persist guild preference; recommendations unavailable without a supported provider |
| `/lyrics` | Report unavailable lyrics until a licensed provider is integrated |
| `/history [limit]` | Read guild playback events from SQLite; default 10, maximum 20 |
| `/help` | Show one static, ephemeral repertoire embed |
| `/stop` | Stop playback, cancel searches, clear the queue, remain in voice |
| `/leave` | Stop, cancel searches, clear the queue, and disconnect |

See [CR-005 behavior, limitations, and verification](docs/bard-command-expansion.md) for position and loop semantics. Autoplay and lyrics are **not operational providers** in this release; seek is explicitly unavailable.

`/queue` needs no flags and fills one Discord Components V2 message from a consistent snapshot. Current appears once as text; the first ten displayed future tracks have individual artwork when available, followed by compact text until the safe character budget is full. Whole titles and Unicode are preserved. The footer reports displayed and remaining counts and the next position. `/queue from:60 to:70` uses inclusive future positions; `from` alone fills the budget and `limit:25` caps the count. No follow-ups or automatic continuation messages are sent. See [queue layout, budgets and contract investigation](docs/queue-experience.md).

Command registration inventories the selected scope, upserts changed Bard commands and verifies the published contracts without deleting unrelated commands. Run `bard commands-sync` with the usual Discord environment variables to publish/read back without opening gateway or voice. Failures are logged and return a nonzero exit status. The real REST audit found the correct `/queue limit` schema already published; the original “outdated” client error was not reproduced. See [CR-002 audit and acceptance](docs/queue-artwork-hotfix.md).

Known Spotify track URLs reuse a shared SQLite catalog before contacting Spotify; confirmed Spotify → YouTube mappings retain their existing short availability lease and stream checks. Guild queues and requesters stay separate. Cache I/O failures fall back to media resolution. The catalog stores metadata only; queue artwork uses a separate bounded memory cache. No audio is stored. See [persistent music catalog](docs/persistent-music-cache.md).

`/skip count:50` skips the current track plus the next 49 queued entries in one operation. For current track 1 and queued tracks 2–123, track 51 is selected next and tracks 52–123 remain queued. Omitted count means 1. Counts beyond the available tracks skip everything available; 0, negative values, and values above 1001 are rejected. During cancellation cleanup, further skips advance queued successors rather than recounting the cancelled track. With queued tracks but no active track, counting starts at the queue head. Replies report the selected **Next** track, not a claim that audio has started. The operation also works while paused or starting playback, and does not cancel pending searches.

Pause holds delivery at an Opus packet boundary using bounded pipe backpressure. Resume keeps the same yt-dlp and FFmpeg processes; it does not search, download again, or restart the track. One already in-flight frame may finish. Skip, stop, leave, voice disconnect, and shutdown remain cancellable while paused. The next track starts unpaused.

After **two minutes with no human listeners**, the Bard stops playback, clears the queue, cancels pending resolutions, and leaves. Other bots do not count. A human returning during the grace period cancels the deadline and preserves the session. Voice events wake the existing watcher; its one-second check handles deadlines and missed transport events. Unknown member identities conservatively count as listeners until known, so incomplete Discord cache data can delay auto-leave.

A finished or failed track advances automatically. Playback failures are logged. A voice disconnect, forced channel move, or detected gateway loss clears that session; summon again after connectivity returns.

## Collections and Spotify setup

`/play` accepts YouTube playlists (including album playlists), Spotify tracks, Spotify playlists, and Spotify albums. Normal `https://open.spotify.com/track/...`, `/album/...`, and `/playlist/...` links accept share parameters such as `?si=...` and localized `intl-...` paths. Malformed or unsupported Spotify links are rejected, never searched as song text. Spotify short/redirect links and URIs are not supported. Collections append atomically in source order and start automatically when idle. `/queue` displays the current track and as many future positions as fit in one message. `/clear` removes only upcoming tracks; `/stop` also cancels playback and pending searches.

YouTube playlist URLs use flat metadata, preserve video IDs/order/duplicates, and skip known unavailable entries. Each exact video is verified when its turn arrives; no alternate recording is searched. Explicit video URLs (even with a playlist share parameter) play that video without a music/category/duration filter. Direct live URLs are supported with no fixed playback-duration deadline. Audio streams are extracted fresh only at playback. Individual failures advance; five consecutive failures halt with the remaining queue retained for `/resume` or an accepted `/play`.

Spotify is optional and uses the official Web API through Go's HTTP client, with an anonymous public-playlist metadata fallback parsed using `golang.org/x/net/html`. Configure these variables in `.env` and recreate the container:

| Variable | Meaning |
| --- | --- |
| `SPOTIFY_CLIENT_ID` | Spotify developer application client ID |
| `SPOTIFY_CLIENT_SECRET` | Application client secret |
| `SPOTIFY_REFRESH_TOKEN` | User refresh token from the authorization-code flow; required for development-mode playlist access |
| `SPOTIFY_MARKET` | Optional ISO country code, e.g. `BR`; specify for client-credentials requests without a user country |

Create an app in the [Spotify developer dashboard](https://developer.spotify.com/dashboard), register a redirect URI, and complete the [authorization-code flow](https://developer.spotify.com/documentation/web-api/tutorials/code-flow) once for the account whose playlists Bard should read. Request `playlist-read-private` and `playlist-read-collaborative` as needed; exchange the authorization code for tokens and store the returned refresh token in `.env`. Bard refreshes access tokens automatically; it does not host a login callback. Never commit credentials. A rotated refresh token is retained in memory; if authorization stops working after a restart, update the configured refresh token.

Without a refresh token, Bard uses client credentials, useful for album metadata and any collections the app's access tier permits. [Spotify's development-mode restrictions](https://developer.spotify.com/documentation/web-api/tutorials/february-2026-migration-guide) require a Premium app owner and restrict playlist contents to playlists the authorized user owns or collaborates on. Arbitrary public playlists are therefore not guaranteed to work. This is a shared operator-configured account, not per-Discord-user Spotify authorization. URL validation, authorization/access failures, YouTube matching failures, and later playback failures are separate stages. Access errors are reported without exposing credentials, API paths, or raw responses. A syntactically valid URL does not guarantee access.

For playlist API 403/404 responses only, Bard fetches public metadata anonymously (two-minute overall deadline, 15 seconds per request, 8 MiB per response). Configuration/authentication failures, API 401/429/5xx responses, and track/album errors do not trigger this fallback. It first tries complete public HTML, then a complete playlist Embed verified against the public count and exposed prefix.

If Embed is truncated, Bard extracts Spotify's own runtime anonymous session token from that Embed and paginates the public spclient playlist metadata in 50-entry pages. It verifies offsets, occurrence IDs, totals and a stable revision, reuses the validated Embed metadata, and fetches missing titles/artists from individual public track Embeds with at most four workers. A final revision check protects against edits during resolution. Repeated track IDs retain every playlist position; only metadata requests are deduplicated. Tokens stay in memory for that operation, respect expiry, refresh at most once, and never appear in logs or errors. No account cookies, private playlists, or Spotify audio are used. Missing or inconsistent metadata rejects the entire collection before YouTube matching.

Live metadata checks on 2026-09-23 passed for `35m4wmHSlcS2RNsafX4dEg` (123/123, three metadata pages) and `37i9dQZF1DZ06evO3VkoW4` (50/50 through the existing Embed path). Run `BARD_SPOTIFY_LIVE_TEST=1 go test ./internal/media -run 'TestSpotify(PublicLarge|Embed)LiveRegression' -v -count=1` to repeat them. Normal tests are offline. See [the investigation results](docs/spotify-public-metadata.md) for mechanism details and limitations.

Spotify matching combines bounded YouTube Music song search and normal YouTube search, deduplicates video IDs, and verifies up to four candidates per round (eight unique IDs total). Two bounded alternative searches can broaden discovery; corroborated recording groups are compared before choosing an upload. It requires title/artist evidence, bidirectional version compatibility, and close duration where Spotify supplies it. Source authority breaks ties only among compatible recordings; “Official Audio” in a title is not proof. Failed matches are skipped in original logical order. See [the source-selection and lifecycle policy](docs/source-selection-and-lifecycle.md) for thresholds, pinned yt-dlp research, failure recovery, and investigation evidence.

Collections are limited to 500 entries and queues to 1,000 upcoming logical positions, including pending tracks. Oversized collections or batches that do not fit are rejected in full. Commands acknowledge immediately. Spotify collections validate complete metadata, reserve ordered queue positions, and start the first matched track without waiting for the rest. Three workers per collection resolve remaining tracks; a shared limiter allows at most three background matches and four total matches, retaining capacity for first tracks. Results become playable only in contiguous playlist order; failed matches are skipped. Concurrent plays reserve whole batches atomically in metadata-completion order. `/queue` and `/queue limit:N` are public channel responses preserving all positions, including unresolved songs, without exposing internal matching states. `/skip count:N` counts both playable and pending positions; `/clear` cancels unfinished loading and preserves the current song. Stop, leave, and voice disconnection cancel loading too. The ten-minute collection deadline discards unresolved positions while retaining already committed tracks. Queue metadata is persisted in SQLite; voice attachment after restart is explicit. Leave clears the queue.

`/play` and `/queue` responses are public. Every actual first voice frame triggers one compact public Now Playing container with individual artwork, title, artist, known duration and the next song title. `/now` uses the same design and remains ephemeral. The latest accepted `/play` sets the text channel for future announcements. Provider details and recording identity remain in metadata and logs, outside the music cards.

Lifecycle logs correlate queue selection, process exits, voice submission, cleanup, and advancement. A pause-aware 90-second inactivity deadline and two-second post-EOF cleanup deadline prevent indefinite fake Current states. Transient gateway reconnects receive 60 seconds of grace; confirmed moves/leaves still clear immediately. See [the investigation and tests](docs/source-selection-and-lifecycle.md).

## Architecture

```text
cmd/bot/main.go           Configuration, startup, signals, shutdown
internal/discord/        Slash commands, voice orchestration, guild registry
internal/media/          yt-dlp resolution, isolated ranking/filtering, processes
internal/player/         Queue, cancellation, streaming, Ogg packet parsing
Dockerfile               Multi-stage Go build and playback runtime
compose.yaml             Single service with persistent SQLite volume
```

Each guild owns one synchronized player, queue, current track, and voice transport. Searches run outside its control lock. Session tickets reject results from searches overtaken by stop/leave, including results already resolving when the command arrives. Up to four searches run concurrently; each guild queues up to 1,000 upcoming tracks.

```text
YouTube → yt-dlp stdout → FFmpeg → 48 kHz stereo Opus → Discord voice
```

Audio travels through pipes, never permanent audio files. FFmpeg emits 20 ms Opus frames at 96 kb/s; Go reconstructs the Ogg packets and paces delivery. Skip, stop, disconnect, and shutdown cancel process groups, including helper processes, and reap the two direct children. Startup deadlines bound stream resolution and first audio; they end once the first Opus packet arrives, so an intentional pause cannot expire a playback lifetime timer.

The client uses [DisGo](https://github.com/disgoorg/disgo) and the experimental pure-Go [dave-go](https://github.com/thomas-vilte/dave-go) implementation. [Discord requires DAVE encryption for voice](https://discord.com/blog/bringing-dave-to-all-discord-platforms); audio waits for encryption handshakes. The image includes FFmpeg/libopus, Python, yt-dlp with its EJS dependencies, Deno, and certificates. Deno supports [YouTube's JavaScript challenges](https://github.com/yt-dlp/yt-dlp/wiki/EJS). The process runs as a non-root user with temporary runtime storage.

## Music selection

Text searches use `ytsearch10`, extract candidate metadata, then apply `internal/media/ranking.go`. When the initial results lack a canonical source and no alternate version/video was requested, one additional `ytsearch10` with “official audio” broadens the pool. Both sets are ranked against the original request, with at most three full candidate verifications and the same 90-second search deadline. No candidate passing the filter means: **“The Bard could find no song worthy of playing.”** There is no random-video fallback.

- Reject obvious podcasts, interviews, reactions, reviews, tutorials, documentaries, gameplay, news, shorts, ongoing livestreams, and upcoming streams.
- Require evidence such as the Music category, artist/track metadata, Topic/VEVO source, official music labeling, or an auto-generated music description.
- Rank query relevance and explicit version intent before canonical-source signals. Prefer a matching canonical artist source, Topic/auto-generated release, official audio, and matching VEVO/artist upload. Music videos remain eligible but normally lose to canonical audio. A verified archive/aggregator does not receive an artist bonus; the entire normalized channel identity must match.
- Penalize unrequested live, cover, remix, slowed, reverb, nightcore, lyrics, karaoke, unplugged, and acoustic versions. Explicit requests remove that variant penalty and reward matches. Unplugged/concert requests also permit live labeling. Obvious reaction content remains rejected.
- Accept YouTube watch, youtu.be, embed, and playlist links. Explicit `/playlist` URLs queue their selected IDs in order. Video URLs with `list=` still select the exact video. Direct URLs bypass music ranking/filtering; watch, embed, shorts, and live video paths are supported. Spotify uses the separate strict policy above. Other sites remain unsupported.

These are fallible heuristics. yt-dlp does not reliably expose an Official Artist Channel badge, so artist metadata and verification provide an approximation; verification alone is not proof. Missing metadata may reject a real song, and misleading music metadata may still pass. The free-text music filter accepts tracks between **45 seconds and 20 minutes**. These constraints do not override explicit YouTube video/playlist intent.

## Development and verification

With Go 1.27 and FFmpeg with libopus installed (the Docker test stage installs and verifies both):

```sh
gofmt -w cmd internal
go vet ./...
go test ./...
go test -race ./...
go build ./...
```

The Docker build also runs vet and unit tests, and verifies runtime tools and FFmpeg's Opus encoder. Tests cover canonical/variant selection fixtures, strict Spotify metadata matching and cache isolation, Spotify URL/access errors, ordered atomic collections, pause/resume and cancellation during pause, queue ordering/isolation, automatic advancement, stale searches, empty-channel deadlines/rejoins/bot exclusion, concurrent state access, Ogg parsing, subprocess cancellation, independent two-track media delivery, and provider-challenge queue retention and recovery. These deterministic tests use local media/provider helpers and a recording voice sink; they do not contact YouTube or Discord. Review player/provider changes against [the Bard behavioral requirements](docs/bard-requirements.md) using [the reliability audit role](docs/bard-reliability-agent.md): “How could Bard violate this requirement while every current test still passes?”

After installation, manually check `/summon`, text and URL `/play`, Spotify track/album/accessible and inaccessible playlist links, `/now`, `/queue`, `/pause`, `/resume`, automatic advancement, and `/skip`, `/stop`, `/leave` while paused. Check a human rejoining before the two-minute deadline and a bots-only channel reaching it. Repeat in a second server to verify live isolation, and disconnect/move the bot while playing to check cleanup. This requires your Discord account, server, and bot credentials.

## Limitations

- After restart, persisted queue metadata is recovered; voice must be attached explicitly and the interrupted track restarts from the beginning. One voice channel per guild; no seek, volume or Stage channels.
- Long pauses keep the existing stream open; upstream connection expiry or network failure can still end a track. Track-local playback errors advance the queue; a classified YouTube challenge or rate limit blocks playback, retains the logical queue, and waits for `/resume` to retry the same position.
- Search can take up to 90 seconds; concurrent searches enqueue in resolution-completion order. Selection is heuristic, not a guarantee of the canonical recording.
- YouTube can block server IPs, impose regional restrictions or login requirements, and change extraction behavior. V1 has no cookies, authentication, proxy, or PO-token configuration. Classified provider challenges retain the queue and require an explicit `/resume`; the command does not make the provider available.
- DAVE support depends on an experimental upstream implementation and still needs real Discord playback verification. No transparent voice reconnection or queue recovery after disconnect.
- An empty human audience triggers auto-leave after two minutes, including while paused. Remaining in a channel with listeners and an empty queue is allowed.
- Linux containers are the supported runtime; process-group cleanup is Linux-specific.

Go tests and the Docker build/runtime checks are offline acceptance checks. Live Discord audibility, DAVE recovery across pause, Spotify access for the configured app/account, and YouTube availability still require live verification; a successful build does not establish those end-to-end.

## Qualidade, fidelidade e recuperação (outubro de 2026)

`/previous` reinicia a música anterior e recoloca a interrompida a seguir. Como
os demais controles, exige o mesmo canal de voz do Bardo. O histórico considera
inícios efetivos de áudio e mantém até 100 entradas.

SQLite persiste fila, metadados, histórico, canal de anúncios e mapeamentos
Spotify → YouTube. Configure `BARD_DB_PATH` (default `bard.db`); Compose fornece
volume `bard-data`. Após reinício, a voz precisa ser conectada explicitamente e a
faixa interrompida recomeça do início. Não há recuperação de posição por seek.

Spotify é a fonte de identidade; candidatos incompatíveis ou ambíguos não são
reproduzidos. `SPOTIFY_MATCH_POLICY` permite configurar os critérios documentados.
`/queue` apresenta o resumo da última importação.

- [Auditoria e limites reais](docs/quality-audit.md)
- [Política Spotify → YouTube](docs/spotify-fidelity.md)
- [First Confirmed Match Wins: arquitetura e medições](docs/first-confirmed-match.md)
- [Matriz, CI e testes externos](docs/test-matrix.md)

Os testes locais não comprovam áudio audível no Discord real.


### Exact YouTube video

Use `/play query:<YouTube-video-URL> force:true` to queue exactly the selected
video. The Bard confirms `Queued: <title> — Direct YouTube`, preserves its
thumbnail and never searches for a replacement recording. Spotify URLs, text
searches and playlist-only URLs are rejected with force. Video links containing
playlist parameters still select only that video. Omitting force or choosing
false keeps the existing behavior. Queue order and voice permissions apply
normally. See [Force Play](docs/force-play.md) for validation and limitations.
