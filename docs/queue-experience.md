# Queue — CR-007.1

`/queue` works with no options. `limit`, `from`, and `to` are independent,
optional positive integers, in that order in the Discord contract. One detached
player snapshot supplies current and future tracks. Rendering performs no media
or artwork requests and does not hold player locks.

- `/queue`: current song, then future positions until the message budget is full.
- `/queue from:60 to:70`: future positions 60 through 70 inclusive.
- `/queue from:60`: start at 60 and fill the budget.
- `/queue limit:25`: at most 25 future positions.
- Combined options intersect the inclusive range with the maximum count.

Explicit positions outside the snapshot or a reversed range return an error in
the original response. Empty queues without explicit positions remain valid.
Current does not consume a future position or the upcoming artwork allowance.
The first ten displayed future positions use individual artwork when available;
a missing image does not suppress images on subsequent preview positions.
Later positions share a compact Text Display. Titles are escaped, never clipped;
Unicode is retained. A complete row that cannot fit stops the listing.

## Discord budgets and delivery

The [Discord component reference](https://docs.discord.com/developers/components/reference)
defines 40 components per message, including nested components. A Section holds
one to three text children plus its accessory; ours has one Text Display and one
Thumbnail, so costs three components. Container is a grouping component, not an
additional text allowance. The [discord.js component guide](https://discordjs.guide/legacy/popular-topics/display-components)
specifies an aggregate 4,000-character Text Display budget; the current Discord
reference does not explicitly specify a separate numeric Text Display cap. We
use that compatibility budget and conservatively measure UTF-16 units. The heading, all
rows, inter-row newlines and complete candidate footer share this budget. A
footer is a Text Display, not an embed footer with a separate allowance. V2 uses
neither ordinary message content nor embeds. No independent larger Text Display,
Section or Container allowance is assumed.

The footer reserves its space before each row: exact displayed future count,
total future count, count not displayed (including positions before `from`),
and next position after the displayed range when one exists. An oversized current
song is explicitly reported as omitted. The listing does not skip oversized
future songs to fit smaller ones later.

An interaction defers once and PATCHes `@original`. No follow-ups, channel
messages, continuation messages or exports are created. On edit failure it
attempts a bounded error edit to the same original. A failed acknowledgement
stops before snapshot/rendering. REST 429 retries target the same message.

## Outdated investigation — 2026-10-20

Read-only authenticated API requests using this project's configured application
returned global `/queue` ID `1548720833114153074`, version
`1562070939611439114`. All three options are type 4 (integer), minimum 1,
with `required` absent (false), ordered `limit`, `from`, `to`, matching local
code. The bot belongs to two guilds: neither has a guild-scoped `/queue` override.
No commands were published or deleted during this inspection.

Startup calls `SyncCommands` before opening normal operation. Sync inventories,
upserts changed definitions by name/type, then reads back and compares the
published contract. Tests cover both global and guild scopes, stale options,
readback failures and preservation of unrelated commands. `TestCR007PublishedQueueContract`
adds the observed global queue contract as an offline regression input.

`docker compose ps` returned no Bard containers; the full container inventory
contained no Bard runtime. Therefore the binary, startup sync logs, command ID
and version at the reported failure cannot be correlated here. The current
published contract rules out missing flags as an explanation for today's
contract. Earlier propagation/client cache or an older deployment remain
unverified hypotheses. Capture the affected interaction's command ID, time,
startup sync logs and runtime image before assigning a root cause.

## Validation

Coverage includes 300 future tracks, default/independent/combined options,
inclusive ranges, invalid positions, current, missing artwork, long Unicode,
whole-row budget exhaustion, exact footer counts, no extra message, failed
acknowledgements, update failures and HTTP 429. `scripts/check_ui_mutations.py`
runs targeted mutants in disposable copies and rejects compile failures as
mutation evidence. Force Play, Spotify Fidelity and mapping-cache regressions
remain in the full Go suite.

Local unit/race/vet and Docker build results are recorded in
[CR-007.1 validation](cr007-validation.md). Real Discord interaction/rendering
and audio playback have not been homologated by this change.
