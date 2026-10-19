# Requirement → test → boundary audit

Audited 2026-09-27 against the current working tree. Requirement IDs refer to
[bard-requirements.md](bard-requirements.md). This table groups tests sharing a
boundary; the inventory below names every test function in the related packages,
including adjacent ranking/metadata/diagnostic tests. “Can pass while broken”
means the real second-track symptom can coexist with the assertion passing; it
does not mean a focused unit test is useless.

| Requirement | Production responsibility | Existing tests / family | What actually runs and asserts | Substitution / untested behavior | Can pass while real Bard fails? |
|---|---|---|---|---|---|
| P1/P2 start and EOF advancement | player.run | TestQueueAdvanceSkipFailureAndStop; TestBatchOrderClearAndCapacity | Queue, state copies, channels representing start and completion | Whole PlayFunc replaced; no extraction, EOF pipe, decoder, audio or voice | Yes; title sent before any media work |
| P2 exactly once during controls | player.run, SkipN, Stop | TestNaturalEOFRacesWithSkipAndStop | Two racing goroutines, synthetic completion, successor count <=1, cleanup | No real EOF/audio; allows zero successors; Close precedes assertion | Yes; cannot prove successor liveness |
| P2/C3 many sequential pipelines | Streamer, Command, player.run | TestPipelineSoak125Transitions | 125 tracks, 250 child PIDs, process waits, aggregate first-frame announcements and event counts | Shell source/decoder, one synthetic byte accepted by sink, no provider or per-track audio identity | Yes; explicitly allows isolated provider errors and queue consumption |
| P3 fresh contexts | player.run, Streamer, cache.resolve | EOF tests; TestCoalescedSearchHasIndependentCancellation; TestAllWaitersCancelSearchAndFailuresAreNotCached | Synthetic track cancellation; cache waiter ownership/counts | Cache work is channel callback, not pipeline; no integrated successor media | Yes; cache independence is not playback independence |
| P4 actual valid media | Streamer, oggPackets | TestStreamerLocalFFmpeg | Real FFmpeg from short local WAV, frame count and Speaking=false | Fake yt-dlp; one stream; count includes silence trailers | Yes; no second track/provider/Discord |
| P4 long producer drain | Streamer yt.WaitDelay=0 | TestStreamerDrainsAfterProducerExit | Real FFmpeg and five-second local Opus fixture, paced drain, frame count | No player transition or external provider; FFmpeg-dependent skip | Yes; useful drain check, unrelated to challenges |
| P4 startup stages | Streamer timing/writers | TestStreamResolutionMarkerAcrossWrites; TestStreamerReportsStagesBeforeVoiceFailure | Fragmented marker parsing, bounded diagnostics; real FFmpeg and delayed local sink | No real failing voice adapter; title of latter is stronger than actual success assertion | Yes; no second stream or UDP |
| C3 startup deadlines | watchStartup | TestStartupDeadlinesEndAtFirstOpus | Milestone channels, timeout messages and watcher completion | Artificial readiness/Opus; wall-time 20ms deadlines | Yes; not a process/provider test |
| C3 process failure/cancel | Command, Streamer | TestStreamerFailuresAndCancellation; TestStreamerMissingExecutable | Real shell children, killed stalled pipeline, nonnil errors and elapsed bounds, missing binary | Decoder/source fakes; mostly weak cause oracle; no next audio | Yes |
| C3 EOF with live source | Streamer drain | TestSourceSurvivesAudioEOFCleanup | Valid-to-parser output from fake decoder and sleeping producer; bounded failure | No real encoding, successor or provider challenge | Yes, but covers a real wait boundary |
| C3 stall/decoder remains alive | watchActivity, Streamer drain | TestAudioStallAndDecoderAfterEOF | Fake Ogg followed by hang or closed stdout; exact stall/drain category | Synthetic one-byte packet and shell decoder | Yes |
| C3 panic and descendants | callPlay, Streamer recovery, force callback | TestPanicReapsPipeline; TestExitedSourceLeaderCannotLeakPipeHoldingHelper | Real exec, panic sink, inherited stdout held by child; deadline completion | No provider or successor; helper test emphasizes copier unblock, not full descendant census | Yes |
| P5 pause/resume | pauseGate, Streamer | TestPauseResumeSameTrackAndQueue; TestPausedGateCancellation; TestActivityDeadlineHonorsPause | synctest gates/clock, same track and queue, cancellation, idle suspension | Fake playback/frames; no encoding or voice | Yes |
| P5 process pause/resume | Streamer backpressure | TestStreamerPauseResumeAndCancel | Real pipes/helpers, gate blocks packet sequence, resumes/cancels | Synthetic Ogg, cat decoder, one track, 80ms quiet window | Yes; useful gate/pipeline coverage |
| P5 stop/leave while paused | cancelTrackLocked, Leave, Close | TestCancellationWhilePaused | All controls, fake play loop blocked in pause, synctest, closed voice state | No real child cleanup or Discord connection | Yes |
| Q2 logical SkipN | SkipN, discardEntryLocked | All skip_test.go tests | Count/current/pending order, stale current removal, concurrent skips and mutation races, snapshots | PlayFunc channels; cancellation count instrumentation accesses internals | Yes; asserts selection and mutation, not actual successor audio |
| Q1 progressive first start | BeginCollection/job.run | TestProgressiveFirstStartsWith122Unresolved | First fake start before 122 matches; three workers; pending snapshots; pause with resolution | Matcher callbacks gated by channels; PlayFunc waits cancellation | Yes; first commit/start means no real media |
| Q1 ordered prefixes | job.run/commit | TestProgressiveOutOfOrderContiguousPrefix | Out-of-order result gates; pending slots; ordered queue and skipped traversal | Deterministic matcher, Skip-driven fake playback | Yes; natural EOF and external extraction absent |
| Q4 individual matching failure | job.record/commit | TestProgressiveFailureSemantics; TestProgressiveWorkerPanicFailsPositionAndAdvances | ErrSpotifyMatch/panic removes position; ordered remaining selections | No typed provider challenge; every failure treated as individual | Yes; this policy burns positions for provider failures |
| Q2/C3 cancellation | job context/searches, Clear/Stop/Leave | TestProgressiveCancellation; TestProgressiveSkipFiftyWithTwentyResolved; TestProgressiveSnapshotsDuringResolutionAndControls | Real workers, active counts, job.Done, late results cannot resurrect slots | Matcher always cooperates with cancellation; no actual subprocess group | Yes |
| Q3 concurrent ordering | BeginCollection/EnqueueMany | TestProgressiveMultiplePlaysReserveOrder; TestProgressiveConcurrentReservationsAndGuildIsolation | Serialized reservation order, noninterleaved batches, cross-guild cancel isolation | Prepare timing/real concurrent Discord command arrival not modeled | Yes; no arrival-order promise before reservation |
| P1 startup latency | Prepare/BeginCollection | TestProgressive123StartupMeasurement | 10ms fixtures compare synchronous vs progressive time; one fake frame | Nil limiter; no processes, real metadata/network/voice; wall-time ratio | Yes; not a production performance result |
| R1 public metadata → queue | Spotify fallback + CollectionResolver.Resolve | TestPublicSpotifyOrderedAtomicQueue | Fake HTTP pages, blocked Search, atomic batch insertion, first fake title | Old synchronous Resolve/Enqueue path, not production progressive Prepare | Yes |
| R1 API completeness | Spotify.apiCollection | All spotify_test.go tests | Injected HTTP status/pagination/token/metadata, later-page failure atomicity | No external Spotify; no early terminal page inconsistent with declared total test | Yes |
| R1 public HTML/Embed | publicPlaylist/parseSpotifyPlaylist/parseSpotifyEmbed | spotify_public_test.go and spotify_embed_test.go deterministic tests | Fixture counts, prefix, identity, duplicates, malformed responses, cancellation, no premature matching | Fake HTTP/search or resolver scripts; static provider schema; no streams | Yes; strong metadata checks do not imply audio |
| R1 anonymous metadata | anonymousPlaylist, hydration | spotify_anonymous_test.go deterministic tests | Revision/offset/count/occurrence consistency, bounded refresh, HTTP errors/privacy, order, hydration | Mock transport and fixtures; no real anonymous-provider availability | Yes |
| R1 source fidelity | ranking/discovery/spotify_rank/URL parsers | ranking_test.go, canonical_test.go, source_intent_test.go | Candidate selection, explicit IDs, versions/artists/duration, search/verification budget | Fixed candidates or JSON helpers; some expected selections derived from production selector | Yes; even correct recording metadata can be unplayable |
| R2 bounded discovery | Resolver.extract/resolveSpotify, discovery | resolver_test.go, discovery_test.go | Executable scripts echo fixtures/record arguments; candidate calls bounded | No network or real yt-dlp formats/challenges; verification != extraction | Yes |
| R2 priority/limiter | MatchLimiter | TestMatchLimiterReservesFirstCapacityAndReleasesCanceledWaiters | Fills three background slots, takes fourth first-match slot, cancels waiters and counts release | No playback user of limiter, no global request rate, no load/fairness proof | Yes; reserved first-match slot is not playback priority |
| R2 cache pressure/isolation | SearchCache | All cache_test.go tests | TTL/cap/key independence, process fixture reuse, shared work lifetime | No stream URL or playback availability; channel work in flight tests | Yes |
| R3 challenge/rate-limit preservation | sourceFailure, player.run, job.commit | No old requirement test; soak injects HTTP 403 | Existing assertions reward skipping and continuing | No challenge/429 classification, retention, cooldown or suspension oracle | Yes, directly |
| R4 halt/recovery | player.run, SetPaused | TestConsecutiveFailuresHaltAndResume | Five instantaneous injected errors then nil returns, exact remaining count | Implementation's threshold is expectation; no processes, provider state, retry/cooldown | Yes; encodes consumption of five failures |
| P5 Discord controls | command/guild session orchestration | TestControlsAndSearchCancellation; TestPlayCollectionAndClearCommands; TestPauseResumeNowAndLeaveCommands | Parsed commands, fake REST/cache, search reset, queue state, dummy close | Pre-attached dummy connection, replaced PlayFunc, no real gateway join/voice/processes | Yes |
| Q1/Q2 Discord presentation | buildQueueView/replyQueue/skipText | queue_skip_test.go; queue-limit/registration tests | Logical positions, pending labels, complete exports, limits, count, response semantics | State fixtures or fake players; no audio | Yes; correctly avoids claiming Skip itself started playback |
| V1 first-frame announcements | startVoice, announceStart, announcement worker | announcements_test.go | Fake WriteFrame then once-only REST message for EOF/skip and each source | One arbitrary fake byte; synthetic completion; dummy transport | Yes; real send can fail |
| V1 DAVE recovery | waitVoiceReady | readiness_test.go | Fake readiness channel and deadline propagation | No DAVE epochs/crypto/network; separately scoped contexts | Yes |
| C1 guild isolation | player maps/state, voice watcher | TestLeaveAndGuildIsolation; TestPauseIsolationAndConcurrentState; progressive isolation | Independent fake players, queue/close/cancel isolation, concurrent reads | Global provider pressure, real gateway transports absent | Yes |
| C1/P5 inactivity/voice health | watchVoice/checkEmpty/checkVoiceHealth | All idle_test.go tests | Fake caches, dummy connections, timestamps/synctest, worker lifecycle | No real Discord reconnect/DAVE; test clock often virtual | Yes |
| C4 race freedom | player/cache/guild locking | TestConcurrentQueueAccess; snapshot/control races; full -race | Concurrent state mutations and exercised memory accesses | Ignored operation errors, limited schedules; race-free can still violate liveness/policy | Yes |
| P4 parser | oggPackets | TestOggPackets; TestOggRejectsInvalidAndTruncated | Packet segmentation/continuation/header checks | Handcrafted pages, no CRC/full Opus validation or actual decoding | Yes |
| C5 safe diagnostics | Spotify diagnostic helpers/CLI | spotify_diagnostics_test.go; cmd/bot/spotify_diagnose_test.go; TestSpotifyErrorsHideDiagnosticDetails | Sanitized categories/errors and metadata CLI behavior | Metadata-only; Streamer/raw resolver stderr privacy not comprehensively covered | Yes |
| Provider smoke | Resolver.Resolve; publicPlaylist | TestLiveResolverTiming; TestSpotifyEmbedLiveRegression; TestSpotifyPublicLargeLiveRegression | Only run with opt-in env; external metadata when enabled | Default skip; no complete two-track extraction/audio/Discord | Yes |
| P1–P4/V1 combined local audio (NEW) | Real player + Streamer + Command + FFmpeg | TestTwoTracksNaturalEOFDeliverIndependentAudio | Two distinct media streams, natural completions, independent live contexts, process starts, media-only sink frames; ready and progressive cases | Controlled provider, callback matching, fake final transport | Yes for real provider/Discord; closes local successor-audio gap |
| R3 provider preservation (NEW) | Same real pipeline + queue | TestProviderChallengeDoesNotConsumeQueue | First real media/EOF, second external-like rejection, failed position + successors must remain | Controlled provider challenge, fake voice; no external YouTube | Currently FAILS; does expose current queue-policy defect |

## Scope limits

No audio prefetch or provider cooldown implementation exists to test. A test
cannot validate those absent policies. Every old test can coexist with an external
provider failure; the relevant question is which application response it proves.
Some tests genuinely cover os/exec and process cancellation, so describing the
whole suite as mock-only would be false. Full-suite green results never supplied
the missing combined second-track/real-provider/real-Discord guarantee.

## Test function inventory

The following mechanically enumerated inventory prevents omission by test name.
The boundary assessments above, rather than test names, define each guarantee.

### [internal/discord/announcements_test.go](../internal/discord/announcements_test.go)

- `TestAnnouncementsExactlyOnceOnEOFAndSkip`
- `TestAnnouncementPendingNextAndNoResolutionSpam`
- `TestSuccessfulPlayPublicForEachSource`

### [internal/discord/commands_test.go](../internal/discord/commands_test.go)

- `TestCommandsRequireGuildAndVoice`
- `TestControlsAndSearchCancellation`
- `TestQueueResponseStaysWithinDiscordLimit`
- `TestPlayAcknowledgesWithContentBeforeGuildWork`
- `TestPlayCollectionAndClearCommands`
- `TestClearRegistered`
- `TestPauseResumeNowAndLeaveCommands`
- `TestNewCommandsRegisteredAndNowNeedsNoVoice`
- `TestSpotifyErrorsHideDiagnosticDetails`

### [internal/discord/idle_test.go](../internal/discord/idle_test.go)

- `TestEmptyChannelTimeoutAndIsolation`
- `TestRejoinCancelsEmptyDeadline`
- `TestVoiceEventResetsBriefRejoin`
- `TestManualLeaveCancelsIdleAndResummonIsClean`
- `TestUnknownListenerAndShutdownAreConservative`
- `TestWatchVoiceDisconnectClearsPausedSession`
- `TestBotShutdownWhilePausedCancelsIdleWatcher`
- `TestVoiceEventCachesBotWithGuildScope`
- `TestTransientGatewayStateRetainsPlayback`

### [internal/discord/queue_skip_test.go](../internal/discord/queue_skip_test.go)

- `TestQueueAndSkipOptionRegistration`
- `TestQueueAndBulkSkipCommands`
- `TestSkipResponseDoesNotClaimPlaybackStarted`
- `TestPublicQueueDuringProgressiveLoading`
- `TestOtherCommandResponsesRemainEphemeral`

### [internal/discord/readiness_test.go](../internal/discord/readiness_test.go)

- `TestVoiceRecoveryWait`
- `TestVoiceRecoveryFailureAndCancellation`

### [internal/media/cache_test.go](../internal/media/cache_test.go)

- `TestSearchCacheExpiryBoundAndRequestIsolation`
- `TestResolverCacheReusesMetadataButKeepsVariantsSeparate`
- `TestResolverDoesNotCacheFailure`
- `TestSearchCacheConcurrentAccess`
- `TestNormalizedQueryKeepsVariantsAndURLCase`
- `TestCoalescedSearchHasIndependentCancellation`
- `TestAllWaitersCancelSearchAndFailuresAreNotCached`

### [internal/media/canonical_test.go](../internal/media/canonical_test.go)

- `TestCanonicalFixtures`
- `TestSpotifyStructuredMatchAndCacheIsolation`
- `TestSpotifyRejectsWrongTitleAndArtist`
- `TestCanonicalDiscoveryBroadensSearchWhenReleaseMissing`
- `TestSpotifyAlbumVariantAndConflictingArtist`

### [internal/media/collection_test.go](../internal/media/collection_test.go)

- `TestYouTubeCollectionPartialAndLazy`
- `TestCollectionEmptyAndLimit`
- `TestSpotifyMatchingPartialOrderAndMetadata`
- `TestCollectionCancellation`
- `TestCollectionURL`
- `TestReportedSpotifyPlaylistURL`
- `TestSpotifyURLKindsAndMalformedNeverSearch`
- `TestInaccessibleSpotifyCollectionDoesNotMatch`
- `TestSpotifySingleTrackMatchFailure`

### [internal/media/discovery_test.go](../internal/media/discovery_test.go)

- `TestDiscoveryVerifiesUncertainMusic`
- `TestDiscoveryRejectsNonMusicBeforeVerification`
- `TestDiscoveryDoesNotTreatMissingCategoryAsMusic`
- `TestDiscoveryPrefersCanonicalAndRequestedVariants`
- `TestDiscoveryReRanksUnconfirmedOfficialChannel`
- `TestDiscoveryTriesNextCandidateAfterVerificationFailure`
- `TestDiscoveryDoesNotPromoteUnrelatedVerifiedChannel`
- `TestDiscoveryWithCompleteMetadataMatchesExistingSelection`
- `TestDiscoveryPreservesVerificationError`
- `TestCapturedDiscoveryPreservesSelection`
- `TestDiscoveryRegressionQueries`
- `TestDiscoveryStates`
- `TestDiscoveryVerificationBudget`
- `TestLiveResolverTiming`

### [internal/media/progressive_test.go](../internal/media/progressive_test.go)

- `TestMatchLimiterReservesFirstCapacityAndReleasesCanceledWaiters`

### [internal/media/ranking_test.go](../internal/media/ranking_test.go)

- `TestCanonicalSelection`
- `TestRequestedVariants`
- `TestCanonicalSourceOrder`
- `TestNonMusicRejection`
- `TestURLValidation`
- `TestExactSongTitleRelevance`
- `TestTokenMatchStrength`
- `TestTypoScoreRemainsUseful`
- `TestOfficialVideoIsStillCredibleFallback`

### [internal/media/resolver_test.go](../internal/media/resolver_test.go)

- `TestResolverSearchesMultipleCandidates`
- `TestResolverDirectVideoRetainsIntent`
- `TestResolverInvalidAndEmptyMetadata`
- `TestResolverFlatSearchExtractsOnlySelectedVideo`

### [internal/media/source_intent_test.go](../internal/media/source_intent_test.go)

- `TestDirectSourceIntent`
- `TestPlaylistPreservesIDsOrderDuplicatesAndLive`
- `TestSpotifyRecordingFidelity`
- `TestSpotifyUnicodeCreditsAndFeatures`
- `TestSpotifyCandidatePoolsAndVerificationBound`
- `TestMusicPoolCannotBeStarvedByRichVideoResults`
- `TestSpotifyRejectsConcertWithoutLiveWord`
- `TestSpotifyRemixIdentityAndCreditsInQuery`

### [internal/media/spotify_anonymous_test.go](../internal/media/spotify_anonymous_test.go)

- `TestAnonymousCompletePagination`
- `TestAnonymousDuplicateEntriesAndCollectionOrder`
- `TestAnonymousRejectsIncompleteAndMalformed`
- `TestAnonymousExpiryAndRefresh`
- `TestAnonymousCancellationAndTransportPrivacy`
- `TestSpotifyPublicLargeLiveRegression`
- `TestAnonymousHTTPFailuresNeverRetryOrFollowRedirects`
- `TestAnonymousRefreshBetweenPages`
- `TestAnonymousBootstrapRefreshFailure`

### [internal/media/spotify_diagnostics_test.go](../internal/media/spotify_diagnostics_test.go)

- `TestSpotifyProductionDiagnostics`
- `TestSpotifySafeTransportCause`

### [internal/media/spotify_embed_test.go](../internal/media/spotify_embed_test.go)

- `TestSpotifyEmbedComplete`
- `TestSpotifyEmbedValidation`
- `TestSpotifyEmbedEvidence`
- `TestSpotifyEmbedFallbackAndOrdering`
- `TestSpotifyEmbedCancellation`
- `TestSpotifyEmbedLiveRegression`

### [internal/media/spotify_public_test.go](../internal/media/spotify_public_test.go)

- `TestSpotifyPublicParser`
- `TestSpotifyPublicFallbackConditions`
- `TestSpotifyPublicHTTP`
- `TestSpotifyPublicCanonicalResolver`
- `TestSpotifyObservedTruncationNeverMatches`

### [internal/media/spotify_test.go](../internal/media/spotify_test.go)

- `TestSpotifyPaginationAndTokenReuse`
- `TestSpotifyFailures`
- `TestSpotifyUnavailableEntriesAndBounds`
- `TestSpotifySingleTrack`
- `TestSpotifyTokenAuthorizationError`
- `TestSpotifyLaterPageFailureIsAtomic`

### [internal/player/collection_test.go](../internal/player/collection_test.go)

- `TestProgressiveFirstStartsWith122Unresolved`
- `TestProgressiveOutOfOrderContiguousPrefix`
- `TestProgressiveFailureSemantics`
- `TestProgressiveCancellation`
- `TestProgressiveSkipFiftyWithTwentyResolved`
- `TestProgressiveMultiplePlaysReserveOrder`
- `TestProgressiveConcurrentReservationsAndGuildIsolation`
- `TestProgressive123StartupMeasurement`
- `TestProgressiveSnapshotsDuringResolutionAndControls`

### [internal/player/lifecycle_test.go](../internal/player/lifecycle_test.go)

- `TestSourceSurvivesAudioEOFCleanup`
- `TestPipelineSoak125Transitions`
- `TestConsecutiveFailuresHaltAndResume`
- `TestNaturalEOFRacesWithSkipAndStop`
- `TestProgressiveWorkerPanicFailsPositionAndAdvances`
- `TestAudioStallAndDecoderAfterEOF`
- `TestActivityDeadlineHonorsPause`
- `TestPanicReapsPipeline`
- `TestExitedSourceLeaderCannotLeakPipeHoldingHelper`

### [internal/player/ogg_test.go](../internal/player/ogg_test.go)

- `TestOggPackets`
- `TestOggRejectsInvalidAndTruncated`

### [internal/player/pause_test.go](../internal/player/pause_test.go)

- `TestPauseResumeSameTrackAndQueue`
- `TestCancellationWhilePaused`
- `TestPauseIsolationAndConcurrentState`
- `TestPausedGateCancellation`

### [internal/player/player_test.go](../internal/player/player_test.go)

- `TestQueueAdvanceSkipFailureAndStop`
- `TestLeaveAndGuildIsolation`
- `TestConcurrentQueueAccess`
- `TestBatchOrderClearAndCapacity`

### [internal/player/reliability_test.go](../internal/player/reliability_test.go)

- `TestTwoTracksNaturalEOFDeliverIndependentAudio`
- `TestProviderChallengeDoesNotConsumeQueue`

### [internal/player/skip_test.go](../internal/player/skip_test.go)

- `TestBulkSkipPositions`
- `TestBulkSkipInvalidEmptyAndStarting`
- `TestBulkSkipPaused`
- `TestConcurrentBulkSkipDoesNotRecountCancelledTrack`
- `TestBulkSkipStopClearEnqueueRaces`
- `TestSnapshotCopiesMetadataAndConsistentBatch`
- `TestBulkSkipDuringPlaybackStartup`

### [internal/player/spotify_public_test.go](../internal/player/spotify_public_test.go)

- `TestPublicSpotifyOrderedAtomicQueue`

### [internal/player/stream_test.go](../internal/player/stream_test.go)

- `TestStreamerLocalFFmpeg`
- `TestStreamerFailuresAndCancellation`
- `TestStreamerMissingExecutable`
- `TestStreamResolutionMarkerAcrossWrites`
- `TestStartupDeadlinesEndAtFirstOpus`
- `TestStreamerReportsStagesBeforeVoiceFailure`
- `TestStreamerDrainsAfterProducerExit`
- `TestStreamerPauseResumeAndCancel`

### [cmd/bot/spotify_diagnose_test.go](../cmd/bot/spotify_diagnose_test.go)

- `TestConfiguredSpotify`

## Search intent and album/track disambiguation

See [Bard search intent](bard-search-intent.md) for BARD-SEARCH-001 through
BARD-SEARCH-008, deterministic fixtures, mutation evidence and scope limits.

## CR-001 queue presentation

See [queue experience](queue-experience.md) for the twenty required scenarios,
artwork persistence, hybrid full-list delivery, rate-limit and failure tests.

## CR-002

See [queue/artwork audit and regression matrix](queue-artwork-hotfix.md) and [persistent catalog](persistent-music-cache.md). `cr002_test.go` covers command HTTP upserts/readback/idempotence, native embeds, final playback metadata, concurrent guilds and HTML failure with TXT retained. Storage catalog tests cover real SQLite restart/concurrency/isolation/expiry/constraints and conditional mapping invalidation; cache-failure regression tests fail on `762fbb5`.
