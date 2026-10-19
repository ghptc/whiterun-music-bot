# Queue experience — current contract (CR-006)

`/queue` displays all upcoming logical positions from one detached snapshot.
`/queue limit:10` and `/queue limit:50` show only the selected future positions.
The optional integer must be at least 1; values above availability show everything
available. Current is always separate and never consumes the requested limit.
Unresolved collection entries keep their positions, metadata and artwork.

The response is public and requires no voice connection. Application command
names and option schemas are unchanged. The initial response is deferred promptly.
Registration still inventories, upserts, and verifies the selected scope without
removing unrelated commands.

The presentation uses native Components V2: one container per message, up to 12
song sections, one individual thumbnail for every song, and short title/artist
labels. Current appears once, before numbered future songs. There are no
interactive controls, manual pages, exports, or attachments. Sections use the
native accessory position; the API has no thumbnail-left layout option. The
supported alternative is text left and small artwork right. See the complete
[investigation, component budgets, and benchmarks](bard-ui-v2.md).

Spotify artwork wins over the selected YouTube thumbnail. Matching never replaces
an album cover with a video image. Before queue insertion, missing Spotify artwork
can be recovered from the existing SQLite catalog by exact Spotify Track ID.
Nothing resolves or downloads during rendering. Every missing/invalid image gets
a Discord-hosted placeholder with accessible alternative text. The bot cannot
detect CDN 404s without downloading images, which it deliberately does not do.

Messages are delivered sequentially through DisGo's REST limiter. The original
response holds the first block; subsequent blocks are public interaction
followups. A footer identifies each range and block. A final edit clears the
initial delivery-progress notice. On partial failure, further sends stop and the
first card reports the confirmed count. Already delivered blocks remain visible.
If the notice itself cannot be delivered, the failure is logged; there is no
silent export fallback or claim of successful complete delivery.

A new queue query in the same guild/channel cancels an older unfinished sequence.
Different channels and guilds remain independent. Playback and queue mutations
remain responsive during REST waits. A snapshot is historical and does not update
when playback advances. A followup accepted by Discord cannot be recalled by
cancelling its local request.

At the chosen component budget, 123 future songs use 11 messages, 500 use 42, and
1,000 use 84 (the same counts for those sizes with current included). Very large
native lists therefore generate many messages; images cannot be moved into
exports or silently omitted to reduce that count. `/queue limit:N` remains an
explicit user choice. Server installation is required when more than five
followups are needed. A personal-only installation receives an explanatory
message instead of a silently truncated list.

The renderer has no player, resolver, database, HTTP, or filesystem dependencies.
Existing Track JSON persists both artwork URLs and recording identity across
restart. No audio, image binary cache, Redis, or duplicate catalog is introduced.
