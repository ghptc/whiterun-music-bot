# Native queue experience — CR-006.4

`/queue` displays current and all upcoming logical positions from one detached
player snapshot. `/queue limit:N` limits upcoming positions only. Current appears
once before position 1; pending entries keep their order and stored metadata.
Spotify Fidelity, Force Play and SQLite persistence are unchanged.

## Native layout and artwork

Each message contains one Container with song rows and a discreet range footer.
Only the first block includes the heading and upcoming count. A row uses Section + Text Display + Thumbnail when
artwork is available; otherwise it uses Text Display alone. Position, title and
artist are text, with channel as artist fallback. Now Playing and announcements
use the same minimal native song presentation.

Artwork selection uses the persisted Spotify URL first, then the persisted
YouTube thumbnail, then a thumbnail derived from a valid YouTube video ID.
Invalid URLs fall through to the next source. No placeholder, image download,
image cache, compositing, PNG encoding, HTML or TXT export runs during rendering.
SQLite catalog and queue artwork recovery across restart remain covered by the
storage tests. Discord loads artwork directly from its URL.

## Discord limits

The [official component reference](https://docs.discord.com/developers/components/reference)
limits messages to 40 total components. The first full block uses 38: Container,
heading, footer, nine rows of three components each and eight small Separators.
Continuations use 37 components because the heading is omitted. Text labels
are escaped, mentions disabled, and titles/artists clipped to 100/80 UTF-16 units;
including footer delivery notices, each message stays below 4000 text units.
The same nine-row bound also applies to rows without artwork. Tests validate
actual serialized component trees, including nested accessories.

Thumbnail is a Section accessory. There is no field selecting its side; Discord
places it on the right. The API provides no fixed container width, row height,
CSS, or precise music-column alignment. Discord controls fonts and layout across
clients. A valid URL does not guarantee a reachable CDN image: the bot cannot
observe client loading failure or automatically swap a broken Spotify image for
YouTube after delivery. Fallback applies to absent/invalid metadata. Snapshots
remain static as playback advances.

## Delivery and responsiveness

The interaction is publicly deferred before the detached snapshot is taken.
Component construction performs no provider/network I/O. Sequential REST sends
hold no player lock, respect disgo rate limiting, use twenty-second request
budgets and a twelve-minute delivery deadline. Replacement requests cancel old
deliveries in the same guild/channel; other guilds/channels remain independent.
Only confirmed positions count as delivered. A failed send stops further blocks
and attempts a notice on the first block; failed notices are logged.

There is no implicit queue limit: 10 upcoming songs use two messages, 123 use 14,
and 500 use 56. Current shares the first block and can require another block at
an exact multiple of nine. The [interaction API](https://docs.discord.com/developers/interactions/receiving-and-responding)
limits personal-only installations to five followups; oversized requests receive
an explicit explanation, not a truncated list. Server installations can deliver
larger queues, subject to API deadlines and rate limits.

## Verification

Tests cover 1/10/20/123/500 positions, limit/current/pending behavior, actual native
artwork URLs and YouTube fallback, missing artwork without placeholders, component
and text budgets, order without duplicates, detached snapshots, guild isolation,
SQLite restart recovery, playback responsiveness, send failures and HTTP 429
retries using JSON payloads. Raster-only tests were removed with the renderer.
Run `go test ./...`, `go test -race ./...`, `go vet ./...` and
`docker compose build`.

Live Discord client appearance and CDN availability require manual verification;
no live Discord/voice session is claimed by the automated tests. This document
supersedes the presentation in CR-006.1 and older queue layout investigations.

## CR-006.5 — native visual refinement

Queue titles now use the smallest native heading (H3) with bold title text;
artists use normal text rather than subtext. Each song has two text lines without
blank lines. The current song keeps its discreet “Now playing” prefix. Small
Separators appear only between songs, with no leading/trailing separator. Each
block remains a single Container using the same native horizontal layout and gold
accent; continuation blocks retain only their range footer. Now Playing and
playback announcements retain their previous typography.

Adding Separators requires nine songs per block to stay below Discord's
40-component limit. All songs, current position, ordering, artwork fallback and
`/queue limit:N` semantics remain intact. More blocks are sent for large queues;
the existing personal-installation followup restriction still applies.

The [official component reference](https://docs.discord.com/developers/components/reference)
provides no configurable Thumbnail size, Section height, Container width or font
size. **Thumbnails retain their actual native size; no reduction is claimed.**
H3 improves title hierarchy and normal text improves artist legibility, while
small Separators provide the minimum supported separator padding. Exact spacing,
wrapping and dimensions remain client-controlled; this does not guarantee a
smaller total row height or exact pixel targets. The Container is not narrowed
or split into per-song cards. Original artwork URLs and their query parameters
remain unchanged, with Spotify → YouTube → valid-video-ID fallback, and text-only
rows when no artwork exists.

The unused A/B/C script and investigation document were removed at the user's
request. No prototypes were published, and no real-client visual homologation is
claimed. This implementation supersedes the previous investigation gate following
explicit authorization to refine production directly. Player behavior, Spotify
Fidelity, Force Play and persistence were not changed.

Validation on 2026-10-19: `go test ./...`, `go test -race ./...`,
`go vet ./...` and `docker compose build` passed. Race/vet required access to the
host Go cache; Docker required daemon access and used the classic builder because
buildx was unavailable. Image `e00b82610a15` was built without restarting the bot.
Tests include nine-row block boundaries, current-only/small/full queues, complete
delivery, limit handling, artwork fallback, URL preservation, heading/artist
formatting and small separators only between songs.
