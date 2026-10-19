# Native queue experience — CR-006.3

`/queue` displays current and all upcoming logical positions from one detached
player snapshot. `/queue limit:N` limits upcoming positions only. Current appears
once before position 1; pending entries keep their order and stored metadata.
Spotify Fidelity, Force Play and SQLite persistence are unchanged.

## Native layout and artwork

Each message contains one Container with a heading, song rows
and a range/count footer. A row uses Section + Text Display + Thumbnail when
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
limits messages to 40 total components. A full block uses 39: Container,
heading, footer and twelve rows of three components each. Text labels
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

Tests cover 10/123/500 positions, limit/current/pending behavior, actual native
artwork URLs and YouTube fallback, missing artwork without placeholders, component
and text budgets, order without duplicates, detached snapshots, guild isolation,
SQLite restart recovery, playback responsiveness, send failures and HTTP 429
retries using JSON payloads. Raster-only tests were removed with the renderer.
Run `go test ./...`, `go test -race ./...`, `go vet ./...` and
`docker build -t whiterun-music-bot:cr-006.3 .`.

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
The footer contains only range/count and temporary delivery feedback.

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
