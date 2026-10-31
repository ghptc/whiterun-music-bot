# FIX-029 — EOF transition and unchanged CI

Date: 2026-10-31. Branch: `rel028/github-verified-releases`.
Starting revision: `6c2bce057b9062b5839b077b5cc52d45bc0dda48`.
PR: <https://github.com/ghptc/whiterun-music-bot/pull/1>.

No EC2 access, restart, deployment, real provider requests or Discord Voice
sessions were performed. The original dirty checkout was preserved; changes
were made in the existing isolated PR worktree.

## Confirmed root cause

The ready-track failure is a **failure-classification/recovery defect**, not a
confirmed mutex deadlock or missing EOF signal.

The unchanged performance fixture supplies 100 ms of real Opus through local
yt-dlp stand-in/FFmpeg processes while retaining full recording metadata.
`Streamer.Play`, `internal/player/stream.go:419`, correctly detects the short
recording and returns `StreamError{Category: "audio_eof"}` after its producer and
decoder exit cleanly. This diagnostic remains unchanged, including the separate
`TestBotPlaybackRejectsWrongRecordingBeforeStreaming` assertion that requires it.

Previously `GuildPlayer.run` included `audio_eof` in `networkRetry` at
`internal/player/player.go:400`. The recovery branch copied the incomplete
current track, attached its confirmed offset, and prepended it to the queue
ahead of the ready next occurrence. Recovery could therefore retry the already
exhausted source or suspend a failed resume instead of selecting the next track.
The bounded retries did not constitute successful recovery. The ordinary
terminal-error transition and next-track selection were bypassed.

Evidence:

- Original GitHub run [`41224011104`](https://github.com/ghptc/whiterun-music-bot/actions/runs/41224011104),
  job `deterministic-ttfa`, ran SHA `6c2bce0`. The artifact reports missing
  `ready-transition/ready`, missing `load-10-guilds/warm`, and `go test exited 1`.
  Raw events contain Go's 600-second timeout and goroutine diagnostics.
- The exact existing targeted test failed locally with
  `ready EOF transition stalled` in 8.062 seconds.
- An independent diagnostic execution of the same compiled test received
  SIGQUIT after two seconds. Its player worker was in `schedulerWait`, not stuck
  on a mutex or an unreaped FFmpeg process. This interrupted diagnostic is not
  counted as a test pass; normal acceptance runs were not signalled.
- The new queue regression, run with an overlay containing the **unchanged
  pre-fix production player**, received position `1` again when position `2`
  was expected. Its failure snapshot retained position `1` with a confirmed
  offset ahead of original positions `2` and `3`. No timing or existing test
  assertions were changed to obtain that result.
- REL-028's preserved baseline execution against `62a44ef` also failed the
  original transition test at line 617 after 8.10 seconds. The relevant player
  and storage code is identical between that baseline and `6c2bce0`. The defect
  predates REL-028 and was still a release blocker.

## State machine and ownership

Normal successful EOF is unchanged:

`PLAYING → EOF → FINISHED → ADVANCING → READY → PLAYING`.

The failing fixture takes the explicit incomplete-source path:

`PLAYING → premature EOF → network retention/retry → same exhausted position → suspended recovery`.

After FIX-029:

`PLAYING → premature EOF → FAILED → ADVANCING → READY → PLAYING`.

The failed recording is **not** reported as successfully completed. It still
produces the existing failure notification; the next explicitly queued recording
is selected through the normal worker, without a replacement identity lookup.

`GuildPlayer.run` retains ownership of advancement and `trackDone`. Terminal
cleanup still clears current/cancellation fields and closes `trackDone` once.
Enqueue generation checks, cancellation, pause gating, the Voice connection,
collection ownership and wake channels are unchanged. The regression verifies
three ordered occurrences, including duplicate recordings, one advancement per
occurrence, one terminal failure event, and no Voice Close.

## Production changes

1. `internal/player/player.go:400`: remove only `audio_eof` from network-retry
   eligibility. A clean, prematurely exhausted finite source does not by itself
   prove transient network failure. Keep early-EOF detection and normal failure
   handling; do not add a retry loop, goroutine, sleep or timeout.
2. `internal/storage/sqlite.go:145`: batch the existing track metadata UPSERTs
   in groups of at most 128, within the existing `SaveGuild` transaction. Keep
   hashes, serialized metadata, progress normalization, constraints, triggers,
   ordering and rollback behavior unchanged. At 500 queued positions this cuts
   500 separately compiled SQL executions to four; it does **not** reduce rows,
   skip duplicate occurrences or change the SQLite quota.

The second change addresses measured persistence work in the same transition
critical path. The pre-batching ten-guild CPU profile attributed 60.26% cumulative
CPU to `DB.SaveGuild`; the player holds its state mutex while saving. The block
profile included 92.88 aggregate goroutine-seconds waiting through `BeginTx`,
and the mutex profile attributed substantial waiting to the player worker,
collection commit and `BeginCollection`. These are aggregate concurrent waits,
not wall time or proof of deadlock. GitHub's timeout stacks also show active
SQLite commits/physical-quota checks and other workers waiting for the shared
single SQLite connection. Local baseline ten-guild execution did complete.

A preliminary attempt to omit existing metadata writes failed the unchanged
INSERT/UPDATE trigger fault-injection tests. It was discarded. The final batch
executes **every original UPSERT and trigger**. A new late-failure regression
injects an error at position 500 and verifies that preceding batches and the
guild snapshot roll back together. No existing test was edited.

FIX-025's retry/50% policy remains for actual network/transport interruptions,
stalls, producer/decoder failures and existing failed-resume preservation.
REL-024 watchdog and OPT-026 admission/prefetch/cache/matching code are unchanged.
An `audio_eof` does not open the global provider breaker. A source that exits
cleanly but too early now follows the existing terminal-source-failure policy;
it is not retried merely because metadata predicts a longer duration.

## Logstore investigation

The earlier Quality failure occurred in
`TestConcurrentWritesBatchAndFinalFlush`, with `context deadline exceeded` at
the test's existing three-second Close deadline. `Store.Close` closes admission
under its mutex, closes the queue once, and waits on `done`. The single writer
drains that channel, flushes its final batch, closes resources and finally
signals `done`. No lost completion signal or shutdown-order defect was found.

Twenty profiled targeted iterations under race passed (13.018 seconds including
startup/profiling); the final full race suite also exercises logstore. The profile
attributes most writer CPU to SQLite writes. Additional race/SQLite and full-gate
experiments can compete for CPU and disk; scheduling or I/O pressure is plausible
for a fixed deadline, but the original timeout was **not reproduced or diagnosed
conclusively**. No logstore code, timeout, batch limit or assertion was changed.
Do not claim the historical logstore timeout is fixed.

## Validation and evidence

Existing commands, workflow definitions, runner configuration, budgets, sample
counts, assertions, execution filters and evaluator were preserved. The diff
against `6c2bce0` for `.github/workflows`, `internal/perfgate`, the performance
test and both evaluator scripts is empty. No ruleset or required check changed
during FIX-029. New tests add coverage rather than altering existing expectations.

Local final code passed:

- `go test ./... -count=1`.
- `go vet ./...`.
- `go test -race ./... -count=1` (including logstore).
- Identity corpus: 14 positives, 31 negatives, zero false positives.
- FIX-025, REL-024, OPT-026 provider recovery, source identity, queue and SQLite
  regressions, including the original trigger fault injection.
- Release verifier's existing policy tests and `govulncheck ./...` with no
  reachable vulnerabilities; `go mod verify` passed.

Preliminary overlapping experiments did produce failures: the rejected metadata
write-elision candidate broke trigger tests, and concurrent heavy validation hit
fixed first-frame deadlines in several scenarios. These logs are preserved;
they are not represented as passing runs or proof of resource-exhaustion root
cause. Final acceptance is performed sequentially on the corrected application.
Each rerun follows a concrete implementation correction or separation of the
diagnostic workload; CI is not rerun until green without investigation.

Docker and the final original gate both exited zero in their sequential
acceptance run. All 30 scenario reports passed with the original sample counts.
The package completed in 237.293 seconds. Ten-guild goroutines returned from
3 before the scenario to 3 after cleanup, with the existing four-match limit.
Evidence is in `evidence-fix029/final-gate.json`, its compressed complete raw Go
events, `docker.txt`, and `build-gate-status.json`. The unchanged command:

```sh
python3 scripts/instant_playback_gate.py --run --output gate-artifacts/deterministic.json
```

The output path only locates the report; Go arguments, 100 samples per scenario,
timing checks and evaluator are those defined in the existing script.

| Scenario | Original local p95 (ms) | Final p95 (ms) |
| --- | ---: | ---: |
| ready transition | Failed: no next frame within 8 s | 23.799 |
| ten guilds, warm | 341.721 | 276.420 |
| 123 positions, cold / warm | 55.663 / 30.911 | 53.031 / 27.887 |
| 500 positions, cold / warm | 143.563 / 50.054 | 137.934 / 45.121 |

The baseline full gate failed in 251.616 seconds because the ready-transition
scenario failed. GitHub's original gate timed out at 600 seconds before the
ten-guild scenario completed. Host speed and diagnostic overhead differ; these
are controlled offline fixture measurements, not YouTube HTTP request counts
or real Discord audibility measurements.

## GitHub verification and remaining risks

### First fix commit: GitHub timeout still blocked release

Commit `e77be67da5f1a9119e0612ec95b986d78be17727` passed Release CI and
Quality on both push and PR. Its unchanged performance workflow failed:
[PR run 41228033355](https://github.com/ghptc/whiterun-music-bot/actions/runs/41228033355).
It timed out after 600.027 seconds during `load-5-guilds/warm`; ten-guild and
ready-transition scenarios therefore had not executed. No rerun was requested.
The preceding local pass did not establish a GitHub pass.

The new timeout has direct I/O evidence: goroutine `2308062` was in
`libc.Xfsync → SQLite unixSync → quotaSync → RecordPlayback → announceStart`,
while `2305472` waited for the same SQLite connection in
`SaveGuild → persistLocked → announceStart`. The fixture owner was waiting in
`GuildPlayer.Close` for the player's mutex. This is synchronous first-frame
storage contention, not proof of a permanent lock cycle. SQL statement batching
alone was insufficient on the CI storage device.

The follow-up application correction uses an optional `PlaybackCheckpointStore`
contract implemented by the same SQLite DB. Selection and first-frame handling
checkpoint the already-durable queue without re-executing all its metadata
UPSERTs. Current-track metadata is still saved. At first start, checkpoint,
history and the original play event are committed in one transaction rather
than a full queue save followed by a second synchronous event transaction.
The actual playback event retains its original metadata and is omitted for a
resume, preventing duplicate history events.

Queue mutations still use the full original `SaveGuild` path, including every
track INSERT/UPDATE trigger. The fast checkpoint requires the mutation owner's
clean persistence state; a failed preceding mutation takes the full repair path.
Stores without the optional capability retain the original behavior. The same
existing bounded dirty-checkpoint recovery handles write failures; no retry
layer, goroutine or scheduler was added. An atomic write error is not followed
by an independent event retry, so an uncertain commit cannot duplicate a play
event. As before, a failed play-event write is an audit limitation and is not
claimed to have succeeded.

`TestFIX029PlaybackCheckpointAndEventCommitAtomically` verifies persisted queue
duplicates, lack of future-track rewrites, no event duplication on resume, and
rollback of checkpoint/history if event insertion fails. FULL synchronization,
physical quota, database files, healthchecks and CI remain unchanged.

The measurements above describe `e77be67` locally. The follow-up checkpoint
correction then passed the full sequential local validation: Go suite 63.662 s,
vet 0.220 s, full race 103.703 s, Docker 84.842 s, and the exact unchanged gate
221.777 s wall time. All 30 gate scenarios passed. Final p95 measurements:

| Scenario | Checkpoint correction p95 (ms) |
| --- | ---: |
| ready transition | 24.713 |
| ten guilds, warm | 210.451 |
| 123 positions, cold / warm | 50.270 / 25.613 |
| 500 positions, cold / warm | 131.731 / 39.935 |

These final local logs, complete compressed test events and machine-readable
status codes use the `checkpoint-` prefix in the evidence directory. The follow-up
still requires exact-head GitHub validation; local results are not substituted
for those checks.

### Identical trees, incomplete CI margin

Commit `810e5fa0aebaa53049aea4499e79b4d13c573f6f` passed Release CI and Quality
in both events. Its [push performance run 41230576150](https://github.com/ghptc/whiterun-music-bot/actions/runs/41230576150)
passed all scenarios in 469.894 seconds. Its
[PR performance run 41230580227](https://github.com/ghptc/whiterun-music-bot/actions/runs/41230580227)
timed out at 600.011 seconds before finishing five guilds. This was **not** an
all-green HEAD, and neither run was retried.

The PR checked out synthetic merge `926b394de9f98c518126c1fc32d98637842036ae`;
GitHub's Git API confirmed both revisions have the identical tree
`61ad7f3a22dc497fb1f393347bd08f869c16879d`. Both hosts had four logical CPUs;
the artifacts identify an EPYC 9V45 for push and EPYC 7763 for PR. That records
environment differences, not proof that CPU model alone caused the timeout.
The application needed more margin on the unchanged runners.

The next correction removes remaining repeated work in the same persistence
path. A resolution owner now supplies only the changed track metadata to the
existing SQLite transaction, together with the **complete** guild/queue/history
snapshot and current metadata. Unchanged positions were durably stored at
reservation or their last mutation. Dirty prior writes and stores without this
optional capability still use full `SaveGuild` repair. Canceled or stale results
retain their original checks and cannot resurrect queue entries. This replaces
500 metadata UPSERTs with one when one position resolves, without discarding any
queue position or duplicate occurrence.

`SaveCatalogBatch` also groups the same existing source-metadata UPSERTs in
128-row statements. It retains the exact metadata whitelist, artwork merge,
per-row triggers, quota accounting and single transaction. It does not skip
identity validation or alter provider access. New tests verify untouched duplicate
positions and rollback/quota restoration after invalid metadata at position 500.

These are further application corrections justified by the failed runs; no
runner, timing budget, sample count, assertion, evaluator or required check was
changed. Sequential local validation passed: Go suite 64.518 s, vet 0.178 s,
full race 105.102 s, Docker 82.719 s, and the original full gate 188.233 s wall
time. All 30 scenarios passed with their unchanged sample counts and budgets.

| Scenario | Metadata-delta correction p95 (ms) |
| --- | ---: |
| ready transition | 24.753 |
| ten guilds, warm | 180.066 |
| 123 positions, cold / warm | 41.987 / 24.574 |
| 500 positions, cold / warm | 105.295 / 41.497 |

Complete local evidence uses the `sparse-` prefix. Exact-head GitHub results
remain required before calling the fix complete.

The fix must be pushed to the existing PR and all workflows observed on its exact
HEAD: `Release CI`, `Quality`, and `Instant playback performance gate`, including
all seven required checks. A local pass does not satisfy this requirement.
The final result matrix will be linked here after those runs finish. No merge
or production operation is authorized by successful tests.

Remaining limits:

- The historical logstore deadline failure remains unexplained.
- Clean early EOF can be caused by bad source data or silent truncation. Without
  a transport/process failure there is no proof that retrying will recover it;
  the recording is explicitly failed and the next queue occurrence advances.
  Provider challenges and real interruptions keep their own preservation policy.
- Profiles perturb timing and are diagnostic evidence only. Final gate results
  must come from its unmodified command without profiling instrumentation.
- No real YouTube extraction, EC2 rollout or Discord audio verification was done.
