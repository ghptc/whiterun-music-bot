# Public Spotify metadata investigation — 2026-09-23

**Runtime correction:** the host-only success recorded below preceded a real Docker `/play` failure. See [the subsequent runtime discrepancy investigation](spotify-runtime-discrepancy.md) for the DNS root cause, build identity evidence and successful production-equivalent Docker acceptance.

Regression playlist: `37i9dQZF1DZ06evO3VkoW4` (This Is Dr. Dre).
No Spotify audio, account cookies, operator credentials, or private playlists were used in the experiments. Tokens used by the API probes were extracted at runtime and never printed. Temporary raw HTML files were deleted after the investigation; only sanitized metadata fixtures remain in the repository.

## Results before implementation

- Public playlist HTML: 30 initial entries, `totalCount=50`, `nextOffset=30` in base64 JSON `initialState`.
- Public Embed HTML: 50 entries in `__NEXT_DATA__.props.pageProps.state.data.entity.trackList`. Fields include track URI, occurrence UID, title, subtitle (artist credits), type and duration. All 30 public prefix URIs matched in order. All 50 occurrence UIDs were distinct.
- Embed has no independent declared playlist total in this entity. A track-list length alone is not completeness evidence. Its playlist page bundle exposes no pagination parameters or loading operation; this is not a supported pagination API.
- Isolated result: `playlist=37i9dQZF1DZ06evO3VkoW4 resolved=50 total=50 pages=1 source=embed prefix_verified=30`. First: The Next Episode. Last: Encore.
- Regular public-page initial state did not contain an access token; Embed bootstrap did.
- The runtime Embed anonymous token returned HTTP 429 on both official `/v1/playlists/{id}/tracks` and `/items` probes. A 429 does **not** prove authorization or successful pagination. No retry flood was performed.
- Legacy public `get_access_token` returned HTTP 403.
- A separate anonymous metadata probe of `spclient.wg.spotify.com/playlist/v2/playlist/{id}`, with `Accept: application/json`, `from=0/25` and `length=25`, returned two pages of 25 entries, total 50, positions 0/25 and truncated true/false. All 50 URIs matched Embed in order. This response contains URIs and attributes, not the required titles and artist credits. Without the JSON Accept header the response was binary; no audio was requested.

## Open-source approaches evaluated

- [SoulSync's anonymous-token change](https://git.canadabot.net/canadabot/SoulSync/commit/8b060ee79a3b5731e63d6b46baa9c53f736f40da) moves extraction to Embed after ordinary public-page token extraction stopped working. Its own report observes 429, not successful full pagination. Its graceful partial fallback is unsuitable for Bard's completeness requirement.
- [Sunnify API status](https://github.com/sunnypatell/sunnify-spotify-downloader/blob/main/API_STATUS.md) and [implementation](https://github.com/sunnypatell/sunnify-spotify-downloader/blob/main/spotifydown_api.py) use Embed, then spclient URI enumeration and individual track embeds. This explains the successful URI experiment. Its track-ID position map/set and partial-result fallback cannot be copied: legitimate repeated playlist entries would be lost and incomplete collections could escape.
- [Spotify's Embed documentation](https://developer.spotify.com/documentation/embeds) supports playlist embedding, but does not promise a stable metadata extraction schema or complete arbitrary-length snapshots.

## Complete public playlists beyond 100 entries

Before production changes, an isolated experiment against `35m4wmHSlcS2RNsafX4dEg` observed:

- Public page: total 123, initial prefix 30.
- Playlist Embed: 100 entries.
- spclient JSON metadata: pages at offsets 0, 50 and 100 with 50, 50 and 23 occurrences; total 123; one stable revision; truncated true, true, false. Occurrences expose `attributes.itemId`.
- All 100 Embed URIs matched the paginated prefix in order.
- Individual track Embeds supplied the remaining titles and artists.
- Result: `playlist=35m4wmHSlcS2RNsafX4dEg resolved=123 total=123 pages=3`; first “Sexy Bitch (feat. Akon)”, last “Radioactive”.

The implemented Go resolver keeps official API → complete public HTML → independently verified complete playlist Embed. Only a truncated Embed proceeds to anonymous pagination and metadata hydration. A successful 50/50 Embed still needs neither token extraction nor spclient requests.

## Validation and bounds

The anonymous session is extracted from `__NEXT_DATA__.props.pageProps.state.settings.session`, requires `isAnonymous=true`, and uses its explicit expiration timestamp. It belongs only to the current operation, is never logged or persisted, and refreshes at most once across both expiry and HTTP 401. Other HTTP errors fail without retrying. Authenticated operator credentials and cookies never reach these requests. All spclient URLs are constructed locally; redirects are disabled. Only public metadata fields are decoded; audio URLs in HTML are ignored.

The existing 500-entry collection cap bounds pagination to ten 50-entry pages, plus one final revision check and at most one HTTP retry after refresh. Empty or short intermediate pages, incorrect offsets, totals, truncation flags, duplicate occurrence IDs and changed revisions reject the whole operation. Track IDs may repeat. Metadata fetches are deduplicated by URI, then expanded back into the occurrence order. At most four workers fetch missing track Embeds, and any metadata failure cancels the batch. Individual track identity, title, artists and duration are validated. Public HTML/Embed requests have a 15-second timeout and 8 MiB limit; the entire fallback has a two-minute context deadline. No guild/player locks are held during this work.

After hydration, the first page is fetched again and its total, revision and URI prefix revalidated. Nothing reaches YouTube matching until complete metadata is assembled. The existing structured matcher, yt-dlp/FFmpeg/Opus pipeline and atomic insertion remain the only playback path.

## Offline and live verification

Offline tests cover complete 50/123/150-entry pagination, final partial pages, duplicate track IDs, multiple artists, empty/non-advancing/repeated pages, missing fields, malformed metadata, count/revision/prefix mismatches, HTTP errors, redirect isolation, cancellation, token bootstrap failure, expiry between pages, bounded refresh and token-free errors/logs. Existing complete Embed, official API eligibility, canonical matching and incomplete fallback tests remain. Tests use synthetic session values and metadata; no live Spotify credentials are needed.

```sh
BARD_SPOTIFY_LIVE_TEST=1 go test ./internal/media -run 'TestSpotify(PublicLarge|Embed)LiveRegression' -v -count=1
```

Both live Go resolver checks passed after implementation:

```text
playlist=35m4wmHSlcS2RNsafX4dEg resolved=123 total=123
playlist=37i9dQZF1DZ06evO3VkoW4 resolved=50 total=50
```

The live tests deliberately expect these playlist counts. If Spotify edits a playlist, investigate the new count rather than weakening completeness checks. The spclient interface and Embed bootstrap schema are undocumented and may change. Missing anonymous tokens, unavailable metadata, rate limits, mismatched public counts or revisions fail closed. The complete-Embed path retains its count/prefix validation but cannot detect every unseen-suffix edit between its two snapshots. Live metadata success does not establish YouTube availability or Discord audibility.

## Queue inspection and bulk skip

`/queue` now selects the complete queue from one consistent snapshot; `/queue limit:N` restricts future positions. A bounded native artwork preview and complete text/HTML exports replace manual pagination. Pending positions retain individual Spotify covers when available. Rendering and REST delivery happen outside player locks and never request image or music resolution. See [CR-001 queue experience](queue-experience.md) for delivery budgets, failure handling and deterministic tests.

`/skip count:N` uses an optional integer (1–1001, default 1) and the player-level `SkipN` operation. It counts the current logical track, cancels it once, clears its logical state, and discards the required queued prefix in one critical section. The existing single playback loop finishes teardown before starting the next selected track. Concurrent skips cannot count the same cancelled track again. Stop/clear/enqueue operations serialize on the same mutex, and playback starts unpaused. Count greater than availability consumes everything available. When no current track exists, counting begins at the queued head. Replies say “Next”, not “Now playing”.

Offline tests cover pagination through 1,000 tracks, command option registration and parsing, default/explicit/bulk/invalid skips, 123-position order, cancellation exactly once, no intermediate playback, startup and paused states, concurrent skips, stop/clear/enqueue races, and snapshot consistency. The 123-track integration-style player test verifies skip 50 selects track 51 and preserves 52–123.

## Verification completed

`gofmt -w cmd internal`, `go vet ./...`, `go test ./...`, `go test -race ./...`, `go build ./...`, and `git diff --check` all passed. Docker image `bard:public-playlists-queue-skip` built successfully (`9033e3b79d1c`), including its vet/unit tests and runtime tool/Opus checks. Both live metadata regressions above passed. The running Bard was not restarted; Discord command options will register on its next normal startup. Live Discord playback was not exercised.

Files changed in this pass: `internal/media/spotify_anonymous.go`, `internal/media/spotify_anonymous_test.go`, `internal/media/spotify_embed.go`, `internal/media/spotify_embed_test.go`, `internal/media/spotify_public.go`, `internal/player/player.go`, `internal/player/queue.go`, `internal/player/skip_test.go`, `internal/discord/commands.go`, `internal/discord/queue_skip_test.go`, `README.md`, and this report. Earlier uncommitted repository work was preserved.
