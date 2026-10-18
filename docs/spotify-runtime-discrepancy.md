# Spotify host/Docker discrepancy — 2026-09-23

The earlier `123/123` report established host metadata success only. It did not establish success through Bard's Docker runtime. The subsequent real `/play` failure was valid.

## Root cause and first divergence

The Docker DNS forwarding path exposed at `127.0.0.11:53` returned malformed/empty-address answers to Go's EDNS queries. A DNS-only probe using Go's resolver recorded the following for `spclient.wg.spotify.com`:

| Query | EDNS | Response code | Answer section | Go result |
| --- | --- | --- | --- | --- |
| A | enabled | NOERROR | one OPT record, no A | no such host |
| AAAA | enabled | NOERROR | one OPT record, no AAAA | no such host |
| A | disabled | NOERROR | A `35.186.224.24` | success |
| AAAA | disabled | NOERROR | AAAA `2600:1901:1:7c5::` | success |

These are observed addresses, not configuration: no addresses are hardcoded. This localizes the defect to DNS responses on the Docker forwarding path; it does not determine which intermediary generated the malformed response. The host's resolver uses the LAN nameserver directly. Neither environment had a configured HTTP proxy.

The original standalone live test was then compiled as a static test binary and run **inside Compose**, before applying the fix. It reproduced the reported failure in approximately one second:

```text
spotify_public_page_complete rendered=30 total=123
spotify_embed_complete embedded=100 total=123
spotify_anonymous_bootstrap_start
spotify_anonymous_bootstrap_complete
spotify_spclient_page_start offset=0 page_size=50
spotify_spclient_response offset=0 status=0
Could not fetch Spotify public playlist metadata.
```

The first diverging request was the anonymous spclient request at offset 0, before any HTTP response. The original generic fetch error discarded the underlying DNS error. The new safe error classification preserves `dns_no_address` without retaining/logging the raw transport error.

Separate production-constructor runs also encountered this DNS problem earlier, at `accounts.spotify.com`, sometimes yielding only an IPv6 address and then `network is unreachable`. Python DNS resolution alone was an inadequate control: it succeeded while Go's EDNS lookups failed. The controlled Go DNS comparison above identified the difference. Disabling EDNS fixes address discovery without disabling IPv6 or changing Spotify request behavior.

## Build identity

The binary in the original running Bard was hashed before any changes:

```text
75eae3aeb2ba81df5548763621480d9cc47c7eeb0bf7ae9076845c568ac419bd
```

It exactly matched `/usr/local/bin/bard` from the previously built `bard:public-playlists-queue-skip` image (`9033e3b79d1c9e18e7774ff18c4cf5b9d64cfbfe977fe8800972869f50feb2e6`). This was the image recorded in the earlier implementation report. There is no evidence of a stale binary.

The corrected Compose build emitted this binary SHA-256, and the diagnostic running inside that image computed the same hash from its own executable:

```text
45857effbd79c2dbad71cd0959575406634b3e0d6b092593e3699290532682d6
```

Corrected image: `39f8a5497b36` (the full binary hash above is the reproducible run/build comparison). Docker cache was left enabled. Host binary hashes differ because the host build has different Go build metadata/toolchain settings; they were not used to infer a stale Docker image.

## Construction/configuration comparison

| Concern | Existing host live verifier | Bard / production diagnostic |
| --- | --- | --- |
| Entry | `Spotify{}.publicPlaylist` | `CollectionResolver.Resolve` → `Spotify.Collection` → fallback |
| Constructor | empty `Spotify` | shared `configuredSpotify(log)` used by both bot and diagnostic |
| Credentials/market | none; official API bypassed | actual Compose credentials and refresh token; market `BR` |
| Official API | bypassed | real 403 for 123-track playlist; real 404 for 50-track playlist |
| HTTP client | default Go client, 30-second timeout | same; no injected transport/client |
| Public/spclient User-Agent | `Mozilla/5.0 (compatible; BardMetadata/1.0)` | same |
| Accept | HTML for public/Embed, JSON for spclient | same |
| Cookies | explicitly disabled on fallback client copies | same |
| Redirects | public: HTTPS/open.spotify.com only, bounded; spclient: disabled | same |
| Limits | 15-second request timeout, 8 MiB bodies, two-minute fallback | same |
| Parent context | background in old test | cancellable context then production ten-minute collection timeout; Discord uses cancellable guild-search context |
| Bootstrap | playlist Embed `__NEXT_DATA__` anonymous session | same |
| Token cache/expiry | resolution-local token; explicit expiry; one refresh maximum | same; live Docker control also exercised near-expiry refresh successfully |
| spclient URL | locally constructed `https://spclient.wg.spotify.com/playlist/v2/playlist/{id}?from=…&length=50` | same |
| Origin/Referer | neither set | same |
| Market/locale in public requests | none | same; market applies only to official API |
| Retries | at most one anonymous refresh for expiry/401; other HTTP failures fail closed | same |
| DNS | host LAN resolver | Docker forwarding resolver; EDNS response discrepancy |

No Spotify headers, token extraction, pagination, hydration, completeness checks or retry policy were changed to obtain success.

## Fix and production diagnostic

The runtime Dockerfile now sets `GODEBUG=netedns0=0`. This is a container-local compatibility setting for Go DNS queries. The normal Docker build prints the built binary hash. A metadata-only command in the **same Bard executable** computes its executable hash and uses the same constructor, environment, default HTTP client and collection orchestration as `/play`:

```sh
docker compose build bard
docker compose run --rm --no-deps bard spotify-diagnose \
  https://open.spotify.com/playlist/35m4wmHSlcS2RNsafX4dEg \
  https://open.spotify.com/playlist/37i9dQZF1DZ06evO3VkoW4
```

The diagnostic substitutes only the downstream YouTube matcher with an in-memory result. It does not connect to Discord, play audio or enqueue tracks. Metadata still must pass the actual production resolver's complete-count/order/revision validation before reaching that substitution. `compose run` uses the service's networking, credentials, read-only filesystem, user and other runtime settings. It does not restart the regular Bard service.

Structured logs now cover public/Embed counts, anonymous bootstrap/refresh, spclient offsets/status/counts, tail enrichment, final revision validation and completion. Failure logs carry fixed endpoint categories and sanitized reasons (DNS, timeout, cancellation, HTTP status, body limit, invalid metadata or completeness mismatch). No tokens, cookies, authorization headers, raw transport error strings or response bodies are logged by these diagnostics.

## Results

| Path | 123-track playlist | 50-track playlist |
| --- | --- | --- |
| Host existing live verifier | 123/123 | 50/50 |
| Host production constructor + collection orchestration | 123/123 | 50/50 |
| Docker existing live verifier, original DNS setting | fails at first spclient request | not needed for fault isolation |
| Docker production constructor, original DNS setting | DNS failures, including official token bootstrap | not reached |
| Docker production constructor + collection orchestration, corrected default image | **123/123** | **50/50** |
| Docker existing live verifier, corrected default image | 123/123 | 50/50 |

The final production-equivalent Docker run logged:

```text
api_status=403
spotify_public_page_complete rendered=30 total=123
spotify_embed_complete embedded=100 total=123
spotify_anonymous_bootstrap_complete
spotify_spclient_page_complete offset=0 entries=50
spotify_spclient_page_complete offset=50 entries=50
spotify_spclient_page_complete offset=100 entries=23
spotify_tail_enrichment_start missing=23
spotify_tail_enrichment_complete resolved=23
spotify_spclient_page_complete offset=0 entries=50
spotify_public_complete resolved=123 total=123
Spotify production diagnostic complete resolved=123
```

The 50-track playlist completed directly from its 50-entry Embed, without anonymous pagination. No incomplete batch was accepted.

After Docker acceptance, full vet, unit tests, race tests, build and whitespace checks passed. The Docker build also ran vet, all unit tests and its runtime tool/Opus checks. New regression tests verify diagnostic stage coverage, DNS-cause preservation, sensitive-data exclusion, shared environment construction and all-or-nothing metadata through the collection orchestrator.

No regular Bard container was stopped, restarted or recreated by this investigation. The original container became unavailable to Docker inspection during the session; no daemon/container mutation was performed to resolve that observation. Acceptance used separate temporary Compose containers. Real Discord playback after the fix remains untested, as requested. Queue pagination, bulk skip, player SkipN and snapshots were not changed in this pass.
