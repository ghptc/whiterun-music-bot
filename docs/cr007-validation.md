# CR-007.1 validation — 2026-10-20

Final source, including the Auto Pilot correction:

| Check | Result |
|---|---|
| `go test ./... -count=1` | PASS, all packages |
| `go test -race ./... -count=1` | PASS, all packages |
| `go vet ./...` | PASS |
| `docker compose build` | PASS; runtime image `73ae8dc89a57` |
| `python3 scripts/check_ui_mutations.py` | 9/9 targeted mutants killed by assertions |
| `python3 scripts/check_auto_pilot_mutations.py` | 4/4 targeted mutants killed by assertions |
| `git diff --check` | PASS |

The runtime binary SHA-256 is
`6e21d1cf22759d285f2172b330f307ac812c33cc00ce7de2655be0d925891b71`.
Docker used the classic builder because buildx is not installed. The image
checks include vet, unit tests, ffmpeg/libopus, yt-dlp 2026.08.19 and Deno 2.5.6.
No bot service was started or restarted.

The baseline Auto Pilot fixture test was run before changing `recordingTitle`
and failed: `extra_title_content`, title precision 0.5. After the change, the
real capture tests pass. Mutation replay independently confirms that removing
the normalization reintroduces a failing assertion. An initial mutation bypassing
only the first cache lookup survived because the cache's single-flight path also
checks entries; it was replaced by an unstable-key mutation, which the repeated
resolution assertion kills. Compile failures are not counted as killed mutants.

[Queue investigation](queue-experience.md) records read-only live Discord API
contract checks, including both guild scopes. [Auto Pilot investigation](auto-pilot-fidelity.md)
records live Spotify/YouTube metadata, format availability and a read-only
production-volume inspection. These probes do not validate Discord messages,
voice packets, audio CDN delivery or audible playback. The user-supplied playlist was confirmed to contain 302 tracks, with the exact
Auto Pilot ID at position 4. Its historical runtime image and resolver logs
remain uncorrelated.

There is no claim of real Discord E2E homologation.
