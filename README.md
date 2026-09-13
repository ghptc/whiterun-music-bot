# The Bard, Slave of Whiterun

A small Go Discord music bot. **The Bard plays songs, not content.** State lives in memory, independently per server. No database or Redis.

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

Enable Discord Developer Mode to copy a server ID. With no guild ID, commands register globally and may take time to appear. Startup replaces the application's commands in the selected scope with the six V1 commands. Use a dedicated application; changing scope does not remove commands previously registered in another scope.

Logs: `docker compose logs -f bard`. Shut down: `docker compose down`. To update yt-dlp, change its version in `.env` and rebuild. Rebuild the image rather than updating a running container.

## Commands

Join a regular voice channel first. Commands that change playback require you to be in the Bard's channel; `/queue` can be used by anyone in the server. Responses are concise and visible only to the invoker.

| Command | Behavior |
| --- | --- |
| `/summon` | Join your voice channel |
| `/play query:505 Arctic Monkeys` | Find and enqueue a song; join automatically if necessary |
| `/play query:505 Arctic Monkeys live Reading 2009` | Prefer the requested live performance |
| `/play query:Nutshell Alice in Chains unplugged` | Prefer the unplugged version |
| `/play query:https://www.youtube.com/watch?v=VIDEO_ID` | Resolve that video directly, still checking for music |
| `/skip` | Cancel the current song and advance |
| `/queue` | Show the current song and first ten queued songs, plus remaining count |
| `/stop` | Stop playback, cancel searches, clear the queue, remain in voice |
| `/leave` | Stop, cancel searches, clear the queue, and disconnect |

A finished or failed track advances automatically. Playback failures are logged. A voice disconnect, forced channel move, or detected gateway loss clears that session; summon again after connectivity returns.

## Architecture

```text
cmd/bot/main.go           Configuration, startup, signals, shutdown
internal/discord/        Slash commands, voice orchestration, guild registry
internal/media/          yt-dlp resolution, isolated ranking/filtering, processes
internal/player/         Queue, cancellation, streaming, Ogg packet parsing
Dockerfile               Multi-stage Go build and playback runtime
compose.yaml             Single service; no persistent storage
```

Each guild owns one synchronized player, queue, current track, and voice transport. Searches run outside its control lock. Session tickets reject results from searches overtaken by stop/leave, including results already resolving when the command arrives. Up to four searches run concurrently; each guild queues up to 100 songs.

```text
YouTube → yt-dlp stdout → FFmpeg → 48 kHz stereo Opus → Discord voice
```

Audio travels through pipes, never permanent audio files. FFmpeg emits 20 ms Opus frames at 96 kb/s; Go reconstructs the Ogg packets and paces delivery. Skip, stop, disconnect, and shutdown cancel process groups, including helper processes, and reap the two direct children. Startup/stalled-stream timeouts prevent a broken source from blocking the queue indefinitely.

The client uses [DisGo](https://github.com/disgoorg/disgo) and the experimental pure-Go [dave-go](https://github.com/thomas-vilte/dave-go) implementation. [Discord requires DAVE encryption for voice](https://discord.com/blog/bringing-dave-to-all-discord-platforms); audio waits for encryption handshakes. The image includes FFmpeg/libopus, Python, yt-dlp with its EJS dependencies, Deno, and certificates. Deno supports [YouTube's JavaScript challenges](https://github.com/yt-dlp/yt-dlp/wiki/EJS). The process runs as a non-root user with temporary runtime storage.

## Music selection

Text searches use `ytsearch10`, extract candidate metadata, then apply `internal/media/ranking.go`. No candidate passing the filter means: **“The Bard could find no song worthy of playing.”** There is no random-video fallback.

- Reject obvious podcasts, interviews, reactions, reviews, tutorials, documentaries, gameplay, news, shorts, ongoing livestreams, and upcoming streams.
- Require evidence such as the Music category, artist/track metadata, Topic/VEVO source, official music labeling, or an auto-generated music description.
- Rank query relevance and explicit version intent before canonical-source signals. Prefer an inferred official artist source, official audio/video, Topic/auto-generated upload, VEVO, then a relevant verified source and other reasonable music.
- Penalize unrequested live, cover, remix, slowed, reverb, nightcore, lyrics, karaoke, unplugged, and acoustic versions. Explicit requests remove that variant penalty and reward matches. Unplugged/concert requests also permit live labeling. Obvious reaction content remains rejected.
- Accept single-video YouTube watch, youtu.be, and embed links; remove playlist/timestamp parameters. Reject other sites, playlist-only URLs, and Shorts links. Direct URLs bypass search ranking, not the music filter.

These are fallible heuristics. yt-dlp does not reliably expose an Official Artist Channel badge, so artist metadata and verification provide an approximation; verification alone is not proof. Missing metadata may reject a real song, and misleading music metadata may still pass. V1 accepts only tracks between **45 seconds and 20 minutes**, favoring songs over long-form content.

## Development and verification

With Go 1.27 installed (FFmpeg is optional for the local audio smoke test):

```sh
gofmt -w cmd internal
go vet ./...
go test ./...
go test -race ./...
go build ./...
```

The Docker build also runs vet and unit tests, and verifies runtime tools and FFmpeg's Opus encoder. Tests cover selection, requested variants, URL validation, resolver fixtures, queue ordering/isolation, automatic advancement after failures, stop/leave, stale searches, concurrent access, Ogg parsing, and subprocess cancellation. A local FFmpeg test uses synthetic PCM; tests never contact YouTube or Discord.

After installation, manually check `/summon`, text and URL `/play`, queue two songs, automatic advancement, `/skip`, `/stop`, and `/leave`. Repeat in a second server to verify live isolation, and disconnect/move the bot while playing to check cleanup. This requires your Discord account, server, and bot credentials.

## V1 limitations

- Restarting loses queues. One voice channel per guild; no pause, seek, volume, playlists, persistent history, or Stage channels.
- Search can take up to 90 seconds; concurrent searches enqueue in resolution-completion order. Selection is heuristic, not a guarantee of the canonical recording.
- YouTube can block server IPs, impose regional restrictions or login requirements, and change extraction behavior. V1 has no cookies, authentication, proxy, or PO-token configuration. Such tracks fail and the queue continues.
- DAVE support depends on an experimental upstream implementation and still needs real Discord playback verification. No transparent voice reconnection or queue recovery after disconnect.
- Disconnect detection polls once per second. Idle voice connections remain until `/leave` or disconnection.
- Linux containers are the supported runtime; process-group cleanup is Linux-specific.

In this workspace, Go checks and local FFmpeg tests can run. Full image build and live Discord/YouTube playback need a running Docker daemon and configured bot credentials; configuration validation alone does not establish those work end-to-end.
