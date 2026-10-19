# CR-006 — The Bard: Minimalist Music UI V2

## Investigation and platform limits

Inspected the pinned DisGo **v0.19.6** source in the local Go module cache and the
[official component reference](https://docs.discord.com/developers/components/reference).
The SDK implements ContainerComponent (17), SectionComponent (9),
TextDisplayComponent (10), ThumbnailComponent (11) and SeparatorComponent (14),
plus V2 constructors for message creates and updates. No SDK upgrade or new
dependency was necessary.

Section exposes content and an accessory; neither the API schema nor the SDK
provides an accessory alignment/order field. **Image-left is not supported by the
available native layout contract.** The chosen alternative was presented before
implementation: text on the left and a small thumbnail in the native accessory
position on the right. Legacy embed thumbnails offer no left-position control;
media galleries put larger images above/below text rather than beside it. No
composite images, website, HTML, or layout workarounds are introduced. Thumbnail
size, crop, and final appearance remain Discord-client controlled; exact square
pixels and cross-client appearance have not been verified live.

V2 messages require IS_COMPONENTS_V2 and cannot use legacy content/embeds. The
flag cannot subsequently be removed. The maximum is 40 components per message,
including nested content and accessories. We conservatively bound all display
text to 4,000 UTF-16 units per message; actual generated text stays comfortably
below that. The current reference's Text Display table does not state a numeric
text limit, so this is an explicit implementation ceiling rather than a claim
that every Discord client interprets character counts identically. Unicode and
escaped metadata are tested against that ceiling.

The [interaction documentation](https://docs.discord.com/developers/interactions/receiving-and-responding)
requires an initial response within three seconds, permits followups during the
15-minute token lifetime, and limits personal-only installations to five
followups. Guild-installed interactions do not have that specific ceiling.
Followups can carry V2 and ephemeral flags. Ephemeral visibility is fixed by the
original acknowledgement for the original message. We preserve `/queue` as
public, `/now` as ephemeral, and automatic announcements as public. No new posting
outside the requested interaction/established announcement channel is added.

## Components and shared visual language

Every music message contains exactly one native container. A short heading and a
small divider-free separator introduce compact song sections. Each section has
one Text Display for title/artist and one thumbnail accessory. There are no
per-song containers, repeated color accents, buttons, menus, or media galleries.

Now Playing and `/now` use the same renderer: title, artist (or known channel),
known duration, a discreet playing/paused indicator, and next title when known.
The playing indicator is absent until audio has actually started. Queue highlights
current once and numbers upcoming positions independently; current does not
consume the requested limit. Pending entries use available metadata without
showing internal readiness or resolution labels.

Names are normalized and bounded, Markdown is escaped, mention pings are disabled,
and standalone URL tokens are removed from visual labels. Full metadata remains
unchanged in player state, logs, and persistence. Failure notices retain a short
human-readable failure category and song identity; raw provider errors, internal
IDs, and resolver positions remain in diagnostic logs.

Automatic announcements still originate from the existing first-successful-frame
hook. The bounded per-guild worker and stale-sequence filtering are preserved.
No image request or REST operation runs in the voice frame path. Existing tests
verify one announcement per actual start, no announcement for pre-audio failure,
no resolution spam, guild isolation, and responsiveness during REST failure.

## Artwork, identity and persistence

Order of preference:

1. Valid album artwork already associated with the requested Spotify track.
2. Existing SQLite catalog artwork with the **same Spotify Track ID**, recovered
   before queue insertion if the incoming metadata lacks a valid cover.
3. Selected video's supplied thumbnail, or a thumbnail URL derived from its
   validated Video ID.
4. A muted Discord-hosted default image, with `No artwork available` alternative
   text, individually attached as each missing song's thumbnail accessory.

The placeholder is Discord's gray default-avatar asset at
`https://cdn.discordapp.com/embed/avatars/1.png`, not an album identity or a newly
generated image. A syntactically valid but expired/missing remote image may still
fail to load in Discord. The bot does not download images to probe that condition.

Album-art recovery uses the existing catalog, with one shared 200 ms lookup budget
for an entire collection. Errors skip enrichment and preserve normal resolution.
Existing valid incoming covers are not replaced. A catalog upsert with missing
cover data preserves the old album artwork atomically; a valid new cover can
refresh it. No schema migration, duplicate cache, or image binary storage is
needed. The existing catalog freshness lease remains unchanged.

Spotify Track ID, Video ID, title, artists, album, duration, both artwork URLs and
identity evidence retain their existing durable storage paths. Provider metadata
and update timestamps are in `music_catalog`; Spotify-to-video evidence remains
in `mappings`, while per-guild playback state retains the full queued Track.
Neither matching keys nor Fidelity/Force Play selection rules change.

Optional YouTube-to-Spotify enrichment was evaluated and **not added**. A direct
YouTube request does not by itself establish a Spotify recording identity. Title
similarity cannot justify an album cover. Existing trusted Spotify identity can
supply a cover; otherwise direct YouTube tracks keep their own Video ID and
thumbnail. There is no background title search, and `/queue` performs no lookup or
image download.

## Complete queue delivery

One full block uses:

| Component | Count |
| --- | ---: |
| Container | 1 |
| Heading Text Display | 1 |
| Small separator, without divider | 1 |
| 12 × (Section + Text Display + Thumbnail) | 36 |
| Range/block footer Text Display | 1 |
| **Total** | **40** |

Twelve rows is the maximum under that layout. A current row uses one row in the
first block but has no future position number. Subsequent blocks continue future
positions without gaps, repetition or manual navigation. `/queue limit:N` selects
future entries before splitting. Rows beyond the limit are deliberately excluded;
without a limit, all logical positions are rendered. Empty queues still use one
small container, and current-only queues display that song's artwork.

The original deferred response is edited into the first block. Remaining blocks
are sequential public followups, with DisGo owning bucket waits and 429 retries.
Each request has a 20-second bound and the delivery sequence has a 12-minute bound,
leaving margin before token expiry. The first card starts with a short progress
notice; successful complete delivery clears it with an edit. There is no blanket
retry of an ambiguous followup failure, which could duplicate already accepted
messages. A failure stops further blocks and updates the first card with the
confirmed upcoming count. An unreachable Discord endpoint can also prevent that
notice; both failures are logged.

A new `/queue` in the same guild/channel cancels the prior delivery. Already
accepted messages remain, and the old original card receives a replacement notice
when possible. Other channels and guilds are independent. Cancelling local work
cannot recall a message already accepted by Discord. Shutdown cancels requests.
No voice/player lock is held during rendering or delivery.

A personal-only installation needing more than six total messages receives an
explicit server-installation/limit explanation before any song block is sent.
We do not change the full-list contract to fit five followups. Bot permissions,
rate limits, token expiry, network failure, and channel removal may still prevent
complete delivery; no offline test establishes unconditional network delivery.

## Measurements

Local Go 1.27 benchmark, AMD Ryzen 7 9800X3D, 2026-10-19. Source snapshots are built
before the timed region. Fixtures have valid individual Spotify artwork and no
current track. These are render/allocation measurements, **not Discord latency**.

| Future songs | Render time/op | Allocated bytes/op | Components across blocks | Messages | Missing art |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 10 | 8.572 µs | 9,500 | 34 | 1 | 0 |
| 123 | 104.380 µs | 118,845 | 413 | 11 | 0 |
| 500 | 425.586 µs | 490,966 | 1,668 | 42 | 0 |
| 1,000 | 861.851 µs | 986,983 | 3,336 | 84 | 0 |

Allocations are cumulative bytes/op, not measured resident memory. The same
message counts apply at 123, 500, and 1,000 future songs with current included.
Other counts can need another block when current crosses a 12-row boundary.
At 500–1,000 songs, dozens of messages are unavoidable with individual native
thumbnails. Larger batches would exceed the component budget; no title/image is
silently dropped to reduce channel volume. Users may explicitly choose a limit.

`queue_render` logs microseconds, component/message counts, shown/total positions,
and missing artwork. `queue_delivery_complete` logs total render/send time,
confirmed positions/messages and success. Send failures and failed notices are
logged separately. `playback_announcement_delivery` retains first-frame-to-REST
completion timing and artwork diagnostics. CDN load times and live perceived
response times require Discord-client observation and were not measured here.

Reproduce:

```sh
go test ./internal/discord -run '^$' -bench '^BenchmarkQueueV2/' -benchmem -count=1
python3 scripts/check_ui_mutations.py
```

## Validation and remaining acceptance

Automated coverage includes 0/1/10/123/500/1,000 upcoming positions; current-only
and current-plus-future layouts; limits 1/10/50/above availability; exact order and
individual artwork for every row; missing/invalid image fallback; blocked/import
state concealment; nested component/text budgets; snapshot resolution races;
complete followup delivery; partial failures; real DisGo JSON encoding and 429
limiter behavior; cancelled delivery scope; personal-installation ceiling;
matching/current artwork; and persistence/cache failures and restart.

Five targeted UI mutants are rejected: replacing all artwork with placeholders,
truncating after the first block, shifting position numbers, exposing readiness,
and giving YouTube artwork priority over Spotify. The script mutates disposable
source copies, never the checkout; this is not an exhaustive mutation score.

Required offline checks are `go test ./... -count=1`, `go test -race ./... -count=1`,
`go vet ./...`, and `docker compose build`. Fidelity, Force Play, streaming and
player regression suites remain in those checks. No player state machine was
rewritten. All four required checks passed locally on 2026-10-19, along with
the five targeted UI mutants.

**Live Discord E2E was not run.** Remaining acceptance: native card appearance and
thumbnail crop on desktop/mobile, live guild-installed long-queue followups,
actual permissions/rate limits, CDN availability, audible playback, and the
first-frame-to-visible-card delay. The left-image mockup is not claimed as met.
