# CR-007.1 — Auto Pilot

A real false negative is confirmed in the baseline matcher: the verified catalog
upload `mhXmqO7hCfA`, **Auto Pilot (Album Version)**, failed both full and flat
video comparison with `extra_title_content` (title precision 0.5, below 0.65).
YouTube Music returned the same ID with the short title **Auto Pilot**; that
summary could pass discovery, but full metadata still failed on the suffix.

## Identity and evidence

The existing Compose volume `whiterun-music-bot_bard-data` contains Spotify ID
`3hWu4Z5IkREyWNT4zuuXM5` in `music_catalog`. The metadata was timestamped
`1792497187` and had no matching Auto Pilot row in `mappings`. No Bard container
was running and only `bard.db`, without WAL/SHM files, was present. The volume
was mounted read-only and copied into isolated tmpfs for SQLite inspection;
integrity was `ok`. The production volume was not changed.

The supplied playlist `6BL3uZVI7Q41TtpsLbmmLt` was resolved by the production
Spotify constructor at 09:10:37 -03:00: all **302 tracks** were returned via API,
and Auto Pilot was at **position 4**, with the same ID and metadata. The direct
client-credentials playlist probe had returned HTTP 403; the configured production
constructor, including its refresh-token access path, succeeded. Selected
metadata is captured in [playlist evidence](auto-pilot-playlist-evidence.json).

A fresh Spotify Web API lookup agreed with the stored title, artist, album,
duration and ISRC:

| Field | Spotify | YouTube full metadata |
|---|---|---|
| Title | Auto Pilot | Auto Pilot (Album Version) |
| Artist | Queens of the Stone Age | Queens Of The Stone Age |
| Album | Rated R - Deluxe Edition | Rated R - Deluxe Edition |
| Duration | 240.986 s | 241 s |
| ISRC | USIR10000329 | absent |
| Provenance | Spotify catalog | verified artist channel, Universal Music Group catalog description, auto-generated release |
| ID | 3hWu4Z5IkREyWNT4zuuXM5 | mhXmqO7hCfA |

The 14 ms duration difference, exact album/artist, verified channel and catalog
release description support recording identity without inventing an absent
YouTube ISRC. Separate full format extraction found public audio-only AAC/Opus
formats 139, 249, 250, 140 and 251. Metadata and format commands exited 0 with
empty stderr. Audio downloading, CDN streaming and Discord playback were not
validated.

## Candidate inspection

The committed real video discovery fixture contains ten results. After the fix:

| Video ID | Provisional result |
|---|---|
| mhXmqO7hCfA | accepted; full metadata confirms first |
| FwzbLtJrX2E | version_or_semantic_mismatch (live) |
| Iy1XHQQ-qiQ | duration_mismatch (520 s) |
| gPzS3sC2VRw | extra_title_content |
| FW7WunYvj5g | extra_title_content |
| tM9YfWN1rcE | extra_title_content |
| 9Cula-Bx0xk | extra_title_content |
| 9IKUKOpq7w8 | summary permits verification; not selected after first confirmation |
| Xn1_B8IR-Ac | version_or_semantic_mismatch (live) |
| rsPCx_G5tFM | extra_title_content |

These are decisions replayed from captured summaries, not invented historical
log entries. Nonselected candidates have not been fully verified and are not
asserted to be equivalent recordings. YouTube Music's first result is the same
confirmed ID; other music results do not replace the first confirmed match.

## Correction and reproduction

`recordingTitle` removes only a terminal, case-insensitive `(Album Version)`.
It does not remove arbitrary parenthetical descriptions. Original artist,
credit, duration, source provenance and live/remix/cover/version checks still
apply to full metadata. Discovery order and FIRST CONFIRMED MATCH WINS are
unchanged. No special Spotify/video mapping or song-specific rule was added.

Before the correction, `TestAutoPilotRealAlbumVersion` failed and reported
`extra_title_content` for the first candidate. With the correction, both real
video and real music discovery stop after one full verification. Run:

```sh
go test ./internal/media -run TestAutoPilot -v -count=1
python3 scripts/check_auto_pilot_mutations.py
```

The tests also reject covers, live versions, remixes, wrong artists, excessive
duration and extra song titles. A controlled subprocess test exercises the
production Resolver and its resolution cache using captured full metadata;
a repeat avoids an additional extraction. Full-suite tests continue to cover
persistent mapping validation/reuse, invalidation, Force Play and Spotify Fidelity.
Targeted mutations catch removal of suffix normalization, bypassed duration
checks, discarded first confirmations and unstable resolution-cache keys.

Fixtures and extraction provenance are in
[`internal/media/testdata/auto-pilot`](../internal/media/testdata/auto-pilot/README.md).

## Remaining historical correlation

The supplied 302-track playlist is now correlated to this Spotify ID at position
4. Corresponding historical resolution logs and the runtime image from the
reported session were not available. The title suffix is a confirmed baseline
false negative for that exact recording, but the precise historical announcement
may also have involved unavailable extraction, a custom match policy, cache
state or a different binary. Resolving that final linkage requires the original
session logs/image. No real Discord E2E approval is claimed.
