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
limits messages to 40 total components. The first full block uses 39: Container,
heading, footer and twelve rows of three components each. Continuations use 38
components because the heading is omitted. Text labels
are escaped, mentions disabled, and titles/artists clipped to 100/80 UTF-16 units;
including footer delivery notices, each message stays below 4000 text units.
The same twelve-row bound also applies to rows without artwork. Tests validate
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

There is no implicit queue limit: 10 upcoming songs use one message, 123 use 11,
and 500 use 42. Current shares the first block and can require another block at
an exact multiple of twelve. The [interaction API](https://docs.discord.com/developers/interactions/receiving-and-responding)
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
o live Discord/voice session is claimed by the automated tests. This document
supersedes the presentation in CR-006.1 and older queue layout investigations.

## Visual refinement — Spotify organization, Skyrim personality

The shared heading reads “Bardão · Queue” or “Bardão · Now playing”, in sentence
case rather than an all-caps banner. A single aged-gold accent (#A68B5B) on each
outer container evokes Whiterun. Surface and text colors follow Discord's theme;
there is no attempt to recolor individual titles or enforce a dark background.

Queue rows read `1 · **Title**` followed by the artist in native subtext. Only
the title is bold: the position supports scanning without competing with the
song. Artwork remains individual on the right. Current uses a plain “Now playing”
prefix; playback state/duration and next-track metadata remain secondary in the
shared Now Playing layout. Heading padding separators, per-song dividers,
per-song cards, buttons, pseudo-table whitespace and decorative emoji are absent.
The first footer contains range/count and temporary delivery feedback; later
footers contain only the position range.

Structural review: two text lines per normal song, a consistent left text edge,
one title emphasis per row, one color accent per block, and no extra separator
components. Long labels retain the existing clipping and native wrapping rules;
no fixed-width padding is added for positions 9/10 or 99/100. The twelve-row bound
remains conservative: adding a thirteenth artwork row would exceed 40 components.

Visual acceptance remains open until observed in real Discord desktop/mobile
clients. Review a ten-song queue, mixed artwork/missing-art rows, long Unicode
labels, positions crossing 9/10 and 99/100, current/paused states, empty queue and
multi-block continuations in dark and light themes. Check that title/artist
hierarchy stays readable, thumbnails do not dominate, row rhythm stays coherent,
and Now Playing feels like the same music interface. Automated payload checks
confirm structure and data; they do not establish final visual quality or exact
spacing. No simulated screenshot is presented as a Discord preview.

## CR-006.4 — density and thumbnail sizing investigation

The [official Thumbnail schema](https://docs.discord.com/developers/components/reference#thumbnail)
accepts media, description and spoiler, with no configurable display size.
Section and Container likewise expose no row-height, gap, margin or padding
control. Media width/height metadata does not provide a requested display size.
The [Separator schema](https://docs.discord.com/developers/components/reference#separator)
offers small/large padding only, without a pixel guarantee. Adding separators
would add padding and consume component budget; consecutive song rows therefore
retain no explicit separators, empty lines or additional text components.

The 40–48 px artwork and 8–12 px gap targets cannot be enforced through this API.
Reducing source resolution is not evidence of a smaller displayed thumbnail.
Original persisted URLs, including query parameters, are passed through unchanged;
no size query is injected, and no image is downloaded, resized or padded.
The two-line title/artist row is already the minimal existing layout preserving
both pieces of information and individual native artwork. Thumbnail-imposed row
height and text wrapping remain controlled by Discord, including differences
between rows with and without artwork. This change reduces repeated block chrome;
it does not claim to shrink thumbnail boxes or to achieve pixel-level spacing.

No authenticated Discord client was available for a real resolution comparison.
That part of the investigation and visual acceptance remain pending. To verify,
compare identical square artwork hosted at 48, 96 and 320 px in otherwise identical
Section/Thumbnail payloads, in desktop and mobile at recorded zoom/font settings.
Record displayed box dimensions and inter-row distances, separately from source
image dimensions, for 1/10/20 songs and continuations in 123/500-song queues.
Check mixed aspect ratios, missing art and long labels as well. Do not infer the
result from a CDN response or a simulated preview. API-schema review establishes
the missing sizing control; automated tests establish URL preservation and native
structure, not actual client rendering.

Continuations omit repeated headings and counts, keep the same gold accent and
row structure, and end with a subtext position range. Current remains present
only once. The twelve-row boundary, delivery failure handling, player operations,
Spotify Fidelity and Force Play are unchanged. Rendering remains a pure snapshot
transformation without provider or image requests.

Validation on 2026-10-19: `go test ./... -count=1`,
`go test -race ./... -count=1`, `go vet ./...` and `docker compose build`
all passed. Race/vet required access to the Go cache outside the filesystem
sandbox; Docker required access to its daemon. Docker used the classic builder
because buildx was unavailable and successfully produced the image. No live
Discord client, voice session or measured thumbnail-resolution comparison was
performed. Visual acceptance for smaller artwork remains unresolved.
