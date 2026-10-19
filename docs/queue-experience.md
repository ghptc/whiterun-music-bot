# CR-001 — Queue experience

## Command contract

`/queue` selects every upcoming logical position from one consistent snapshot.
`/queue limit:10` and `/queue limit:50` select the next 10 or 50 positions.
`limit` is an optional native integer with a minimum of 1, also checked in the
handler. Values above availability select everything available. The current track
is always included separately and does not consume the future-track limit.
Pending tracks count as future positions. There is no manual pagination and no
text-command interface; Unix flags are not accepted as slash options.

The response stays public, available to guild members without joining voice.
Registration inventories the selected scope, upserts changed Bard definitions and
verifies all contracts by readback. It preserves command IDs and unrelated commands.
Restart the bot or run `bard commands-sync` with the normal Discord environment.
Failures are logged and fail startup/the CLI. See [CR-002 findings](queue-artwork-hotfix.md)
for the real REST audit and the remaining client acceptance.

## Presentation and complete delivery

Each response has a current/paused/starting heading, total upcoming and overall
counts, import/failure/progress information, and an explicit selected count when
`limit` is supplied. The current card has a gold accent and play marker. Up to ten
cards (including current) show title, artist, requester and resolution state, with
individual artwork when known. Native card labels are bounded in UTF-16 units;
the exports keep full titles, artist names and requester metadata.

Nonempty selections include two files:

- `queue.txt`: every selected future position, plus current, in order; complete
  metadata and artwork URLs, readable without an image-capable client.
- `queue.html`: a responsive gallery of the same complete selection. Download and
  open in a browser. Current is highlighted; each row has its own artwork or a
  musical-note placeholder. No scripts or playback controls. Remote HTTPS images
  load lazily when viewed; opening the gallery contacts image providers.

For hundreds of tracks this hybrid deliberately keeps the channel preview small
while leaving every selected position accessible in both files. Files contain a
snapshot, not a live queue. A later query gets a new snapshot. No hundreds of
images are generated or attached. Empty queues without a current track need no
attachments. The preview explicitly points to the complete list and gallery.

Discord supports at most ten embeds and 6,000 combined embed text characters per
message; content has its own 2,000-character limit. The implementation stays below
these limits rather than multiplying messages per track. Sources:
[message limits](https://github.com/discord/discord-api-docs/blob/main/developers/resources/message.mdx),
[interaction responses and files](https://github.com/discord/discord-api-docs/blob/main/developers/interactions/receiving-and-responding.mdx),
[rate limits](https://github.com/discord/discord-api-docs/blob/main/developers/topics/rate-limits.mdx).

## Artwork and identity

`media.Track` retains `SpotifyArtworkURL` and `YouTubeThumbnailURL`. The priority
is a valid Spotify album cover, a valid extracted YouTube thumbnail, then the
standard thumbnail derived from a validated YouTube video ID. With none, native
cards omit the image and the gallery shows a placeholder. HTTPS URLs are validated
before use; invalid schemes, credentials, ports and malformed URLs fall through
to the next source. An image provider may still return a missing/expired image;
URL validation does not prove remote availability.

Spotify API track/playlist entries supply `album.images`; album imports inherit
root album `images`. Public playlist rows and Embed entries preserve individual
covers when exposed; hydrated track Embeds use their cover-art sources. Metadata
without artwork remains valid. Matching copies only the YouTube thumbnail along
with its audio locator, preserving the Spotify cover and identity. Pending rows
can therefore display Spotify art before matching. Persisted Spotify metadata
without an audio locator is labeled as awaiting resolution after restart.

SQLite already stores Track metadata as JSON, so both optional URLs survive queue,
current/history and player recovery without a schema migration. Older records
without the fields remain readable. Artwork is excluded from recording mapping
keys, preserving identity/cache semantics. Matching/ranking, first confirmation,
queue mutations and audio delivery are unchanged.

## Performance and failure handling

The handler defers the public interaction immediately, obtains `player.State()`
once, then renders and sends without holding player/guild locks. The renderer has
no network/provider/player dependencies. It does not resolve songs, fetch artwork
or download images. Repeated queries reuse stored URLs. Gallery generation is
linear in the selected queue length, bounded by the existing 1,000-future-track
capacity. No disk files are created by queue queries.

The normal delivery is one multipart edit of the deferred original response, with
two metadata-only attachments. This fits supported queues without follow-ups.
The existing disgo REST limiter owns bucket waiting and 429 retries, including
replaying the entire multipart body. Queue delivery has a 25-second shared context,
well within the interaction token lifetime; no application retry loop sends
per-track messages. A longer advertised rate-limit wait fails promptly rather
than ignoring the API's Retry-After.

If rich delivery fails, a second edit attempts the complete textual export with
an explicit artwork-unavailable notice and removes embeds/old attachments. If
that also fails, a final content-only notice says delivery was incomplete and
suggests retrying. That final notice has a separate three-second deadline. Failures
are logged; playback/queue are never changed. If Discord also rejects the failure
notice, only the server log can report that failure. Delivery cannot be guaranteed
through Discord outages or unavailable attachment permissions.

The combined export budget is conservatively capped at 8 MiB and the interaction's
advertised attachment limit when smaller. If the gallery cannot fit, delivery
uses the full text export. If text itself cannot fit, the response explicitly
states that no complete list was delivered and suggests a smaller `limit`;
metadata is never silently cut. This is a bounded delivery strategy, not a promise
of unlimited output in a single interaction.

## Deterministic validation

| Cases | Coverage |
|---|---|
| 1–7: empty, current only, pending, no limit, 1, 10, above availability | `TestQueueCompleteAndLimitedSnapshots`, `TestQueueCurrentPendingAndArtwork`, handler tests |
| 8: zero/negative | `TestQueueLimitValidationAndGuildIsolation`; slash integer registration test |
| 9–11: 123/500 tracks, order | Snapshot tests also cover 1,000 future positions; text/gallery tail and order assertions |
| 12–15: Spotify, YouTube fallback, absent, invalid URLs | `TestArtworkPriorityAndInvalidURLs`, API/Embed artwork tests, current/pending presentation tests |
| 16: partial send failure | `TestQueuePartialDeliveryFallback`: rich failure, text failure, failure-notice failure |
| 17: rate limiting | Real disgo encoder/limiter with fake HTTP 429, Retry-After, complete multipart retry; long-wait test |
| 18: concurrent resolution | `TestQueueSnapshotDuringConcurrentResolution`, existing progressive handler coverage; detached snapshots |
| 19: guild isolation | Handler isolation test and persisted second-guild metadata |
| 20: restart with persisted queue | `TestArtworkSurvivesDatabaseAndPlayerRestart`: real SQLite close/reopen and player recovery |
| Discord/API budgets and escaping | Worst-case emoji/HTML injection, attachment limit, native embed total |
| Responsiveness | `TestQueueSlowDeliveryDoesNotHoldPlayerLock`: mutate/read player while REST send is blocked |
| Identity/cache preservation | `TestArtworkDoesNotChangeRecordingMappingKey` and existing fidelity/first-confirmed suites |

Validation commands: `go test ./...`, `go test -race ./...`, `go vet ./...`.
Tests use deterministic metadata, fake HTTP/REST/voice and real local SQLite;
no Spotify, YouTube or Discord service is contacted. Real Discord E2E was not run.
Manual follow-up should check slash registration, attachment/embed permissions,
mobile gallery downloads, remote image availability and rate limits in a real guild.
