# Queue experience — CR-006.1

`/queue` shows current and all upcoming logical positions from one detached
`player.State()` snapshot. `/queue limit:N` limits upcoming songs only; current
appears once before position 1. Pending entries retain their position and stored
metadata. No music search, resolver or catalog read runs during rendering.
Spotify Fidelity and Force Play behavior are unchanged.

## Discord investigation

The [official component reference](https://docs.discord.com/developers/components/reference)
was checked before implementation. Section exposes text plus an accessory, with
no accessory-side field. Thumbnail is only a Section accessory. Container exposes
components, accent color and spoiler options, with no width field. Action Row
holds interactive components, not arbitrary image/text columns. Separators can
adjust their own spacing but do not set song-row height or alignment. Consequently,
native Components V2 cannot guarantee left artwork, controlled width, compact
fixed-height horizontal rows or Spotify-like column alignment.

The implementation draws PNG directly in Go using `image`, `image/png` and
`golang.org/x/image`. Each block is a single dark music list, with position,
48-pixel cover, title and artist at fixed coordinates. There are no individual
song cards or borders. Each PNG is exposed through a Components V2 Media Gallery
using `attachment://queue.png`; no HTML, TXT, CSS or headless browser is involved.
Image alternative text includes clipped song titles and artists.

## Bounds and performance

- 12 rows per PNG, 960 pixels wide, at most 856 pixels high (64 pixels per row).
- One PNG attachment per message, capped at 4 MiB; no 500-song bitmap.
- At most two image renders run concurrently across the process. Each render
  handles a block at a time; delivery retains the first PNG for status edits and
  releases subsequent PNG buffers after sending. Waiting renders honor cancellation.
- Artwork fetches use stored Spotify artwork first, then stored YouTube thumbnail
  or the existing video ID thumbnail, then a local placeholder. No track lookup.
- Only HTTPS provider hosts `i.scdn.co`, `i.ytimg.com`, `img.youtube.com` are fetched;
  redirects are refused. Custom artwork hosts get the local placeholder.
- HTTP timeout: 800 ms per request; total artwork budget: 2 seconds per block.
  Once exhausted, cached artwork still works and misses use the placeholder.
- Artwork bodies are capped at 2 MiB and decoded images at 4 million pixels,
  checked before full decode. Cropped covers are cached at 48×48 only.
- Shared mutex-protected cache holds at most 256 entries (about 2.25 MiB of cover
  pixels), with one-hour positive and five-minute negative TTLs. Eviction is
  bounded, not LRU; simultaneous misses may fetch twice. There is no disk cache.
- Pixel working sets are bounded by two concurrent blocks and two source images,
  independent of queue length. HTTP buffers and PNG output also have explicit
  caps. Snapshot and lightweight block-plan memory scale with queue length.

The interaction is publicly deferred before taking the snapshot. Rendering and
REST waits hold no player lock. Messages are sent sequentially through DisGo's
REST limiter, with 20-second request budgets and a 12-minute delivery deadline.
A newer request cancels an older delivery in the same guild/channel. Different
channels/guilds remain independent. Rendering or sending failures stop further
blocks; a best-effort edit reports confirmed positions without discarding the
first image. Failure to send that edit is logged.

An empty queue uses a short native text component without an attachment. A render
failure before the first send produces a short retry message. No export fallback
or silent song truncation is used. Server installation is required for more than
five followups; a personal-only installation receives an explanation instead.
123 songs need 11 messages; 500 need 42. The current song shares the first block.

## Visual limitations

Discord controls display width and scales images on smaller clients; the API
cannot force a wide desktop container. Open the image to inspect full resolution.
Text is not selectable or individually linked. Titles/artists use single-line
pixel clipping with an ellipsis; full metadata remains in the queue. The embedded
Go font supports Latin, Greek and Cyrillic but lacks many CJK and emoji glyphs;
unsupported characters render the font's missing-glyph symbol. There is no system
font or network font dependency. A placeholder may persist for five minutes after
an artwork error. Snapshots do not update as playback advances.

## Verification

Tests cover empty/1/10/123/500-song queues, limits/current/pending positions,
Unicode and long text, decoded PNG dimensions, cover placement/order, positive
and negative cache behavior, expiration/eviction, invalid and oversized images,
provider fallback, canceled/invalid renders, output-byte cap, concurrent rendering,
playback responsiveness, partial Discord delivery, personal-install limits and
multipart retries after rate limits. Run `go test ./...`, `go test -race ./...`,
`go vet ./...` and `docker build -t whiterun-music-bot:cr-006.1 .`.

Older layout investigations in `bard-ui-v2.md` and `queue-artwork-hotfix.md` describe
the preceding native-thumbnail layout; this document supersedes that presentation.
